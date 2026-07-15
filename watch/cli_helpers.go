package main

import (
	"fmt"
	"os"
)

// rejectExtra enforces the strict-parsing convention shared by every cenci
// subcommand: any positional argument left over after flag parsing is a
// usage error (exit 2), since the flag parser stops at the first non-flag
// token and would otherwise silently swallow everything after it. cmdPrefix
// is the full "cenci <command...>" prefix used in the error message.
func rejectExtra(cmdPrefix string, extra []string) {
	if len(extra) > 0 {
		fmt.Fprintf(os.Stderr, "%s: unexpected argument %q\n", cmdPrefix, extra[0])
		os.Exit(2)
	}
}
