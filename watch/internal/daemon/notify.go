package daemon

import "github.com/matteobortolazzo/agent-stack/agentwatch/v4/internal/ipc"

// DeliverEvent sends a hook event without surfacing failures to the calling
// agent. If the default daemon is unavailable, it is started on demand and the
// exact event is retried once. Custom sockets are explicitly managed by their
// callers and never trigger default-daemon startup.
func DeliverEvent(socketPath string, event ipc.HookEvent) {
	deliverEvent(socketPath, event, ipc.SendEvent, EnsureRunning)
}

func deliverEvent(socketPath string, event ipc.HookEvent, send func(string, ipc.HookEvent) error, ensure func()) {
	if err := send(socketPath, event); err == nil {
		return
	}
	if socketPath != ipc.DefaultEventSocketPath() {
		return
	}
	ensure()
	_ = send(socketPath, event)
}
