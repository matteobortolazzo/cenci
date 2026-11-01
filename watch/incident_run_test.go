package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	azlog "github.com/Azure/azure-sdk-for-go/sdk/azcore/log"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/incident"
)

const (
	incTenantID = "11111111-1111-4111-8111-111111111111"
	incClientID = "22222222-2222-4222-8222-222222222222"
	incOtherID  = "33333333-3333-4333-8333-333333333333"
	// incObjectID is the principal object ID the fake authority puts in every token.
	incObjectID = "44444444-4444-4444-8444-444444444444"
)

// incIsolateEnv removes every ambient Azure identity variable, so a test sees
// only the ones it sets itself. t.Setenv registers the restore; the Unsetenv makes
// the variable truly absent.
func incIsolateEnv(t *testing.T) {
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
	incKeyOnce sync.Once
	incKey     *rsa.PrivateKey
)

// incCertificateFile writes an unencrypted PEM holding a self-signed certificate
// expiring at notAfter and its private key, with mode 0600 regardless of umask.
func incCertificateFile(t *testing.T, notAfter time.Time) string {
	t.Helper()
	incKeyOnce.Do(func() {
		k, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			t.Fatal(err)
		}
		incKey = k
	})
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "incident-worker"}, NotBefore: notAfter.Add(-48 * time.Hour), NotAfter: notAfter}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &incKey.PublicKey, incKey)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(incKey)
	if err != nil {
		t.Fatal(err)
	}
	data := append(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})...)
	path := filepath.Join(t.TempDir(), "worker.pem")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func incJWT(claims map[string]any) string {
	enc := base64.RawURLEncoding
	b, err := json.Marshal(claims)
	if err != nil {
		panic(err)
	}
	return enc.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`)) + "." + enc.EncodeToString(b) + "." + enc.EncodeToString([]byte("sig"))
}

func incClaim(t *testing.T, token, name string) string {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("not a JWT: %q", token)
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		t.Fatal(err)
	}
	value, _ := claims[name].(string)
	return value
}

type incAuthorityRequest struct {
	Method, Host, Path string
	Form               url.Values
}

// incAuthority answers the Microsoft Entra ID metadata and token endpoints with
// tokens carrying the tenant and application the request named, and records
// every request. Any other host or path is recorded and answered 404.
type incAuthority struct {
	mu           sync.Mutex
	requests     []incAuthorityRequest
	tenantClaim  string // when set, issued tokens carry this tid instead of the requested tenant
	rejectTokens bool   // answer every token request with invalid_client
}

func (f *incAuthority) Do(r *http.Request) (*http.Response, error) {
	req := incAuthorityRequest{Method: r.Method, Host: r.URL.Host, Path: r.URL.Path}
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
			"tenant_discovery_endpoint": "https://login.microsoftonline.com/" + incTenantID + "/v2.0/.well-known/openid-configuration",
			"api-version":               "1.1",
			"metadata":                  []any{map[string]any{"preferred_network": "login.microsoftonline.com", "preferred_cache": "login.windows.net", "aliases": []string{"login.microsoftonline.com", "login.windows.net"}}},
		})
	case r.URL.Host == "login.microsoftonline.com" && strings.HasSuffix(r.URL.Path, "/v2.0/.well-known/openid-configuration"):
		base := "https://login.microsoftonline.com/" + segments[0]
		return reply(200, map[string]any{"authorization_endpoint": base + "/oauth2/v2.0/authorize", "token_endpoint": base + "/oauth2/v2.0/token", "issuer": base + "/v2.0"})
	case r.URL.Host == "login.microsoftonline.com" && r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/oauth2/v2.0/token"):
		if f.rejectTokens {
			return reply(401, map[string]any{"error": "invalid_client", "error_description": "AADSTS7000215: invalid client assertion"})
		}
		tid := segments[0]
		if f.tenantClaim != "" {
			tid = f.tenantClaim
		}
		token := incJWT(map[string]any{"tid": tid, "appid": req.Form.Get("client_id"), "oid": incObjectID})
		return reply(200, map[string]any{"token_type": "Bearer", "expires_in": 3600, "ext_expires_in": 3600, "access_token": token})
	}
	return reply(404, map[string]any{"error": "not_found"})
}

func (f *incAuthority) seen() []incAuthorityRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]incAuthorityRequest(nil), f.requests...)
}

type incRoundTrip func(*http.Request) (*http.Response, error)

func (f incRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// incIdleReceiver stands in for the Service Bus receiver: it never delivers.
type incIdleReceiver struct{}

func (incIdleReceiver) Receive(ctx context.Context) ([]incident.Delivery, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}
func (incIdleReceiver) Close(context.Context) error { return nil }

// incFixture wires startIncident to a fake identity authority, a fake receiver
// factory, a fake telemetry endpoint, and recording container-runtime and az
// fakes first on PATH.
type incFixture struct {
	cfg          incident.Config
	deps         incidentDeps
	authority    *incAuthority
	stderr       *bytes.Buffer
	runtimeTrace string
	azTrace      string

	receiverCalls int
	brokerToken   string // token the receiver factory obtained from the credential it was given
	telemetryAuth string // Authorization header the telemetry request carried
}

func incSetup(t *testing.T, certNotAfter time.Time) *incFixture {
	t.Helper()
	incIsolateEnv(t)
	bin := t.TempDir()
	f := &incFixture{authority: &incAuthority{}, stderr: &bytes.Buffer{}, runtimeTrace: filepath.Join(bin, "runtime-calls"), azTrace: filepath.Join(bin, "az-calls")}
	for name, trace := range map[string]string{"podman": f.runtimeTrace, "az": f.azTrace} {
		exit := "0"
		if name == "az" {
			exit = "1" // any az call would fail the identity, and is recorded
		}
		script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> '" + trace + "'\nexit " + exit + "\n"
		if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	f.cfg = incident.Config{Enabled: true, StateDir: t.TempDir(), Namespace: "contoso.servicebus.windows.net", Queue: "alerts", Concurrency: 1, TimeoutSeconds: 30, MaxAttempts: 2, BudgetUSD: 1,
		Agent:     incident.ContainerConfig{Runtime: "podman", Image: "tools:reviewed", APIKeyEnv: "ANTHROPIC_API_KEY", Network: "incident-internal", ProxyURL: "http://proxy:3128"},
		Auth:      &incident.AuthConfig{Mode: incident.AuthModeCertificate, TenantID: incTenantID, ClientID: incClientID, CertificatePath: incCertificateFile(t, certNotAfter)},
		Resources: []incident.Resource{{ID: "/subscriptions/s/resourceGroups/r/providers/Microsoft.Web/sites/app", Repo: "owner/repo", Workspace: "00000000-0000-0000-0000-000000000000", Query: "AppExceptions | take 10"}}}
	f.deps = incidentDeps{
		transport: f.authority,
		stderr:    f.stderr,
		newReceiver: func(_ incident.Config, cred azcore.TokenCredential) (incidentReceiver, error) {
			f.receiverCalls++
			token, err := cred.GetToken(context.Background(), policy.TokenRequestOptions{Scopes: []string{incident.ServiceBusScope}})
			if err != nil {
				return nil, err
			}
			f.brokerToken = token.Token
			return incIdleReceiver{}, nil
		},
		telemetryClient: &http.Client{Transport: incRoundTrip(func(r *http.Request) (*http.Response, error) {
			f.telemetryAuth = r.Header.Get("Authorization")
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"tables":[]}`))}, nil
		})},
	}
	return f
}

