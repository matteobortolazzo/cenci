package incident

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/messaging/azservicebus"
)

const (
	testObjectID  = "44444444-4444-4444-8444-444444444444"
	testWorkspace = "00000000-0000-0000-0000-000000000000"
	otherWorkspID = "55555555-5555-4555-8555-555555555555"
	rowMarker     = "ROW-CONTENT-MARKER"
	bodyMarker    = "MESSAGE-BODY-MARKER"
)

var allProbeClasses = []string{"authentication-failed", "identity-mismatch", "authorization-denied", "rbac-propagation-timeout", "resource-not-found", "unreachable"}

// fakeClock is the injectable time source: Sleep advances it instead of waiting.
type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	sleeps []time.Duration
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Sleep(_ context.Context, d time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sleeps = append(c.sleeps, d)
	c.now = c.now.Add(d)
	return nil
}

// fakeBroker implements the full receiver seam and counts every call, so a test
// can prove a peek received and settled nothing.
type fakeBroker struct {
	mu                                      sync.Mutex
	opens, closes                           int
	receive, renew, complete, abandon, dead int
	peekMax                                 []int
	peekLeft                                []time.Duration
	closeLeft                               []time.Duration
	peek                                    func(attempt int) ([]*azservicebus.ReceivedMessage, error)
	openErr                                 error
}

func (b *fakeBroker) ReceiveMessages(context.Context, int, *azservicebus.ReceiveMessagesOptions) ([]*azservicebus.ReceivedMessage, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.receive++
	return nil, nil
}

func (b *fakeBroker) PeekMessages(ctx context.Context, max int, _ *azservicebus.PeekMessagesOptions) ([]*azservicebus.ReceivedMessage, error) {
	b.mu.Lock()
	b.peekMax = append(b.peekMax, max)
	if dl, ok := ctx.Deadline(); ok {
		b.peekLeft = append(b.peekLeft, time.Until(dl))
	} else {
		b.peekLeft = append(b.peekLeft, -1)
	}
	attempt := len(b.peekMax)
	peek := b.peek
	b.mu.Unlock()
	if peek == nil {
		return nil, nil
	}
	return peek(attempt)
}

func (b *fakeBroker) RenewMessageLock(context.Context, *azservicebus.ReceivedMessage, *azservicebus.RenewMessageLockOptions) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.renew++
	return nil
}

func (b *fakeBroker) CompleteMessage(context.Context, *azservicebus.ReceivedMessage, *azservicebus.CompleteMessageOptions) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.complete++
	return nil
}

func (b *fakeBroker) AbandonMessage(context.Context, *azservicebus.ReceivedMessage, *azservicebus.AbandonMessageOptions) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.abandon++
	return nil
}

func (b *fakeBroker) DeadLetterMessage(context.Context, *azservicebus.ReceivedMessage, *azservicebus.DeadLetterOptions) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.dead++
	return nil
}

func (b *fakeBroker) Close(ctx context.Context) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.closes++
	if dl, ok := ctx.Deadline(); ok {
		b.closeLeft = append(b.closeLeft, time.Until(dl))
	} else {
		b.closeLeft = append(b.closeLeft, -1)
	}
	return nil
}

// telemetryReply is what the fake Log Analytics endpoint answers.
type telemetryReply struct {
	status int
	body   string
	err    error
}

type telemetryRequest struct {
	workspace string
	query     string
	timespan  string
	left      time.Duration
	at        time.Time
}

// verifyRig wires Verify to boundary fakes only: a scripted credential, a fake
// Log Analytics transport, a fake broker and a fake clock.
type verifyRig struct {
	t         *testing.T
	cfg       Config
	cred      *scopeCredential
	clock     *fakeClock
	broker    *fakeBroker
	telemetry func(workspace string, attempt int) telemetryReply

	mu       sync.Mutex
	requests []telemetryRequest
	attempts map[string]int
}

func identityToken(tid, appid, oid string, extra map[string]any) string {
	claims := map[string]any{"tid": tid, "appid": appid, "oid": oid}
	for k, v := range extra {
		claims[k] = v
	}
	return testJWT(claims)
}

func newVerifyRig(t *testing.T) *verifyRig {
	t.Helper()
	cfg := validConfig(t)
	cfg.Resources[0].Workspace = testWorkspace
	r := &verifyRig{
		t:        t,
		cfg:      cfg,
		clock:    &fakeClock{now: time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)},
		broker:   &fakeBroker{},
		attempts: map[string]int{},
		telemetry: func(string, int) telemetryReply {
			return telemetryReply{status: 200, body: `{"tables":[{"name":"PrimaryResult","rows":[["` + rowMarker + `"],["` + rowMarker + `"]]}]}`}
		},
	}
	r.cred = &scopeCredential{token: func(string) (string, error) {
		return identityToken(testTenantID, testClientID, testObjectID, nil), nil
	}}
	return r
}

