package launcher

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/errcode"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/sandbox"
)

// memoryMode is how the workload container's memory limit is chosen.
type memoryMode int

const (
	// memoryDefault derives the limit from the container runtime's total host
	// memory (50%, rounded down to whole MiB).
	memoryDefault memoryMode = iota
	// memoryExplicit uses the size named by sandbox.memoryLimit.
	memoryExplicit
	// memoryNone applies no memory limit (sandbox.memoryLimit "none").
	memoryNone
)

// defaultPidsLimit is the pids limit applied when sandbox.pidsLimit is absent.
const defaultPidsLimit int64 = 4096

// workloadLimits is the resolved memory and pids limit configuration for the
// long-lived workload container (#1216).
type workloadLimits struct {
	// Memory selects default, explicit, or no memory limit.
	Memory memoryMode
	// MemorySize is the verbatim sandbox.memoryLimit value (e.g. "16g");
	// only meaningful when Memory is memoryExplicit.
	MemorySize string
	// Pids is the pids limit; 0 means no limit (the flag is omitted).
	Pids int64
}

// sendsFlags reports whether a create argv built from l carries any limit
// flag: every memory mode but "none" sends --memory/--memory-swap (the
// default mode fails the launch rather than omit them), and a positive pids
// limit sends --pids-limit. Launch passes it to createFailureError as limitsOn.
func (l workloadLimits) sendsFlags() bool {
	return l.Memory != memoryNone || l.Pids > 0
}

const (
	bytesPerMiB = int64(1) << 20
	bytesPerGiB = int64(1) << 30

	// limitProbeTimeout bounds the best-effort per-container probes (the
	// attach-path drift inspect and diagnose's limits and OOM reads). Each
	// also sets WaitDelay to the same value, so the worst case is twice this.
	limitProbeTimeout = 3 * time.Second

	// maxMemoryEventsBytes caps what the OOM probe reads from the container: a
	// real cgroup v2 memory.events is well under 1 KiB, so anything larger is
	// not that file and is treated as unreadable rather than parsed.
	maxMemoryEventsBytes = 4 << 10
	// maxStderrDetailBytes caps the runtime stderr text quoted in a LIMITS-001
	// error.
	maxStderrDetailBytes = 512
)

// hostMemQueryTimeout bounds the `<runtime> info` call that reads the engine
// host's total memory, so a wedged daemon cannot hang a launch. A variable
// only so a test can shorten it.
var hostMemQueryTimeout = 10 * time.Second

// runtimeOutput runs `<runtime> args...` and returns its stdout, killing it
// after timeout. WaitDelay bounds how long Output waits for the stdout/stderr
// pipes to close after the kill, so a grandchild holding them open cannot
// defeat the timeout (watch/docs/go-gotchas.md #26); the worst case is
// therefore twice timeout. A failure caused by the deadline wraps
// context.DeadlineExceeded and says "timed out after <timeout>".
func runtimeOutput(timeout time.Duration, runtime string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, runtime, args...)
	cmd.WaitDelay = timeout
	out, err := cmd.Output()
	if err != nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return out, fmt.Errorf("timed out after %s (%w): %w", timeout, context.DeadlineExceeded, err)
	}
	return out, err
}

// memoryLimitPattern is the accepted sandbox.memoryLimit size form: a positive
// integer without leading zeros plus a binary unit (m = MiB, g = GiB).
var memoryLimitPattern = regexp.MustCompile(`^[1-9][0-9]*[mg]$`)

// memorySizeBytes converts a validated memoryLimit size ("512m", "16g") to
// bytes, failing when the value does not match the accepted form or overflows
// int64 bytes.
func memorySizeBytes(size string) (int64, error) {
	if !memoryLimitPattern.MatchString(size) {
		return 0, fmt.Errorf("%q is not a size like \"16g\" or \"512m\"", size)
	}
	unit := bytesPerMiB
	if strings.HasSuffix(size, "g") {
		unit = bytesPerGiB
	}
	n, err := strconv.ParseInt(size[:len(size)-1], 10, 64)
	if err != nil || n > math.MaxInt64/unit {
		return 0, fmt.Errorf("%q is larger than the maximum representable size", size)
	}
	return n * unit, nil
}

