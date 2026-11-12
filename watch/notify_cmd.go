package main

import (
	"encoding/json"
	"flag"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/daemon"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/frontend"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/hookhealth"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/ipc"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/measurement"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/modelswitch"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/run"
)

// fleetWatchFile is the narrow slice of the fleet config that notify reads:
// the opt-in watch.excludeCwd list. Any decode error discards the whole list.
// Elements decode as *string because encoding/json turns a JSON null into a
// string as a silent no-op (""), which would let a bad element slip through.
type fleetWatchFile struct {
	Watch struct {
		ExcludeCwd []*string `json:"excludeCwd"`
	} `json:"watch"`
}

// loadExcludeCwd reads watch.excludeCwd from the fleet config at path. It
// fails open to visibility: an empty path, an unreadable file, malformed JSON
// or a wrongly typed key or element (a null included) yields nil, silently.
func loadExcludeCwd(path string) []string {
	if path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var cfg fleetWatchFile
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil
	}
	entries := make([]string, 0, len(cfg.Watch.ExcludeCwd))
	for _, entry := range cfg.Watch.ExcludeCwd {
		if entry == nil {
			return nil
		}
		entries = append(entries, *entry)
	}
	return entries
}

// cwdExcluded reports whether cwd equals a listed absolute entry or sits under
// one at a path-segment boundary, after filepath.Clean on both sides. It is
// deliberately narrow: an empty or relative cwd, and empty or relative
// entries, never match, so everything else stays visible. Symlinks are not
// resolved and case is not folded.
func cwdExcluded(cwd string, entries []string) bool {
	if cwd == "" || !filepath.IsAbs(cwd) {
		return false
	}
	cwd = filepath.Clean(cwd)
	sep := string(filepath.Separator)
	for _, entry := range entries {
		if entry == "" || !filepath.IsAbs(entry) {
			continue
		}
		entry = filepath.Clean(entry)
		if cwd == entry || entry == sep || strings.HasPrefix(cwd, entry+sep) {
			return true
		}
	}
	return false
}

// terminalBackgroundTaskStatus lists the background-task states that cannot
// wake the session on their own. Claude Code's task status enum is
// pending|running|completed|failed|killed|paused, and a Stop event's
// background_tasks array is already documented to carry only in-flight work
// ("running/pending + backgrounded"). So this excludes exactly the three
// finished states plus paused — a paused task resumes only when the user or
// the agent resumes it, and either way that resumption fires its own events
// (#1079) — and treats every other value, including one added by a future
// Claude Code release, as still in flight. Only a later main-agent event can
// resolve that evidence: a quiet shell or subagent can run for arbitrarily long.
var terminalBackgroundTaskStatus = map[string]bool{
	"completed": true,
	"failed":    true,
	"killed":    true,
	"paused":    true,
}

// lenientString reads a model-switch field that may hold any JSON value. It
// returns the string for a JSON string, "" for an absent or null field, and
// notString for any other JSON type, so a malformed value is visible to the
// caller instead of failing the whole payload decode.
func lenientString(raw json.RawMessage) (value string, notString bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", false
	}
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", true
	}
	return value, false
}

// measurementSource is the constant source label notify puts on every native
// measurement it sends; the validator's label pattern rejects spaces.
const measurementSource = "session.measure"

// measurementMaxBytes is the receiver's per-line limit (eventMaxBytes in
// internal/ipc); the mod's stdin is read up to this size and nothing larger is
// sent.
const measurementMaxBytes = 4096

// measurementInput is the stdin contract of `notify -measurement`, written by the
// hooks/register.js mods module: the session cwd (only compared against
// watch.excludeCwd, never forwarded) and the measurement body.
type measurementInput struct {
	Cwd         string                     `json:"cwd"`
	Measurement map[string]json.RawMessage `json:"measurement"`
}

// projectObject keeps only the allowed keys of a JSON object, each as the
// sender wrote it. A value that is not an object is returned unchanged so the
// daemon's own validation marks it invalid instead of this projection hiding it.
func projectObject(raw json.RawMessage, allowed ...string) json.RawMessage {
	var fields map[string]json.RawMessage
	if len(raw) == 0 || raw[0] != '{' || json.Unmarshal(raw, &fields) != nil {
		return raw
	}
	kept := make(map[string]json.RawMessage, len(allowed))
	for _, key := range allowed {
		if value, ok := fields[key]; ok {
			kept[key] = value
		}
	}
	out, err := json.Marshal(kept)
	if err != nil {
		return raw
	}
	return out
}