func (r *verifyRig) addResource(id, workspace string) {
	res := r.cfg.Resources[0]
	res.ID = id
	res.Workspace = workspace
	r.cfg.Resources = append(r.cfg.Resources, res)
}

func (r *verifyRig) roundTrip(req *http.Request) (*http.Response, error) {
	var body map[string]string
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		r.t.Errorf("telemetry request body: %v", err)
	}
	if req.Method != http.MethodPost || req.URL.Host != "api.loganalytics.azure.com" || !strings.HasPrefix(req.URL.Path, "/v1/workspaces/") || !strings.HasSuffix(req.URL.Path, "/query") {
		r.t.Errorf("unexpected telemetry request %s %s", req.Method, req.URL)
	}
	ws := strings.TrimSuffix(strings.TrimPrefix(req.URL.Path, "/v1/workspaces/"), "/query")
	left := time.Duration(-1)
	if dl, ok := req.Context().Deadline(); ok {
		left = time.Until(dl)
	}
	r.mu.Lock()
	r.attempts[ws]++
	attempt := r.attempts[ws]
	r.requests = append(r.requests, telemetryRequest{workspace: ws, query: body["query"], timespan: body["timespan"], left: left, at: r.clock.Now()})
	r.mu.Unlock()
	reply := r.telemetry(ws, attempt)
	if reply.err != nil {
		return nil, reply.err
	}
	return &http.Response{StatusCode: reply.status, Body: io.NopCloser(strings.NewReader(reply.body)), Request: req}, nil
}

func (r *verifyRig) seen() []telemetryRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]telemetryRequest(nil), r.requests...)
}

// run calls Verify and returns the verdict with the captured stdout and stderr.
func (r *verifyRig) run(wait time.Duration) (ok bool, stdout, stderr string) {
	r.t.Helper()
	return r.runWith(context.Background(), wait, r.clock.Sleep)
}

// runWith is run with the context and the wait seam under the test's control.
func (r *verifyRig) runWith(ctx context.Context, wait time.Duration, sleep func(context.Context, time.Duration) error) (ok bool, stdout, stderr string) {
	r.t.Helper()
	var out, errb bytes.Buffer
	ok = Verify(ctx, r.cfg, r.cred, VerifyOptions{
		PropagationWait: wait,
		HTTPClient:      &http.Client{Transport: roundTripFunc(r.roundTrip)},
		OpenBroker: func(Config, azcore.TokenCredential) (ServiceBusReceiver, error) {
			r.broker.mu.Lock()
			r.broker.opens++
			err := r.broker.openErr
			r.broker.mu.Unlock()
			if err != nil {
				return nil, err
			}
			return r.broker, nil
		},
		Now:    r.clock.Now,
		Sleep:  sleep,
		Stdout: &out,
		Stderr: &errb,
	})
	return ok, out.String(), errb.String()
}

func workspaceLine(workspace, id, result string) string {
	return "workspace " + workspace + " (" + id + "): " + result
}

func hasLine(out, want string) bool {
	for _, l := range strings.Split(out, "\n") {
		if l == want {
			return true
		}
	}
	return false
}

func requireLine(t *testing.T, out, want string) {
	t.Helper()
	if !hasLine(out, want) {
		t.Fatalf("missing line %q in:\n%s", want, out)
	}
}

// requireClassLine asserts the exact line and that no other class word shares it.
func requireClassLine(t *testing.T, out, want, class string) {
	t.Helper()
	requireLine(t, out, want)
	for _, other := range allProbeClasses {
		if other != class && strings.Contains(want, other) {
			t.Fatalf("line %q names a second class %q", want, other)
		}
	}
	if !strings.HasSuffix(want, ": "+class) {
		t.Fatalf("line %q does not end with class %q", want, class)
	}
}

const resourceID = "/subscriptions/s/resourceGroups/r/providers/Microsoft.Web/sites/app"

func TestVerifyPrintsIdentityClaimsAndNeverTheTokenOrKey(t *testing.T) {
	r := newVerifyRig(t)
	const displayName = "SECRET-DISPLAY-NAME-CLAIM"
	token := identityToken(testTenantID, testClientID, testObjectID, map[string]any{"name": displayName})
	r.cred.token = func(string) (string, error) { return token, nil }
	ok, stdout, stderr := r.run(0)
	if !ok {
		t.Fatalf("verify failed:\n%s\n%s", stdout, stderr)
	}
	requireLine(t, stdout, "identity: tenant "+testTenantID+", application "+testClientID+", principal "+testObjectID)
	for _, secret := range []string{token, strings.Split(token, ".")[1], strings.Split(token, ".")[2], displayName, "Bearer", "PRIVATE KEY", "CERTIFICATE"} {
		if strings.Contains(stdout, secret) || strings.Contains(stderr, secret) {
			t.Fatalf("output carries %q:\n%s\n%s", secret, stdout, stderr)
		}
	}
}

