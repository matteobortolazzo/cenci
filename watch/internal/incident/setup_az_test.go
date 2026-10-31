package incident

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A call that outlives its deadline is a failed discovery step, not an
// unreadable value: IsAzUnavailable must stay false so the CLI exits 1. The
// production bound is command()'s fixed 60 seconds; this test shortens the
// context deadline that the same code path honors, so it stays fast.
func TestAzCallPastItsDeadlineIsAFailedStepNotAnUnavailableValue(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "az")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexec sleep 30\n"), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := (&Az{bin: bin}).AccountShow(ctx)
	if time.Since(start) > 10*time.Second {
		t.Fatalf("the call was not bounded: %v", time.Since(start))
	}
	var azErr *AzError
	if !errors.As(err, &azErr) || azErr.Reason != "timed out" || azErr.Step != "account show" {
		t.Fatalf("want a timed-out account show step, got %v", err)
	}
	if IsAzUnavailable(err) {
		t.Fatal("a timeout must be a failed step, not an unavailable value")
	}
}

func fakeAz(t *testing.T, script string) *Az {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "az")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"+script+"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	return &Az{bin: bin}
}

// Classification keys on the errOutputLimit sentinel, never on error text: az's
// own stderr is part of the wrapped error and may say anything.
func TestAzCallWhoseStderrMentionsTheOutputLimitIsStillAnUnavailableValue(t *testing.T) {
	az := fakeAz(t, "echo 'command output exceeds 4 MiB' >&2\nexit 1")
	_, err := az.AccountShow(context.Background())
	var azErr *AzError
	if !errors.As(err, &azErr) || azErr.Reason != "exited with code 1" {
		t.Fatalf("want an exited-with-code-1 step, got %v", err)
	}
	if !IsAzUnavailable(err) {
		t.Fatal("a non-zero exit is an unavailable value whatever its stderr says")
	}
}

func TestAzCallCancelledMidFlightIsAFailedStepNotAnUnavailableValue(t *testing.T) {
	az := fakeAz(t, "exec sleep 30")
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(300*time.Millisecond, cancel)
	start := time.Now()
	_, err := az.AccountShow(ctx)
	if time.Since(start) > 10*time.Second {
		t.Fatalf("the call was not interrupted: %v", time.Since(start))
	}
	var azErr *AzError
	if !errors.As(err, &azErr) || azErr.Reason != "interrupted" || azErr.Step != "account show" {
		t.Fatalf("want an interrupted account show step, got %v", err)
	}
	if IsAzUnavailable(err) {
		t.Fatal("an interrupted call must be a failed step, not an unavailable value")
	}
}

func TestAzCallKilledBySignalIsAFailedStepNotAnUnavailableValue(t *testing.T) {
	az := fakeAz(t, "kill -KILL $$")
	_, err := az.AccountShow(context.Background())
	var azErr *AzError
	if !errors.As(err, &azErr) || azErr.Reason != "terminated by signal" || azErr.Step != "account show" {
		t.Fatalf("want a terminated-by-signal account show step, got %v", err)
	}
	if IsAzUnavailable(err) {
		t.Fatal("a signal death must be a failed step, not an unavailable value")
	}
}

// Only the exec-free refusal protects the preview from a mutating subcommand a
// future caller might pass, so the fake proves nothing ran.
func TestAzRunReadOnlyRefusesMutatingSubcommandsWithoutExecutingAnything(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "ran")
	az := fakeAz(t, "touch '"+marker+"'")
	for _, args := range [][]string{
		{"role", "assignment", "create", "--assignee-object-id", "x"},
		{"ad", "sp", "create", "--id", "x"},
		{"ad", "app", "create", "--display-name", "x"},
		{"login"},
	} {
		_, err := az.runReadOnly(context.Background(), args...)
		var azErr *AzError
		if !errors.As(err, &azErr) || azErr.Step != "(refused)" {
			t.Fatalf("%v: want a (refused) AzError, got %v", args, err)
		}
		if IsAzUnavailable(err) {
			t.Fatalf("%v: a refusal is not an unavailable value", args)
		}
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("a refused argv executed az (marker stat: %v)", err)
	}
	// Control: an allowlisted argv does execute the fake.
	_, _ = az.runReadOnly(context.Background(), "account", "show")
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("control failed, the fake az was not the executable in use: %v", err)
	}
}

func TestAzCommandOverflowingStderrReportsTheOutputLimit(t *testing.T) {
	az := fakeAz(t, "head -c 5000000 /dev/zero >&2")
	_, err := commandDeadline(context.Background(), "", nil, nil, az.bin)
	if !errors.Is(err, errOutputLimit) {
		t.Fatalf("want errOutputLimit for stderr past 4 MiB, got %v", err)
	}
}
