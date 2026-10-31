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

func runIncident(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "cenci incident: use run, status, or cancel")
		os.Exit(2)
	}
	verb := args[0]
	if verb != "run" && verb != "status" && verb != "cancel" {
		fmt.Fprintln(os.Stderr, "cenci incident: use run, status, or cancel")
		os.Exit(2)
	}
	fs := flag.NewFlagSet("incident "+verb, flag.ExitOnError)
	config := fs.String("config", "", "path to opt-in incident worker JSON config")
	state := fs.String("state-dir", "", "absolute incident state directory (status/cancel only)")
	key := fs.String("id", "", "incident key to cancel (cancel only)")
	_ = fs.Parse(args[1:])
	rejectExtra("cenci incident "+verb, fs.Args())
	if verb == "run" && (*config == "" || *state != "" || *key != "") || verb != "run" && (*state == "" || *config != "") || verb == "status" && *key != "" || verb == "cancel" && *key == "" {
		fmt.Fprintln(os.Stderr, "cenci incident: run needs --config; status/cancel need --state-dir; cancel also needs --id")
		os.Exit(2)
	}
	fail := func(err error) { fmt.Fprintf(os.Stderr, "cenci incident: %v\n", err); os.Exit(1) }
	if verb != "run" {
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
		fmt.Fprintf(stderr, "cenci incident: warning: %s\n", warning)
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
	fmt.Fprintf(stderr, "cenci incident: ignored and removed ambient Azure environment variables (the worker authenticates only as the identity in its auth section): %s\n", strings.Join(names, ", "))
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
		fmt.Fprintf(r.stderr, "cenci incident: close receiver: %v\n", err)
	}
	_ = r.lock.Close()
}