func TestVerifyPrintsUnknownForClaimsThatAreNotUUIDShaped(t *testing.T) {
	r := newVerifyRig(t)
	// A managed identity without a client does not constrain appid, so the
	// token's own (hostile) value reaches the display path.
	r.cfg.Auth = &AuthConfig{Mode: AuthModeManagedIdentity, TenantID: testTenantID}
	r.cred.token = func(string) (string, error) {
		return identityToken(testTenantID, "evil\x1b[31mred", "not-a-uuid", nil), nil
	}
	ok, stdout, _ := r.run(0)
	if !ok {
		t.Fatalf("verify failed:\n%s", stdout)
	}
	requireLine(t, stdout, "identity: tenant "+testTenantID+", application unknown, principal unknown")
	if strings.Contains(stdout, "\x1b") || strings.Contains(stdout, "evil") || strings.Contains(stdout, "not-a-uuid") {
		t.Fatalf("a non-UUID claim reached the output: %q", stdout)
	}
}

func TestVerifyWorkspaceProbeRunsTheConfiguredQueryOverFiveMinutesWithinThirtySeconds(t *testing.T) {
	r := newVerifyRig(t)
	r.addResource("/subscriptions/s/resourceGroups/r/providers/Microsoft.Web/sites/app2", otherWorkspID)
	r.cfg.Resources[1].Query = "AppRequests | take 5"
	start := r.clock.Now()
	ok, stdout, stderr := r.run(0)
	if !ok {
		t.Fatalf("verify failed:\n%s\n%s", stdout, stderr)
	}
	reqs := r.seen()
	if len(reqs) != 2 {
		t.Fatalf("want one query per resource, got %+v", reqs)
	}
	for i, want := range []struct{ ws, query string }{{testWorkspace, r.cfg.Resources[0].Query}, {otherWorkspID, "AppRequests | take 5"}} {
		got := reqs[i]
		if got.workspace != want.ws || got.query != want.query {
			t.Fatalf("request %d: %+v, want workspace %s query %q", i, got, want.ws, want.query)
		}
		from, to, found := strings.Cut(got.timespan, "/")
		if !found {
			t.Fatalf("timespan %q is not start/end", got.timespan)
		}
		t0, err0 := time.Parse(time.RFC3339, from)
		t1, err1 := time.Parse(time.RFC3339, to)
		if err0 != nil || err1 != nil || t1.Sub(t0) != 5*time.Minute || !t1.Equal(start) {
			t.Fatalf("timespan %q, want the 5 minutes ending at %s", got.timespan, start.Format(time.RFC3339))
		}
		if got.left <= 0 || got.left > 30*time.Second {
			t.Fatalf("request %d deadline %v, want within (0, 30s]", i, got.left)
		}
	}
	requireLine(t, stdout, workspaceLine(testWorkspace, resourceID, "ok (2 rows)"))
	requireLine(t, stdout, workspaceLine(otherWorkspID, "/subscriptions/s/resourceGroups/r/providers/Microsoft.Web/sites/app2", "ok (2 rows)"))
	if strings.Contains(stdout, rowMarker) || strings.Contains(stderr, rowMarker) {
		t.Fatalf("result rows reached the output:\n%s\n%s", stdout, stderr)
	}
}

