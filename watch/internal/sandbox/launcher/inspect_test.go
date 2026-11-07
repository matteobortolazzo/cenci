package launcher

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/errcode"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/exectest"
)

// Fake-runtime contract for these tests: `images` lists inspectFakeImage (so
// imageCurrent's imageExists probe passes) and every other verb records its
// argv NUL-separated into the returned log (truncated per call), writes
// stdout/stderr, and exits with exitCode. Tests drive the real inspect helpers
// and their migrated call sites across a genuine subprocess boundary.
const (
	inspectFakeImage     = "img"
	inspectFakeContainer = "c1"
)

type inspectFake struct {
	runtime string
	argvLog string
}

func newInspectFake(t *testing.T, stdout, stderr string, exitCode int) inspectFake {
	t.Helper()
	dir := t.TempDir()
	f := inspectFake{
		runtime: filepath.Join(dir, "fake-runtime"),
		argvLog: filepath.Join(dir, "argv.log"),
	}
	body := "#!/bin/sh\n" +
		"if [ \"$1\" = images ]; then printf '%s\\n' " + exectest.ShellQuote(inspectFakeImage) + "; exit 0; fi\n" +
		"printf '%s\\000' \"$@\" > " + exectest.ShellQuote(f.argvLog) + "\n" +
		"printf '%s' " + exectest.ShellQuote(stdout) + "\n" +
		"printf '%s' " + exectest.ShellQuote(stderr) + " >&2\n" +
		fmt.Sprintf("exit %d\n", exitCode)
	exectest.WriteExecutable(t, f.runtime, body)
	return f
}

func (f inspectFake) engine() *Engine {
	return &Engine{Runtime: f.runtime, Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}}
}

// argv returns the recorded argv of the last non-`images` invocation.
func (f inspectFake) argv(t *testing.T) []string {
	t.Helper()
	data, err := os.ReadFile(f.argvLog)
	if err != nil {
		t.Fatalf("fake runtime never recorded an invocation: %v", err)
	}
	return strings.Split(strings.TrimSuffix(string(data), "\x00"), "\x00")
}

func TestInspectFormat_WrapsExecErrorWithOp(t *testing.T) {
	fake := newInspectFake(t, "", "Error: No such object: c1\n", 1)
	e := fake.engine()

	_, err := e.inspectFormat("c1", "{{.X}}", "lifecycle label")
	if err == nil {
		t.Fatal("inspectFormat: want error, got nil")
	}
	wantPrefix := fake.runtime + " inspect c1 lifecycle label: "
	if !strings.HasPrefix(err.Error(), wantPrefix) {
		t.Errorf("error = %q, want prefix %q", err, wantPrefix)
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("errors.As(*exec.ExitError) failed on %q", err)
	}
	if got := string(exitErr.Stderr); got != "Error: No such object: c1\n" {
		t.Errorf("ExitError.Stderr = %q, want the fake's stderr (helper must use .Output() with cmd.Stderr unset)", got)
	}
}

func TestInspectFormat_EmptyOpNoTrailingSpace(t *testing.T) {
	fake := newInspectFake(t, "", "boom", 1)
	e := fake.engine()

	_, err := e.inspectFormat("c1", "{{.X}}", "")
	if err == nil {
		t.Fatal("inspectFormat: want error, got nil")
	}
	wantPrefix := fake.runtime + " inspect c1: "
	if !strings.HasPrefix(err.Error(), wantPrefix) {
		t.Errorf("error = %q, want prefix %q", err, wantPrefix)
	}
}

func TestImageInspectFormat_EmptyOp(t *testing.T) {
	fake := newInspectFake(t, "", "boom", 1)
	e := fake.engine()

	_, err := e.imageInspectFormat("img", "{{.X}}", "")
	if err == nil {
		t.Fatal("imageInspectFormat: want error, got nil")
	}
	wantPrefix := fake.runtime + " image inspect img: "
	if !strings.HasPrefix(err.Error(), wantPrefix) {
		t.Errorf("error = %q, want prefix %q", err, wantPrefix)
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Errorf("errors.As(*exec.ExitError) failed on %q", err)
	}
}

func TestImageInspectFormat_WithOp(t *testing.T) {
	fake := newInspectFake(t, "", "boom", 1)
	e := fake.engine()

	_, err := e.imageInspectFormat("img", "{{.X}}", "labels")
	if err == nil {
		t.Fatal("imageInspectFormat: want error, got nil")
	}
	wantPrefix := fake.runtime + " image inspect img labels: "
	if !strings.HasPrefix(err.Error(), wantPrefix) {
		t.Errorf("error = %q, want prefix %q", err, wantPrefix)
	}
}

