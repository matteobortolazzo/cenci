package main

import (
	"bytes"
	"context"
	"encoding/pem"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/messaging/azservicebus"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/incident"
)

const (
	verifyRowMarker  = "ROW-CONTENT-MARKER"
	verifyBodyMarker = "MESSAGE-BODY-MARKER"
	verifyWorkspace  = "00000000-0000-0000-0000-000000000000"
	verifyResourceID = "/subscriptions/s/resourceGroups/r/providers/Microsoft.Web/sites/app"
)

// verifyPeeker is the broker boundary: it answers PeekMessages with the messages
// it was given and fails the test on any receive or settlement call.
type verifyPeeker struct {
	t        *testing.T
	messages []*azservicebus.ReceivedMessage
	peeks    int
}

func (p *verifyPeeker) PeekMessages(_ context.Context, _ int, _ *azservicebus.PeekMessagesOptions) ([]*azservicebus.ReceivedMessage, error) {
	p.peeks++
	return p.messages, nil
}
func (p *verifyPeeker) ReceiveMessages(context.Context, int, *azservicebus.ReceiveMessagesOptions) ([]*azservicebus.ReceivedMessage, error) {
	p.t.Error("verify received a message")
	return nil, nil
}
func (p *verifyPeeker) RenewMessageLock(context.Context, *azservicebus.ReceivedMessage, *azservicebus.RenewMessageLockOptions) error {
	p.t.Error("verify renewed a lock")
	return nil
}
func (p *verifyPeeker) CompleteMessage(context.Context, *azservicebus.ReceivedMessage, *azservicebus.CompleteMessageOptions) error {
	p.t.Error("verify completed a message")
	return nil
}
func (p *verifyPeeker) AbandonMessage(context.Context, *azservicebus.ReceivedMessage, *azservicebus.AbandonMessageOptions) error {
	p.t.Error("verify abandoned a message")
	return nil
}
func (p *verifyPeeker) DeadLetterMessage(context.Context, *azservicebus.ReceivedMessage, *azservicebus.DeadLetterOptions) error {
	p.t.Error("verify dead-lettered a message")
	return nil
}
func (p *verifyPeeker) Close(context.Context) error { return nil }

// verifyRig wires verifyIncident to the incSetup fixture: the fake identity
// authority as the credential transport, a fake Log Analytics endpoint and a
// fake broker.
type verifyRig struct {
	*incFixture
	deps        verifyDeps
	stdout      *bytes.Buffer
	broker      *verifyPeeker
	queryStatus int
	bearer      string // the Authorization header the telemetry request carried
}

