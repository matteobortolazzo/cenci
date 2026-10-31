package incident

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/messaging/azservicebus"
)

// ProbeClass is the closed set of ways a verify probe can fail. Nothing adds a
// seventh class: an outcome that matches none of the named ones is unreachable.
type ProbeClass string

const (
	ClassAuthenticationFailed ProbeClass = "authentication-failed"
	ClassIdentityMismatch     ProbeClass = "identity-mismatch"
	ClassAuthorizationDenied  ProbeClass = "authorization-denied"
	// ClassRBACPropagationTimeout is authorization-denied that persisted through
	// the whole --propagation-wait window.
	ClassRBACPropagationTimeout ProbeClass = "rbac-propagation-timeout"
	ClassResourceNotFound       ProbeClass = "resource-not-found"
	ClassUnreachable            ProbeClass = "unreachable"
)

const (
	verifyProbeTimeout   = 30 * time.Second
	verifyCloseTimeout   = 5 * time.Second
	verifyRetryInterval  = 30 * time.Second
	verifyTelemetrySpan  = 5 * time.Minute
	verifyEmptyPeekNote  = "note: an empty peek proves receive authorization only; it does not show that alerts reach this entity"
	verifyIncompleteNote = "telemetry query incomplete or failed"
)

// VerifyOptions carries the seams of Verify. The zero value is production: the
// default no-redirect HTTP client, a real broker client that tries once, the
// wall clock and os-level waiting, and discarded output.
type VerifyOptions struct {
	// PropagationWait is the opt-in window in which authorization-denied probes
	// are retried every 30 seconds. Zero means one attempt per probe.
	PropagationWait time.Duration
	HTTPClient      *http.Client
	OpenBroker      func(Config, azcore.TokenCredential) (ServiceBusReceiver, error)
	Now             func() time.Time
	Sleep           func(context.Context, time.Duration) error
	Stdout          io.Writer
	Stderr          io.Writer
}

type probeResult struct {
	ok      bool
	class   ProbeClass
	summary string // "<n> rows" or "<n> messages visible", when ok
	note    string // an extra stdout line after an ok result
	detail  string // one stderr line for a failure; a status or fixed note, never service text
}

type probe struct {
	label string
	run   func(context.Context) probeResult
}

type verifier struct {
	cfg  Config
	cred azcore.TokenCredential
	opts VerifyOptions
}

// Verify authenticates as the configured worker identity and runs one bounded,
// non-destructive probe per resource workspace and one for the broker. It
// prints non-secret identity claims and one result line per probe to Stdout,
// details to Stderr, and reports whether everything passed. It writes no file,
// takes no lock and receives or settles no message.
func Verify(ctx context.Context, cfg Config, cred azcore.TokenCredential, opts VerifyOptions) bool {
	if opts.OpenBroker == nil {
		opts.OpenBroker = openVerifyBroker
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Sleep == nil {
		opts.Sleep = sleepContext
	}
	if opts.Stdout == nil {
		opts.Stdout = io.Discard
	}
	if opts.Stderr == nil {
		opts.Stderr = io.Discard
	}
	v := &verifier{cfg: cfg, cred: cred, opts: opts}
	if cfg.Auth == nil {
		_, _ = fmt.Fprintln(opts.Stderr, "no explicit worker identity is configured")
		return false
	}
	claims, ok := v.checkIdentity(ctx)
	if !ok {
		return false
	}
	_, _ = fmt.Fprintf(opts.Stdout, "identity: tenant %s, application %s, principal %s\n", displayClaim(claims.TenantID), displayClaim(claims.AppID), displayClaim(claims.ObjectID))

	probes := v.probes()
	results := make([]probeResult, len(probes))
	var pending []int
	for i, p := range probes {
		results[i] = p.run(ctx)
		if results[i].class == ClassAuthorizationDenied {
			pending = append(pending, i)
		}
	}
	var waitErr error
	if opts.PropagationWait > 0 && len(pending) > 0 {
		waitErr = v.waitForPropagation(ctx, probes, results, pending)
	}
	allOK := true
	for i, p := range probes {
		allOK = v.render(p.label, results[i]) && allOK
	}
	interrupted := ctx.Err()
	if interrupted == nil {
		interrupted = waitErr
	}
	if interrupted != nil {
		_, _ = fmt.Fprintf(opts.Stderr, "interrupted: %s\n", stripControl(interrupted.Error()))
		return false
	}
	return allOK
}

// stripControl drops C0 and C1 control characters, keeping newline and tab, from
// text that did not originate in this process: it may carry terminal escape
// sequences. Clean text comes back byte-identical.
func stripControl(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if r < 0x20 || (r >= 0x7f && r <= 0x9f) {
			return -1
		}
		return r
	}, s)
}