func TestInspectHelpers_ArgvAndStdout(t *testing.T) {
	const tmpl = `{{range .Mounts}}{{.Source}}{{"\n"}}{{end}}`
	t.Run("container", func(t *testing.T) {
		fake := newInspectFake(t, "out-a\nout-b\n", "", 0)
		got, err := fake.engine().inspectFormat("c1", tmpl, "mounts")
		if err != nil {
			t.Fatalf("inspectFormat: %v", err)
		}
		if string(got) != "out-a\nout-b\n" {
			t.Errorf("stdout = %q, want it verbatim", got)
		}
		want := []string{"inspect", "--format", tmpl, "c1"}
		if argv := fake.argv(t); !reflect.DeepEqual(argv, want) {
			t.Errorf("argv = %q, want %q", argv, want)
		}
	})
	t.Run("image", func(t *testing.T) {
		fake := newInspectFake(t, "sha256:abc\n", "", 0)
		got, err := fake.engine().imageInspectFormat("img", tmpl, "")
		if err != nil {
			t.Fatalf("imageInspectFormat: %v", err)
		}
		if string(got) != "sha256:abc\n" {
			t.Errorf("stdout = %q, want it verbatim", got)
		}
		want := []string{"image", "inspect", "--format", tmpl, "img"}
		if argv := fake.argv(t); !reflect.DeepEqual(argv, want) {
			t.Errorf("argv = %q, want %q", argv, want)
		}
	})
}

func TestContainerStartupState_ExecFailureWrappedAndExitErrorReachable(t *testing.T) {
	fake := newInspectFake(t, "", "Error: No such object: c1\n", 1)
	e := fake.engine()

	_, _, err := e.containerStartupState("c1")
	if err == nil {
		t.Fatal("containerStartupState: want error, got nil")
	}
	wantPrefix := fake.runtime + " inspect c1 state: "
	if !strings.HasPrefix(err.Error(), wantPrefix) {
		t.Errorf("error = %q, want prefix %q", err, wantPrefix)
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("errors.As(*exec.ExitError) failed on wrapped error %q", err)
	}
	if !strings.Contains(string(exitErr.Stderr), "No such object") {
		t.Errorf("ExitError.Stderr = %q, want the fake's stderr", exitErr.Stderr)
	}
}

func TestContainerStartupState_ParseFailureStaysUnwrapped(t *testing.T) {
	fake := newInspectFake(t, "running\n", "", 0)

	_, _, err := fake.engine().containerStartupState("c1")
	if err == nil {
		t.Fatal("containerStartupState: want parse error, got nil")
	}
	if want := `unexpected container state "running"`; err.Error() != want {
		t.Errorf("error = %q, want exactly %q (the parse failure gains no prefix)", err, want)
	}
}

func TestContainerStartupState_ParsesStatusAndExit(t *testing.T) {
	fake := newInspectFake(t, "exited 3\n", "", 0)

	status, code, err := fake.engine().containerStartupState("c1")
	if err != nil {
		t.Fatalf("containerStartupState: %v", err)
	}
	if status != "exited" || code != "3" {
		t.Errorf("got (%q, %q), want (exited, 3)", status, code)
	}
	want := []string{"inspect", "--format", "{{.State.Status}} {{.State.ExitCode}}", "c1"}
	if argv := fake.argv(t); !reflect.DeepEqual(argv, want) {
		t.Errorf("argv = %q, want %q", argv, want)
	}
}

// TestMigratedSites_ExecErrorTextUnchanged pins the byte-identical exec-error
// text of every migrated call site that returns the error (diagnose's
// imageBaseVersion and inspectMounts swallow it by design).
func TestMigratedSites_ExecErrorTextUnchanged(t *testing.T) {
	cases := []struct {
		name   string
		suffix string
		call   func(e *Engine) error
	}{
		{"lifecycleLabel", " inspect c1 lifecycle label: ", func(e *Engine) error {
			_, err := e.lifecycleLabel("c1")
			return err
		}},
		{"containerHasSharedAgentMount", " inspect c1 mounts: ", func(e *Engine) error {
			_, err := e.containerHasSharedAgentMount("c1", "claude")
			return err
		}},
		{"inspectReusePosture", " inspect c1 reuse posture: ", func(e *Engine) error {
			_, err := e.inspectReusePosture("c1")
			return err
		}},
		{"warnIfUnwired", " inspect c1 mounts: ", func(e *Engine) error {
			return e.warnIfUnwired("c1", true, "/sock")
		}},
		{"inspectObservedPosture", " inspect c1 observed posture: ", func(e *Engine) error {
			_, err := e.inspectObservedPosture("c1")
			return err
		}},
		{"imageCurrent", " image inspect img: ", func(e *Engine) error {
			_, _, err := e.imageCurrent("img")
			return err
		}},
		{"imageID", " image inspect img: ", func(e *Engine) error {
			_, err := e.imageID("img")
			return err
		}},
		{"containerImage", " inspect c1: ", func(e *Engine) error {
			_, _, err := e.containerImage("c1")
			return err
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := newInspectFake(t, "", "boom", 1)
			err := tc.call(fake.engine())
			if err == nil {
				t.Fatal("want exec error, got nil")
			}
			want := fake.runtime + tc.suffix
			if !strings.HasPrefix(err.Error(), want) {
				t.Errorf("error = %q, want prefix %q", err, want)
			}
			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) {
				t.Errorf("errors.As(*exec.ExitError) failed on %q", err)
			}
		})
	}
}

