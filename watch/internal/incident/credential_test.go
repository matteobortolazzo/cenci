package incident

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
)

const (
	testTenantID = "11111111-1111-4111-8111-111111111111"
	testClientID = "22222222-2222-4222-8222-222222222222"
	otherUUID    = "33333333-3333-4333-8333-333333333333"
)

// Message markers, one per failure class; tests assert the one that applies
// and the absence of the others so the classes cannot collapse into one.
const (
	markRequestFailed  = "token request failed"
	markTenantMismatch = "token tenant mismatch"
	markAppMismatch    = "token application mismatch"
	markClaims         = "unreadable token claims"
	markManagedHosts   = "managed identity works only on supported Azure hosts"

	markUnreadable = "certificate unreadable"
	markNotRegular = "not a regular file"
	markOwner      = "owned by uid"
	markMode       = "has mode"
	markParse      = "could not be parsed"
	markTooLarge   = "too large"
	markExpired    = "certificate expired"

	requiredModeText = "mode 0600 or 0400"
)

// isolateAzureEnv removes every ambient Azure identity variable so a test sees
// only what it sets itself. t.Setenv registers the restore; the Unsetenv makes
// the variable truly absent, which os.LookupEnv-based SDK probes distinguish
// from empty.
func isolateAzureEnv(t *testing.T) {
	t.Helper()
	names := []string{"IDENTITY_ENDPOINT", "IDENTITY_HEADER", "MSI_ENDPOINT", "MSI_SECRET", "IMDS_ENDPOINT"}
	for _, kv := range os.Environ() {
		if name, _, _ := strings.Cut(kv, "="); strings.HasPrefix(name, "AZURE_") {
			names = append(names, name)
		}
	}
	for _, name := range names {
		t.Setenv(name, "")
		if err := os.Unsetenv(name); err != nil {
			t.Fatal(err)
		}
	}
}

var (
	testKeyOnce sync.Once
	testKey     *rsa.PrivateKey
)

func testRSAKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	testKeyOnce.Do(func() {
		k, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			t.Fatal(err)
		}
		testKey = k
	})
	return testKey
}

func certPEM(t *testing.T, notBefore, notAfter time.Time) []byte {
	t.Helper()
	key := testRSAKey(t)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "incident-worker"}, NotBefore: notBefore, NotAfter: notAfter}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	out := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	return append(out, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})...)
}

// writeCertFile writes an unencrypted PEM (certificate and key) with exactly the
// given mode, immune to the process umask.
func writeCertFile(t *testing.T, notAfter time.Time, mode os.FileMode) string {
	t.Helper()
	return writeFileMode(t, certPEM(t, notAfter.Add(-48*time.Hour), notAfter), mode)
}

func writeFileMode(t *testing.T, data []byte, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "worker.pem")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	return path
}

func certAuth(path string) AuthConfig {
	return AuthConfig{Mode: AuthModeCertificate, TenantID: testTenantID, ClientID: testClientID, CertificatePath: path}
}

