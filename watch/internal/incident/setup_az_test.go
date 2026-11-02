package incident

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
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

// The 60-second per-call bound is pinned by value, and by effect: with no
// context deadline of its own, a call that outlives callTimeout is cut off.
func TestAzCallHasASixtySecondBoundThatCommandEnforces(t *testing.T) {
	if callTimeout != 60*time.Second {
		t.Fatalf("per-call bound %v, want 60s", callTimeout)
	}
	callTimeout = 200 * time.Millisecond
	t.Cleanup(func() { callTimeout = 60 * time.Second })
	start := time.Now()
	_, err := fakeAz(t, "exec sleep 30").AccountShow(context.Background())
	var azErr *AzError
	if !errors.As(err, &azErr) || azErr.Reason != "timed out" || time.Since(start) > 10*time.Second {
		t.Fatalf("want a prompt timed-out step from command's own bound, got %v after %v", err, time.Since(start))
	}
}

func writeFakeAz(t *testing.T, script string) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "az")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"+script+"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	return bin
}

// fakeAz is a preview Az (the nil allowlist) over a fake az script.
func fakeAz(t *testing.T, script string) *Az {
	t.Helper()
	return &Az{bin: writeFakeAz(t, script)}
}

// fakeApplyAz is an Az with the apply allowlist over a fake az script.
func fakeApplyAz(t *testing.T, script string) *Az {
	t.Helper()
	return &Az{bin: writeFakeAz(t, script), allow: azApply}
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

const (
	azTestRoleGUID = RoleServiceBusDataReceiver
	azTestScope    = "/subscriptions/" + testTenantID + "/resourceGroups/rg/providers/Microsoft.ServiceBus/namespaces/contoso/queues/alerts"
	azTestRoleDef  = "/subscriptions/" + testTenantID + "/providers/Microsoft.Authorization/roleDefinitions/" + azTestRoleGUID
)

func stepKeys(set [][]string) []string {
	keys := make([]string, 0, len(set))
	for _, prefix := range set {
		keys = append(keys, strings.Join(prefix, " "))
	}
	sort.Strings(keys)
	return keys
}

func TestAzApplyAllowlistIsThePreviewSetPlusRoleAssignmentCreate(t *testing.T) {
	want := append(stepKeys(azReadOnly), "role assignment create")
	sort.Strings(want)
	if got := stepKeys(azApply); !reflect.DeepEqual(got, want) {
		t.Fatalf("apply allowlist is not the preview set plus role assignment create:\n got  %v\n want %v", got, want)
	}
	for _, prefix := range azReadOnly {
		if prefix[len(prefix)-1] == "create" {
			t.Fatalf("the preview allowlist holds %v", prefix)
		}
	}
}

func TestAzApplyAzRefusesEverythingOutsideItsAllowlistWithoutExecuting(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "ran")
	az := fakeApplyAz(t, "touch '"+marker+"'")
	for _, args := range [][]string{
		{"role", "assignment", "delete", "--ids", "x"},
		{"ad", "sp", "create", "--id", "x"},
		{"ad", "app", "credential", "reset", "--id", "x"},
		{"login"},
	} {
		_, err := az.run(context.Background(), args...)
		var azErr *AzError
		if !errors.As(err, &azErr) || azErr.Step != "(refused)" {
			t.Fatalf("%v: want a (refused) AzError, got %v", args, err)
		}
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("a refused argv executed az (marker stat: %v)", err)
	}
	// The read-only entry point keeps refusing a create even on an apply Az.
	if _, err := az.runReadOnly(context.Background(), "role", "assignment", "create"); err == nil {
		t.Fatal("runReadOnly must refuse role assignment create on an apply Az")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("runReadOnly executed a create")
	}
	// Control: an allowlisted mutating argv does execute the fake.
	_, _ = az.run(context.Background(), "role", "assignment", "create")
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("control failed, the fake az was not the executable in use: %v", err)
	}
}