// projectMeasurement rebuilds the measurement body from an allowlist, nested
// keys included, so `cost` and every other unlisted field never reach the
// socket (P46), and sets the constant source label. Field values pass through
// untouched: only a whole-sample failure drops a sample.
func projectMeasurement(in map[string]json.RawMessage) ([]byte, error) {
	body := map[string]json.RawMessage{}
	for _, key := range []string{"session_id", "measured_at"} {
		if value, ok := in[key]; ok {
			body[key] = value
		}
	}
	if value, ok := in["context"]; ok {
		body["context"] = projectObject(value, "tokens", "window", "percent")
	}
	if value, ok := in["rate_limits"]; ok {
		body["rate_limits"] = value
		var entries []json.RawMessage
		if len(value) > 0 && value[0] == '[' && json.Unmarshal(value, &entries) == nil {
			for i, entry := range entries {
				entries[i] = projectObject(entry, "kind", "percent_used", "resets_at")
			}
			if projected, err := json.Marshal(entries); err == nil {
				body["rate_limits"] = projected
			}
		}
	}
	source, err := json.Marshal(measurementSource)
	if err != nil {
		return nil, err
	}
	body["source"] = source
	return json.Marshal(body)
}

// runNotifyMeasurement is `notify -agent claude -measurement`: it reads the
// mods module's {"cwd", "measurement"} object from stdin and sends the
// allowlisted, validated body as a native-measurement envelope. Every outcome is
// silent with exit 0 and no hook-health record (the categories are a closed set
// none of which describes a measurement). Delivery never autostarts the daemon
// and never reaches DeliverEvent.
func runNotifyMeasurement(socketPath, agentName string) {
	if agentName != "claude" {
		return
	}
	data, err := io.ReadAll(io.LimitReader(os.Stdin, measurementMaxBytes+1))
	if err != nil || len(data) > measurementMaxBytes {
		return
	}
	var input measurementInput
	if err := json.Unmarshal(data, &input); err != nil || input.Measurement == nil {
		return
	}
	if input.Cwd != "" && filepath.IsAbs(input.Cwd) &&
		cwdExcluded(input.Cwd, loadExcludeCwd(run.DefaultConfigPath())) {
		return
	}
	body, err := projectMeasurement(input.Measurement)
	if err != nil {
		return
	}
	if _, err := measurement.Parse(body, time.Now()); err != nil {
		return
	}
	_ = ipc.SendNativeMeasurement(socketPath, ipc.NativeMeasurement{Measurement: body})
}