// TestImageCurrentAndImageBaseVersion_SameFormatArgv is the behavioural proof
// of the shared format constant: both call sites send the runtime the same
// argv, byte for byte.
func TestImageCurrentAndImageBaseVersion_SameFormatArgv(t *testing.T) {
	fake := newInspectFake(t, "shared-v2|tag1\n", "", 0)
	e := fake.engine()

	if _, _, err := e.imageCurrent(inspectFakeImage); err != nil {
		t.Fatalf("imageCurrent: %v", err)
	}
	currentArgv := fake.argv(t)

	if _, ok := e.imageBaseVersion(inspectFakeImage); !ok {
		t.Fatal("imageBaseVersion: want ok")
	}
	baseVersionArgv := fake.argv(t)

	if !reflect.DeepEqual(currentArgv, baseVersionArgv) {
		t.Errorf("argv differs: imageCurrent %q vs imageBaseVersion %q", currentArgv, baseVersionArgv)
	}
	wantTmpl := `{{ index .Config.Labels "` + imageAgentLifecycleLabel + `" }}|{{ index .Config.Labels "` + imageBaseVersionLabel + `" }}`
	want := []string{"image", "inspect", "--format", wantTmpl, inspectFakeImage}
	if !reflect.DeepEqual(currentArgv, want) {
		t.Errorf("argv = %q, want %q", currentArgv, want)
	}
}

// TestDiagnose_InspectExitNonzero_StatusNotFound is edge case 6: an inspect
// that exits non-zero still classifies as "not found" through the new wrap,
// and the runtime's stderr still reaches the finding detail.
func TestDiagnose_InspectExitNonzero_StatusNotFound(t *testing.T) {
	fake := newInspectFake(t, "", "Error: No such object: c1\n", 1)
	var stdout bytes.Buffer
	e := fake.engine()
	e.Stdout = &stdout
	scope := Scope{
		ContainerName: inspectFakeContainer,
		VolumeName:    "claude-cenci-home-test",
		Image:         "cenci-sandbox:test",
	}

	if err := e.Diagnose(scope); err != nil {
		t.Fatalf("Diagnose: %v", err)
	}

	out := stdout.String()
	if !strings.Contains(out, "Status: not found") {
		t.Errorf("want \"Status: not found\", got:\n%s", out)
	}
	if !strings.Contains(out, string(errcode.SandboxSessionNotFound)) {
		t.Errorf("want %s in the report, got:\n%s", errcode.SandboxSessionNotFound, out)
	}
	if !strings.Contains(out, "No such object: c1") {
		t.Errorf("want the runtime's stderr in the finding detail, got:\n%s", out)
	}
	if strings.Contains(out, "runtime unreachable") {
		t.Errorf("a non-zero inspect exit must not read as runtime unreachable, got:\n%s", out)
	}

	f, ok := e.containerExistenceFinding(scope)
	if !ok || f == nil || f.Code != errcode.SandboxSessionNotFound {
		t.Errorf("containerExistenceFinding = (%+v, %v), want SandboxSessionNotFound with ok=true", f, ok)
	}
}

// TestBestEffortInspect_FailureReturnsEmptyFalse is edge case 8: the two
// best-effort readers never surface an inspect failure.
func TestBestEffortInspect_FailureReturnsEmptyFalse(t *testing.T) {
	fake := newInspectFake(t, "", "boom", 1)
	e := fake.engine()

	if v, ok := e.imageBaseVersion(inspectFakeImage); v != "" || ok {
		t.Errorf("imageBaseVersion = (%q, %v), want (\"\", false)", v, ok)
	}
	if m, ok := e.inspectMounts(inspectFakeContainer); m != "" || ok {
		t.Errorf("inspectMounts = (%q, %v), want (\"\", false)", m, ok)
	}
}