func TestVerifyBrokerProbePeeksOneAndSettlesNothing(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Config)
		entity string
	}{
		{"queue", func(*Config) {}, "alerts"},
		{"topic-subscription", func(c *Config) { c.Queue, c.Topic, c.Subscription = "", "alerts-topic", "incident-worker" }, "alerts-topic/subscriptions/incident-worker"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newVerifyRig(t)
			tc.mutate(&r.cfg)
			r.broker.peek = func(int) ([]*azservicebus.ReceivedMessage, error) {
				return []*azservicebus.ReceivedMessage{{Body: []byte(bodyMarker)}}, nil
			}
			ok, stdout, stderr := r.run(0)
			if !ok {
				t.Fatalf("verify failed:\n%s\n%s", stdout, stderr)
			}
			b := r.broker
			if len(b.peekMax) != 1 || b.peekMax[0] != 1 {
				t.Fatalf("PeekMessages calls with max %v, want exactly one with max 1", b.peekMax)
			}
			if b.receive != 0 || b.renew != 0 || b.complete != 0 || b.abandon != 0 || b.dead != 0 {
				t.Fatalf("the probe received or settled: receive=%d renew=%d complete=%d abandon=%d deadletter=%d", b.receive, b.renew, b.complete, b.abandon, b.dead)
			}
			if b.peekLeft[0] <= 0 || b.peekLeft[0] > 30*time.Second {
				t.Fatalf("peek deadline %v, want within (0, 30s]", b.peekLeft[0])
			}
			if b.opens != 1 || b.closes != 1 || b.closeLeft[0] <= 0 || b.closeLeft[0] > 5*time.Second {
				t.Fatalf("receiver opened %d times, closed %d times within %v; want 1, 1, (0, 5s]", b.opens, b.closes, b.closeLeft)
			}
			requireLine(t, stdout, "broker "+tc.entity+": ok (1 messages visible)")
			if strings.Contains(stdout, bodyMarker) || strings.Contains(stderr, bodyMarker) {
				t.Fatalf("a message body reached the output:\n%s\n%s", stdout, stderr)
			}
			if strings.Contains(stdout, "empty peek") {
				t.Fatalf("a non-empty peek must not carry the empty-peek note:\n%s", stdout)
			}
		})
	}
}

func TestVerifyEmptyPeekNotesReceiveAuthorizationOnly(t *testing.T) {
	r := newVerifyRig(t)
	ok, stdout, stderr := r.run(0)
	if !ok {
		t.Fatalf("an empty queue is not a failure:\n%s\n%s", stdout, stderr)
	}
	requireLine(t, stdout, "broker alerts: ok (0 messages visible)")
	requireLine(t, stdout, "note: an empty peek proves receive authorization only; it does not show that alerts reach this entity")
}