func newVerifyRig(t *testing.T) *verifyRig {
	t.Helper()
	f := incSetup(t, incFarFuture())
	r := &verifyRig{incFixture: f, stdout: &bytes.Buffer{}, queryStatus: http.StatusOK}
	r.broker = &verifyPeeker{t: t, messages: []*azservicebus.ReceivedMessage{{Body: []byte(verifyBodyMarker)}}}
	r.deps = verifyDeps{
		transport: f.authority,
		telemetryClient: &http.Client{Transport: incRoundTrip(func(req *http.Request) (*http.Response, error) {
			r.bearer = req.Header.Get("Authorization")
			body := `{"tables":[{"name":"PrimaryResult","rows":[["` + verifyRowMarker + `"]]}]}`
			return &http.Response{StatusCode: r.queryStatus, Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
		})},
		openBroker: func(incident.Config, azcore.TokenCredential) (incident.ServiceBusReceiver, error) {
			return r.broker, nil
		},
		stdout: r.stdout,
		stderr: f.stderr,
	}
	return r
}

func (r *verifyRig) run() int {
	return verifyIncident(context.Background(), r.cfg, r.deps, 0)
}

func requireStdoutLine(t *testing.T, out, want string) {
	t.Helper()
	for _, l := range strings.Split(out, "\n") {
		if l == want {
			return
		}
	}
	t.Fatalf("missing line %q in:\n%s", want, out)
}

func TestIncidentVerifyAuthenticatesAsTheConfiguredIdentityWithoutAzOrFiles(t *testing.T) {
	r := newVerifyRig(t)
	// Ambient credentials that would select another identity if anything read them.
	t.Setenv("AZURE_TENANT_ID", incOtherID)
	t.Setenv("AZURE_CLIENT_ID", incOtherID)
	t.Setenv("AZURE_CLIENT_SECRET", "ambient-secret-value")

	if code := r.run(); code != 0 {
		t.Fatalf("exit %d, want 0; stdout:\n%s\nstderr:\n%s", code, r.stdout, r.stderr)
	}

	requireStdoutLine(t, r.stdout.String(), "identity: tenant "+incTenantID+", application "+incClientID+", principal "+incObjectID)
	requireStdoutLine(t, r.stdout.String(), "workspace "+verifyWorkspace+" ("+verifyResourceID+"): ok (1 rows)")
	requireStdoutLine(t, r.stdout.String(), "broker alerts: ok (1 messages visible)")

	posts := 0
	for _, req := range r.authority.seen() {
		if req.Host != "login.microsoftonline.com" {
			t.Fatalf("credential left the public authority: %+v", req)
		}
		if req.Method == http.MethodPost && strings.HasSuffix(req.Path, "/oauth2/v2.0/token") {
			posts++
			if req.Path != "/"+incTenantID+"/oauth2/v2.0/token" || req.Form.Get("client_id") != incClientID {
				t.Fatalf("token requested as another identity: %+v", req)
			}
			assertion := req.Form.Get("client_assertion")
			if assertion == "" || strings.Contains(r.stdout.String()+r.stderr.String(), assertion) {
				t.Fatalf("certificate assertion missing or printed")
			}
		}
	}
	if posts == 0 {
		t.Fatal("no token request reached the identity authority")
	}
	if got := incClaim(t, strings.TrimPrefix(r.bearer, "Bearer "), "appid"); got != incClientID {
		t.Fatalf("telemetry probe sent a token for application %q, want %q", got, incClientID)
	}

	// Neither stream carries the token, the certificate, the key or what the probes read.
	out := r.stdout.String() + r.stderr.String()
	secrets := []string{strings.TrimPrefix(r.bearer, "Bearer "), "ambient-secret-value", verifyRowMarker, verifyBodyMarker, "BEGIN CERTIFICATE", "PRIVATE KEY"}
	pemBytes, err := os.ReadFile(r.cfg.Auth.CertificatePath)
	if err != nil {
		t.Fatal(err)
	}
	for rest := pemBytes; ; {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		secrets = append(secrets, strings.Split(strings.TrimSpace(string(pem.EncodeToMemory(block))), "\n")[1])
	}
	for _, secret := range secrets {
		if secret == "" || strings.Contains(out, secret) {
			t.Fatalf("output carries %q:\n%s", secret, out)
		}
	}

	// Ambient variables are reported by name and removed, as in run.
	for _, name := range []string{"AZURE_TENANT_ID", "AZURE_CLIENT_ID", "AZURE_CLIENT_SECRET"} {
		if !strings.Contains(r.stderr.String(), name) {
			t.Fatalf("notice does not name %s: %q", name, r.stderr)
		}
		if _, set := os.LookupEnv(name); set {
			t.Fatalf("%s still set after verify", name)
		}
	}

	if got := incReadTrace(t, r.azTrace); got != "" {
		t.Fatalf("az was invoked: %s", got)
	}
	if got := incReadTrace(t, r.runtimeTrace); got != "" {
		t.Fatalf("container runtime was invoked: %s", got)
	}
	entries, err := os.ReadDir(r.cfg.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("verify wrote into the state directory: %v", entries)
	}
	if r.broker.peeks != 1 {
		t.Fatalf("broker peeked %d times, want once", r.broker.peeks)
	}
}

func TestIncidentVerifyRunsNextToALiveWorker(t *testing.T) {
	r := newVerifyRig(t)
	held, err := incident.NewStore(r.cfg.StateDir).WorkerLock()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = held.Close() }()

	if code := r.run(); code != 0 {
		t.Fatalf("exit %d, want 0 while the worker lock is held; stderr:\n%s", code, r.stderr)
	}
}