func TestAzPreviewStillRefusesRoleAssignmentCreate(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "ran")
	script := "touch '" + marker + "'"
	preview := fakeAz(t, script)
	err := preview.RoleAssignmentCreate(context.Background(), testObjectID, azTestRoleGUID, azTestScope)
	var azErr *AzError
	if !errors.As(err, &azErr) || azErr.Step != "(refused)" {
		t.Fatalf("want the preview Az to refuse the create, got %v", err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("the preview Az executed a create (marker stat: %v)", err)
	}
	// Control: the apply Az runs the very same call.
	_ = fakeApplyAz(t, script).RoleAssignmentCreate(context.Background(), testObjectID, azTestRoleGUID, azTestScope)
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("control failed, the apply Az did not execute the create: %v", err)
	}
}

// answeringApplyAz returns an apply Az whose fake az records its argv, one
// element per line, and prints output with the given exit code.
func answeringApplyAz(t *testing.T, output string, exit int) (*Az, string) {
	t.Helper()
	dir := t.TempDir()
	args, out := filepath.Join(dir, "args"), filepath.Join(dir, "out")
	put(t, out, output)
	return fakeApplyAz(t, "printf '%s\\n' \"$@\" > '"+args+"'\ncat '"+out+"'\nexit "+strconv.Itoa(exit)), args
}

func argvOf(t *testing.T, path string) []string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the fake az was not run: %v", err)
	}
	return strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
}

func assignmentJSON(principal, roleDef, scope string) string {
	return `{"principalId":"` + principal + `","roleDefinitionId":"` + roleDef + `","scope":"` + scope + `"}`
}

func requireMalformed(t *testing.T, name, step string, err error) {
	t.Helper()
	var azErr *AzError
	if !errors.As(err, &azErr) || azErr.Step != step || azErr.Reason != "malformed or unexpected output" || IsAzUnavailable(err) {
		t.Fatalf("%s: want a malformed-output %s step, got %v", name, step, err)
	}
}

