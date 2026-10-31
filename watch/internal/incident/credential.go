package incident

import (
	"context"
	"crypto"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/cloud"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
)

// Token scopes the worker requests. Both are acquired by Preflight before any
// other work starts; the telemetry adapter requests LogAnalyticsScope.
const (
	ServiceBusScope   = "https://servicebus.azure.net/.default"
	LogAnalyticsScope = "https://api.loganalytics.io/.default"
)

// Worker identity modes. There is deliberately no client-secret mode.
const (
	AuthModeCertificate      = "servicePrincipalCertificate"
	AuthModeManagedIdentity  = "managedIdentity"
	AuthModeWorkloadIdentity = "workloadIdentity"
)

const (
	maxCertificateBytes      = 256 << 10
	certificateWarningWindow = 30 * 24 * time.Hour
	preflightRequestTimeout  = 30 * time.Second
	azureEnvPrefix           = "AZURE_"
	certificateRequirement   = "mode 0600 or 0400, regular file owned by the worker"
)

// AuthConfig names the one identity the worker authenticates as. Which fields
// apply depends on Mode; a field of another mode is rejected, not ignored.
type AuthConfig struct {
	Mode            string `json:"mode"`
	TenantID        string `json:"tenantId"`
	ClientID        string `json:"clientId,omitempty"`
	CertificatePath string `json:"certificatePath,omitempty"`
	TokenFilePath   string `json:"tokenFilePath,omitempty"`
}

// CredentialOptions are Go-level seams only; nothing here is read from the
// configuration file, the environment or an alert.
type CredentialOptions struct {
	Transport policy.Transporter // nil means the SDK default HTTP transport
	Now       func() time.Time   // nil means time.Now
}

const authKeyMaxRunes = 64

var authKeyPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)

// authFieldName renders an unknown key for an error message. A plain key is
// shown as is; any other key is quoted (control characters escaped) and cut to
// 64 runes with an ellipsis, so an attacker-chosen name can neither forge log
// lines nor grow the message without bound.
func authFieldName(k string) string {
	if authKeyPattern.MatchString(k) {
		return k
	}
	runes := []rune(k)
	if len(runes) <= authKeyMaxRunes {
		return fmt.Sprintf("%q", k)
	}
	return fmt.Sprintf("%q...", string(runes[:authKeyMaxRunes]))
}

// UnmarshalJSON decodes the auth object with exact-case keys (encoding/json
// would otherwise match "ClientId" to clientId) and rejects unknown ones,
// naming only the key, never its value.
func (a *AuthConfig) UnmarshalJSON(data []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil || fields == nil {
		return fmt.Errorf("auth must be an object")
	}
	var out AuthConfig
	targets := map[string]*string{"mode": &out.Mode, "tenantId": &out.TenantID, "clientId": &out.ClientID, "certificatePath": &out.CertificatePath, "tokenFilePath": &out.TokenFilePath}
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		dst, ok := targets[k]
		if !ok {
			return fmt.Errorf("unknown field auth.%s", authFieldName(k))
		}
		if err := json.Unmarshal(fields[k], dst); err != nil {
			return fmt.Errorf("auth.%s must be a string", k)
		}
	}
	*a = out
	return nil
}

// Validate checks the closed set of modes, the fields each mode requires and
// the fields it forbids. Every message names the offending auth field.
func (a AuthConfig) Validate() error {
	switch a.Mode {
	case AuthModeCertificate, AuthModeManagedIdentity, AuthModeWorkloadIdentity:
	default:
		return fmt.Errorf("auth.mode must be %s, %s or %s", AuthModeCertificate, AuthModeManagedIdentity, AuthModeWorkloadIdentity)
	}
	if a.TenantID == "" {
		return fmt.Errorf("auth.tenantId is required")
	}
	if !uuidPattern.MatchString(a.TenantID) {
		return fmt.Errorf("auth.tenantId must be a UUID")
	}
	switch {
	case a.ClientID == "" && a.Mode != AuthModeManagedIdentity:
		return fmt.Errorf("auth.clientId is required for mode %s", a.Mode)
	case a.ClientID != "" && !uuidPattern.MatchString(a.ClientID):
		return fmt.Errorf("auth.clientId must be a UUID")
	}
	if err := checkAuthPath("certificatePath", a.CertificatePath, a.Mode, a.Mode == AuthModeCertificate); err != nil {
		return err
	}
	return checkAuthPath("tokenFilePath", a.TokenFilePath, a.Mode, a.Mode == AuthModeWorkloadIdentity)
}

func checkAuthPath(field, value, mode string, used bool) error {
	switch {
	case !used && value != "":
		return fmt.Errorf("auth.%s is not used by mode %s", field, mode)
	case used && value == "":
		return fmt.Errorf("auth.%s is required for mode %s", field, mode)
	case used && (!filepath.IsAbs(value) || strings.ContainsAny(value, "\x00\r\n")):
		return fmt.Errorf("auth.%s must be an absolute path", field)
	}
	return nil
}

