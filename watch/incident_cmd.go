package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	azlog "github.com/Azure/azure-sdk-for-go/sdk/azcore/log"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/incident"
)

const (
	incidentVerbHint = "cenci incident: use run, verify, status, or cancel"
	// The inclusive bounds of verify's --propagation-wait.
	minPropagationWait = 30 * time.Second
	maxPropagationWait = 15 * time.Minute
)

func runIncident(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, incidentVerbHint)
		os.Exit(2)
	}
	verb := args[0]
	if verb != "run" && verb != "verify" && verb != "status" && verb != "cancel" {
		fmt.Fprintln(os.Stderr, incidentVerbHint)
		os.Exit(2)
	}
	fs := flag.NewFlagSet("incident "+verb, flag.ExitOnError)
	config := fs.String("config", "", "path to opt-in incident worker JSON config (run/verify only)")
	state := fs.String("state-dir", "", "absolute incident state directory (status/cancel only)")
	key := fs.String("id", "", "incident key to cancel (cancel only)")
	wait := fs.Duration("propagation-wait", 0, "retry authorization-denied probes every 30s for up to this long, 30s to 15m (verify only)")
	_ = fs.Parse(args[1:])
	rejectExtra("cenci incident "+verb, fs.Args())
	waitSet := false
	fs.Visit(func(f *flag.Flag) { waitSet = waitSet || f.Name == "propagation-wait" })
	needsConfig := verb == "run" || verb == "verify"
	flagsFitVerb := needsConfig && *config != "" && *state == "" && *key == "" ||
		!needsConfig && *state != "" && *config == "" && (verb != "status" || *key == "") && (verb != "cancel" || *key != "")
	waitFitsVerb := !waitSet || verb == "verify" && *wait >= minPropagationWait && *wait <= maxPropagationWait
	if !flagsFitVerb || !waitFitsVerb {
		fmt.Fprintln(os.Stderr, "cenci incident: run/verify need --config; status/cancel need --state-dir; cancel also needs --id; only verify takes --propagation-wait (30s to 15m)")
		os.Exit(2)
	}
	fail := func(err error) { fmt.Fprintf(os.Stderr, "cenci incident: %v\n", err); os.Exit(1) }
	if !needsConfig {
		s := incident.NewStore(*state)
		if verb == "cancel" {
			if err := s.Cancel(*key); err != nil {
				fail(err)
			}
			return
		}
		rows, err := s.List()
		if err != nil {
			fail(err)
		}
		if err := json.NewEncoder(os.Stdout).Encode(rows); err != nil {
			fail(err)
		}
		return
	}
	c, err := incident.LoadConfig(*config)
	if err != nil {
		fail(err)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if verb == "verify" {
		code := verifyIncident(ctx, c, verifyDeps{}, *wait)
		cancel()
		os.Exit(code)
	}
	run, err := startIncident(ctx, c, incidentDeps{newReceiver: newAzureIncidentReceiver, stderr: os.Stderr})
	if err != nil {
		fail(err)
	}
	runErr := run.serve(ctx)
	cancel()
	run.close()
	if runErr != nil {
		fail(runErr)
	}
}

// incidentReceiver is the broker boundary startIncident drives.
type incidentReceiver interface {
	incident.Receiver
	Close(context.Context) error
}

// incidentDeps are the Go-level seams of startIncident. The zero value of every
// field selects production behavior; nothing here is read from the environment,
// a flag or the configuration file.
type incidentDeps struct {
	transport       policy.Transporter                                                      // identity-authority transport for the credential; nil means the SDK default
	newReceiver     func(incident.Config, azcore.TokenCredential) (incidentReceiver, error) // required
	telemetryClient *http.Client                                                            // nil means the telemetry adapter's default
	stderr          io.Writer                                                               // nil means os.Stderr
}

// incidentRun is a started worker: the lock held, the preflight passed, the
// ledger recovered and the broker receiver and pipeline built.
type incidentRun struct {
	cfg      incident.Config
	store    *incident.Store
	lock     io.Closer
	receiver incidentReceiver
	pipeline *incident.Pipeline
	stderr   io.Writer
}

func newAzureIncidentReceiver(c incident.Config, credential azcore.TokenCredential) (incidentReceiver, error) {
	r, err := incident.NewAzureReceiver(c, credential)
	if err != nil {
		return nil, err
	}
	return r, nil
}