func incReadTrace(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func incFarFuture() time.Time { return time.Now().Add(365 * 24 * time.Hour) }

func TestIncidentStartupAuthenticatesBothBoundariesAsTheConfiguredIdentityWithoutAz(t *testing.T) {
	f := incSetup(t, incFarFuture())
	// Ambient credentials that would select another identity if anything read them.
	t.Setenv("AZURE_TENANT_ID", incOtherID)
	t.Setenv("AZURE_CLIENT_ID", incOtherID)
	t.Setenv("AZURE_CLIENT_SECRET", "ambient-secret")
	ctx := context.Background()

	run, err := startIncident(ctx, f.cfg, f.deps)
	if err != nil {
		t.Fatal(err)
	}
	defer run.close()

	if f.receiverCalls != 1 {
		t.Fatalf("receiver built %d times, want once", f.receiverCalls)
	}
	if got := incClaim(t, f.brokerToken, "appid"); got != incClientID {
		t.Fatalf("broker boundary received a token for application %q, want %q", got, incClientID)
	}
	if _, err := run.pipeline.Telemetry.Collect(ctx, incident.Incident{FiredAt: time.Now()}, f.cfg.Resources[0]); err != nil {
		t.Fatal(err)
	}
	if got := incClaim(t, strings.TrimPrefix(f.telemetryAuth, "Bearer "), "appid"); got != incClientID {
		t.Fatalf("telemetry boundary sent a token for application %q (header %q), want %q", got, f.telemetryAuth, incClientID)
	}
	if got := incReadTrace(t, f.azTrace); got != "" {
		t.Fatalf("az was invoked: %s", got)
	}
	posts := 0
	for _, r := range f.authority.seen() {
		if r.Host != "login.microsoftonline.com" {
			t.Fatalf("credential left the public authority: %+v", r)
		}
		if r.Method == http.MethodPost && strings.HasSuffix(r.Path, "/oauth2/v2.0/token") {
			posts++
			if r.Path != "/"+incTenantID+"/oauth2/v2.0/token" || r.Form.Get("client_id") != incClientID {
				t.Fatalf("token requested as another identity: %+v", r)
			}
		}
	}
	if posts == 0 {
		t.Fatal("no token request reached the identity authority")
	}
}

func TestIncidentPreflightFailureStartsNothing(t *testing.T) {
	cases := []struct {
		name   string
		set    func(*incAuthority)
		marker string
	}{
		{"token-request-rejected", func(a *incAuthority) { a.rejectTokens = true }, "token request failed"},
		{"tenant-mismatch", func(a *incAuthority) { a.tenantClaim = incOtherID }, "token tenant mismatch"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := incSetup(t, incFarFuture())
			tc.set(f.authority)
			// A job interrupted by a crashed worker: recovery would requeue it.
			key := "ca978112ca1bbdcafac231b39a23dc4da786eff8147c4e72b9807785afee48bb"
			ledger := filepath.Join(f.cfg.StateDir, "incidents.json")
			seeded := `{"version":1,"incidents":{"` + key + `":{"key":"` + key + `","alertId":"a","status":"running"}}}`
			if err := os.WriteFile(ledger, []byte(seeded), 0600); err != nil {
				t.Fatal(err)
			}

			run, err := startIncident(context.Background(), f.cfg, f.deps)
			if err == nil {
				run.close()
				t.Fatal("startup succeeded with a failing identity")
			}
			if !strings.Contains(err.Error(), "worker identity preflight") || !strings.Contains(err.Error(), tc.marker) {
				t.Fatalf("error must name the preflight and %q: %v", tc.marker, err)
			}
			if got := incReadTrace(t, f.runtimeTrace); got != "" {
				t.Fatalf("container runtime invoked before the preflight passed: %s", got)
			}
			if _, err := os.Stat(filepath.Join(f.cfg.StateDir, "owner-id")); !os.IsNotExist(err) {
				t.Fatalf("container cleanup started (owner-id present): %v", err)
			}
			b, err := os.ReadFile(ledger)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(b), `"status":"running"`) || strings.Contains(string(b), `"status":"queued"`) {
				t.Fatalf("ledger recovery ran before the preflight passed: %s", b)
			}
			if f.receiverCalls != 0 {
				t.Fatalf("receiver built %d times before the preflight passed", f.receiverCalls)
			}
			// The worker lock taken for the preflight must not outlive the failure.
			lock, err := incident.NewStore(f.cfg.StateDir).WorkerLock()
			if err != nil {
				t.Fatalf("worker lock leaked by the failed startup: %v", err)
			}
			_ = lock.Close()
		})
	}
}