// NewCredential builds the one credential the worker uses, from explicit
// options only: no environment variable, alert, telemetry, repository or agent
// output selects the identity, the tenant or the authority, and the Azure
// public cloud is pinned. The one environment the credential still reads is the
// host's own managed-identity endpoint (IDENTITY_ENDPOINT, IDENTITY_HEADER,
// IDENTITY_SERVER_THUMBPRINT, MSI_ENDPOINT, MSI_SECRET, IMDS_ENDPOINT), which
// MSAL follows for managedIdentity; the operator-controlled host environment is
// the trust boundary there. The warning is empty unless a certificate is close
// to expiry.
func NewCredential(auth AuthConfig, opts CredentialOptions) (azcore.TokenCredential, string, error) {
	if err := auth.Validate(); err != nil {
		return nil, "", err
	}
	client := azcore.ClientOptions{Cloud: cloud.AzurePublic, Transport: opts.Transport}
	switch auth.Mode {
	case AuthModeCertificate:
		return newCertificateCredential(auth, opts, client)
	case AuthModeManagedIdentity:
		o := &azidentity.ManagedIdentityCredentialOptions{ClientOptions: client}
		if auth.ClientID != "" {
			o.ID = azidentity.ClientID(auth.ClientID)
		}
		cred, err := azidentity.NewManagedIdentityCredential(o)
		if err != nil {
			return nil, "", fmt.Errorf("create managed identity credential: %w", err)
		}
		return cred, "", nil
	default:
		cred, err := azidentity.NewWorkloadIdentityCredential(&azidentity.WorkloadIdentityCredentialOptions{ClientOptions: client, ClientID: auth.ClientID, TenantID: auth.TenantID, TokenFilePath: auth.TokenFilePath})
		if err != nil {
			return nil, "", fmt.Errorf("create workload identity credential: %w", err)
		}
		return cred, "", nil
	}
}

func newCertificateCredential(auth AuthConfig, opts CredentialOptions, client azcore.ClientOptions) (azcore.TokenCredential, string, error) {
	data, err := readCertificateFile(auth.CertificatePath)
	if err != nil {
		return nil, "", err
	}
	certs, key, err := azidentity.ParseCertificates(data, nil)
	if err != nil || len(certs) == 0 || key == nil {
		// The parser's detail is withheld: it handled private key material.
		return nil, "", fmt.Errorf("certificate %s could not be parsed as an unencrypted PEM certificate with its private key", auth.CertificatePath)
	}
	now := time.Now
	if opts.Now != nil {
		now = opts.Now
	}
	leaf := certificateForKey(certs, key)
	if leaf == nil {
		return nil, "", fmt.Errorf("certificate %s has no certificate matching its private key", auth.CertificatePath)
	}
	notAfter := leaf.NotAfter.UTC().Format(time.RFC3339)
	var warning string
	switch remaining := leaf.NotAfter.Sub(now()); {
	case remaining <= 0:
		return nil, "", fmt.Errorf("certificate expired: %s NotAfter %s", auth.CertificatePath, notAfter)
	case remaining < certificateWarningWindow:
		warning = fmt.Sprintf("certificate %s expires within 30 days (NotAfter %s)", auth.CertificatePath, notAfter)
	}
	cred, err := azidentity.NewClientCertificateCredential(auth.TenantID, auth.ClientID, certs, key, &azidentity.ClientCertificateCredentialOptions{ClientOptions: client})
	if err != nil {
		return nil, "", fmt.Errorf("create certificate credential: %w", err)
	}
	return cred, warning, nil
}

// certificateForKey returns the certificate whose public key is the private
// key's. azidentity.ParseCertificates lists certificates in file order, so the
// first one need not be the leaf; nil means none matches.
func certificateForKey(certs []*x509.Certificate, key crypto.PrivateKey) *x509.Certificate {
	signer, ok := key.(interface{ Public() crypto.PublicKey })
	if !ok {
		return nil
	}
	pub := signer.Public()
	for _, c := range certs {
		if eq, ok := c.PublicKey.(interface{ Equal(crypto.PublicKey) bool }); ok && eq.Equal(pub) {
			return c
		}
	}
	return nil
}

// unixMode renders a file mode as its unix octal permission and special bits,
// so a setuid 0600 file reads 04600 rather than a bare 0600. Plain modes keep
// the four-digit form (0644).
func unixMode(m os.FileMode) string {
	bits := uint32(m.Perm())
	for flag, bit := range map[os.FileMode]uint32{os.ModeSetuid: 04000, os.ModeSetgid: 02000, os.ModeSticky: 01000} {
		if m&flag != 0 {
			bits |= bit
		}
	}
	if bits > 0777 {
		return fmt.Sprintf("0%o", bits)
	}
	return fmt.Sprintf("%04o", bits)
}