func TestVerifyClassifiesEachFailure(t *testing.T) {
	denied := &azservicebus.Error{Code: azservicebus.CodeUnauthorizedAccess}
	notFound := &azservicebus.Error{Code: azservicebus.CodeNotFound}
	lost := &azservicebus.Error{Code: azservicebus.CodeConnectionLost}
	dial := &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connect: connection refused")}
	status := func(code int) func(*verifyRig) {
		return func(r *verifyRig) {
			r.telemetry = func(string, int) telemetryReply { return telemetryReply{status: code, body: `{"error":{"code":"x"}}`} }
		}
	}
	peekErr := func(err error) func(*verifyRig) {
		return func(r *verifyRig) {
			r.broker.peek = func(int) ([]*azservicebus.ReceivedMessage, error) { return nil, err }
		}
	}
	wsLine := func(class string) string { return workspaceLine(testWorkspace, resourceID, class) }
	cases := []struct {
		name   string
		class  string
		wait   time.Duration
		mutate func(*verifyRig)
		line   string
		other  string // a probe line that must still be printed: the failure stops nothing else
	}{
		{"authentication-failed/token-rejected", "authentication-failed", 0, func(r *verifyRig) {
			r.cred.token = func(string) (string, error) { return "", errors.New("AADSTS7000215 bad assertion") }
		}, "identity (Service Bus): authentication-failed", ""},
		{"authentication-failed/workspace-401", "authentication-failed", 0, status(401), wsLine("authentication-failed"), "broker alerts: ok (0 messages visible)"},
		{"authentication-failed/broker-credential-unavailable", "authentication-failed", 0, peekErr(azidentity.NewCredentialUnavailableError("no credential")), "broker alerts: authentication-failed", wsLine("ok (2 rows)")},
		{"identity-mismatch/tid", "identity-mismatch", 0, func(r *verifyRig) {
			r.cred.token = func(string) (string, error) { return identityToken(otherUUID, testClientID, testObjectID, nil), nil }
		}, "identity (Service Bus): identity-mismatch", ""},
		{"identity-mismatch/appid-on-log-analytics", "identity-mismatch", 0, func(r *verifyRig) {
			r.cred.token = func(scope string) (string, error) {
				if scope == LogAnalyticsScope {
					return identityToken(testTenantID, otherUUID, testObjectID, nil), nil
				}
				return identityToken(testTenantID, testClientID, testObjectID, nil), nil
			}
		}, "identity (Log Analytics): identity-mismatch", ""},
		{"identity-mismatch/unreadable-claims-not-a-jwt", "identity-mismatch", 0, func(r *verifyRig) {
			r.cred.token = func(string) (string, error) { return "opaque-token-without-dots", nil }
		}, "identity (Service Bus): identity-mismatch", ""},
		{"identity-mismatch/unreadable-claims-payload-not-json", "identity-mismatch", 0, func(r *verifyRig) {
			enc := base64.RawURLEncoding
			r.cred.token = func(string) (string, error) {
				return enc.EncodeToString([]byte(`{"alg":"none"}`)) + "." + enc.EncodeToString([]byte("not json")) + "." + enc.EncodeToString([]byte("sig")), nil
			}
		}, "identity (Service Bus): identity-mismatch", ""},
		{"authentication-failed/broker-authentication-failed-error", "authentication-failed", 0, peekErr(&azidentity.AuthenticationFailedError{}), "broker alerts: authentication-failed", wsLine("ok (2 rows)")},
		{"authentication-failed/broker-wrapped-authentication-failed-error", "authentication-failed", 0, peekErr(fmt.Errorf("receiver: %w", &azidentity.AuthenticationFailedError{})), "broker alerts: authentication-failed", wsLine("ok (2 rows)")},
		{"authorization-denied/workspace-403", "authorization-denied", 0, status(403), wsLine("authorization-denied"), "broker alerts: ok (0 messages visible)"},
		{"authorization-denied/broker-unauthorized", "authorization-denied", 0, peekErr(denied), "broker alerts: authorization-denied", wsLine("ok (2 rows)")},
		{"rbac-propagation-timeout/workspace-403-persists", "rbac-propagation-timeout", time.Minute, status(403), wsLine("rbac-propagation-timeout"), "broker alerts: ok (0 messages visible)"},
		{"rbac-propagation-timeout/broker-unauthorized-persists", "rbac-propagation-timeout", time.Minute, peekErr(denied), "broker alerts: rbac-propagation-timeout", wsLine("ok (2 rows)")},
		{"resource-not-found/workspace-404", "resource-not-found", 0, status(404), wsLine("resource-not-found"), "broker alerts: ok (0 messages visible)"},
		{"resource-not-found/broker-entity-missing", "resource-not-found", 0, peekErr(notFound), "broker alerts: resource-not-found", wsLine("ok (2 rows)")},
		{"unreachable/workspace-dial", "unreachable", 0, func(r *verifyRig) {
			r.telemetry = func(string, int) telemetryReply { return telemetryReply{err: dial} }
		}, wsLine("unreachable"), "broker alerts: ok (0 messages visible)"},
		{"unreachable/broker-connection-lost", "unreachable", 0, peekErr(lost), "broker alerts: unreachable", wsLine("ok (2 rows)")},
		{"unreachable/broker-dial", "unreachable", 0, func(r *verifyRig) { r.broker.openErr = dial }, "broker alerts: unreachable", wsLine("ok (2 rows)")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newVerifyRig(t)
			tc.mutate(r)
			ok, stdout, stderr := r.run(tc.wait)
			if ok {
				t.Fatalf("a failed probe must fail verify:\n%s", stdout)
			}
			requireClassLine(t, stdout, tc.line, tc.class)
			if tc.other != "" {
				requireLine(t, stdout, tc.other)
			}
			for _, secret := range []string{"Bearer", "SECRET"} {
				if strings.Contains(stdout, secret) || strings.Contains(stderr, secret) {
					t.Fatalf("output carries %q:\n%s\n%s", secret, stdout, stderr)
				}
			}
		})
	}
}

func TestVerifyIdentityFailureNamesThePreflightReasonOnStderrOnly(t *testing.T) {
	r := newVerifyRig(t)
	r.cred.token = func(string) (string, error) { return identityToken(otherUUID, testClientID, testObjectID, nil), nil }
	_, stdout, stderr := r.run(0)
	if !strings.Contains(stderr, markTenantMismatch) {
		t.Fatalf("stderr must carry the preflight message: %q", stderr)
	}
	if strings.Contains(stdout, markTenantMismatch) || strings.Contains(stdout, "identity: tenant") {
		t.Fatalf("stdout must carry the class only:\n%s", stdout)
	}
}

const controlBytes = "\x1b\u009b\x07\x00\x7f"

func requireNoControl(t *testing.T, name, out string) {
	t.Helper()
	for _, r := range controlBytes {
		if strings.ContainsRune(out, r) {
			t.Fatalf("%s carries control character %U: %q", name, r, out)
		}
	}
}

func TestVerifyStripsTerminalControlsFromStderrErrorText(t *testing.T) {
	r := newVerifyRig(t)
	r.cred.token = func(string) (string, error) {
		return "", errors.New("AADSTS70011 \x1b[31mred\u009b\x07 invalid\tscope\nsecond line")
	}
	ok, stdout, stderr := r.run(0)
	if ok {
		t.Fatal("a token failure must fail verify")
	}
	requireLine(t, stdout, "identity (Service Bus): authentication-failed")
	requireNoControl(t, "stderr", stderr)
	requireNoControl(t, "stdout", stdout)
	if !strings.Contains(stderr, "AADSTS70011 [31mred invalid\tscope\nsecond line") {
		t.Fatalf("stderr lost the readable text, tab or newline: %q", stderr)
	}
}

