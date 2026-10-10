package incident

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// The fake runtime retains a container after failed removal, and implements the
// owner-label filter used by real runtimes. Cleanup must remove that exact orphan.
func ownerRuntime(t *testing.T) (*ContainerAgent, string) {
	t.Helper()
	a, trace := runtimeFixture(t)
	inventory := filepath.Join(filepath.Dir(trace), "orphan")
	t.Setenv("CENCI_INCIDENT_TEST_ORPHAN", inventory)
	put(t, filepath.Join(filepath.Dir(trace), "podman"), `#!/bin/sh
case "$1" in
 network) printf 'true\n' ;;
 run)
   for arg do
     case "$arg" in cenci.incident.owner=*) printf '%s\n' "$arg" > "$CENCI_INCIDENT_TEST_ORPHAN" ;; esac
   done
   cat >/dev/null
   printf '%s\n' "$CENCI_INCIDENT_TEST_RESULT"
   ;;
 ps)
   [ -f "$CENCI_INCIDENT_TEST_ORPHAN" ] || exit 0
   IFS= read -r owner < "$CENCI_INCIDENT_TEST_ORPHAN"
   for arg do
     if [ "$arg" = "label=$owner" ]; then printf 'abcdef012345\n'; fi
   done
   ;;
 rm)
   [ "$CENCI_INCIDENT_TEST_REMOVE_EXIT" = 1 ] && exit 1
   rm -f "$CENCI_INCIDENT_TEST_ORPHAN"
   ;;
 *) exit 9 ;;
esac
`)
	return a, inventory
}

func orphanForOwner(t *testing.T, a *ContainerAgent, inventory string) {
	t.Helper()
	t.Setenv("CENCI_INCIDENT_TEST_REMOVE_EXIT", "1")
	_, err := a.Run(context.Background(), AgentRequest{Phase: "investigate", Incident: Incident{Key: Key("a")}, Worktree: "/worktree", History: "/history", BudgetUSD: 0.5})
	var cleanup *CleanupFailure
	if !errors.As(err, &cleanup) {
		t.Fatalf("fixture must leave an orphan after cleanup fails: %v", err)
	}
	if _, err := os.Stat(inventory); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CENCI_INCIDENT_TEST_REMOVE_EXIT", "0")
}

func TestOwnerCleanupAcrossEquivalentStateDirectories(t *testing.T) {
	for _, spelling := range []string{"trailing-slash", "symlink", "case-alias"} {
		t.Run(spelling, func(t *testing.T) {
			a, inventory := ownerRuntime(t)
			// Exercise first startup: the worker lock creates a missing state dir.
			a.Config.StateDir = filepath.Join(t.TempDir(), "new-state")
			store := NewStore(a.Config.StateDir)
			lock, err := store.WorkerLock()
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = lock.Close() }()
			ingest(t, store, "a", "Fired", a.Config)
			orphanForOwner(t, a, inventory)
			alternate := a.Config.StateDir + "/"
			if spelling == "symlink" {
				alternate = filepath.Join(t.TempDir(), "state-alias")
				if err := os.Symlink(a.Config.StateDir, alternate); err != nil {
					t.Fatal(err)
				}
			}
			if spelling == "case-alias" {
				alternate = filepath.Join(filepath.Dir(a.Config.StateDir), "NEW-STATE")
				if _, err := os.Stat(alternate); os.IsNotExist(err) {
					t.Skip("filesystem is case sensitive")
				} else if err != nil {
					t.Fatal(err)
				}
			}
			a.Config.StateDir = alternate
			aliasStore := NewStore(alternate)
			if other, err := aliasStore.WorkerLock(); err == nil {
				_ = other.Close()
				t.Fatal("equivalent path must share the lifetime lock")
			}
			rows, err := aliasStore.List()
			if err != nil || len(rows) != 1 || rows[0].AlertID != "a" {
				t.Fatalf("equivalent path must share ledger: %+v %v", rows, err)
			}
			if err := a.Cleanup(context.Background()); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(inventory); !os.IsNotExist(err) {
				t.Fatalf("recovery missed orphan through equivalent path: %v", err)
			}
			// Reverse the direction to cover launch through an alias as well.
			orphanForOwner(t, a, inventory)
			a.Config.StateDir = store.Dir
			if err := a.Cleanup(context.Background()); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(inventory); !os.IsNotExist(err) {
				t.Fatalf("agent launched with noncanonical owner: %v", err)
			}
		})
	}
}

