// Package exectest provides shared helpers for writing fake executable
// scripts in tests (e.g. the writeFakeDocker/writeFakeRuntime style fakes
// used to stand in for docker/podman) across watch's test packages.
package exectest

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// WriteExecutable writes body to path and makes it executable (0o755).
func WriteExecutable(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// ShellArgv runs commandLine through /bin/sh -c with a recording fake named
// program first on PATH and returns the argv the shell handed it: [program,
// args...]. The fake writes its args NUL-separated, so a recovered element
// boundary is exact even for args holding spaces, quotes, `$` or newlines.
func ShellArgv(t *testing.T, commandLine, program string) []string {
	t.Helper()
	dir := t.TempDir()
	logPath := filepath.Join(dir, "argv.log")
	WriteExecutable(t, filepath.Join(dir, program), "#!/bin/sh\nprintf '%s\\000' \"$@\" > "+ShellQuote(logPath)+"\n")
	cmd := exec.Command("/bin/sh", "-c", commandLine)
	cmd.Env = append(os.Environ(), "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("running %q: %v\n%s", commandLine, err, out)
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("fake %s never ran for %q: %v", program, commandLine, err)
	}
	args := strings.Split(strings.TrimSuffix(string(data), "\x00"), "\x00")
	return append([]string{program}, args...)
}

// DryRunCommand returns the `command:` value of `cenci run --dry-run` output:
// the text between "command: " and the final "\ntemplate: " line, so a
// multi-line prompt survives.
func DryRunCommand(t *testing.T, stdout string) string {
	t.Helper()
	_, rest, ok := strings.Cut(stdout, "\ncommand: ")
	end := strings.LastIndex(rest, "\ntemplate: ")
	if !ok || end < 0 {
		t.Fatalf("no command:/template: lines in dry-run output:\n%s", stdout)
	}
	return rest[:end]
}

// ShellQuote single-quotes s for embedding in a generated shell script body.
func ShellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