// checkIdentity runs the claim check the worker's Preflight runs, for both
// scopes in the same order, and stops at the first failure. Only the failure
// class goes to stdout; the preflight message, which never carries token text,
// goes to stderr.
func (v *verifier) checkIdentity(ctx context.Context) (IdentityClaims, bool) {
	var claims IdentityClaims
	for i, target := range []struct{ name, scope string }{{"Service Bus", ServiceBusScope}, {"Log Analytics", LogAnalyticsScope}} {
		got, err := CheckIdentity(ctx, v.cred, *v.cfg.Auth, target.name, target.scope)
		if err != nil {
			_, _ = fmt.Fprintf(v.opts.Stdout, "identity (%s): %s\n", target.name, classifyIdentity(err))
			_, _ = fmt.Fprintln(v.opts.Stderr, stripControl(err.Error()))
			return IdentityClaims{}, false
		}
		if i == 0 {
			claims = got
		}
	}
	return claims, true
}

func classifyIdentity(err error) ProbeClass {
	var identity *IdentityError
	if errors.As(err, &identity) && identity.Kind == IdentityMismatch {
		return ClassIdentityMismatch
	}
	return ClassAuthenticationFailed
}

// displayClaim prints a claim only when it is UUID-shaped: the value comes from
// a token, so anything else could carry terminal control sequences.
func displayClaim(claim string) string {
	if uuidPattern.MatchString(claim) {
		return claim
	}
	return "unknown"
}

func (v *verifier) probes() []probe {
	out := make([]probe, 0, len(v.cfg.Resources)+1)
	for _, r := range v.cfg.Resources {
		out = append(out, probe{
			label: stripControl("workspace " + r.Workspace + " (" + r.ID + ")"),
			run:   func(ctx context.Context) probeResult { return v.probeWorkspace(ctx, r) },
		})
	}
	entity := v.cfg.Queue
	if entity == "" {
		entity = v.cfg.Topic + "/subscriptions/" + v.cfg.Subscription
	}
	return append(out, probe{label: stripControl("broker " + entity), run: v.probeBroker})
}

// waitForPropagation retries the probes that are still authorization-denied
// together at 30s, 60s, ... after the first round, for as long as that offset
// is within the wait. A non-403 retry result is final. A probe still denied at
// the end of the window becomes rbac-propagation-timeout. A failed wait (the
// context was cancelled) stops the retries, leaves the classes as they stand and
// returns the wait's error.
func (v *verifier) waitForPropagation(ctx context.Context, probes []probe, results []probeResult, pending []int) error {
	_, _ = fmt.Fprintf(v.opts.Stderr, "%d probe(s) were denied; retrying every %s for up to %s (--propagation-wait)\n", len(pending), verifyRetryInterval, v.opts.PropagationWait)
	first := v.opts.Now()
	for round := 1; time.Duration(round)*verifyRetryInterval <= v.opts.PropagationWait && len(pending) > 0; round++ {
		if wait := first.Add(time.Duration(round) * verifyRetryInterval).Sub(v.opts.Now()); wait > 0 {
			if err := v.opts.Sleep(ctx, wait); err != nil {
				return err
			}
		}
		var still []int
		for _, i := range pending {
			results[i] = probes[i].run(ctx)
			if results[i].class == ClassAuthorizationDenied {
				still = append(still, i)
			}
		}
		pending = still
	}
	for _, i := range pending {
		results[i].class = ClassRBACPropagationTimeout
	}
	return nil
}

func (v *verifier) render(label string, r probeResult) bool {
	if r.ok {
		_, _ = fmt.Fprintf(v.opts.Stdout, "%s: ok (%s)\n", label, r.summary)
		if r.note != "" {
			_, _ = fmt.Fprintln(v.opts.Stdout, r.note)
		}
		return true
	}
	_, _ = fmt.Fprintf(v.opts.Stdout, "%s: %s\n", label, r.class)
	if r.detail != "" {
		_, _ = fmt.Fprintf(v.opts.Stderr, "%s: %s\n", label, r.detail)
	}
	return false
}

func failed(class ProbeClass, detail string) probeResult {
	return probeResult{class: class, detail: detail}
}

func (v *verifier) probeWorkspace(ctx context.Context, r Resource) probeResult {
	ctx, cancel := context.WithTimeout(ctx, verifyProbeTimeout)
	defer cancel()
	now := v.opts.Now().UTC()
	timespan := now.Add(-verifyTelemetrySpan).Format(time.RFC3339) + "/" + now.Format(time.RFC3339)
	status, body, err := queryWorkspace(ctx, v.cred, v.opts.HTTPClient, r.Workspace, r.Query, timespan)
	return classifyWorkspace(status, body, err)
}