func TestVerifyStripsTerminalControlsFromConfigLabels(t *testing.T) {
	r := newVerifyRig(t)
	r.cfg.Resources[0].ID = "/sub\x1b[31mred\u009b/app"
	r.cfg.Queue = "al\x1b]0;x\x07erts"
	r.telemetry = func(string, int) telemetryReply { return telemetryReply{status: 403, body: `{}`} }
	r.broker.peek = func(int) ([]*azservicebus.ReceivedMessage, error) {
		return nil, &azservicebus.Error{Code: azservicebus.CodeNotFound}
	}
	ok, stdout, stderr := r.run(0)
	if ok {
		t.Fatal("denied probes must fail verify")
	}
	requireNoControl(t, "stdout", stdout)
	requireNoControl(t, "stderr", stderr)
	requireLine(t, stdout, workspaceLine(testWorkspace, "/sub[31mred/app", "authorization-denied"))
	requireLine(t, stdout, "broker al]0;xerts: resource-not-found")
}

func TestVerifyDoesNotQueryAWorkspaceThatIsNotAUUID(t *testing.T) {
	r := newVerifyRig(t)
	r.cfg.Resources[0].Workspace = "../other/path"
	ok, stdout, _ := r.run(0)
	if ok {
		t.Fatal("a non-UUID workspace must fail verify")
	}
	if n := len(r.seen()); n != 0 {
		t.Fatalf("a non-UUID workspace reached the network: %+v", r.seen())
	}
	requireClassLine(t, stdout, workspaceLine("../other/path", resourceID, "unreachable"), "unreachable")
}

func TestVerifyReportsInterruptionWhenTheContextIsCancelled(t *testing.T) {
	countInterrupted := func(stderr string) int {
		n := 0
		for _, l := range strings.Split(stderr, "\n") {
			if strings.HasPrefix(l, "interrupted: ") {
				n++
			}
		}
		return n
	}
	t.Run("during-the-propagation-wait", func(t *testing.T) {
		r := newVerifyRig(t)
		r.telemetry = func(string, int) telemetryReply { return telemetryReply{status: 403, body: `{}`} }
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		sleep := func(ctx context.Context, _ time.Duration) error {
			cancel()
			return ctx.Err()
		}
		ok, stdout, stderr := r.runWith(ctx, time.Minute, sleep)
		if ok {
			t.Fatal("an interrupted verify must fail")
		}
		requireLine(t, stderr, "interrupted: context canceled")
		if n := countInterrupted(stderr); n != 1 {
			t.Fatalf("want one interrupted line, got %d:\n%s", n, stderr)
		}
		requireClassLine(t, stdout, workspaceLine(testWorkspace, resourceID, "authorization-denied"), "authorization-denied")
	})
	t.Run("during-a-probe", func(t *testing.T) {
		r := newVerifyRig(t)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		r.telemetry = func(string, int) telemetryReply {
			cancel()
			return telemetryReply{err: context.Canceled}
		}
		ok, stdout, stderr := r.runWith(ctx, 0, r.clock.Sleep)
		if ok {
			t.Fatal("an interrupted verify must fail")
		}
		requireLine(t, stderr, "interrupted: context canceled")
		if n := countInterrupted(stderr); n != 1 {
			t.Fatalf("want one interrupted line, got %d:\n%s", n, stderr)
		}
		requireClassLine(t, stdout, workspaceLine(testWorkspace, resourceID, "unreachable"), "unreachable")
	})
	t.Run("an-uncancelled-run-prints-no-interrupted-line", func(t *testing.T) {
		r := newVerifyRig(t)
		_, _, stderr := r.run(0)
		if n := countInterrupted(stderr); n != 0 {
			t.Fatalf("unexpected interrupted line:\n%s", stderr)
		}
	})
}