func TestIncidentVerifyExitsOneWhenAnyProbeFails(t *testing.T) {
	cases := []struct {
		name   string
		set    func(*verifyRig)
		stdout string // a line verify must print
		stderr string // a substring stderr must carry, when not empty
	}{
		{"workspace-denied", func(r *verifyRig) { r.queryStatus = http.StatusForbidden },
			"workspace " + verifyWorkspace + " (" + verifyResourceID + "): authorization-denied", ""},
		{"workspace-not-found", func(r *verifyRig) { r.queryStatus = http.StatusNotFound },
			"workspace " + verifyWorkspace + " (" + verifyResourceID + "): resource-not-found", ""},
		{"broker-unreachable", func(r *verifyRig) {
			r.deps.openBroker = func(incident.Config, azcore.TokenCredential) (incident.ServiceBusReceiver, error) {
				return nil, &url.Error{Op: "dial", URL: "amqps://contoso.servicebus.windows.net", Err: io.ErrUnexpectedEOF}
			}
		}, "broker alerts: unreachable", ""},
		{"token-rejected", func(r *verifyRig) { r.authority.rejectTokens = true },
			"identity (Service Bus): authentication-failed", "token request failed for Service Bus"},
		{"tenant-mismatch", func(r *verifyRig) { r.authority.tenantClaim = incOtherID },
			"identity (Service Bus): identity-mismatch", "token tenant mismatch for Service Bus"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newVerifyRig(t)
			tc.set(r)
			if code := r.run(); code != 1 {
				t.Fatalf("exit %d, want 1; stdout:\n%s\nstderr:\n%s", code, r.stdout, r.stderr)
			}
			requireStdoutLine(t, r.stdout.String(), tc.stdout)
			if !strings.Contains(r.stderr.String(), tc.stderr) {
				t.Fatalf("stderr missing %q: %q", tc.stderr, r.stderr)
			}
		})
	}
}

// A credential that cannot be built (here a certificate with a loose mode) is
// reported as run reports it, not as a probe line, and reaches no endpoint.
func TestIncidentVerifyReportsACredentialBuildFailureLikeRun(t *testing.T) {
	r := newVerifyRig(t)
	if err := os.Chmod(r.cfg.Auth.CertificatePath, 0644); err != nil {
		t.Fatal(err)
	}
	if code := r.run(); code != 1 {
		t.Fatalf("exit %d, want 1", code)
	}
	if !strings.HasPrefix(r.stderr.String(), "cenci incident: ") || !strings.Contains(r.stderr.String(), "has mode 0644") {
		t.Fatalf("want the certificate failure with the cenci incident prefix: %q", r.stderr)
	}
	if r.stdout.Len() != 0 {
		t.Fatalf("a credential failure printed probe output: %q", r.stdout)
	}
	if reqs := r.authority.seen(); len(reqs) != 0 {
		t.Fatalf("identity endpoints contacted after a failed credential build: %+v", reqs)
	}
}

func TestIncidentVerifyWarnsWhenTheCertificateExpiresSoon(t *testing.T) {
	r := newVerifyRig(t)
	notAfter := time.Now().Add(10 * 24 * time.Hour)
	r.cfg.Auth.CertificatePath = incCertificateFile(t, notAfter)
	if code := r.run(); code != 0 {
		t.Fatalf("a certificate inside the warning window must still verify: exit %d, stderr %q", code, r.stderr)
	}
	if got := r.stderr.String(); !strings.Contains(got, "warning") || !strings.Contains(got, "30 days") || !strings.Contains(got, notAfter.UTC().Format("2006-01-02")) {
		t.Fatalf("warning must state the window and carry the NotAfter date: %q", got)
	}
}