// startIncident brings the worker up in the one order that keeps a bad identity
// from touching anything: ambient Azure variables are removed, the credential is
// built from the config's auth section only (certificate checks included), the
// worker lock is taken, both tokens are acquired, and only then are stale
// containers removed, the ledger recovered and the receiver built. The same
// credential serves the receiver and the telemetry adapter.
func startIncident(ctx context.Context, c incident.Config, deps incidentDeps) (*incidentRun, error) {
	stderr := deps.stderr
	if stderr == nil {
		stderr = os.Stderr
	}
	if c.Auth == nil {
		return nil, errors.New("startup requires a validated auth section")
	}
	if err := removeAmbientAzureEnv(stderr); err != nil {
		return nil, err
	}
	credential, warning, err := incident.NewCredential(*c.Auth, incident.CredentialOptions{Transport: deps.transport})
	if err != nil {
		return nil, err
	}
	if warning != "" {
		_, _ = fmt.Fprintf(stderr, "cenci incident: warning: %s\n", warning)
	}
	store := incident.NewStore(c.StateDir)
	lock, err := store.WorkerLock()
	if err != nil {
		return nil, err
	}
	started := false
	defer func() {
		if !started {
			_ = lock.Close()
		}
	}()
	if err := incident.Preflight(ctx, credential, *c.Auth); err != nil {
		return nil, err
	}
	agent := &incident.ContainerAgent{Config: c}
	if err := agent.Cleanup(ctx); err != nil {
		return nil, err
	}
	if err := store.Recover(); err != nil {
		return nil, err
	}
	receiver, err := deps.newReceiver(c, credential)
	if err != nil {
		return nil, err
	}
	started = true
	return &incidentRun{
		cfg:      c,
		store:    store,
		lock:     lock,
		receiver: receiver,
		pipeline: &incident.Pipeline{Config: c, Agent: agent, Checks: incident.ContainerChecks{Agent: agent}, Telemetry: incident.AzureTelemetry{Credential: credential, Client: deps.telemetryClient}, Publisher: incident.GitHub{StateDir: c.StateDir}},
		stderr:   stderr,
	}, nil
}

// verifyDeps are the Go-level seams of verifyIncident. The zero value of every
// field selects production behavior; nothing here is read from the environment,
// a flag or the configuration file.
type verifyDeps struct {
	transport       policy.Transporter                                                                 // identity-authority transport for the credential; nil means the SDK default
	telemetryClient *http.Client                                                                       // nil means the telemetry adapter's default
	openBroker      func(incident.Config, azcore.TokenCredential) (incident.ServiceBusReceiver, error) // nil means a real Service Bus client that tries once
	now             func() time.Time                                                                   // nil means the wall clock
	sleep           func(context.Context, time.Duration) error                                         // nil means waiting on the timer
	stdout          io.Writer                                                                          // nil means os.Stdout
	stderr          io.Writer                                                                          // nil means os.Stderr
}

// verifyIncident authenticates as the configured worker identity and runs the
// bounded verify probes, returning the process exit code: 0 when every probe
// passed, 1 otherwise. It takes no worker lock, opens no store, cleans no
// container and writes no file, so it can run next to a live worker. The only
// state it changes is its own environment: ambient AZURE_* variables are removed
// exactly as run removes them, before the credential is built.
func verifyIncident(ctx context.Context, c incident.Config, deps verifyDeps, wait time.Duration) int {
	stdout, stderr := deps.stdout, deps.stderr
	if stdout == nil {
		stdout = os.Stdout
	}
	if stderr == nil {
		stderr = os.Stderr
	}
	refuse := func(err error) int { _, _ = fmt.Fprintf(stderr, "cenci incident: %v\n", err); return 1 }
	if c.Auth == nil {
		return refuse(errors.New("verify requires a validated auth section"))
	}
	if err := removeAmbientAzureEnv(stderr); err != nil {
		return refuse(err)
	}
	credential, warning, err := incident.NewCredential(*c.Auth, incident.CredentialOptions{Transport: deps.transport})
	if err != nil {
		return refuse(err)
	}
	if warning != "" {
		_, _ = fmt.Fprintf(stderr, "cenci incident: warning: %s\n", warning)
	}
	if !incident.Verify(ctx, c, credential, incident.VerifyOptions{
		PropagationWait: wait,
		HTTPClient:      deps.telemetryClient,
		OpenBroker:      deps.openBroker,
		Now:             deps.now,
		Sleep:           deps.sleep,
		Stdout:          stdout,
		Stderr:          stderr,
	}) {
		return 1
	}
	return 0
}

// sdkLoggingEnv is the variable the Azure SDK reads at package init to install
// a console logger that writes every identity request to stderr.
const sdkLoggingEnv = "AZURE_SDK_GO_LOGGING"

// removeAmbientAzureEnv prints one notice naming (never valuing) every
// AZURE_-prefixed variable in the process environment and removes each, so
// nothing the worker starts afterwards can inherit or read them.
func removeAmbientAzureEnv(stderr io.Writer) error {
	names := incident.IgnoredAzureEnv(os.Environ())
	if len(names) == 0 {
		return nil
	}
	_, _ = fmt.Fprintf(stderr, "cenci incident: ignored and removed ambient Azure environment variables (the worker authenticates only as the identity in its auth section): %s\n", strings.Join(names, ", "))
	for _, name := range names {
		if name == sdkLoggingEnv {
			// The SDK read this in a package init, before main: unsetting it
			// cannot undo the console logger that init installed.
			azlog.SetListener(nil)
		}
		if err := os.Unsetenv(name); err != nil {
			return fmt.Errorf("remove %s from the worker environment: %w", name, err)
		}
	}
	return nil
}

// serve runs the receiver and the worker until ctx ends or the worker fails.
func (r *incidentRun) serve(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	consumerDone := make(chan struct{})
	go func() { incident.Consume(ctx, r.receiver, r.store, r.cfg); close(consumerDone) }()
	runErr := incident.NewWorker(r.cfg, r.store, r.pipeline).Run(ctx)
	cancel()
	<-consumerDone
	return runErr
}

// close releases the receiver, then the worker lock.
func (r *incidentRun) close() {
	closeCtx, closeCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer closeCancel()
	if err := r.receiver.Close(closeCtx); err != nil {
		_, _ = fmt.Fprintf(r.stderr, "cenci incident: close receiver: %v\n", err)
	}
	_ = r.lock.Close()
}
