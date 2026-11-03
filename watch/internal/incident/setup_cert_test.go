package incident

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

const (
	fsizeHelperPathEnv = "CENCI_TEST_FSIZE_HELPER_PATH"
	// fsizeHelperDenied is the exit code of a helper that may not lower the limit.
	fsizeHelperDenied = 77
)

// TestCreateCertificateFileFSizeHelperProcess is not a test of its own: it is the
// child process of the test below and does nothing unless that test started it.
// It lowers RLIMIT_FSIZE, which is process-wide, so it must never run inside the
// test binary that also runs the other tests and writes the harness's own files.
func TestCreateCertificateFileFSizeHelperProcess(t *testing.T) {
	path := os.Getenv(fsizeHelperPathEnv)
	if path == "" {
		return
	}
	var old syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_FSIZE, &old); err != nil {
		os.Exit(fsizeHelperDenied)
	}
	// SIGXFSZ would otherwise kill the process instead of failing the write with EFBIG.
	signal.Ignore(syscall.SIGXFSZ)
	if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &syscall.Rlimit{Cur: 0, Max: old.Max}); err != nil {
		os.Exit(fsizeHelperDenied)
	}
	leaf, err := createCertificateFile(path, time.Now())
	if leaf == nil {
		fmt.Println("leaf=nil")
	}
	if err != nil {
		fmt.Println("err=" + strings.ReplaceAll(err.Error(), "\n", " "))
	}
	os.Exit(0)
}

// Q8
func TestCreateCertificateFileRemovesOnlyTheFileThisCallCreatedWhenTheWriteFails(t *testing.T) {
	t.Run("a-failed-write-leaves-no-partial-key-behind", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "worker.pem")
		// The write must fail as on a full disk (EFBIG), which needs the file-size limit
		// lowered; that is process-wide, so it happens in a child of the test binary.
		cmd := exec.Command(os.Args[0], "-test.run=^TestCreateCertificateFileFSizeHelperProcess$")
		cmd.Env = append(os.Environ(), fsizeHelperPathEnv+"="+path)
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err := cmd.Run()
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == fsizeHelperDenied {
			t.Skip("this environment does not allow lowering RLIMIT_FSIZE")
		}
		if err != nil {
			t.Fatalf("the helper process failed: %v\nstdout: %s\nstderr: %s", err, stdout.String(), stderr.String())
		}
		lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
		var gotLeafNil, gotErr bool
		for _, l := range lines {
			gotLeafNil = gotLeafNil || l == "leaf=nil"
			gotErr = gotErr || strings.HasPrefix(l, "err=write certificate file "+path)
		}
		if !gotLeafNil || !gotErr {
			t.Fatalf("a write that fails after the create must fail the call without a leaf, helper said:\n%s", stdout.String())
		}
		if _, statErr := os.Lstat(path); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("the file this call created must be removed after the failed write: %v", statErr)
		}
	})

	t.Run("a-pre-existing-file-is-never-touched", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "worker.pem")
		original := []byte("operator-owned key material")
		if err := os.WriteFile(path, original, 0600); err != nil {
			t.Fatal(err)
		}
		before := mustStat(t, path)
		if _, err := createCertificateFile(path, time.Now()); err == nil {
			t.Fatal("an existing file must make the exclusive create fail")
		}
		after, err := os.Lstat(path)
		if err != nil || !os.SameFile(before, after) || !bytes.Equal(original, readBytes(t, path)) {
			t.Fatalf("the pre-existing file was removed or rewritten: %v", err)
		}
	})
}