// RepoSandboxLimits reads <repoRoot>/.cenci/config.json's sandbox.memoryLimit
// and sandbox.pidsLimit keys through readRepoSandboxObject's two-stage decode,
// with RepoSandboxPlugins' failure taxonomy: an absent config file, "sandbox" object, or key
// resolves to the default for that key (default memory, 4096 pids). Every
// other failure class — an unreadable file, unparsable JSON, a non-object top
// level or "sandbox", or a malformed value (wrong JSON type including null,
// a size outside ^[1-9][0-9]*[mg]$ and "none", a size overflowing int64 bytes,
// a negative or non-integer pids limit) — resolves to a zero workloadLimits
// and a plain, path- and key-bearing, non-usage error (IsUsage(err) is
// false), so malformed config hard-fails the caller (exit 1) instead of
// silently launching with a default (#632).
func RepoSandboxLimits(repoRoot string) (workloadLimits, error) {
	defaults := workloadLimits{Memory: memoryDefault, Pids: defaultPidsLimit}

	sandboxObj, present, err := readRepoSandboxObject(repoRoot)
	if err != nil {
		return workloadLimits{}, err
	}
	if !present {
		return defaults, nil
	}
	configPath := repoConfigPath(repoRoot)

	limits := defaults
	if raw, ok := sandboxObj["memoryLimit"]; ok {
		if err := parseMemoryLimit(raw, &limits); err != nil {
			return workloadLimits{}, fmt.Errorf("%s: \"memoryLimit\" %w", configPath, err)
		}
	}
	if raw, ok := sandboxObj["pidsLimit"]; ok {
		pids, err := parsePidsLimit(raw)
		if err != nil {
			return workloadLimits{}, fmt.Errorf("%s: \"pidsLimit\" %w", configPath, err)
		}
		limits.Pids = pids
	}
	return limits, nil
}

