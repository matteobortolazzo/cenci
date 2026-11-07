package launcher

import (
	"fmt"
	"os/exec"
)

// inspectFormat runs `<runtime> inspect --format <tmpl> <name>` and returns its
// stdout. It is the one place the launcher's untimed container-inspect probes
// exec the runtime.
//
// The call deliberately uses .Output() with cmd.Stderr left unset: that makes
// os/exec fill (*exec.ExitError).Stderr, which Diagnose's not-found branch
// reads for its finding detail. On failure the error is wrapped with %w as
// "<runtime> inspect <name>[ <op>]: <err>", so errors.As still reaches the
// *exec.ExitError (diagnose.go classifies "no such container" that way). op is
// the operation label of the caller's historical error text; an empty op adds
// no trailing space. Callers that also wrap a parse failure keep that wrap.
func (e *Engine) inspectFormat(name, tmpl, op string) ([]byte, error) {
	out, err := exec.Command(e.Runtime, "inspect", "--format", tmpl, name).Output()
	if err != nil {
		return nil, fmt.Errorf("%s inspect %s%s: %w", e.Runtime, name, opSuffix(op), err)
	}
	return out, nil
}

// imageInspectFormat is inspectFormat for `<runtime> image inspect --format
// <tmpl> <image>`; its errors read "<runtime> image inspect <image>[ <op>]:
// <err>" under the same untimed .Output() and %w contract.
func (e *Engine) imageInspectFormat(image, tmpl, op string) ([]byte, error) {
	out, err := exec.Command(e.Runtime, "image", "inspect", "--format", tmpl, image).Output()
	if err != nil {
		return nil, fmt.Errorf("%s image inspect %s%s: %w", e.Runtime, image, opSuffix(op), err)
	}
	return out, nil
}

// opSuffix renders an operation label as a leading-space suffix, or "" when
// the label is empty.
func opSuffix(op string) string {
	if op == "" {
		return ""
	}
	return " " + op
}