func TestVerifyUnmappedOutcomesFallBackToUnreachable(t *testing.T) {
	reply := func(code int, body string) func(*verifyRig) {
		return func(r *verifyRig) {
			r.telemetry = func(string, int) telemetryReply { return telemetryReply{status: code, body: body} }
		}
	}
	peekErr := func(err error) func(*verifyRig) {
		return func(r *verifyRig) {
			r.broker.peek = func(int) ([]*azservicebus.ReceivedMessage, error) { return nil, err }
		}
	}
	wsLine := workspaceLine(testWorkspace, resourceID, "unreachable")
	cases := []struct {
		name   string
		mutate func(*verifyRig)
		line   string
		stderr string // the one detail line, empty when none is expected
	}{
		{"http-400-bad-query", reply(400, `{"error":{"code":"BadArgumentError"}}`), wsLine, "HTTP 400"},
		{"http-429", reply(429, ``), wsLine, "HTTP 429"},
		{"http-500", reply(500, `{"error":{"message":"boom"}}`), wsLine, "HTTP 500"},
		{"malformed-body", reply(200, `not json`), wsLine, ""},
		{"no-tables", reply(200, `{"error":{"code":"x"}}`), wsLine, ""},
		{"empty-tables-array", reply(200, `{"tables":[]}`), wsLine, "telemetry query incomplete or failed"},
		{"over-1-MiB-body", reply(200, `{"tables":[{"rows":[["`+strings.Repeat("x", 1<<20)+`"]]}]}`), wsLine, "telemetry exceeds 1 MiB; narrow configured query"},
		{"unknown-service-bus-code", peekErr(&azservicebus.Error{Code: azservicebus.Code("brand-new-code")}), "broker alerts: unreachable", ""},
		{"service-bus-lock-lost-code", peekErr(&azservicebus.Error{Code: azservicebus.CodeLockLost}), "broker alerts: unreachable", ""},
		{"non-service-bus-error", peekErr(errors.New("something else entirely")), "broker alerts: unreachable", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newVerifyRig(t)
			tc.mutate(r)
			ok, stdout, stderr := r.run(0)
			if ok {
				t.Fatalf("an unmapped outcome must fail verify:\n%s", stdout)
			}
			requireClassLine(t, stdout, tc.line, "unreachable")
			if tc.stderr != "" && !strings.Contains(stderr, tc.stderr) {
				t.Fatalf("stderr %q does not carry %q", stderr, tc.stderr)
			}
			for _, leak := range []string{"boom", "BadArgumentError", "something else entirely", "brand-new-code"} {
				if strings.Contains(stdout, leak) || strings.Contains(stderr, leak) {
					t.Fatalf("service text %q reached the output:\n%s\n%s", leak, stdout, stderr)
				}
			}
		})
	}
}