// isJSONNull reports whether raw is the JSON null literal. A null must be
// rejected explicitly: json.Unmarshal of null into a string or int64 leaves
// the zero value, which for pidsLimit would silently read as "no limit".
func isJSONNull(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

// parseMemoryLimit decodes sandbox.memoryLimit into limits. The returned
// error starts mid-sentence ("must be ...") so the caller can prefix it with
// the config path and key.
func parseMemoryLimit(raw json.RawMessage, limits *workloadLimits) error {
	const form = "must be a size string like \"16g\" or \"512m\" (binary units), or \"none\""
	var value string
	if isJSONNull(raw) {
		return errors.New(form + ", got null")
	}
	if err := json.Unmarshal(raw, &value); err != nil {
		return fmt.Errorf("%s: %w", form, err)
	}
	if value == "none" {
		limits.Memory = memoryNone
		return nil
	}
	if _, err := memorySizeBytes(value); err != nil {
		return fmt.Errorf("%s: %w", form, err)
	}
	limits.Memory = memoryExplicit
	limits.MemorySize = value
	return nil
}

// parsePidsLimit decodes sandbox.pidsLimit: an integer >= 1, or 0 for no
// limit. The returned error starts mid-sentence (see parseMemoryLimit).
func parsePidsLimit(raw json.RawMessage) (int64, error) {
	const form = "must be an integer >= 1, or 0 for no limit"
	if isJSONNull(raw) {
		return 0, errors.New(form + ", got null")
	}
	var value int64
	if err := json.Unmarshal(raw, &value); err != nil {
		return 0, fmt.Errorf("%s: %w", form, err)
	}
	if value < 0 {
		return 0, fmt.Errorf("%s, got %d", form, value)
	}
	return value, nil
}

// ResolveWorkloadLimits resolves the limit configuration for scope, mirroring
// ResolveSandboxPlugins' repo-scope-only read gate: outside repo scope the
// defaults apply with no override and the config is never read — a corrupt
// config outside repo scope must never hard-fail a launch that never needed
// it. In repo scope RepoSandboxLimits' result (value or error) is returned
// unchanged.
func ResolveWorkloadLimits(scope Scope) (workloadLimits, error) {
	if scope.WorkspaceScope != "repo" {
		return workloadLimits{Memory: memoryDefault, Pids: defaultPidsLimit}, nil
	}
	return RepoSandboxLimits(scope.RepoRoot)
}

// limitRunArgs builds the create-time limit flags for l: --memory and
// --memory-swap set to the same value (no container swap) and --pids-limit,
// each flag adjacent to its value. A default memory limit is the only case
// that asks the container runtime anything (hostMemTotal); an explicit size,
// "none" and pidsLimit 0 never do. Called only from planArgvs' create branch,
// so an attach never queries host memory or fails because of limit resolution.
func (e *Engine) limitRunArgs(l workloadLimits) ([]string, error) {
	var args []string
	switch l.Memory {
	case memoryExplicit:
		args = append(args, "--memory", l.MemorySize, "--memory-swap", l.MemorySize)
	case memoryDefault:
		total, err := e.hostMemTotal()
		if err != nil {
			return nil, err
		}
		mib := total / 2 / bytesPerMiB
		if mib < 1 {
			return nil, hostMemoryError(fmt.Errorf("the reported total of %d bytes would derive a memory limit under 1 MiB", total))
		}
		size := strconv.FormatInt(mib, 10) + "m"
		args = append(args, "--memory", size, "--memory-swap", size)
	}
	if l.Pids > 0 {
		args = append(args, "--pids-limit", strconv.FormatInt(l.Pids, 10))
	}
	return args, nil
}

// hostMemoryError builds the LIMITS-001 error: it names the cause, the
// "memoryLimit": "none" opt-out, and where the key lives (it only takes effect
// in a repo's .cenci/config.json).
func hostMemoryError(cause error) error {
	//nolint:staticcheck // ST1005: multi-sentence user-facing guidance, matching the --agent auth errors in launch.go
	return fmt.Errorf("could not derive the default sandbox memory limit from the container runtime's total memory [%s]: %w\n"+
		"Set \"sandbox\": {\"memoryLimit\": \"none\"} (or an explicit size such as \"memoryLimit\": \"8g\") in the repo's .cenci/config.json to launch without the query; "+
		"the key is only read from a repo's .cenci/config.json.",
		errcode.SandboxLimitsHostMemoryUnreadable, cause)
}

// hostMemTotal reads the engine host's total memory in bytes. Docker reports
// it as `info` .MemTotal and podman as .Host.MemTotal (podman is picked by the
// runtime binary's name); if the first template fails, the other is tried once
// so a podman-docker shim still works — unless the first timed out, which says
// the engine is wedged, not that the template is wrong. Under Docker Desktop
// and podman machine this is the VM's memory. Any failure is the LIMITS-001
// error, carrying the first (runtime-appropriate) template's error and the
// fallback's, so the real cause is not masked by the fallback's.
func (e *Engine) hostMemTotal() (int64, error) {
	const dockerTemplate, podmanTemplate = "{{.MemTotal}}", "{{.Host.MemTotal}}"
	templates := []string{dockerTemplate, podmanTemplate}
	if filepath.Base(e.Runtime) == "podman" {
		templates = []string{podmanTemplate, dockerTemplate}
	}

	var firstErr error
	for i, template := range templates {
		total, err := e.queryHostMemTotal(template)
		if err == nil {
			return total, nil
		}
		if i == 0 {
			firstErr = err
			if errors.Is(err, context.DeadlineExceeded) {
				return 0, hostMemoryError(firstErr)
			}
			continue
		}
		return 0, hostMemoryError(fmt.Errorf("%w; fallback attempt: %w", firstErr, err))
	}
	return 0, hostMemoryError(firstErr)
}

// queryHostMemTotal runs `<runtime> info --format <template>` and parses a
// positive whole byte count out of its stdout. A non-zero exit includes the
// runtime's stderr (bounded, single line) in the error.
func (e *Engine) queryHostMemTotal(template string) (int64, error) {
	out, err := runtimeOutput(hostMemQueryTimeout, e.Runtime, "info", "--format", template)
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			if detail := stderrDetail(exitErr.Stderr); detail != "" {
				return 0, fmt.Errorf("%s info --format %s: %w (stderr: %s)", e.Runtime, template, err, detail)
			}
		}
		return 0, fmt.Errorf("%s info --format %s: %w", e.Runtime, template, err)
	}
	text := strings.TrimSpace(string(out))
	total, err := strconv.ParseInt(text, 10, 64)
	if err != nil || total <= 0 {
		return 0, fmt.Errorf("%s info --format %s reported %q, want a positive whole number of bytes", e.Runtime, template, text)
	}
	return total, nil
}