func TestAzRoleAssignmentWrappersValidateTheirOutput(t *testing.T) {
	ctx := context.Background()
	upperOID := strings.ToUpper(testObjectID)

	t.Run("list-parses-and-sends-the-exact-argv", func(t *testing.T) {
		az, args := answeringApplyAz(t, "["+assignmentJSON(upperOID, strings.ToUpper(azTestRoleDef), azTestScope)+"]", 0)
		got, err := az.RoleAssignmentsAt(ctx, testObjectID, azTestScope)
		if err != nil {
			t.Fatal(err)
		}
		want := []ScopedAssignment{{PrincipalID: upperOID, RoleGUID: strings.ToUpper(azTestRoleGUID), Scope: azTestScope}}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %+v want %+v", got, want)
		}
		wantArgs := []string{"role", "assignment", "list", "--assignee-object-id", testObjectID, "--scope", azTestScope, "-o", "json"}
		if !reflect.DeepEqual(argvOf(t, args), wantArgs) {
			t.Fatalf("argv %v want %v", argvOf(t, args), wantArgs)
		}
	})
	t.Run("list-of-nothing-is-empty-not-an-error", func(t *testing.T) {
		az, _ := answeringApplyAz(t, "[]", 0)
		got, err := az.RoleAssignmentsAt(ctx, testObjectID, azTestScope)
		if err != nil || got == nil || len(got) != 0 {
			t.Fatalf("want an empty non-nil list, got %v, %v", got, err)
		}
	})
	t.Run("list-rejects-malformed-output", func(t *testing.T) {
		for name, out := range map[string]string{
			"empty":            "",
			"null":             "null",
			"object":           assignmentJSON(testObjectID, azTestRoleDef, azTestScope),
			"not-json":         "Traceback",
			"trailing-value":   "[] []",
			"bad-principal":    "[" + assignmentJSON("not-a-uuid", azTestRoleDef, azTestScope) + "]",
			"role-without-id":  "[" + assignmentJSON(testObjectID, "/providers/Microsoft.Authorization/roleDefinitions/Reader", azTestScope) + "]",
			"role-bad-segment": "[" + assignmentJSON(testObjectID, azTestRoleDef+"/extra", azTestScope) + "]",
			"empty-scope":      "[" + assignmentJSON(testObjectID, azTestRoleDef, "") + "]",
			"newline-scope":    `[{"principalId":"` + testObjectID + `","roleDefinitionId":"` + azTestRoleDef + `","scope":"/subscriptions/x\ny"}]`,
		} {
			az, _ := answeringApplyAz(t, out, 0)
			_, err := az.RoleAssignmentsAt(ctx, testObjectID, azTestScope)
			requireMalformed(t, name, "role assignment list", err)
		}
	})
	t.Run("list-failure-keeps-its-class", func(t *testing.T) {
		az, _ := answeringApplyAz(t, "", 3)
		_, err := az.RoleAssignmentsAt(ctx, testObjectID, azTestScope)
		if !IsAzUnavailable(err) {
			t.Fatalf("a non-zero exit is an unavailable value, got %v", err)
		}
	})
	t.Run("invalid-inputs-never-reach-az", func(t *testing.T) {
		for name, in := range map[string][3]string{
			"object-id":    {"me", azTestRoleGUID, azTestScope},
			"role":         {testObjectID, "Owner", azTestScope},
			"relative":     {testObjectID, azTestRoleGUID, "queues/alerts"},
			"flag-scope":   {testObjectID, azTestRoleGUID, "--all"},
			"spaced-scope": {testObjectID, azTestRoleGUID, azTestScope + " --all"},
		} {
			az, args := answeringApplyAz(t, "[]", 0)
			if name != "role" { // the list call takes no role
				_, listErr := az.RoleAssignmentsAt(ctx, in[0], in[2])
				requireMalformed(t, name, "role assignment list", listErr)
			}
			createErr := az.RoleAssignmentCreate(ctx, in[0], in[1], in[2])
			requireMalformed(t, name, "role assignment create", createErr)
			if _, err := os.Stat(args); !os.IsNotExist(err) {
				t.Fatalf("%s: invalid input reached az", name)
			}
		}
	})

	t.Run("create-accepts-its-own-echo-and-sends-the-exact-argv", func(t *testing.T) {
		az, args := answeringApplyAz(t, assignmentJSON(upperOID, strings.ToUpper(azTestRoleDef), strings.ToUpper(azTestScope)), 0)
		if err := az.RoleAssignmentCreate(ctx, testObjectID, azTestRoleGUID, azTestScope); err != nil {
			t.Fatal(err)
		}
		wantArgs := []string{"role", "assignment", "create", "--assignee-object-id", testObjectID, "--assignee-principal-type", "ServicePrincipal", "--role", azTestRoleGUID, "--scope", azTestScope, "-o", "json"}
		if !reflect.DeepEqual(argvOf(t, args), wantArgs) {
			t.Fatalf("argv %v want %v", argvOf(t, args), wantArgs)
		}
	})
	t.Run("create-rejects-an-echo-that-differs-from-the-request", func(t *testing.T) {
		otherRole := strings.Replace(azTestRoleDef, azTestRoleGUID, RoleLogAnalyticsReader, 1)
		parent := strings.TrimSuffix(azTestScope, "/queues/alerts")
		for name, out := range map[string]string{
			"other-principal": assignmentJSON("55555555-5555-4555-8555-555555555555", azTestRoleDef, azTestScope),
			"other-role":      assignmentJSON(testObjectID, otherRole, azTestScope),
			"parent-scope":    assignmentJSON(testObjectID, azTestRoleDef, parent),
			"child-scope":     assignmentJSON(testObjectID, azTestRoleDef, azTestScope+"/x"),
			"empty":           "",
			"array":           "[" + assignmentJSON(testObjectID, azTestRoleDef, azTestScope) + "]",
			"no-fields":       "{}",
			"trailing-value":  assignmentJSON(testObjectID, azTestRoleDef, azTestScope) + " {}",
		} {
			az, _ := answeringApplyAz(t, out, 0)
			requireMalformed(t, name, "role assignment create", az.RoleAssignmentCreate(ctx, testObjectID, azTestRoleGUID, azTestScope))
		}
	})
	t.Run("create-failure-keeps-its-class", func(t *testing.T) {
		az, _ := answeringApplyAz(t, "", 1)
		err := az.RoleAssignmentCreate(ctx, testObjectID, azTestRoleGUID, azTestScope)
		var azErr *AzError
		if !errors.As(err, &azErr) || azErr.Step != "role assignment create" || !IsAzUnavailable(err) {
			t.Fatalf("want an unavailable role assignment create step, got %v", err)
		}
	})
}
