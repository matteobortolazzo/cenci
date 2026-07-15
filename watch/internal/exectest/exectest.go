// Package exectest provides shared helpers for writing fake executable
// scripts in tests (e.g. the writeFakeDocker/writeFakeRuntime style fakes
// used to stand in for docker/podman) across watch's test packages.
package exectest

import (
	"os"
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

// ShellQuote single-quotes s for embedding in a generated shell script body.
func ShellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