func runNotify(args []string) {
	fs := flag.NewFlagSet("notify", flag.ExitOnError)
	socketPath := fs.String("event-socket", ipc.DefaultEventSocketPath(), "event socket path")
	agent := fs.String("agent", "", "agent name (claude, codex, or opencode)")
	measurementMode := fs.Bool("measurement", false, "send one native measurement read from stdin (used by hooks/measure.sh)")
	_ = fs.Parse(args)

	// Hooks stay silent and exit 0, so a failure is left as a bounded
	// hook-health record (#1239). Only claude and codex ever record; any other
	// agent value (OpenCode stays disabled, #1219) is ignored by the store.
	agentName := strings.ToLower(strings.TrimSpace(*agent))

	if *measurementMode {
		runNotifyMeasurement(*socketPath, agentName)
		return
	}

	// Read hook JSON from stdin.
	data, err := io.ReadAll(os.Stdin)
	if err != nil {
		hookhealth.RecordFailure(hookhealth.MalformedInput, agentName)
		os.Exit(0) // fail silently
	}

	// Parse the hook input to extract event type and relevant fields.
	var hookInput struct {
		HookEventName string `json:"hook_event_name"`
		SessionID     string `json:"session_id"`
		// Cwd is only compared against the opt-in watch.excludeCwd list; it
		// never reaches IPC.
		Cwd string `json:"cwd"`
		// Notification fields
		Notification struct {
			Type string `json:"type"`
		} `json:"notification"`
		// PreToolUse fields
		ToolName string `json:"tool_name"`
		// UserPromptSubmit field. It is reduced to a compact label before IPC.
		Prompt string `json:"prompt"`
		// PostToolUseFailure fields
		IsInterrupt bool `json:"is_interrupt"`
		// AgentID is set when the hook fires inside a subagent (Task tool) call.
		AgentID string `json:"agent_id"`
		// SessionEnd field: why the session ended. "clear" and "resume" are
		// continuations — a successor session keeps the pane — and route to
		// the window handoff instead of a full teardown (#707).
		Reason string `json:"reason"`
		// Stop field. Only each task's status is read: descriptions and shell
		// command lines stay local, like the raw prompt above.
		BackgroundTasks []struct {
			Status string `json:"status"`
		} `json:"background_tasks"`
		// Stop field. ScheduleWakeup and /loop timers are reported here, not
		// in background_tasks. Entries carry no status — presence alone means
		// the session will be woken (#705). Only the count is read: schedules
		// and prompts stay local, like the raw prompt above.
		SessionCrons []json.RawMessage `json:"session_crons"`
		// PostModelSwitch fields (#1179). They decode as raw JSON so a
		// non-string value never fails the whole payload and drops an event of
		// any type; lenientString reads them, and only for PostModelSwitch.
		// Source in particular is read only for that event: SessionStart has its
		// own `source` (startup|resume|clear).
		FromModel json.RawMessage `json:"from_model"`
		ToModel   json.RawMessage `json:"to_model"`
		AgentType json.RawMessage `json:"agent_type"`
		Source    json.RawMessage `json:"source"`
	}
	if err := json.Unmarshal(data, &hookInput); err != nil {
		hookhealth.RecordFailure(hookhealth.MalformedInput, agentName)
		os.Exit(0) // fail silently
	}

	// TMUX_PANE may be empty: sessions outside tmux (plain terminals,
	// sandbox) are still tracked by the daemon as paneless sessions.
	tmuxPane := os.Getenv("TMUX_PANE")

	// $TMUX is "<socket>,<server pid>,<session>". The first field is sent as-is:
	// the daemon's trusted-directory check at ingest is the authority, so notify
	// does not filter. An unset $TMUX or an empty first field omits the key. The
	// second field, the server pid (#1529), is sent as-is for the same reason: the
	// daemon checks its shape and compares it with the live server's. An unset
	// $TMUX or an empty second field omits that key.
	tmuxSocket, tmuxRest, _ := strings.Cut(os.Getenv("TMUX"), ",")
	tmuxServerPID, _, _ := strings.Cut(tmuxRest, ",")

	event := ipc.HookEvent{
		EventType:        hookInput.HookEventName,
		SessionID:        hookInput.SessionID,
		Agent:            agentName,
		TmuxPane:         tmuxPane,
		TmuxSocket:       tmuxSocket,
		TmuxServerPID:    tmuxServerPID,
		NotificationType: hookInput.Notification.Type,
		ToolName:         hookInput.ToolName,
		IsInterrupt:      hookInput.IsInterrupt,
		AgentID:          hookInput.AgentID,
		Timestamp:        time.Now().UTC().Format(time.RFC3339),
	}
	if event.EventType == "SessionEnd" {
		event.SessionEndReason = hookInput.Reason
	}
	// An invalid model id or agent type is never stored or forwarded; the
	// switch is still delivered with a placeholder. The daemon validates again.
	var modelSwitch modelswitch.Switch
	if event.EventType == "PostModelSwitch" {
		// A non-string value reads as "" for the models and agent type, which the
		// sanitizers turn into their placeholders; a non-string source is an
		// invalid source, not an absent one.
		fromModel, _ := lenientString(hookInput.FromModel)
		toModel, _ := lenientString(hookInput.ToModel)
		agentType, _ := lenientString(hookInput.AgentType)
		source, sourceNotString := lenientString(hookInput.Source)
		if sourceNotString {
			source = modelswitch.UnknownSource
		}
		event.FromModel = modelswitch.SanitizeModel(fromModel)
		event.ToModel = modelswitch.SanitizeModel(toModel)
		if source != "" {
			event.ModelSwitchSource = modelswitch.SanitizeSource(source)
		}
		modelSwitch = modelswitch.Switch{
			Source:   source,
			From:     event.FromModel,
			To:       event.ToModel,
			Subagent: event.AgentID != "",
		}
		if modelSwitch.Subagent {
			event.AgentType = modelswitch.SanitizeAgentType(agentType)
			modelSwitch.AgentType = event.AgentType
		}
	}
	for _, task := range hookInput.BackgroundTasks {
		if !terminalBackgroundTaskStatus[task.Status] {
			event.BackgroundWork = true
			event.BackgroundTasksActive = true
			break
		}
	}
	if len(hookInput.SessionCrons) > 0 {
		event.BackgroundWork = true
	}
	if (event.Agent == "codex" || event.Agent == "opencode") && event.EventType == "UserPromptSubmit" {
		event.TaskName = frontend.PromptTaskName(hookInput.Prompt)
	}

	// Opt-in exclusion of headless probe sessions (watch.excludeCwd), decided
	// per event. Returning before DeliverEvent also keeps an excluded event
	// from autostarting a missing daemon. The config is read only for an
	// absolute cwd; every failure leaves the event visible.
	if hookInput.Cwd != "" && filepath.IsAbs(hookInput.Cwd) &&
		cwdExcluded(hookInput.Cwd, loadExcludeCwd(run.DefaultConfigPath())) {
		return
	}

	// Persist a Claude model switch on the hook side so a later process (Phase 9)
	// can read it without the daemon; Record swallows every error. Only Claude
	// has the flag fallback, so no other agent records.
	if event.EventType == "PostModelSwitch" && event.Agent == "claude" {
		modelswitch.Record(modelswitch.Dir(), event.SessionID, modelSwitch, time.Now())
	}

	// Delivery is silent and non-fatal. For the default socket it starts a
	// missing daemon on demand and retries this exact event once.
	daemon.DeliverEvent(*socketPath, event)
}