func testJWT(claims map[string]any) string {
	enc := base64.RawURLEncoding
	b, err := json.Marshal(claims)
	if err != nil {
		panic(err)
	}
	return enc.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`)) + "." + enc.EncodeToString(b) + "." + enc.EncodeToString([]byte("sig"))
}

// newCredentialBounded fails the test instead of hanging when credential
// construction blocks (for example on a FIFO at the certificate path).
func newCredentialBounded(t *testing.T, auth AuthConfig, opts CredentialOptions) (azcore.TokenCredential, string, error) {
	t.Helper()
	type result struct {
		cred    azcore.TokenCredential
		warning string
		err     error
	}
	done := make(chan result, 1)
	go func() {
		c, w, err := NewCredential(auth, opts)
		done <- result{c, w, err}
	}()
	select {
	case r := <-done:
		return r.cred, r.warning, r.err
	case <-time.After(10 * time.Second):
		t.Fatal("NewCredential blocked")
		return nil, "", nil
	}
}

func expectMarker(t *testing.T, err error, want string, others ...string) {
	t.Helper()
	if err == nil {
		t.Fatalf("want an error containing %q, got nil", want)
	}
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("want marker %q in: %v", want, err)
	}
	for _, other := range others {
		if strings.Contains(err.Error(), other) {
			t.Fatalf("failure class collapsed: %q also contains %q: %v", want, other, err)
		}
	}
}

func TestCertificateFileMustBeAnOwnedRegularFileWithRestrictedMode(t *testing.T) {
	future := time.Now().Add(365 * 24 * time.Hour)
	allMarkers := []string{markUnreadable, markNotRegular, markOwner, markMode, markParse, markTooLarge}
	others := func(keep string) []string {
		var out []string
		for _, m := range allMarkers {
			if m != keep {
				out = append(out, m)
			}
		}
		return out
	}
	const keyText = "TOPSECRETKEYBYTES"
	cases := []struct {
		name   string
		path   func(t *testing.T) string
		marker string   // empty means accepted
		extra  []string // further text the message must carry
	}{
		{"mode-0600", func(t *testing.T) string { return writeCertFile(t, future, 0600) }, "", nil},
		{"mode-0400", func(t *testing.T) string { return writeCertFile(t, future, 0400) }, "", nil},
		{"mode-0644", func(t *testing.T) string { return writeCertFile(t, future, 0644) }, markMode, []string{"0644", "0600 or 0400"}},
		{"mode-0640", func(t *testing.T) string { return writeCertFile(t, future, 0640) }, markMode, []string{"0640"}},
		{"mode-0700", func(t *testing.T) string { return writeCertFile(t, future, 0700) }, markMode, []string{"0700"}},
		{"mode-0660", func(t *testing.T) string { return writeCertFile(t, future, 0660) }, markMode, []string{"0660"}},
		{"mode-setuid-0600-shows-the-special-bit", func(t *testing.T) string {
			p := writeFileMode(t, certPEM(t, future.Add(-48*time.Hour), future), 0600|os.ModeSetuid)
			if fi, err := os.Stat(p); err != nil || fi.Mode()&os.ModeSetuid == 0 {
				t.Skip("filesystem does not keep the setuid bit")
			}
			return p
		}, markMode, []string{"has mode 04600; required: " + requiredModeText}},
		{"mode-checked-before-parsing", func(t *testing.T) string {
			return writeFileMode(t, []byte("-----BEGIN PRIVATE KEY-----\n"+keyText+"\n-----END PRIVATE KEY-----\n"), 0644)
		}, markMode, nil},
		{"missing", func(t *testing.T) string { return filepath.Join(t.TempDir(), "absent.pem") }, markUnreadable, nil},
		{"symlink", func(t *testing.T) string {
			link := filepath.Join(t.TempDir(), "link.pem")
			if err := os.Symlink(writeCertFile(t, future, 0600), link); err != nil {
				t.Fatal(err)
			}
			return link
		}, markNotRegular, []string{requiredModeText}},
		{"directory", func(t *testing.T) string { return t.TempDir() }, markNotRegular, []string{requiredModeText}},
		{"fifo", func(t *testing.T) string {
			p := filepath.Join(t.TempDir(), "fifo.pem")
			if err := syscall.Mkfifo(p, 0600); err != nil {
				t.Skipf("mkfifo unavailable: %v", err)
			}
			return p
		}, markNotRegular, []string{requiredModeText}},
		{"foreign-owner", func(t *testing.T) string {
			if os.Getuid() == 0 {
				p := writeCertFile(t, future, 0600)
				if err := os.Chown(p, 12345, -1); err != nil {
					t.Skipf("chown unavailable: %v", err)
				}
				return p
			}
			// A root-owned world-readable file: ownership is rejected before its mode.
			fi, err := os.Lstat("/etc/passwd")
			if err != nil || !fi.Mode().IsRegular() {
				t.Skip("no root-owned regular file to use")
			}
			return "/etc/passwd"
		}, markOwner, []string{requiredModeText}},
		{"garbage-never-echoed", func(t *testing.T) string {
			return writeFileMode(t, []byte("-----BEGIN PRIVATE KEY-----\n"+keyText+"\n-----END PRIVATE KEY-----\n"), 0600)
		}, markParse, nil},
		{"oversized", func(t *testing.T) string { return writeFileMode(t, []byte(strings.Repeat("A", 2<<20)), 0600) }, markTooLarge, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			isolateAzureEnv(t)
			path := tc.path(t)
			cred, warning, err := newCredentialBounded(t, certAuth(path), CredentialOptions{})
			if tc.marker == "" {
				if err != nil || cred == nil || warning != "" {
					t.Fatalf("valid certificate rejected: cred=%v warning=%q err=%v", cred, warning, err)
				}
				return
			}
			expectMarker(t, err, tc.marker, others(tc.marker)...)
			if !strings.Contains(err.Error(), path) {
				t.Fatalf("message must name the path %s: %v", path, err)
			}
			for _, want := range tc.extra {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("message missing %q: %v", want, err)
				}
			}
			if strings.Contains(err.Error(), keyText) {
				t.Fatalf("key bytes reached the message: %v", err)
			}
		})
	}
}

func TestCertificateExpiryFailsAndNearExpiryWarns(t *testing.T) {
	isolateAzureEnv(t)
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	opts := CredentialOptions{Now: func() time.Time { return now }}
	date := func(d time.Time) string { return d.UTC().Format("2006-01-02") }

	expired := now.Add(-time.Hour)
	_, _, err := newCredentialBounded(t, certAuth(writeCertFile(t, expired, 0600)), opts)
	expectMarker(t, err, markExpired)
	if !strings.Contains(err.Error(), date(expired)) {
		t.Fatalf("expired message must carry NotAfter %s: %v", date(expired), err)
	}

	soon := now.Add(10 * 24 * time.Hour)
	cred, warning, err := newCredentialBounded(t, certAuth(writeCertFile(t, soon, 0600)), opts)
	if err != nil || cred == nil {
		t.Fatalf("certificate inside the warning window must still start: %v", err)
	}
	if !strings.Contains(warning, date(soon)) || !strings.Contains(warning, "30 days") {
		t.Fatalf("warning must carry the NotAfter date and window: %q", warning)
	}

	far := now.Add(60 * 24 * time.Hour)
	_, warning, err = newCredentialBounded(t, certAuth(writeCertFile(t, far, 0600)), opts)
	if err != nil || warning != "" {
		t.Fatalf("certificate outside the window warned: %q %v", warning, err)
	}
}

// chainPEM writes a PEM that lists a CA certificate BEFORE the leaf (the order
// azidentity.ParseCertificates returns them in), then the leaf's private key.
// The leaf is the certificate matching the key; the CA has a key of its own.
func chainPEM(t *testing.T, caNotAfter, leafNotAfter time.Time) []byte {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	caTmpl := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "incident-ca"}, NotBefore: caNotAfter.Add(-48 * time.Hour), NotAfter: caNotAfter, IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	key := testRSAKey(t)
	leafTmpl := &x509.Certificate{SerialNumber: big.NewInt(3), Subject: pkix.Name{CommonName: "incident-worker"}, NotBefore: leafNotAfter.Add(-48 * time.Hour), NotAfter: leafNotAfter}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTmpl, caTmpl, &key.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	out := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})
	out = append(out, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER})...)
	return append(out, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})...)
}

// The expiry check and the 30-day warning judge the certificate the private key
// belongs to, not whichever certificate the file lists first.
func TestCertificateExpiryJudgesTheLeafMatchingThePrivateKeyNotTheFirstCertificate(t *testing.T) {
	isolateAzureEnv(t)
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	opts := CredentialOptions{Now: func() time.Time { return now }}
	date := func(d time.Time) string { return d.UTC().Format("2006-01-02") }
	longLived := now.Add(5 * 365 * 24 * time.Hour)

	soon := now.Add(10 * 24 * time.Hour)
	cred, warning, err := newCredentialBounded(t, certAuth(writeFileMode(t, chainPEM(t, longLived, soon), 0600)), opts)
	if err != nil || cred == nil {
		t.Fatalf("near-expiry leaf behind a long-lived CA must still start: %v", err)
	}
	if !strings.Contains(warning, date(soon)) || !strings.Contains(warning, "30 days") {
		t.Fatalf("warning must carry the leaf's NotAfter %s: %q", date(soon), warning)
	}

	expiredLeaf := now.Add(-time.Hour)
	_, _, err = newCredentialBounded(t, certAuth(writeFileMode(t, chainPEM(t, longLived, expiredLeaf), 0600)), opts)
	expectMarker(t, err, markExpired)
	if !strings.Contains(err.Error(), date(expiredLeaf)) {
		t.Fatalf("expired message must carry the leaf's NotAfter %s: %v", date(expiredLeaf), err)
	}

	cred, warning, err = newCredentialBounded(t, certAuth(writeFileMode(t, chainPEM(t, now.Add(-time.Hour), longLived), 0600)), opts)
	if err != nil || cred == nil || warning != "" {
		t.Fatalf("an expired CA listed before a valid leaf must not block startup: cred=%v warning=%q err=%v", cred, warning, err)
	}
}

func TestCertificateWithoutAMatchingPrivateKeyIsRejected(t *testing.T) {
	isolateAzureEnv(t)
	otherKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	notAfter := time.Now().Add(365 * 24 * time.Hour)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(4), Subject: pkix.Name{CommonName: "someone-else"}, NotBefore: notAfter.Add(-48 * time.Hour), NotAfter: notAfter}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &otherKey.PublicKey, otherKey)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(testRSAKey(t))
	if err != nil {
		t.Fatal(err)
	}
	data := append(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})...)
	path := writeFileMode(t, data, 0600)

	_, _, err = newCredentialBounded(t, certAuth(path), CredentialOptions{})
	expectMarker(t, err, "has no certificate matching its private key", markParse, markExpired)
	if !strings.Contains(err.Error(), path) {
		t.Fatalf("message must name the path %s: %v", path, err)
	}
}

// scopeCredential records every token request a Preflight makes.
type scopeCredential struct {
	mu        sync.Mutex
	scopes    []string
	deadlines []time.Duration
	token     func(scope string) (string, error)
}

func (c *scopeCredential) GetToken(ctx context.Context, o policy.TokenRequestOptions) (azcore.AccessToken, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	scope := strings.Join(o.Scopes, " ")
	c.scopes = append(c.scopes, scope)
	if dl, ok := ctx.Deadline(); ok {
		c.deadlines = append(c.deadlines, time.Until(dl))
	} else {
		c.deadlines = append(c.deadlines, -1)
	}
	tok, err := c.token(scope)
	if err != nil {
		return azcore.AccessToken{}, err
	}
	return azcore.AccessToken{Token: tok, ExpiresOn: time.Now().Add(time.Hour)}, nil
}

func claimsToken(tid, appid string) func(string) (string, error) {
	return func(string) (string, error) {
		claims := map[string]any{}
		if tid != "" {
			claims["tid"] = tid
		}
		if appid != "" {
			claims["appid"] = appid
		}
		return testJWT(claims), nil
	}
}

func TestPreflightNamesEachFailureClass(t *testing.T) {
	const tokenText = "SECRET-TOKEN-TEXT"
	managed := AuthConfig{Mode: AuthModeManagedIdentity, TenantID: testTenantID}
	managedWithClient := AuthConfig{Mode: AuthModeManagedIdentity, TenantID: testTenantID, ClientID: testClientID}
	cert := certAuth("/unused.pem")
	allMarkers := []string{markRequestFailed, markTenantMismatch, markAppMismatch, markClaims}
	cases := []struct {
		name   string
		auth   AuthConfig
		token  func(string) (string, error)
		marker string // empty means the preflight passes
		extra  []string
		absent []string
	}{
		{"matching-claims", cert, claimsToken(testTenantID, testClientID), "", nil, nil},
		{"uppercase-claims-match", cert, claimsToken(strings.ToUpper(testTenantID), strings.ToUpper(testClientID)), "", nil, nil},
		{"managed-without-client-ignores-appid", managed, claimsToken(testTenantID, otherUUID), "", nil, nil},
		{"managed-without-client-and-without-appid", managed, claimsToken(testTenantID, ""), "", nil, nil},
		{"request-failed", cert, func(string) (string, error) { return "", errors.New("AADSTS7000215 bad assertion") }, markRequestFailed, []string{"Service Bus", "AADSTS7000215"}, []string{markManagedHosts}},
		{"managed-request-failed", managed, func(string) (string, error) { return "", errors.New("no endpoint") }, markRequestFailed, []string{markManagedHosts}, nil},
		{"log-analytics-request-failed", cert, func(scope string) (string, error) {
			if scope == LogAnalyticsScope {
				return "", errors.New("denied")
			}
			return claimsToken(testTenantID, testClientID)(scope)
		}, markRequestFailed, []string{"Log Analytics"}, nil},
		{"tenant-mismatch", cert, claimsToken(otherUUID, testClientID), markTenantMismatch, nil, nil},
		{"missing-tid", cert, claimsToken("", testClientID), markTenantMismatch, []string{"no tid claim"}, nil},
		{"app-mismatch", cert, claimsToken(testTenantID, otherUUID), markAppMismatch, nil, nil},
		{"missing-appid", cert, claimsToken(testTenantID, ""), markAppMismatch, []string{"no appid claim"}, nil},
		{"managed-with-client-app-mismatch", managedWithClient, claimsToken(testTenantID, otherUUID), markAppMismatch, nil, nil},
		{"not-a-jwt", cert, func(string) (string, error) { return tokenText, nil }, markClaims, nil, []string{tokenText}},
		{"payload-not-json", cert, func(string) (string, error) {
			return "e30." + base64.RawURLEncoding.EncodeToString([]byte(tokenText)) + ".c2ln", nil
		}, markClaims, nil, []string{tokenText}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cred := &scopeCredential{token: tc.token}
			err := Preflight(context.Background(), cred, tc.auth)
			if tc.marker == "" {
				if err != nil {
					t.Fatalf("matching identity rejected: %v", err)
				}
				return
			}
			var others []string
			for _, m := range allMarkers {
				if m != tc.marker {
					others = append(others, m)
				}
			}
			expectMarker(t, err, tc.marker, append(others, tc.absent...)...)
			for _, want := range tc.extra {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("message missing %q: %v", want, err)
				}
			}
		})
	}
}

func TestPreflightRequestsBothScopesWithinThirtySecondsEach(t *testing.T) {
	cred := &scopeCredential{token: claimsToken(testTenantID, testClientID)}
	if err := Preflight(context.Background(), cred, certAuth("/unused.pem")); err != nil {
		t.Fatal(err)
	}
	if len(cred.scopes) != 2 || cred.scopes[0] != ServiceBusScope || cred.scopes[1] != LogAnalyticsScope {
		t.Fatalf("scopes requested in order: %v", cred.scopes)
	}
	for i, d := range cred.deadlines {
		if d <= 0 || d > 30*time.Second {
			t.Fatalf("request %d deadline %v, want within (0, 30s]", i, d)
		}
	}
}

func TestPreflightStopsAtTheFirstFailingScope(t *testing.T) {
	cred := &scopeCredential{token: func(string) (string, error) { return "", errors.New("denied") }}
	if err := Preflight(context.Background(), cred, certAuth("/unused.pem")); err == nil {
		t.Fatal("failing credential passed the preflight")
	}
	if len(cred.scopes) != 1 || cred.scopes[0] != ServiceBusScope {
		t.Fatalf("preflight went on after a failure: %v", cred.scopes)
	}
}

// aadRequest is one request the fake identity endpoints saw.
type aadRequest struct {
	Method, Host, Path string
	Query              url.Values
	Form               url.Values
}

// fakeAAD answers the Microsoft Entra ID metadata and token endpoints, and the
// managed-identity (IMDS) endpoint, and records every request. A request to any
// other host or path is recorded and answered 404.
type fakeAAD struct {
	mu       sync.Mutex
	requests []aadRequest
}

func (f *fakeAAD) Do(r *http.Request) (*http.Response, error) {
	req := aadRequest{Method: r.Method, Host: r.URL.Host, Path: r.URL.Path, Query: r.URL.Query()}
	if r.Body != nil {
		b, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
		req.Form, _ = url.ParseQuery(string(b))
	}
	f.mu.Lock()
	f.requests = append(f.requests, req)
	f.mu.Unlock()
	reply := func(status int, body any) (*http.Response, error) {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(string(b))), Request: r}, nil
	}
	segments := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	switch {
	case r.URL.Host == "login.microsoftonline.com" && r.URL.Path == "/common/discovery/instance":
		return reply(200, map[string]any{
			"tenant_discovery_endpoint": "https://login.microsoftonline.com/" + testTenantID + "/v2.0/.well-known/openid-configuration",
			"api-version":               "1.1",
			"metadata":                  []any{map[string]any{"preferred_network": "login.microsoftonline.com", "preferred_cache": "login.windows.net", "aliases": []string{"login.microsoftonline.com", "login.windows.net"}}},
		})
	case r.URL.Host == "login.microsoftonline.com" && strings.HasSuffix(r.URL.Path, "/v2.0/.well-known/openid-configuration"):
		base := "https://login.microsoftonline.com/" + segments[0]
		return reply(200, map[string]any{"authorization_endpoint": base + "/oauth2/v2.0/authorize", "token_endpoint": base + "/oauth2/v2.0/token", "issuer": base + "/v2.0"})
	case r.URL.Host == "login.microsoftonline.com" && r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/oauth2/v2.0/token"):
		token := testJWT(map[string]any{"tid": segments[0], "appid": req.Form.Get("client_id")})
		return reply(200, map[string]any{"token_type": "Bearer", "expires_in": 3600, "ext_expires_in": 3600, "access_token": token})
	case r.URL.Host == "169.254.169.254" && r.URL.Path == "/metadata/identity/oauth2/token":
		appid := req.Query.Get("client_id")
		if appid == "" {
			appid = otherUUID // the system-assigned identity's application
		}
		token := testJWT(map[string]any{"tid": testTenantID, "appid": appid})
		// MSAL keeps managed identity tokens in a process-wide cache. A lifetime
		// under its refresh margin keeps every test hitting the transport.
		return reply(200, map[string]any{"access_token": token, "expires_in": "60", "resource": req.Query.Get("resource"), "token_type": "Bearer"})
	}
	return reply(404, map[string]any{"error": "not_found"})
}

func (f *fakeAAD) seen() []aadRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]aadRequest(nil), f.requests...)
}

func tokenPosts(reqs []aadRequest) []aadRequest {
	var out []aadRequest
	for _, r := range reqs {
		if r.Method == http.MethodPost && strings.HasSuffix(r.Path, "/oauth2/v2.0/token") {
			out = append(out, r)
		}
	}
	return out
}

func TestCertificateCredentialTalksOnlyToThePublicAuthorityAsTheConfiguredClient(t *testing.T) {
	isolateAzureEnv(t)
	t.Setenv("AZURE_AUTHORITY_HOST", "https://login.evil.example")
	t.Setenv("AZURE_TENANT_ID", otherUUID)
	t.Setenv("AZURE_CLIENT_ID", otherUUID)
	t.Setenv("AZURE_CLIENT_CERTIFICATE_PATH", "/nonexistent/ambient.pem")
	fake := &fakeAAD{}
	auth := certAuth(writeCertFile(t, time.Now().Add(365*24*time.Hour), 0600))
	cred, _, err := newCredentialBounded(t, auth, CredentialOptions{Transport: fake})
	if err != nil {
		t.Fatal(err)
	}
	if err := Preflight(context.Background(), cred, auth); err != nil {
		t.Fatalf("preflight through the fake authority: %v", err)
	}
	reqs := fake.seen()
	for _, r := range reqs {
		if r.Host != "login.microsoftonline.com" {
			t.Fatalf("credential left the public authority: %+v", r)
		}
	}
	posts := tokenPosts(reqs)
	if len(posts) != 2 {
		t.Fatalf("want one token request per scope, got %d in %+v", len(posts), reqs)
	}
	var scopes []string
	for _, p := range posts {
		if p.Path != "/"+testTenantID+"/oauth2/v2.0/token" || p.Form.Get("client_id") != testClientID || p.Form.Get("client_assertion") == "" {
			t.Fatalf("token request not issued as the configured identity: %+v", p)
		}
		scopes = append(scopes, p.Form.Get("scope"))
	}
	if !strings.Contains(strings.Join(scopes, " "), ServiceBusScope) || !strings.Contains(strings.Join(scopes, " "), LogAnalyticsScope) {
		t.Fatalf("token scopes %v", scopes)
	}
}

func TestWorkloadIdentityUsesConfiguredTokenFileNotTheEnvironment(t *testing.T) {
	isolateAzureEnv(t)
	t.Setenv("AZURE_AUTHORITY_HOST", "https://login.evil.example")
	t.Setenv("AZURE_FEDERATED_TOKEN_FILE", "/nonexistent/ambient-token")
	t.Setenv("AZURE_CLIENT_ID", otherUUID)
	t.Setenv("AZURE_TENANT_ID", otherUUID)
	tokenFile := filepath.Join(t.TempDir(), "token")
	const projected = "projected-service-account-token"
	if err := os.WriteFile(tokenFile, []byte(projected), 0644); err != nil {
		t.Fatal(err)
	}
	fake := &fakeAAD{}
	auth := AuthConfig{Mode: AuthModeWorkloadIdentity, TenantID: testTenantID, ClientID: testClientID, TokenFilePath: tokenFile}
	cred, _, err := newCredentialBounded(t, auth, CredentialOptions{Transport: fake})
	if err != nil {
		t.Fatal(err)
	}
	if err := Preflight(context.Background(), cred, auth); err != nil {
		t.Fatalf("preflight: %v", err)
	}
	reqs := fake.seen()
	for _, r := range reqs {
		if r.Host != "login.microsoftonline.com" {
			t.Fatalf("workload identity left the public authority: %+v", r)
		}
	}
	posts := tokenPosts(reqs)
	if len(posts) == 0 {
		t.Fatalf("no token request in %+v", reqs)
	}
	for _, p := range posts {
		if p.Form.Get("client_assertion") != projected || p.Form.Get("client_id") != testClientID || p.Path != "/"+testTenantID+"/oauth2/v2.0/token" {
			t.Fatalf("workload identity not using configured values: %+v", p)
		}
	}
}

func TestManagedIdentitySelectsUserAssignedOnlyWhenClientIDIsConfigured(t *testing.T) {
	for _, withClient := range []bool{false, true} {
		name := "system-assigned"
		if withClient {
			name = "user-assigned"
		}
		t.Run(name, func(t *testing.T) {
			isolateAzureEnv(t)
			fake := &fakeAAD{}
			auth := AuthConfig{Mode: AuthModeManagedIdentity, TenantID: testTenantID}
			if withClient {
				auth.ClientID = testClientID
			}
			cred, _, err := newCredentialBounded(t, auth, CredentialOptions{Transport: fake})
			if err != nil {
				t.Fatal(err)
			}
			if err := Preflight(context.Background(), cred, auth); err != nil {
				t.Fatalf("preflight: %v", err)
			}
			reqs := fake.seen()
			if len(reqs) == 0 {
				t.Fatal("no managed identity request reached the transport")
			}
			for _, r := range reqs {
				if r.Host != "169.254.169.254" {
					t.Fatalf("managed identity left the identity endpoint: %+v", r)
				}
				if got := r.Query.Get("client_id"); got != auth.ClientID {
					t.Fatalf("client_id %q, want %q", got, auth.ClientID)
				}
			}
		})
	}
}

type unreachable struct{}

func (unreachable) Do(*http.Request) (*http.Response, error) {
	return nil, errors.New("dial tcp 169.254.169.254:80: connect: connection refused")
}

func TestManagedIdentityOnAnUnsupportedHostNamesTheHostRequirement(t *testing.T) {
	isolateAzureEnv(t)
	auth := AuthConfig{Mode: AuthModeManagedIdentity, TenantID: testTenantID}
	cred, _, err := newCredentialBounded(t, auth, CredentialOptions{Transport: unreachable{}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	err = Preflight(ctx, cred, auth)
	expectMarker(t, err, markManagedHosts, markTenantMismatch, markAppMismatch, markClaims)
	if !strings.Contains(err.Error(), markRequestFailed) {
		t.Fatalf("unreachable endpoint is a failed token request: %v", err)
	}
}

func TestIgnoredAzureEnvListsSortedNamesAndNeverValues(t *testing.T) {
	got := IgnoredAzureEnv([]string{"PATH=/usr/bin", "AZURE_CLIENT_SECRET=hunter2", "AZURE_AUTHORITY_HOST=https://evil", "NOAZURE_X=1", "AZUREX=1", "azure_lower=1", "AZURE_EMPTY=", "AZURE_NOEQUALS", "AZURE_CLIENT_SECRET=dup", "HOME=/home/dev"})
	want := []string{"AZURE_AUTHORITY_HOST", "AZURE_CLIENT_SECRET", "AZURE_EMPTY", "AZURE_NOEQUALS"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("got %v, want %v", got, want)
	}
	if got := IgnoredAzureEnv([]string{"PATH=/usr/bin"}); got == nil || len(got) != 0 {
		t.Fatalf("no ambient variables must give an empty, non-nil list: %#v", got)
	}
}
