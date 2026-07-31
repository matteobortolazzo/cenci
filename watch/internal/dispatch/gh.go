package dispatch

import (
	"bytes"
	"context"
	"os/exec"
	"time"
)

// ghTimeout bounds every individual `gh` invocation execGh makes (#852),
// mirroring gitTimeout's rationale (mainsync.go): a hung network call must
// never stall a dispatch/reconcile pass indefinitely.
const ghTimeout = 60 * time.Second

// ghWaitDelay bounds how long cmd.Wait can block *after* the gh process
// itself has exited or been killed by ghTimeout's context, mirroring
// gitWaitDelay (mainsync.go) -- without it, a grandchild process that
// inherited the stdout/stderr pipes could keep those pipes open and stall
// indefinitely even though gh itself is gone, defeating ghTimeout's
// guarantee.
const ghWaitDelay = 5 * time.Second

// execGh runs `gh <args...>` under a fresh ghTimeout-bounded context per
// call, with cmd.WaitDelay bounding how long Wait can stall on a lingering
// grandchild (watch/AGENTS.md #825: every new internal/dispatch subprocess
// call must mirror mainsync.go's execGit conventions). stdout and stderr
// are captured into separate buffers rather than via CombinedOutput, so a
// benign stderr diagnostic on an otherwise-successful (exit 0) call can
// never get merged into bytes a caller decodes as JSON.
//
// Named execGh (not runGh) to mirror execGit's own naming rationale
// (mainsync.go): consistency with the sibling bounded-exec helper this
// ticket (#852) introduces it alongside.
func execGh(args ...string) (stdout, stderr string, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), ghTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "gh", args...)
	cmd.WaitDelay = ghWaitDelay
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err = cmd.Run()
	return outBuf.String(), errBuf.String(), err
}
