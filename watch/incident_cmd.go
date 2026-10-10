package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
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
	store := incident.NewStore(c.StateDir)
	lock, err := store.WorkerLock()
	if err != nil {
		fail(err)
	}
	defer func() { _ = lock.Close() }()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	agent := &incident.ContainerAgent{Config: c}
	if err := agent.Cleanup(ctx); err != nil {
		fail(err)
	}
	if err := store.Recover(); err != nil {
		fail(err)
	}
	credential, err := azidentity.NewDefaultAzureCredential(nil)
	if err != nil {
		fail(err)
	}
	receiver, err := incident.NewAzureReceiver(c, credential)
	if err != nil {
		fail(err)
	}
	defer func() {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer closeCancel()
		if err := receiver.Close(closeCtx); err != nil {
			fmt.Fprintf(os.Stderr, "cenci incident: close receiver: %v\n", err)
		}
	}()
	pipeline := &incident.Pipeline{Config: c, Agent: agent, Checks: incident.ContainerChecks{Agent: agent}, Telemetry: incident.AzureTelemetry{Credential: credential}, Publisher: incident.GitHub{StateDir: c.StateDir}}
	consumerDone := make(chan struct{})
	go func() { incident.Consume(ctx, receiver, store, c); close(consumerDone) }()
	worker := incident.NewWorker(c, store, pipeline)
	runErr := worker.Run(ctx)
	cancel()
	<-consumerDone
	if runErr != nil {
		fail(runErr)
	}
}