func TestIncidentStartupReportsAndRemovesAmbientAzureVariables(t *testing.T) {
	f := incSetup(t, incFarFuture())
	ambient := map[string]string{
		"AZURE_CLIENT_SECRET":  "s3cret-client-secret-value",
		"AZURE_AUTHORITY_HOST": "https://login.evil.example",
		"AZURE_SDK_GO_LOGGING": "all-leaky-logging-value",
		"AZURE_TENANT_ID":      incOtherID,
	}
	for name, value := range ambient {
		t.Setenv(name, value)
	}

	run, err := startIncident(context.Background(), f.cfg, f.deps)
	if err != nil {
		t.Fatal(err)
	}
	defer run.close()

	notices := 0
	for _, line := range strings.Split(f.stderr.String(), "\n") {
		if strings.Contains(line, "AZURE_") {
			notices++
		}
	}
	if notices != 1 {
		t.Fatalf("want exactly one notice naming the variables, got %d in: %q", notices, f.stderr.String())
	}
	for name, value := range ambient {
		if !strings.Contains(f.stderr.String(), name) {
			t.Fatalf("notice does not name %s: %q", name, f.stderr.String())
		}
		if strings.Contains(f.stderr.String(), value) {
			t.Fatalf("notice leaks the value of %s: %q", name, f.stderr.String())
		}
		if got, set := os.LookupEnv(name); set {
			t.Fatalf("%s is still in the worker environment (%q)", name, got)
		}
	}
	for _, r := range f.authority.seen() {
		if r.Host != "login.microsoftonline.com" {
			t.Fatalf("hostile AZURE_AUTHORITY_HOST changed the authority: %+v", r)
		}
	}
}

