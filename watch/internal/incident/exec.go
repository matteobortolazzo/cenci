package incident

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

type boundedBuffer struct{ bytes.Buffer }

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 4*1024*1024 {
		return 0, fmt.Errorf("command output exceeds 4 MiB")
	}
	return b.Buffer.Write(p)
}

// Every command has a per-call bound within its incident deadline. Killing the
// process group prevents a timed-out CLI from leaving credential helpers behind.
func command(ctx context.Context, dir string, env []string, input io.Reader, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	return commandDeadline(ctx, dir, env, input, name, args...)
}
func commandDeadline(ctx context.Context, dir string, env []string, input io.Reader, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stdin = input
	cmd.WaitDelay = 2 * time.Second
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	var out, stderr boundedBuffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	err := cmd.Run()
	ctxErr := ctx.Err()
	if ctxErr != nil {
		err = errors.Join(err, ctxErr)
	}
	if errors.Is(err, exec.ErrWaitDelay) {
		err = errors.Join(err, context.DeadlineExceeded)
	}
	if err != nil {
		return out.String(), fmt.Errorf("%s: %w (%s)", name, err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(out.String()), nil
}
