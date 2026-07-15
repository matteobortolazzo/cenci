package daemon

import (
	"errors"
	"reflect"
	"testing"

	"github.com/matteobortolazzo/cenci/watch/v4/internal/ipc"
)

func TestDeliverEventSucceedsWithoutRecovery(t *testing.T) {
	event := ipc.HookEvent{EventType: "Stop", SessionID: "session-1"}
	var sent []ipc.HookEvent
	ensured := false

	deliverEvent(ipc.DefaultEventSocketPath(), event, func(_ string, got ipc.HookEvent) error {
		sent = append(sent, got)
		return nil
	}, func() { ensured = true })

	if ensured {
		t.Error("successful delivery must not ensure the daemon")
	}
	if !reflect.DeepEqual(sent, []ipc.HookEvent{event}) {
		t.Errorf("sent events = %#v, want original event once", sent)
	}
}

func TestDeliverEventRecoversAndRetriesSameEvent(t *testing.T) {
	event := ipc.HookEvent{EventType: "PreToolUse", SessionID: "session-2", ToolName: "shell"}
	var sent []ipc.HookEvent
	ensured := 0

	deliverEvent(ipc.DefaultEventSocketPath(), event, func(_ string, got ipc.HookEvent) error {
		sent = append(sent, got)
		if len(sent) == 1 {
			return errors.New("daemon absent")
		}
		return nil
	}, func() { ensured++ })

	if ensured != 1 {
		t.Errorf("ensure calls = %d, want 1", ensured)
	}
	if !reflect.DeepEqual(sent, []ipc.HookEvent{event, event}) {
		t.Errorf("sent events = %#v, want exact event twice", sent)
	}
}

func TestDeliverEventFailedRecoveryRemainsNonFatal(t *testing.T) {
	event := ipc.HookEvent{EventType: "UserPromptSubmit", SessionID: "session-3"}
	sends := 0
	ensured := 0

	deliverEvent(ipc.DefaultEventSocketPath(), event, func(string, ipc.HookEvent) error {
		sends++
		return errors.New("still unavailable")
	}, func() { ensured++ })

	if sends != 2 {
		t.Errorf("send attempts = %d, want 2", sends)
	}
	if ensured != 1 {
		t.Errorf("ensure calls = %d, want 1", ensured)
	}
}

func TestDeliverEventCustomSocketDoesNotStartDaemon(t *testing.T) {
	event := ipc.HookEvent{EventType: "Stop", SessionID: "managed-instance"}
	sends := 0
	ensured := false

	deliverEvent("/tmp/custom-cenci.sock", event, func(string, ipc.HookEvent) error {
		sends++
		return errors.New("unavailable")
	}, func() { ensured = true })

	if ensured {
		t.Error("custom event socket must not start the default daemon")
	}
	if sends != 1 {
		t.Errorf("send attempts = %d, want only the initial attempt", sends)
	}
}