// The Azure SDK reads AZURE_SDK_GO_LOGGING in a package init, before main, so
// removing the variable cannot disable a logger the init already installed. The
// test binary's init ran without the variable, so a recording listener stands in
// for the logger the init would have installed; startup must clear it, or every
// request the credential makes would be written out.
func TestIncidentStartupDisablesTheAmbientSDKLogger(t *testing.T) {
	f := incSetup(t, incFarFuture())
	var mu sync.Mutex
	var recorded []string
	azlog.SetListener(func(event azlog.Event, message string) {
		mu.Lock()
		defer mu.Unlock()
		recorded = append(recorded, string(event))
	})
	t.Cleanup(func() { azlog.SetListener(nil) })
	t.Setenv("AZURE_SDK_GO_LOGGING", "all")

	run, err := startIncident(context.Background(), f.cfg, f.deps)
	if err != nil {
		t.Fatal(err)
	}
	defer run.close()

	if len(f.authority.seen()) == 0 {
		t.Fatal("startup made no identity request, so the logger was never exercised")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(recorded) != 0 {
		t.Fatalf("the SDK logger survived the removal of AZURE_SDK_GO_LOGGING: %d events recorded", len(recorded))
	}
}

func TestIncidentStartupIsSilentWithoutAmbientAzureVariablesOrNearExpiry(t *testing.T) {
	f := incSetup(t, incFarFuture())
	run, err := startIncident(context.Background(), f.cfg, f.deps)
	if err != nil {
		t.Fatal(err)
	}
	defer run.close()
	if f.stderr.Len() != 0 {
		t.Fatalf("unexpected stderr output: %q", f.stderr.String())
	}
}

func TestIncidentStartupWarnsWhenTheCertificateExpiresSoon(t *testing.T) {
	notAfter := time.Now().Add(10 * 24 * time.Hour)
	f := incSetup(t, notAfter)
	run, err := startIncident(context.Background(), f.cfg, f.deps)
	if err != nil {
		t.Fatalf("a certificate inside the warning window must still start: %v", err)
	}
	defer run.close()
	got := f.stderr.String()
	if !strings.Contains(got, "warning") || !strings.Contains(got, "30 days") || !strings.Contains(got, notAfter.UTC().Format("2006-01-02")) {
		t.Fatalf("warning must state the window and carry the NotAfter date: %q", got)
	}
}

func TestIncidentStartupTakesTheWorkerLockBeforeAnyTokenRequest(t *testing.T) {
	f := incSetup(t, incFarFuture())
	t.Setenv("AZURE_CLIENT_SECRET", "ambient-secret-value")
	held, err := incident.NewStore(f.cfg.StateDir).WorkerLock()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = held.Close() }()

	run, err := startIncident(context.Background(), f.cfg, f.deps)
	if err == nil {
		run.close()
		t.Fatal("startup succeeded while another worker holds the lock")
	}
	if !strings.Contains(err.Error(), "incident worker already running") || strings.Contains(err.Error(), "worker identity preflight") {
		t.Fatalf("startup must fail on the lock, not the preflight: %v", err)
	}
	if reqs := f.authority.seen(); len(reqs) != 0 {
		t.Fatalf("a second worker spent %d identity requests before taking the lock: %+v", len(reqs), reqs)
	}
	if f.receiverCalls != 0 {
		t.Fatalf("receiver built %d times by a worker that does not own the lock", f.receiverCalls)
	}
	if got := incReadTrace(t, f.runtimeTrace); got != "" {
		t.Fatalf("container runtime invoked by a worker that does not own the lock: %s", got)
	}
	// The variables are removed before the lock is attempted.
	if _, set := os.LookupEnv("AZURE_CLIENT_SECRET"); set || !strings.Contains(f.stderr.String(), "AZURE_CLIENT_SECRET") {
		t.Fatalf("ambient variable not reported and removed before the lock (set=%v): %q", set, f.stderr.String())
	}
}