// stderrDetail collapses a runtime's stderr to one printable line of at most
// maxStderrDetailBytes, for quoting in an error message.
func stderrDetail(stderr []byte) string {
	oneLine := strings.Join(strings.Fields(string(stderr)), " ")
	return sanitizeText(oneLine, false, maxStderrDetailBytes, false, "")
}

// containerLimits is a running container's raw .HostConfig.Memory,
// .HostConfig.MemorySwap and .HostConfig.PidsLimit values as the runtime
// prints them. MemorySwap is the memory+swap total (equal to Memory when the
// container has no swap); it is displayed by diagnose but never compared for
// drift.
type containerLimits struct {
	Memory string
	Swap   string
	Pids   string
}

// parseLimitValue classifies one raw limit value from `inspect`. Unlimited is
// "0" (docker), "-1" (podman) or "<nil>" (docker's template engine printing a
// nil pids pointer); anything else must be a positive integer. ok is false for
// garbage, which callers treat as "could not tell", never as drift.
func parseLimitValue(raw string) (n int64, unlimited, ok bool) {
	switch raw {
	case "0", "-1", "<nil>":
		return 0, true, true
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n <= 0 {
		return 0, false, false
	}
	return n, false, true
}

// memoryLimitDisplay renders a raw .HostConfig.Memory value for the drift
// warning and diagnose: whole MiB, "unlimited", or "unknown".
func memoryLimitDisplay(raw string) string {
	n, unlimited, ok := parseLimitValue(raw)
	switch {
	case !ok:
		return "unknown"
	case unlimited:
		return "unlimited"
	default:
		return strconv.FormatInt(n/bytesPerMiB, 10) + " MiB"
	}
}

// pidsLimitDisplay renders a raw .HostConfig.PidsLimit value: the count,
// "unlimited", or "unknown".
func pidsLimitDisplay(raw string) string {
	n, unlimited, ok := parseLimitValue(raw)
	switch {
	case !ok:
		return "unknown"
	case unlimited:
		return "unlimited"
	default:
		return strconv.FormatInt(n, 10)
	}
}

// inspectContainerLimits is a best-effort, bounded probe of a container's
// create-time memory and pids limits in ONE inspect call, shared by the
// attach-path drift warning and diagnose. Like inspectContainerSandboxPlugins
// it never fails its caller: a runtime error, a timeout, or output that is not
// "<Memory>|<MemorySwap>|<PidsLimit>" with parseable values resolves to
// ok=false.
func (e *Engine) inspectContainerLimits(name string) (limits containerLimits, ok bool) {
	out, err := runtimeOutput(limitProbeTimeout, e.Runtime, "inspect", "--format",
		`{{.HostConfig.Memory}}|{{.HostConfig.MemorySwap}}|{{.HostConfig.PidsLimit}}`, name)
	if err != nil {
		return containerLimits{}, false
	}
	fields := strings.Split(strings.TrimSpace(string(out)), "|")
	if len(fields) != 3 {
		return containerLimits{}, false
	}
	for _, raw := range fields {
		if _, _, valid := parseLimitValue(raw); !valid {
			return containerLimits{}, false
		}
	}
	return containerLimits{Memory: fields[0], Swap: fields[1], Pids: fields[2]}, true
}

// limitDrift says which limit dimensions of a running container differ from
// the resolved config.
type limitDrift struct {
	Memory, Pids bool
}

// limitDimensionsDiffer compares a running container's raw inspect values to
// want, per dimension, for the attach-path warning:
//   - default memory: drift only when the container has no memory limit at all
//     (never compared against a freshly computed N);
//   - explicit memory: drift unless the container's bytes match exactly;
//   - "none": drift when the container is limited;
//   - pids: drift unless the container matches want.Pids; a configured
//     pidsLimit of 0 never differs (podman applies its own containers.conf
//     default when the flag is omitted) and does not hide a memory drift.
//
// Unparseable values are not drift.
func limitDimensionsDiffer(want workloadLimits, runningMemory, runningPids string) limitDrift {
	var drift limitDrift
	if mem, unlimited, ok := parseLimitValue(runningMemory); ok {
		switch want.Memory {
		case memoryDefault:
			drift.Memory = unlimited
		case memoryNone:
			drift.Memory = !unlimited
		case memoryExplicit:
			wantBytes, err := memorySizeBytes(want.MemorySize)
			drift.Memory = err == nil && (unlimited || mem != wantBytes)
		}
	}
	if want.Pids != 0 {
		pids, unlimited, ok := parseLimitValue(runningPids)
		drift.Pids = ok && (unlimited || pids != want.Pids)
	}
	return drift
}

// limitsDiffer reports whether any limit dimension drifts from want; see
// limitDimensionsDiffer for the per-dimension rules.
func limitsDiffer(want workloadLimits, runningMemory, runningPids string) bool {
	drift := limitDimensionsDiffer(want, runningMemory, runningPids)
	return drift.Memory || drift.Pids
}

// configuredMemoryDisplay renders the resolved memory configuration for the
// drift warning: the explicit size, "no limit", or the derived default.
func configuredMemoryDisplay(want workloadLimits) string {
	switch want.Memory {
	case memoryExplicit:
		return want.MemorySize
	case memoryNone:
		return "none"
	default:
		return "a limit derived from host memory"
	}
}

// warnLimitsDrift prints exactly one best-effort attach-path warning line when
// a running container's memory or pids limit differs from ctx's resolved
// config (including an unlimited container created before limits shipped),
// naming only the dimension(s) that differ and pointing at the
// stop-and-relaunch remedy. Mirrors warnSandboxPluginsDrift: it never
// recreates the container, and a probe failure or timeout prints nothing so
// the attach always continues.
func (e *Engine) warnLimitsDrift(ctx launchCtx) {
	limits, ok := e.inspectContainerLimits(ctx.Scope.ContainerName)
	if !ok {
		return
	}
	drift := limitDimensionsDiffer(ctx.Limits, limits.Memory, limits.Pids)
	var differing []string
	if drift.Memory {
		differing = append(differing, fmt.Sprintf("memory limit differs: container %s, configured %s",
			memoryLimitDisplay(limits.Memory), configuredMemoryDisplay(ctx.Limits)))
	}
	if drift.Pids {
		differing = append(differing, fmt.Sprintf("pids limit differs: container %s, configured %d",
			pidsLimitDisplay(limits.Pids), ctx.Limits.Pids))
	}
	if len(differing) == 0 {
		return
	}
	_, _ = fmt.Fprintf(e.Stderr, "Warning: running container '%s' has limits that differ from the resolved config (%s); run 'cenci sandbox stop %s' and relaunch to apply the configured limits.\n",
		ctx.Scope.ContainerName, strings.Join(differing, "; "), ctx.Scope.ContainerName)
}

// oomKillCount reads the container cgroup's cumulative OOM-kill count from
// /sys/fs/cgroup/memory.events inside the container (ticket assumption: not
// .State.OOMKilled). It execs the absolute /bin/cat (present in the ubuntu
// base image, sandbox/Dockerfile.base) and reads at most maxMemoryEventsBytes
// of its output, treating anything longer as not the cgroup file. ok is false
// when the count cannot be read — the exec failing or timing out, over-long
// output, no oom_kill line (cgroup v1, or a host cgroup namespace), or a
// non-numeric value — so the caller reports "unknown" rather than a silent 0.
func (e *Engine) oomKillCount(name string) (count int64, ok bool) {
	out, err := sandbox.ReadContainerFiles(context.Background(), e.Runtime, name, limitProbeTimeout, maxMemoryEventsBytes, "/sys/fs/cgroup/memory.events")
	if err != nil {
		return 0, false
	}
	return parseMemoryEvents(string(out))
}

// parseMemoryEvents extracts the oom_kill counter from a cgroup v2
// memory.events file. The separate "oom" line counts times the limit was hit
// and is deliberately ignored: only oom_kill is processes actually killed.
func parseMemoryEvents(content string) (count int64, ok bool) {
	for _, line := range splitLines(content) {
		fields := strings.Fields(line)
		if len(fields) != 2 || fields[0] != "oom_kill" {
			continue
		}
		n, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil || n < 0 {
			return 0, false
		}
		return n, true
	}
	return 0, false
}