func classifyWorkspace(status int, body []byte, err error) probeResult {
	var tokenErr *tokenRequestError
	if errors.As(err, &tokenErr) {
		return failed(ClassAuthenticationFailed, "")
	}
	switch status {
	case http.StatusUnauthorized:
		return failed(ClassAuthenticationFailed, "")
	case http.StatusForbidden:
		return failed(ClassAuthorizationDenied, "")
	case http.StatusNotFound:
		return failed(ClassResourceNotFound, "")
	}
	switch {
	case errors.Is(err, errTelemetryTooLarge):
		return failed(ClassUnreachable, errTelemetryTooLarge.Error())
	case err != nil:
		return failed(ClassUnreachable, "")
	case status != http.StatusOK:
		return failed(ClassUnreachable, fmt.Sprintf("HTTP %d", status))
	}
	rows, ok := countRows(body)
	if !ok {
		return failed(ClassUnreachable, verifyIncompleteNote)
	}
	return probeResult{ok: true, summary: fmt.Sprintf("%d rows", rows)}
}

// countRows counts the rows of every result table and keeps none of them. A
// response with no table at all is not a result.
func countRows(body []byte) (int, bool) {
	var result struct {
		Error  json.RawMessage `json:"error"`
		Tables json.RawMessage `json:"tables"`
	}
	if err := json.Unmarshal(body, &result); err != nil || len(result.Tables) == 0 || string(result.Tables) == "null" {
		return 0, false
	}
	if len(result.Error) > 0 && string(result.Error) != "null" {
		return 0, false
	}
	var tables []struct {
		Rows []json.RawMessage `json:"rows"`
	}
	if err := json.Unmarshal(result.Tables, &tables); err != nil || len(tables) == 0 {
		return 0, false
	}
	n := 0
	for _, t := range tables {
		n += len(t.Rows)
	}
	return n, true
}

func (v *verifier) probeBroker(ctx context.Context) probeResult {
	ctx, cancel := context.WithTimeout(ctx, verifyProbeTimeout)
	defer cancel()
	receiver, err := v.opts.OpenBroker(v.cfg, v.cred)
	if err != nil {
		return failed(classifyBroker(err), "")
	}
	defer func() {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), verifyCloseTimeout)
		defer closeCancel()
		_ = receiver.Close(closeCtx)
	}()
	messages, err := receiver.PeekMessages(ctx, 1, nil)
	if err != nil {
		return failed(classifyBroker(err), "")
	}
	r := probeResult{ok: true, summary: fmt.Sprintf("%d messages visible", len(messages))}
	if len(messages) == 0 {
		r.note = verifyEmptyPeekNote
	}
	return r
}

// classifyBroker maps a Service Bus failure to the closed set. The broker's
// error text is never printed; only its stable code is read.
func classifyBroker(err error) ProbeClass {
	var sbErr *azservicebus.Error
	if errors.As(err, &sbErr) {
		switch sbErr.Code {
		case azservicebus.CodeUnauthorizedAccess:
			return ClassAuthorizationDenied
		case azservicebus.CodeNotFound:
			return ClassResourceNotFound
		default:
			// CodeConnectionLost, CodeTimeout and every code not named above.
			return ClassUnreachable
		}
	}
	var authFailed *azidentity.AuthenticationFailedError
	if errors.As(err, &authFailed) {
		return ClassAuthenticationFailed
	}
	// azidentity's credential-unavailable error is unexported; its NonRetriable
	// marker is the only handle. An HTTP error response carries the same marker
	// but is not a credential failure.
	var nonRetriable interface{ NonRetriable() }
	var response *azcore.ResponseError
	if errors.As(err, &nonRetriable) && !errors.As(err, &response) {
		return ClassAuthenticationFailed
	}
	return ClassUnreachable
}

// brokerHandle closes the client together with its receiver.
type brokerHandle struct {
	*azservicebus.Receiver
	client *azservicebus.Client
}

func (b *brokerHandle) Close(ctx context.Context) error {
	return errors.Join(b.Receiver.Close(ctx), b.client.Close(ctx))
}

// openVerifyBroker opens a fresh client and receiver for one attempt. The SDK
// retry is off (a negative MaxRetries means one try), so "one attempt per probe"
// holds below this package too.
func openVerifyBroker(c Config, cred azcore.TokenCredential) (ServiceBusReceiver, error) {
	client, receiver, err := openReceiver(c, cred, &azservicebus.ClientOptions{RetryOptions: azservicebus.RetryOptions{MaxRetries: -1}})
	if err != nil {
		return nil, err
	}
	return &brokerHandle{Receiver: receiver, client: client}, nil
}

func sleepContext(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