// readCertificateFile opens without following symbolic links, then judges the
// open descriptor (so nothing can change between check and read) before a
// single byte is parsed.
func readCertificateFile(path string) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		if errors.Is(err, syscall.ELOOP) {
			return nil, fmt.Errorf("certificate %s is not a regular file (symbolic link); required: %s", path, certificateRequirement)
		}
		return nil, fmt.Errorf("certificate unreadable: %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("certificate unreadable: %s: %w", path, err)
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("certificate %s is not a regular file; required: %s", path, certificateRequirement)
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return nil, fmt.Errorf("certificate unreadable: %s: owner cannot be determined", path)
	}
	if uid := os.Getuid(); int(st.Uid) != uid {
		return nil, fmt.Errorf("certificate %s is owned by uid %d, the worker runs as uid %d; required: %s", path, st.Uid, uid, certificateRequirement)
	}
	if mode := fi.Mode() & (os.ModePerm | os.ModeSetuid | os.ModeSetgid | os.ModeSticky); mode != 0600 && mode != 0400 {
		return nil, fmt.Errorf("certificate %s has mode %s; required: %s", path, unixMode(mode), certificateRequirement)
	}
	data, err := io.ReadAll(io.LimitReader(f, maxCertificateBytes+1))
	if err != nil {
		return nil, fmt.Errorf("certificate unreadable: %s: %w", path, err)
	}
	if len(data) > maxCertificateBytes {
		return nil, fmt.Errorf("certificate %s is too large (limit %d bytes)", path, maxCertificateBytes)
	}
	return data, nil
}

// Preflight acquires one token per scope, each request bounded to 30 seconds,
// and checks that the token was issued for the configured tenant and, when a
// client is configured, application. Any failure is final: the caller must not
// start work. Token text is never part of an error.
func Preflight(ctx context.Context, cred azcore.TokenCredential, auth AuthConfig) error {
	for _, target := range []struct{ name, scope string }{{"Service Bus", ServiceBusScope}, {"Log Analytics", LogAnalyticsScope}} {
		if err := preflightScope(ctx, cred, auth, target.name, target.scope); err != nil {
			return err
		}
	}
	return nil
}

func preflightScope(ctx context.Context, cred azcore.TokenCredential, auth AuthConfig, name, scope string) error {
	ctx, cancel := context.WithTimeout(ctx, preflightRequestTimeout)
	defer cancel()
	token, err := cred.GetToken(ctx, policy.TokenRequestOptions{Scopes: []string{scope}})
	if err != nil {
		hint := ""
		if auth.Mode == AuthModeManagedIdentity {
			hint = "; managed identity works only on supported Azure hosts"
		}
		return fmt.Errorf("worker identity preflight: token request failed for %s: %w%s", name, err, hint)
	}
	tid, appid, err := tokenClaims(token.Token)
	if err != nil {
		return fmt.Errorf("worker identity preflight: unreadable token claims for %s", name)
	}
	if !sameUUID(tid, auth.TenantID) {
		return fmt.Errorf("worker identity preflight: token tenant mismatch for %s: tid claim does not match auth.tenantId%s", name, missingClaimNote(tid, "tid"))
	}
	if auth.ClientID != "" && !sameUUID(appid, auth.ClientID) {
		return fmt.Errorf("worker identity preflight: token application mismatch for %s: appid claim does not match auth.clientId%s", name, missingClaimNote(appid, "appid"))
	}
	return nil
}

// missingClaimNote tells a missing claim apart from a mismatching one.
func missingClaimNote(claim, name string) string {
	if claim == "" {
		return " (no " + name + " claim)"
	}
	return ""
}

func sameUUID(claim, want string) bool {
	return uuidPattern.MatchString(claim) && strings.EqualFold(claim, want)
}

// tokenClaims reads the tid and appid claims without verifying the signature:
// the token came from the credential over TLS, so this is an identity sanity
// check, not authentication.
func tokenClaims(token string) (tid, appid string, err error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", "", errors.New("not a JWT")
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return "", "", err
	}
	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		return "", "", err
	}
	tid, _ = claims["tid"].(string)
	appid, _ = claims["appid"].(string)
	return tid, appid, nil
}

// IgnoredAzureEnv returns the sorted, de-duplicated names (never values) of the
// AZURE_-prefixed variables in an environment listing.
func IgnoredAzureEnv(environ []string) []string {
	seen := map[string]bool{}
	names := make([]string, 0)
	for _, kv := range environ {
		name, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(name, azureEnvPrefix) && !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}
