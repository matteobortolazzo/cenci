package daemon

import (
	"errors"
	"os"
	"syscall"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/hookhealth"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/ipc"
)

// DeliverEvent sends a hook event without surfacing failures to the calling
// agent. If the default daemon is unavailable, it is started on demand and the
// exact event is retried once. Custom sockets are explicitly managed by their
// callers and never trigger default-daemon startup.
func DeliverEvent(socketPath string, event ipc.HookEvent) {
	deliverEvent(socketPath, event, ipc.SendEvent, EnsureRunning)
}

func deliverEvent(socketPath string, event ipc.HookEvent, send func(string, ipc.HookEvent) error, ensure func()) {
	// Inside a sandbox, EnsureRunning's CENCI_SANDBOX no-op means the retry
	// below can never self-heal — a terminal failure against the default
	// event socket is worth classifying and recording so `cenci diagnose`
	// can surface it host-side (#1122).
	sandboxDefaultSocket := os.Getenv("CENCI_SANDBOX") == "1" && socketPath == ipc.DefaultEventSocketPath()

	err := send(socketPath, event)
	if err == nil {
		hookhealth.RecordOK(event.Agent)
		if sandboxDefaultSocket {
			clearUndeliveredMarker()
		}
		return
	}
	if socketPath != ipc.DefaultEventSocketPath() {
		hookhealth.RecordFailure(classifyHookHealth(socketPath, err), event.Agent)
		return
	}
	ensure()
	err = send(socketPath, event)
	if err == nil {
		hookhealth.RecordOK(event.Agent)
		if sandboxDefaultSocket {
			clearUndeliveredMarker()
		}
		return
	}
	hookhealth.RecordFailure(classifyHookHealth(socketPath, err), event.Agent)
	if sandboxDefaultSocket {
		mountinfo, readErr := os.ReadFile(mountInfoPath)
		code, message := classifyDeliveryFailure(string(mountinfo), readErr == nil, socketPath)
		writeUndeliveredMarker(event.SessionID, code, message)
	}
}

// classifyHookHealth maps a terminal delivery error to its hook-health
// category (#1239): an empty socket path, or a dial error that says nothing is
// there (ENOENT) or nothing is listening (ECONNREFUSED), is an unavailable
// socket; every other error (a denied dial, a write error) is a delivery
// failure. Hook-health records only the client name, never the error text.
func classifyHookHealth(socketPath string, err error) hookhealth.Category {
	if socketPath == "" {
		return hookhealth.SocketUnavailable
	}
	var errno syscall.Errno
	if errors.As(err, &errno) && (errno == syscall.ENOENT || errno == syscall.ECONNREFUSED) {
		return hookhealth.SocketUnavailable
	}
	return hookhealth.DeliveryFailure
}