func TestVerifyRetriesOnlyAuthorizationDeniedDuringPropagationWait(t *testing.T) {
	const retry = 30 * time.Second
	denyThen := func(denials int, then telemetryReply) func(string, int) telemetryReply {
		return func(_ string, attempt int) telemetryReply {
			if attempt <= denials {
				return telemetryReply{status: 403, body: `{}`}
			}
			return then
		}
	}
	okReply := telemetryReply{status: 200, body: `{"tables":[{"rows":[[1]]}]}`}
	wsLine := func(result string) string { return workspaceLine(testWorkspace, resourceID, result) }

	t.Run("403-then-200-ends-ok-after-two-attempts", func(t *testing.T) {
		r := newVerifyRig(t)
		start := r.clock.Now()
		r.telemetry = denyThen(1, okReply)
		ok, stdout, stderr := r.run(time.Minute)
		if !ok {
			t.Fatalf("verify failed:\n%s\n%s", stdout, stderr)
		}
		reqs := r.seen()
		if len(reqs) != 2 || !reqs[0].at.Equal(start) || !reqs[1].at.Equal(start.Add(retry)) {
			t.Fatalf("attempts %+v, want two spaced %v apart from %s", reqs, retry, start)
		}
		requireLine(t, stdout, wsLine("ok (1 rows)"))
		if !strings.Contains(stderr, "--propagation-wait") {
			t.Fatalf("stderr must note the propagation wait: %q", stderr)
		}
	})
	t.Run("404-is-final-after-one-attempt", func(t *testing.T) {
		r := newVerifyRig(t)
		r.telemetry = func(string, int) telemetryReply { return telemetryReply{status: 404, body: `{}`} }
		_, stdout, _ := r.run(15 * time.Minute)
		if n := len(r.seen()); n != 1 || len(r.clock.sleeps) != 0 {
			t.Fatalf("404 made %d attempts and %d sleeps, want 1 and 0", n, len(r.clock.sleeps))
		}
		requireClassLine(t, stdout, wsLine("resource-not-found"), "resource-not-found")
	})
	t.Run("no-wait-means-one-attempt", func(t *testing.T) {
		r := newVerifyRig(t)
		r.telemetry = denyThen(100, okReply)
		_, stdout, stderr := r.run(0)
		if n := len(r.seen()); n != 1 || len(r.clock.sleeps) != 0 {
			t.Fatalf("no wait made %d attempts and %d sleeps, want 1 and 0", n, len(r.clock.sleeps))
		}
		requireClassLine(t, stdout, wsLine("authorization-denied"), "authorization-denied")
		if strings.Contains(stderr, "--propagation-wait") {
			t.Fatalf("no wait must not note one: %q", stderr)
		}
	})
	t.Run("persisting-403-retries-every-30s-through-the-inclusive-window-end", func(t *testing.T) {
		r := newVerifyRig(t)
		start := r.clock.Now()
		r.telemetry = denyThen(100, okReply)
		ok, stdout, _ := r.run(90 * time.Second)
		if ok {
			t.Fatal("a 403 that persists must fail verify")
		}
		reqs := r.seen()
		if len(reqs) != 4 {
			t.Fatalf("want attempts at 0, 30, 60 and 90s, got %+v", reqs)
		}
		for i, req := range reqs {
			if want := start.Add(time.Duration(i) * retry); !req.at.Equal(want) {
				t.Fatalf("attempt %d at %s, want %s", i, req.at, want)
			}
		}
		requireClassLine(t, stdout, wsLine("rbac-propagation-timeout"), "rbac-propagation-timeout")
	})
	t.Run("no-attempt-starts-past-the-window", func(t *testing.T) {
		r := newVerifyRig(t)
		r.telemetry = denyThen(100, okReply)
		r.run(45 * time.Second)
		if n := len(r.seen()); n != 2 {
			t.Fatalf("a 45s window allows attempts at 0 and 30s only, got %d", n)
		}
	})
	t.Run("a-non-403-retry-result-is-final", func(t *testing.T) {
		r := newVerifyRig(t)
		r.telemetry = denyThen(1, telemetryReply{status: 404, body: `{}`})
		_, stdout, _ := r.run(5 * time.Minute)
		if n := len(r.seen()); n != 2 {
			t.Fatalf("want 2 attempts (403 then the final 404), got %d", n)
		}
		requireClassLine(t, stdout, wsLine("resource-not-found"), "resource-not-found")
	})
	t.Run("only-the-denied-probe-is-retried-with-a-fresh-receiver", func(t *testing.T) {
		r := newVerifyRig(t)
		r.broker.peek = func(attempt int) ([]*azservicebus.ReceivedMessage, error) {
			if attempt == 1 {
				return nil, &azservicebus.Error{Code: azservicebus.CodeUnauthorizedAccess}
			}
			return []*azservicebus.ReceivedMessage{{Body: []byte(bodyMarker)}}, nil
		}
		ok, stdout, stderr := r.run(time.Minute)
		if !ok {
			t.Fatalf("verify failed:\n%s\n%s", stdout, stderr)
		}
		if n := len(r.seen()); n != 1 {
			t.Fatalf("the healthy workspace probe ran %d times, want 1", n)
		}
		b := r.broker
		if len(b.peekMax) != 2 || b.opens != 2 || b.closes != 2 {
			t.Fatalf("broker peeks=%d opens=%d closes=%d, want a new receiver per attempt: 2, 2, 2", len(b.peekMax), b.opens, b.closes)
		}
		requireLine(t, stdout, "broker alerts: ok (1 messages visible)")
		requireLine(t, stdout, wsLine("ok (2 rows)"))
	})
}

func TestVerifyIdentityFailureRunsNoResourceProbe(t *testing.T) {
	cases := []struct {
		name   string
		token  func(string) (string, error)
		line   string
		scopes []string
	}{
		{"service-bus-token-fails", func(string) (string, error) { return "", errors.New("denied") }, "identity (Service Bus): authentication-failed", []string{ServiceBusScope}},
		{"log-analytics-claims-mismatch", func(scope string) (string, error) {
			if scope == LogAnalyticsScope {
				return identityToken(otherUUID, testClientID, testObjectID, nil), nil
			}
			return identityToken(testTenantID, testClientID, testObjectID, nil), nil
		}, "identity (Log Analytics): identity-mismatch", []string{ServiceBusScope, LogAnalyticsScope}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newVerifyRig(t)
			r.cred.token = tc.token
			ok, stdout, _ := r.run(time.Minute)
			if ok {
				t.Fatal("an identity failure must fail verify")
			}
			requireLine(t, stdout, tc.line)
			if strings.Join(r.cred.scopes, ",") != strings.Join(tc.scopes, ",") {
				t.Fatalf("token scopes requested %v, want %v (stop at the first failure)", r.cred.scopes, tc.scopes)
			}
			if n := len(r.seen()); n != 0 || r.broker.opens != 0 || len(r.clock.sleeps) != 0 {
				t.Fatalf("resource probes ran after an identity failure: %d queries, %d broker opens, %d sleeps", n, r.broker.opens, len(r.clock.sleeps))
			}
			if strings.Contains(stdout, "workspace ") || strings.Contains(stdout, "broker ") || strings.Contains(stdout, "identity: tenant") {
				t.Fatalf("stdout carries more than the identity failure:\n%s", stdout)
			}
		})
	}
}