func TestOwnerCleanupPreservesCaseDistinctStateDirectory(t *testing.T) {
	a, inventory := ownerRuntime(t)
	parent := t.TempDir()
	a.Config.StateDir = filepath.Join(parent, "state")
	other := filepath.Join(parent, "STATE")
	for _, dir := range []string{a.Config.StateDir, other} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	firstInfo, err := os.Stat(a.Config.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	otherInfo, err := os.Stat(other)
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(firstInfo, otherInfo) {
		t.Skip("filesystem is case insensitive")
	}
	orphanForOwner(t, a, inventory)
	a.Config.StateDir = other
	if err := a.Cleanup(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(inventory); err != nil {
		t.Fatalf("cleanup removed a different state directory's container: %v", err)
	}
}

func TestOwnerCheckLaunchUsesCanonicalStateDirectory(t *testing.T) {
	a, inventory := ownerRuntime(t)
	canonical := a.Config.StateDir
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(canonical, alias); err != nil {
		t.Fatal(err)
	}
	a.Config.StateDir = alias + "/"
	t.Setenv("CENCI_INCIDENT_TEST_REMOVE_EXIT", "1")
	_, err := a.RunCheck(context.Background(), "/repo/.worktrees/incident-a", []string{"go", "test", "./..."})
	var cleanup *CleanupFailure
	if !errors.As(err, &cleanup) {
		t.Fatalf("fixture must leave an orphan after cleanup fails: %v", err)
	}
	t.Setenv("CENCI_INCIDENT_TEST_REMOVE_EXIT", "0")
	a.Config.StateDir = canonical
	if err := a.Cleanup(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(inventory); !os.IsNotExist(err) {
		t.Fatalf("check launched with noncanonical owner: %v", err)
	}
}

func TestOwnerResolutionFailurePreventsRuntimeCommands(t *testing.T) {
	for _, entry := range []string{"cleanup", "agent", "check"} {
		t.Run(entry, func(t *testing.T) {
			a, trace := runtimeFixture(t)
			a.Config.StateDir = filepath.Join(t.TempDir(), "missing")
			var err error
			switch entry {
			case "cleanup":
				err = a.Cleanup(context.Background())
			case "agent":
				_, err = a.Run(context.Background(), AgentRequest{Phase: "investigate", Incident: Incident{Key: Key("a")}, BudgetUSD: 0.5})
			case "check":
				_, err = a.RunCheck(context.Background(), "/worktree", []string{"true"})
			}
			if err == nil || !strings.Contains(err.Error(), "resolve incident state directory") {
				t.Fatalf("missing state directory must fail explicitly: %v", err)
			}
			if entry == "check" {
				var unavailable *CheckUnavailable
				if !errors.As(err, &unavailable) {
					t.Fatalf("identity failure must not count as a red regression: %v", err)
				}
			}
			if _, err := os.Stat(trace); !os.IsNotExist(err) {
				t.Fatalf("runtime called after identity resolution failed: %v", err)
			}
		})
	}
}

func TestOwnerIdentityPersistsAcrossRestartAndDirectoryRename(t *testing.T) {
	a, inventory := ownerRuntime(t)
	orphanForOwner(t, a, inventory)
	id, err := os.ReadFile(filepath.Join(a.Config.StateDir, "owner-id"))
	if err != nil || len(id) != 24 {
		t.Fatalf("missing durable owner identity: %q %v", id, err)
	}
	label, err := os.ReadFile(inventory)
	if err != nil || string(label) != "cenci.incident.owner="+string(id)+"\n" {
		t.Fatalf("runtime does not use durable identity: %q %v", label, err)
	}
	moved := a.Config.StateDir + "-moved"
	if err := os.Rename(a.Config.StateDir, moved); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(moved) })
	config := a.Config
	config.StateDir = moved
	restarted := &ContainerAgent{Config: config}
	if err := restarted.Cleanup(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(inventory); !os.IsNotExist(err) {
		t.Fatalf("restart failed to recover container after state rename: %v", err)
	}
	persisted, err := os.ReadFile(filepath.Join(moved, "owner-id"))
	if err != nil || string(persisted) != string(id) {
		t.Fatalf("restart changed persisted owner: %q %v", persisted, err)
	}
}

func TestOwnerInvalidIdentityPreventsRuntimeCommands(t *testing.T) {
	for _, invalid := range []string{"directory", "", "INVALID-ID", strings.Repeat("a", 23), strings.Repeat("A", 24), strings.Repeat("g", 24), strings.Repeat("a", 24) + "\n"} {
		for _, entry := range []string{"cleanup", "agent", "check"} {
			t.Run(entry+"-"+invalid, func(t *testing.T) {
				a, trace := runtimeFixture(t)
				path := filepath.Join(a.Config.StateDir, "owner-id")
				if invalid == "directory" {
					if err := os.Mkdir(path, 0700); err != nil {
						t.Fatal(err)
					}
				} else {
					put(t, path, invalid)
				}
				var err error
				switch entry {
				case "cleanup":
					err = a.Cleanup(context.Background())
				case "agent":
					_, err = a.Run(context.Background(), AgentRequest{Phase: "investigate", Incident: Incident{Key: Key("a")}, BudgetUSD: 0.5})
				case "check":
					_, err = a.RunCheck(context.Background(), "/worktree", []string{"true"})
				}
				if err == nil || !strings.Contains(err.Error(), "incident owner identity") {
					t.Fatalf("invalid identity must fail explicitly: %v", err)
				}
				if entry == "check" {
					var unavailable *CheckUnavailable
					if !errors.As(err, &unavailable) {
						t.Fatalf("identity failure must not count as regression: %v", err)
					}
				}
				if _, err := os.Stat(trace); !os.IsNotExist(err) {
					t.Fatalf("runtime called after identity validation failed: %v", err)
				}
				if invalid == "directory" {
					info, err := os.Stat(path)
					if err != nil || !info.IsDir() {
						t.Fatalf("unreadable owner identity was replaced: %v", err)
					}
				} else if b, err := os.ReadFile(path); err != nil || string(b) != invalid {
					t.Fatalf("invalid owner identity was replaced: %q %v", b, err)
				}
			})
		}
	}
}

func TestOwnerConcurrentInitializationUsesOneIdentity(t *testing.T) {
	a, trace := runtimeFixture(t)
	const callers = 8
	errs := make(chan error, callers)
	var wg sync.WaitGroup
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			fresh := &ContainerAgent{Config: a.Config}
			errs <- fresh.Cleanup(context.Background())
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	id, err := os.ReadFile(filepath.Join(a.Config.StateDir, "owner-id"))
	if err != nil || len(id) != 24 {
		t.Fatalf("missing durable identity: %q %v", id, err)
	}
	b, err := os.ReadFile(trace)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) != callers {
		t.Fatalf("missing runtime cleanup invocations: %s", b)
	}
	for _, line := range lines {
		if line != "ps -aq --filter label=cenci.incident.owner="+string(id) {
			t.Fatalf("concurrent callers used different owners: %s", b)
		}
	}
}

func TestOwnerCleanupSurvivesCorruptLedger(t *testing.T) {
	a, inventory := ownerRuntime(t)
	orphanForOwner(t, a, inventory)
	put(t, filepath.Join(a.Config.StateDir, "incidents.json"), "not valid JSON")
	if err := a.Cleanup(context.Background()); err != nil {
		t.Fatalf("valid owner identity must allow cleanup before ledger recovery: %v", err)
	}
	if _, err := os.Stat(inventory); !os.IsNotExist(err) {
		t.Fatalf("corrupt ledger prevented orphan cleanup: %v", err)
	}
	var malformed *json.SyntaxError
	if err := NewStore(a.Config.StateDir).Recover(); !errors.As(err, &malformed) {
		t.Fatalf("recovery must still reject the corrupt ledger: %v", err)
	}
}
