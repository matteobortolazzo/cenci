package babysit

import (
	"errors"
	"fmt"
	"os"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/ipc"
)

// armSocketPath is a test seam over ipc.DefaultEventSocketPath (#1094),
// mirroring the package's existing command/execGh/startSupervisor seam
// style (restorable via t.Cleanup). No new --event-socket flag: #966
// decided no new CLI flags on babysit.
var armSocketPath = ipc.DefaultEventSocketPath

// armOnHost sends an arm request for o (with repo already resolved) to the
// host daemon over the event socket and maps the transport outcome onto the
// three user-visible contract outcomes (#1094 Decisions):
//
//   - armed: nil error, a stdout message that the supervisor runs on the
//     host.
//   - not armed: a dial failure (ipc.ErrArmUndelivered, nothing was ever
//     written) or a daemon nack -- a non-nil error; a nack's reason is
//     relayed verbatim, never re-derived or re-worded.
//   - arm status unknown: every other transport failure (write failure,
//     clean EOF with no response, read-deadline timeout, unparseable
//     response) -- textually distinct from "not armed", with the host
//     verification/re-arm command printed (re-arming is safe: Run's lock
//     refuses a duplicate).
func armOnHost(o Options, repo string) error {
	req := ipc.ArmRequest{
		PR:       o.PR,
		Repo:     repo,
		Agent:    o.Agent,
		Interval: o.Interval,
		TmuxPane: os.Getenv("TMUX_PANE"),
	}

	resp, err := ipc.SendArmRequest(armSocketPath(), req)
	if err != nil {
		if errors.Is(err, ipc.ErrArmUndelivered) {
			return fmt.Errorf("not armed: no cenci daemon reachable on the host event socket: %w", err)
		}
		return fmt.Errorf("arm status unknown: no response from the host daemon before the deadline; verify or re-arm from a host tmux pane: cenci babysit %s --agent %s: %w", o.PR, o.Agent, err)
	}
	if !resp.OK {
		return fmt.Errorf("not armed: %s", resp.Reason)
	}

	fmt.Printf("Babysitting PR #%s: request forwarded, the supervisor now runs on the host.\n", o.PR)
	return nil
}