// The credential (and so the certificate checks) is built before the worker lock
// is taken. With the lock held elsewhere and a certificate at a loose mode,
// startup must report the certificate failure, not "already running": the lock
// is only attempted after the credential exists.
func TestIncidentStartupBuildsTheCredentialBeforeTakingTheWorkerLock(t *testing.T) {
	f := incSetup(t, incFarFuture())
	if err := os.Chmod(f.cfg.Auth.CertificatePath, 0644); err != nil {
		t.Fatal(err)
	}
	held, err := incident.NewStore(f.cfg.StateDir).WorkerLock()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = held.Close() }()

	run, err := startIncident(context.Background(), f.cfg, f.deps)
	if err == nil {
		run.close()
		t.Fatal("startup succeeded with a loose certificate while another worker holds the lock")
	}
	if strings.Contains(err.Error(), "already running") {
		t.Fatalf("the lock was attempted before the credential was built: %v", err)
	}
	if !strings.Contains(err.Error(), f.cfg.Auth.CertificatePath) || !strings.Contains(err.Error(), "has mode 0644") || !strings.Contains(err.Error(), "required: mode 0600 or 0400") {
		t.Fatalf("want the certificate failure naming the path and the required mode: %v", err)
	}
}

// A failure while the credential is built (the certificate checks) must find the
// ambient AZURE_* variables already reported and removed, and must reach no
// identity endpoint. The success path cannot show the removal order: the SDK
// credentials built from explicit options read no AZURE_* variable either way,
// and a seam to observe that would exist only for this test. The worker lock is
// released by the failure path; the order of credential and lock is pinned by
// TestIncidentStartupBuildsTheCredentialBeforeTakingTheWorkerLock.
func TestIncidentCredentialBuildFailureStillFindsAmbientVariablesRemoved(t *testing.T) {
	cases := []struct {
		name   string
		expire time.Time
		chmod  os.FileMode
		marker string
	}{
		{"mode-too-open", incFarFuture(), 0644, "has mode"},
		{"expired", time.Now().Add(-time.Hour), 0600, "certificate expired"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := incSetup(t, tc.expire)
			if err := os.Chmod(f.cfg.Auth.CertificatePath, tc.chmod); err != nil {
				t.Fatal(err)
			}
			ambient := map[string]string{
				"AZURE_CLIENT_SECRET":  "hostile-secret-value",
				"AZURE_AUTHORITY_HOST": "https://login.evil.example",
				"AZURE_CLIENT_ID":      incOtherID,
			}
			for name, value := range ambient {
				t.Setenv(name, value)
			}

			run, err := startIncident(context.Background(), f.cfg, f.deps)
			if err == nil {
				run.close()
				t.Fatal("startup succeeded with an unusable certificate")
			}
			if !strings.Contains(err.Error(), tc.marker) {
				t.Fatalf("want the certificate failure %q: %v", tc.marker, err)
			}
			for name, value := range ambient {
				if !strings.Contains(f.stderr.String(), name) || strings.Contains(f.stderr.String(), value) {
					t.Fatalf("notice must name %s and never its value: %q", name, f.stderr.String())
				}
				if got, set := os.LookupEnv(name); set {
					t.Fatalf("%s survived the failed credential build (%q)", name, got)
				}
			}
			if reqs := f.authority.seen(); len(reqs) != 0 {
				t.Fatalf("identity endpoints contacted after a failed credential build: %+v", reqs)
			}
			if got := incReadTrace(t, f.runtimeTrace); got != "" || f.receiverCalls != 0 {
				t.Fatalf("work started after a failed credential build: runtime=%q receivers=%d", got, f.receiverCalls)
			}
			if lock, err := incident.NewStore(f.cfg.StateDir).WorkerLock(); err != nil {
				t.Fatalf("the worker lock is still held after the failed startup: %v", err)
			} else {
				_ = lock.Close()
			}
		})
	}
}
