package launcher

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/errcode"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/exectest"
)

// -- ticket #1216: memory and pids limits for the workload container ---------
//
// sandbox.memoryLimit (a size like "16g"/"512m", or "none") and
// sandbox.pidsLimit (an integer >= 1, or 0 for no limit) are read from the
// repo's .cenci/config.json with the same fail-closed rules as the other
// sandbox.* keys (#632). The create argv carries --memory/--memory-swap
// (equal: no container swap) and --pids-limit; the default memory is 50% of
// the container runtime's reported total memory, rounded down to whole MiB.

// defaultLimits is the resolved configuration when neither key is set.
var defaultLimits = workloadLimits{Memory: memoryDefault, Pids: 4096}

// -- config parse matrix (AC4 unit half) -------------------------------------

// TestRepoSandboxLimits_ValueAndErrorClasses pins RepoSandboxLimits'
// contract the way TestRepoSandboxPlugins_ValueAndErrorClasses pins the
// plugins key: an absent file/"sandbox"/key resolves to the defaults, every
// valid value resolves verbatim, and every other value -- wrong JSON type,
// bad size format, negative or non-integer pids, JSON null, an overflowing
// size -- is a plain (non-usage, exit 1), path-bearing error that names the
// offending key and never falls back to a default (#632).
func TestRepoSandboxLimits_ValueAndErrorClasses(t *testing.T) {
	cases := []struct {
		name   string
		config string
		want   workloadLimits
		// errKey is the config key the error must name; "" means no error.
		errKey string
	}{
		// -- absent values resolve to the defaults
		{name: "no sandbox key", config: `{}`, want: defaultLimits},
		{name: "empty sandbox object", config: `{"sandbox":{}}`, want: defaultLimits},
		{name: "unrelated sandbox keys only", config: `{"sandbox":{"dind":true,"plugins":["cenci"]}}`, want: defaultLimits},
		{name: "mis-cased key is not the key", config: `{"sandbox":{"MemoryLimit":"16g","PidsLimit":1}}`, want: defaultLimits},

		// -- memoryLimit values
		{name: "memory 16g", config: `{"sandbox":{"memoryLimit":"16g"}}`, want: workloadLimits{Memory: memoryExplicit, MemorySize: "16g", Pids: 4096}},
		{name: "memory 512m", config: `{"sandbox":{"memoryLimit":"512m"}}`, want: workloadLimits{Memory: memoryExplicit, MemorySize: "512m", Pids: 4096}},
		{name: "memory 1m", config: `{"sandbox":{"memoryLimit":"1m"}}`, want: workloadLimits{Memory: memoryExplicit, MemorySize: "1m", Pids: 4096}},
		{name: "memory none", config: `{"sandbox":{"memoryLimit":"none"}}`, want: workloadLimits{Memory: memoryNone, Pids: 4096}},
		{name: "largest g size that fits int64 bytes", config: `{"sandbox":{"memoryLimit":"8589934591g"}}`, want: workloadLimits{Memory: memoryExplicit, MemorySize: "8589934591g", Pids: 4096}},
		{name: "largest m size that fits int64 bytes", config: `{"sandbox":{"memoryLimit":"8796093022207m"}}`, want: workloadLimits{Memory: memoryExplicit, MemorySize: "8796093022207m", Pids: 4096}},

		{name: "memory 0g", config: `{"sandbox":{"memoryLimit":"0g"}}`, errKey: "memoryLimit"},
		{name: "memory 0m", config: `{"sandbox":{"memoryLimit":"0m"}}`, errKey: "memoryLimit"},
		{name: "memory uppercase unit", config: `{"sandbox":{"memoryLimit":"16G"}}`, errKey: "memoryLimit"},
		{name: "memory gb suffix", config: `{"sandbox":{"memoryLimit":"16gb"}}`, errKey: "memoryLimit"},
		{name: "memory kilobytes unit", config: `{"sandbox":{"memoryLimit":"512k"}}`, errKey: "memoryLimit"},
		{name: "memory no unit", config: `{"sandbox":{"memoryLimit":"16"}}`, errKey: "memoryLimit"},
		{name: "memory fractional", config: `{"sandbox":{"memoryLimit":"1.5g"}}`, errKey: "memoryLimit"},
		{name: "memory negative", config: `{"sandbox":{"memoryLimit":"-1g"}}`, errKey: "memoryLimit"},
		{name: "memory leading zero", config: `{"sandbox":{"memoryLimit":"016g"}}`, errKey: "memoryLimit"},
		{name: "memory leading space", config: `{"sandbox":{"memoryLimit":" 16g"}}`, errKey: "memoryLimit"},
		{name: "memory trailing newline", config: `{"sandbox":{"memoryLimit":"16g\n"}}`, errKey: "memoryLimit"},
		{name: "memory empty string", config: `{"sandbox":{"memoryLimit":""}}`, errKey: "memoryLimit"},
		{name: "memory none in capitals", config: `{"sandbox":{"memoryLimit":"None"}}`, errKey: "memoryLimit"},
		{name: "memory JSON number", config: `{"sandbox":{"memoryLimit":16}}`, errKey: "memoryLimit"},
		{name: "memory JSON bool", config: `{"sandbox":{"memoryLimit":true}}`, errKey: "memoryLimit"},
		{name: "memory JSON null is malformed not absent", config: `{"sandbox":{"memoryLimit":null}}`, errKey: "memoryLimit"},
		{name: "memory JSON array", config: `{"sandbox":{"memoryLimit":["16g"]}}`, errKey: "memoryLimit"},
		{name: "memory g size overflowing int64 bytes", config: `{"sandbox":{"memoryLimit":"8589934592g"}}`, errKey: "memoryLimit"},
		{name: "memory m size overflowing int64 bytes", config: `{"sandbox":{"memoryLimit":"8796093022208m"}}`, errKey: "memoryLimit"},
		{name: "memory size far past int64", config: `{"sandbox":{"memoryLimit":"99999999999999999999g"}}`, errKey: "memoryLimit"},

		// -- pidsLimit values
		{name: "pids 2048", config: `{"sandbox":{"pidsLimit":2048}}`, want: workloadLimits{Memory: memoryDefault, Pids: 2048}},
		{name: "pids 1", config: `{"sandbox":{"pidsLimit":1}}`, want: workloadLimits{Memory: memoryDefault, Pids: 1}},
		{name: "pids 0 means no limit", config: `{"sandbox":{"pidsLimit":0}}`, want: workloadLimits{Memory: memoryDefault, Pids: 0}},

		{name: "pids -1", config: `{"sandbox":{"pidsLimit":-1}}`, errKey: "pidsLimit"},
		{name: "pids fractional", config: `{"sandbox":{"pidsLimit":1.5}}`, errKey: "pidsLimit"},
		{name: "pids numeric string", config: `{"sandbox":{"pidsLimit":"4096"}}`, errKey: "pidsLimit"},
		{name: "pids JSON bool", config: `{"sandbox":{"pidsLimit":true}}`, errKey: "pidsLimit"},
		{name: "pids JSON null is malformed not absent", config: `{"sandbox":{"pidsLimit":null}}`, errKey: "pidsLimit"},
		{name: "pids JSON array", config: `{"sandbox":{"pidsLimit":[2048]}}`, errKey: "pidsLimit"},
		{name: "pids overflowing int64", config: `{"sandbox":{"pidsLimit":99999999999999999999}}`, errKey: "pidsLimit"},

		// -- the two keys are independent
		{name: "both keys valid", config: `{"sandbox":{"memoryLimit":"2g","pidsLimit":512}}`, want: workloadLimits{Memory: memoryExplicit, MemorySize: "2g", Pids: 512}},
		{name: "both opt-outs", config: `{"sandbox":{"memoryLimit":"none","pidsLimit":0}}`, want: workloadLimits{Memory: memoryNone, Pids: 0}},
		{name: "limits next to other sandbox keys", config: `{"sandbox":{"dind":true,"plugins":["cenci"],"memoryLimit":"4g","pidsLimit":100}}`, want: workloadLimits{Memory: memoryExplicit, MemorySize: "4g", Pids: 100}},
		{name: "valid memory with malformed pids names pids", config: `{"sandbox":{"memoryLimit":"2g","pidsLimit":-5}}`, errKey: "pidsLimit"},
		{name: "malformed memory with valid pids names memory", config: `{"sandbox":{"memoryLimit":"2G","pidsLimit":5}}`, errKey: "memoryLimit"},

		// -- structural failures shared with the other sandbox.* readers
		{name: "unparsable JSON", config: `{not valid json`, errKey: "config.json"},
		{name: "top level is an array", config: `[]`, errKey: "config.json"},
		{name: "sandbox is not an object", config: `{"sandbox":[]}`, errKey: "sandbox"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := t.TempDir()
			writeFile(t, filepath.Join(repo, ".cenci", "config.json"), tc.config)

			got, err := RepoSandboxLimits(repo)

			if tc.errKey == "" {
				if err != nil {
					t.Fatalf("RepoSandboxLimits(%s) = error %v, want %+v", tc.config, err, tc.want)
				}
				if got != tc.want {
					t.Errorf("RepoSandboxLimits(%s) = %+v, want %+v", tc.config, got, tc.want)
				}
				return
			}
			if err == nil {
				t.Fatalf("RepoSandboxLimits(%s) = %+v, nil error; want an error naming %q", tc.config, got, tc.errKey)
			}
			if IsUsage(err) {
				t.Errorf("RepoSandboxLimits(%s) error is a usage error (would exit 2), want a plain error (exit 1, #632): %v", tc.config, err)
			}
			if !strings.Contains(err.Error(), tc.errKey) {
				t.Errorf("RepoSandboxLimits(%s) error = %q, want it to name %q", tc.config, err.Error(), tc.errKey)
			}
			if !strings.Contains(err.Error(), "config.json") {
				t.Errorf("RepoSandboxLimits(%s) error = %q, want it to bear the config path", tc.config, err.Error())
			}
			if got != (workloadLimits{}) {
				t.Errorf("RepoSandboxLimits(%s) = %+v alongside an error, want the zero value (never a silent default)", tc.config, got)
			}
		})
	}
}

// TestRepoSandboxLimits_AbsentConfigFileResolvesToDefaults pins the no-config
// case separately: a repo without .cenci/config.json is the common case and
// must resolve to the defaults, not an error.
func TestRepoSandboxLimits_AbsentConfigFileResolvesToDefaults(t *testing.T) {
	got, err := RepoSandboxLimits(t.TempDir())
	if err != nil {
		t.Fatalf("RepoSandboxLimits(repo without config) = %v, want nil", err)
	}
	if got != defaultLimits {
		t.Errorf("RepoSandboxLimits(repo without config) = %+v, want %+v", got, defaultLimits)
	}
}

// TestResolveWorkloadLimits_LegacyScopeDefaultsWithoutReadingConfig pins the
// repo-scope-only read gate (the ResolveSandboxPlugins precedent): outside
// repo scope the defaults apply with no override, and a corrupt config at the
// would-be repo root is never read.
func TestResolveWorkloadLimits_LegacyScopeDefaultsWithoutReadingConfig(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, ".cenci", "config.json"), `{not valid json`)

	got, err := ResolveWorkloadLimits(Scope{WorkspaceScope: "legacy", RepoRoot: repo})
	if err != nil {
		t.Fatalf("ResolveWorkloadLimits(legacy scope) with a malformed config present: %v", err)
	}
	if got != defaultLimits {
		t.Errorf("ResolveWorkloadLimits(legacy scope) = %+v, want the defaults %+v", got, defaultLimits)
	}
}

func TestResolveWorkloadLimits_RepoScopeReadsConfig(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, ".cenci", "config.json"), `{"sandbox":{"memoryLimit":"2g","pidsLimit":512}}`)

	got, err := ResolveWorkloadLimits(Scope{WorkspaceScope: "repo", RepoRoot: repo})
	if err != nil {
		t.Fatalf("ResolveWorkloadLimits(repo scope): %v", err)
	}
	want := workloadLimits{Memory: memoryExplicit, MemorySize: "2g", Pids: 512}
	if got != want {
		t.Errorf("ResolveWorkloadLimits(repo scope) = %+v, want %+v", got, want)
	}
}

func TestResolveWorkloadLimits_RepoScopeMalformedConfigHardFails(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, ".cenci", "config.json"), `{"sandbox":{"memoryLimit":"16G"}}`)

	_, err := ResolveWorkloadLimits(Scope{WorkspaceScope: "repo", RepoRoot: repo})
	if err == nil {
		t.Fatal("ResolveWorkloadLimits(repo scope) with a malformed memoryLimit = nil error, want an error")
	}
	if IsUsage(err) {
		t.Errorf("ResolveWorkloadLimits malformed-config error is a usage error (would exit 2), want a plain error (exit 1): %v", err)
	}
	if !strings.Contains(err.Error(), "memoryLimit") {
		t.Errorf("ResolveWorkloadLimits error = %q, want it to name memoryLimit", err.Error())
	}
}

// -- limitsDiffer (AC9 comparison) -------------------------------------------

// TestLimitsDiffer pins the attach-path drift comparison between the resolved
// config and a running container's raw .HostConfig.Memory/.PidsLimit values.
// Unlimited is encoded as "0" (docker), "-1" (podman) or "<nil>" (a nil pids
// pointer printed by docker's template engine); every one must read as
// unlimited. A default memory limit is never compared against a freshly
// computed N -- only "the container has no memory limit at all" is drift --
// and a configured pidsLimit of 0 never differs on pids (podman applies its
// own containers.conf default when the flag is omitted).
func TestLimitsDiffer(t *testing.T) {
	const (
		gib8  = "8589934592"
		gib16 = "17179869184"
	)
	explicit16g := workloadLimits{Memory: memoryExplicit, MemorySize: "16g", Pids: 4096}
	explicit512m := workloadLimits{Memory: memoryExplicit, MemorySize: "512m", Pids: 4096}
	noMemory := workloadLimits{Memory: memoryNone, Pids: 4096}
	noPids := workloadLimits{Memory: memoryDefault, Pids: 0}

	cases := []struct {
		name   string
		want   workloadLimits
		memory string
		pids   string
		differ bool
	}{
		// default mode: any memory limit is fine, none at all is drift
		{"default, limited container", defaultLimits, gib8, "4096", false},
		{"default, container at a different size is not drift", defaultLimits, gib16, "4096", false},
		{"default, unlimited container (memory 0)", defaultLimits, "0", "4096", true},
		{"default, unlimited container (memory -1)", defaultLimits, "-1", "4096", true},
		{"default, unlimited legacy container (memory 0, pids nil)", defaultLimits, "0", "<nil>", true},

		// explicit memory: the exact byte count is compared
		{"explicit 16g, matching container", explicit16g, gib16, "4096", false},
		{"explicit 16g, 8g container", explicit16g, gib8, "4096", true},
		{"explicit 16g, unlimited container", explicit16g, "0", "4096", true},
		{"explicit 512m, matching container", explicit512m, "536870912", "4096", false},
		{"explicit 512m, off by one byte", explicit512m, "536870913", "4096", true},

		// memory none: a limited container is drift, an unlimited one is not
		{"none, unlimited container (0)", noMemory, "0", "4096", false},
		{"none, unlimited container (-1)", noMemory, "-1", "4096", false},
		{"none, limited container", noMemory, gib8, "4096", true},

		// pids
		{"pids 4096, matching", defaultLimits, gib8, "4096", false},
		{"pids 4096 configured, 2048 running", defaultLimits, gib8, "2048", true},
		{"pids 2048 configured, 4096 running", workloadLimits{Memory: memoryDefault, Pids: 2048}, gib8, "4096", true},
		{"pids 2048 configured, 2048 running", workloadLimits{Memory: memoryDefault, Pids: 2048}, gib8, "2048", false},
		{"pids configured, unlimited running (0)", defaultLimits, gib8, "0", true},
		{"pids configured, unlimited running (-1)", defaultLimits, gib8, "-1", true},
		{"pids configured, unlimited running (nil)", defaultLimits, gib8, "<nil>", true},

		// pidsLimit 0 never differs on pids, whatever the container has
		{"pidsLimit 0, running 4096", noPids, gib8, "4096", false},
		{"pidsLimit 0, running 0", noPids, gib8, "0", false},
		{"pidsLimit 0, running -1", noPids, gib8, "-1", false},
		{"pidsLimit 0, running nil", noPids, gib8, "<nil>", false},
		{"pidsLimit 0 does not mask a memory drift", noPids, "0", "4096", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := limitsDiffer(tc.want, tc.memory, tc.pids); got != tc.differ {
				t.Errorf("limitsDiffer(%+v, memory=%q, pids=%q) = %v, want %v", tc.want, tc.memory, tc.pids, got, tc.differ)
			}
		})
	}
}

// -- create argv through DryRun (AC1, AC2, AC3, AC5, AC7, AC8) ---------------

// limitsDryRunFixture sets up a git repo whose .cenci/config.json holds config
// (none when empty), an isolated HOME, a fake <runtime> first on PATH, and the
// extra FAKE_* environment; it returns an Engine for that runtime and the
// fake's call-log path. The caller invokes DryRun, which shares planArgvs
// (and so the limit-flag construction) with a real launch (#620).
func limitsDryRunFixture(t *testing.T, runtime, config string, env map[string]string) (*Engine, string) {
	t.Helper()
	repo := newAuditTestRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(repo)
	if config != "" {
		writeFile(t, filepath.Join(repo, ".cenci", "config.json"), config)
	}

	fakeDir := t.TempDir()
	callLog := filepath.Join(fakeDir, "calls.txt")
	writeFakeRuntime(t, fakeDir, runtime, callLog)
	t.Setenv("PATH", fakeDir+":"+os.Getenv("PATH"))
	for k, v := range env {
		t.Setenv(k, v)
	}
	return &Engine{Runtime: runtime, Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}}, callLog
}

// flagValue returns the token following the exact flag token in argv.
func flagValue(argv []string, flag string) (string, bool) {
	for i, tok := range argv {
		if tok == flag && i+1 < len(argv) {
			return argv[i+1], true
		}
	}
	return "", false
}

// hasToken reports whether argv contains the exact token (so "--memory" never
// matches "--memory-swap").
func hasToken(argv []string, tok string) bool {
	for _, a := range argv {
		if a == tok {
			return true
		}
	}
	return false
}

// memTotalCalls returns the call-log lines that are host-memory queries.
func memTotalCalls(lines []string) []string {
	var out []string
	for _, l := range lines {
		if strings.HasPrefix(l, "info") && strings.Contains(l, "MemTotal") {
			out = append(out, l)
		}
	}
	return out
}

// TestDryRun_Limits_Default_HalfOfHostMemoryNoSwapAnd4096Pids is AC1: with no
// override the fresh container's create argv carries --memory <N>m
// --memory-swap <N>m (equal, so no container swap), N being 50% of the
// runtime's reported total memory, plus --pids-limit 4096.
func TestDryRun_Limits_Default_HalfOfHostMemoryNoSwapAnd4096Pids(t *testing.T) {
	// FAKE_INFO_MEMTOTAL left at the fake's default of 17179869184 (16 GiB).
	eng, callLog := limitsDryRunFixture(t, "docker", "", nil)

	plan, err := eng.DryRun(Options{Agent: "claude"})
	if err != nil {
		t.Fatalf("DryRun: %v", err)
	}

	argv := plan.CreateArgv
	if got, _ := flagValue(argv, "--memory"); got != "8192m" {
		t.Errorf("--memory = %q, want 8192m (50%% of 16 GiB); argv:\n%s", got, strings.Join(argv, " "))
	}
	if got, _ := flagValue(argv, "--memory-swap"); got != "8192m" {
		t.Errorf("--memory-swap = %q, want 8192m (equal to --memory: no swap); argv:\n%s", got, strings.Join(argv, " "))
	}
	if got, _ := flagValue(argv, "--pids-limit"); got != "4096" {
		t.Errorf("--pids-limit = %q, want 4096; argv:\n%s", got, strings.Join(argv, " "))
	}
	if calls := memTotalCalls(readCallLog(t, callLog)); len(calls) != 1 {
		t.Errorf("want exactly one host-memory query for a default-memory create, got %d:\n%s", len(calls), strings.Join(calls, "\n"))
	}
}

// TestDryRun_Limits_Default_RoundsDownToWholeMiB pins the N computation
// across sizes: 50% of the reported bytes, floored to whole MiB, with the
// 1 MiB minimum accepted at its boundary.
func TestDryRun_Limits_Default_RoundsDownToWholeMiB(t *testing.T) {
	cases := []struct {
		memTotal string
		want     string
	}{
		{"17179869184", "8192m"},  // 16 GiB -> 8 GiB exactly
		{"34359738368", "16384m"}, // 32 GiB
		{"17179869185", "8192m"},  // one byte over: still 8192
		{"10000000000", "4768m"},  // 4768.37 MiB floors to 4768
		{"3145727", "1m"},         // 1572863 bytes = 1.4999 MiB floors to 1
		{"2097152", "1m"},         // 50% is exactly 1 MiB
	}
	for _, tc := range cases {
		t.Run(tc.memTotal, func(t *testing.T) {
			eng, _ := limitsDryRunFixture(t, "docker", "", map[string]string{"FAKE_INFO_MEMTOTAL": tc.memTotal})

			plan, err := eng.DryRun(Options{Agent: "claude"})
			if err != nil {
				t.Fatalf("DryRun with MemTotal %s: %v", tc.memTotal, err)
			}
			if got, _ := flagValue(plan.CreateArgv, "--memory"); got != tc.want {
				t.Errorf("MemTotal %s: --memory = %q, want %q", tc.memTotal, got, tc.want)
			}
			if got, _ := flagValue(plan.CreateArgv, "--memory-swap"); got != tc.want {
				t.Errorf("MemTotal %s: --memory-swap = %q, want %q", tc.memTotal, got, tc.want)
			}
		})
	}
}

// TestDryRun_Limits_HostMemoryQueryMatchesRuntimeFlavour pins which template
// asks which runtime: podman reports total memory under .Host.MemTotal,
// docker under .MemTotal.
func TestDryRun_Limits_HostMemoryQueryMatchesRuntimeFlavour(t *testing.T) {
	cases := []struct {
		runtime  string
		template string
	}{
		{"docker", "{{.MemTotal}}"},
		{"podman", "{{.Host.MemTotal}}"},
	}
	for _, tc := range cases {
		t.Run(tc.runtime, func(t *testing.T) {
			eng, callLog := limitsDryRunFixture(t, tc.runtime, "", nil)

			if _, err := eng.DryRun(Options{Agent: "claude"}); err != nil {
				t.Fatalf("DryRun: %v", err)
			}
			calls := memTotalCalls(readCallLog(t, callLog))
			if len(calls) == 0 || !strings.Contains(calls[0], tc.template) {
				t.Errorf("the first host-memory query on %s must use %s; got:\n%s", tc.runtime, tc.template, strings.Join(calls, "\n"))
			}
		})
	}
}

// TestDryRun_Limits_Explicit_NoHostMemoryQuery is AC2: an explicit size is
// used verbatim for --memory and --memory-swap without asking the runtime for
// its total memory (proven by letting that query fail), and an explicit
// pidsLimit is used for --pids-limit.
func TestDryRun_Limits_Explicit_NoHostMemoryQuery(t *testing.T) {
	cases := []struct {
		name   string
		config string
		memory string
		pids   string
		// derivesMemory marks the one case whose memory is still the
		// default, so the host-memory query is legitimately made.
		derivesMemory bool
	}{
		{"16g and 2048 pids", `{"sandbox":{"memoryLimit":"16g","pidsLimit":2048}}`, "16g", "2048", false},
		{"512m only", `{"sandbox":{"memoryLimit":"512m"}}`, "512m", "4096", false},
		{"pids only keeps the derived default memory", `{"sandbox":{"pidsLimit":2048}}`, "8192m", "2048", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Where memory is explicit the host-memory query is made to fail:
			// a launch that still asked for it would error out.
			env := map[string]string{}
			if !tc.derivesMemory {
				env["FAKE_INFO_MEMTOTAL_EXIT"] = "1"
			}
			eng, callLog := limitsDryRunFixture(t, "docker", tc.config, env)

			plan, err := eng.DryRun(Options{Agent: "claude"})
			if err != nil {
				t.Fatalf("DryRun: %v", err)
			}

			argv := plan.CreateArgv
			if got, _ := flagValue(argv, "--memory"); got != tc.memory {
				t.Errorf("--memory = %q, want %q; argv:\n%s", got, tc.memory, strings.Join(argv, " "))
			}
			if got, _ := flagValue(argv, "--memory-swap"); got != tc.memory {
				t.Errorf("--memory-swap = %q, want %q; argv:\n%s", got, tc.memory, strings.Join(argv, " "))
			}
			if got, _ := flagValue(argv, "--pids-limit"); got != tc.pids {
				t.Errorf("--pids-limit = %q, want %q; argv:\n%s", got, tc.pids, strings.Join(argv, " "))
			}
			calls := memTotalCalls(readCallLog(t, callLog))
			if !tc.derivesMemory && len(calls) != 0 {
				t.Errorf("an explicit memoryLimit must not query the runtime for total memory; got:\n%s", strings.Join(calls, "\n"))
			}
		})
	}
}

// TestDryRun_Limits_OptOuts_AreIndependent is AC3: "none" leaves out both
// --memory and --memory-swap, pidsLimit 0 leaves out --pids-limit, and each
// opt-out leaves the other limit in place. A memory opt-out also needs no
// host-memory query.
func TestDryRun_Limits_OptOuts_AreIndependent(t *testing.T) {
	cases := []struct {
		name       string
		config     string
		wantMemory bool
		wantPids   bool
	}{
		{"memory none keeps pids", `{"sandbox":{"memoryLimit":"none"}}`, false, true},
		{"pids 0 keeps memory", `{"sandbox":{"pidsLimit":0}}`, true, false},
		{"both opt-outs", `{"sandbox":{"memoryLimit":"none","pidsLimit":0}}`, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			eng, callLog := limitsDryRunFixture(t, "docker", tc.config, nil)

			plan, err := eng.DryRun(Options{Agent: "claude"})
			if err != nil {
				t.Fatalf("DryRun: %v", err)
			}

			argv := plan.CreateArgv
			if got := hasToken(argv, "--memory"); got != tc.wantMemory {
				t.Errorf("--memory present = %v, want %v; argv:\n%s", got, tc.wantMemory, strings.Join(argv, " "))
			}
			if got := hasToken(argv, "--memory-swap"); got != tc.wantMemory {
				t.Errorf("--memory-swap present = %v, want %v; argv:\n%s", got, tc.wantMemory, strings.Join(argv, " "))
			}
			if got := hasToken(argv, "--pids-limit"); got != tc.wantPids {
				t.Errorf("--pids-limit present = %v, want %v; argv:\n%s", got, tc.wantPids, strings.Join(argv, " "))
			}
			if !tc.wantMemory {
				if calls := memTotalCalls(readCallLog(t, callLog)); len(calls) != 0 {
					t.Errorf("a memory opt-out must not query the runtime for total memory; got:\n%s", strings.Join(calls, "\n"))
				}
			}
		})
	}
}

// TestDryRun_Limits_UnreadableHostMemory_FailsBeforeAnyCreate is AC5: with
// the default memory limit and a total that cannot be read (the query fails,
// or answers something that is not a usable size), the launch fails with the
// registered LIMITS-001 error naming the "memoryLimit": "none" opt-out and
// where the key lives, and no create-side runtime call is made.
func TestDryRun_Limits_UnreadableHostMemory_FailsBeforeAnyCreate(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
	}{
		{"query exits nonzero", map[string]string{"FAKE_INFO_MEMTOTAL_EXIT": "1"}},
		{"not a number", map[string]string{"FAKE_INFO_MEMTOTAL": "not-a-number"}},
		{"empty answer", map[string]string{"FAKE_INFO_MEMTOTAL": ""}},
		{"zero", map[string]string{"FAKE_INFO_MEMTOTAL": "0"}},
		{"negative", map[string]string{"FAKE_INFO_MEMTOTAL": "-5"}},
		{"fractional", map[string]string{"FAKE_INFO_MEMTOTAL": "16.5"}},
		{"derived limit under 1 MiB", map[string]string{"FAKE_INFO_MEMTOTAL": "2097151"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			eng, callLog := limitsDryRunFixture(t, "docker", "", tc.env)

			_, err := eng.DryRun(Options{Agent: "claude"})
			if err == nil {
				t.Fatal("DryRun with an unusable host memory total = nil error, want the LIMITS-001 failure")
			}
			if IsUsage(err) {
				t.Errorf("host-memory error is a usage error (would exit 2), want a plain error (exit 1): %v", err)
			}
			msg := err.Error()
			for _, want := range []string{string(errcode.SandboxLimitsHostMemoryUnreadable), `"memoryLimit": "none"`, ".cenci/config.json"} {
				if !strings.Contains(msg, want) {
					t.Errorf("host-memory error missing %q; got:\n%s", want, msg)
				}
			}
			for _, line := range readCallLog(t, callLog) {
				if strings.HasPrefix(line, "run ") || strings.HasPrefix(line, "rm ") {
					t.Errorf("no container may be created or removed when the limit cannot be resolved; saw %q", line)
				}
			}
		})
	}
}

// TestDryRun_Limits_HostMemoryQueryFallsBackToTheOtherTemplate pins that a
// failed first query is retried once with the other runtime's template (so a
// podman-docker shim still works) before giving up.
func TestDryRun_Limits_HostMemoryQueryFallsBackToTheOtherTemplate(t *testing.T) {
	eng, callLog := limitsDryRunFixture(t, "docker", "", map[string]string{"FAKE_INFO_MEMTOTAL_EXIT": "1"})

	if _, err := eng.DryRun(Options{Agent: "claude"}); err == nil {
		t.Fatal("DryRun with a failing host-memory query = nil error, want the LIMITS-001 failure")
	}

	joined := strings.Join(memTotalCalls(readCallLog(t, callLog)), "\n")
	for _, template := range []string{"{{.MemTotal}}", "{{.Host.MemTotal}}"} {
		if !strings.Contains(joined, template) {
			t.Errorf("the failed query was not retried with %s; got:\n%s", template, joined)
		}
	}
}

// TestDryRun_Limits_Preview_ShowsExactlyTheFlagsOfTheCreateArgv is AC7: the
// rendered `cenci open --dry-run` text carries the same limit flags the
// create argv has, for the default and the override cases, and omits the
// flags an opt-out removes.
func TestDryRun_Limits_Preview_ShowsExactlyTheFlagsOfTheCreateArgv(t *testing.T) {
	cases := []struct {
		name    string
		config  string
		present []string
		absent  []string
	}{
		{
			name:    "default",
			present: []string{"--memory 8192m", "--memory-swap 8192m", "--pids-limit 4096"},
		},
		{
			name:    "override",
			config:  `{"sandbox":{"memoryLimit":"2g","pidsLimit":512}}`,
			present: []string{"--memory 2g", "--memory-swap 2g", "--pids-limit 512"},
			absent:  []string{"8192m", "4096"},
		},
		{
			name:   "opt-outs",
			config: `{"sandbox":{"memoryLimit":"none","pidsLimit":0}}`,
			absent: []string{"--memory", "--pids-limit"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			eng, _ := limitsDryRunFixture(t, "docker", tc.config, nil)

			plan, err := eng.DryRun(Options{Agent: "claude"})
			if err != nil {
				t.Fatalf("DryRun: %v", err)
			}
			var out bytes.Buffer
			if err := plan.WriteText(&out); err != nil {
				t.Fatalf("WriteText: %v", err)
			}

			rendered := out.String()
			createSection, _, _ := strings.Cut(rendered, "Agent attach (exec):")
			for _, want := range tc.present {
				if !strings.Contains(createSection, want) {
					t.Errorf("dry-run create preview missing %q; got:\n%s", want, createSection)
				}
			}
			for _, unwanted := range tc.absent {
				if strings.Contains(createSection, unwanted) {
					t.Errorf("dry-run create preview contains %q, want it absent; got:\n%s", unwanted, createSection)
				}
			}
		})
	}
}

// TestDryRun_Limits_Dind_FlagsAlongsideSysboxRuntime is AC8 through the
// preview surface: with dind on, the create argv carries both the limit flags
// and --runtime=sysbox-runc.
func TestDryRun_Limits_Dind_FlagsAlongsideSysboxRuntime(t *testing.T) {
	eng, _ := limitsDryRunFixture(t, "docker",
		`{"sandbox":{"dind":true,"memoryLimit":"4g","pidsLimit":1000}}`,
		map[string]string{"FAKE_INFO_RUNTIMES": `{"sysbox-runc":{},"runc":{}}`})

	plan, err := eng.DryRun(Options{Agent: "claude"})
	if err != nil {
		t.Fatalf("DryRun: %v", err)
	}

	argv := plan.CreateArgv
	if !hasToken(argv, "--runtime=sysbox-runc") {
		t.Fatalf("dind create argv is missing --runtime=sysbox-runc; argv:\n%s", strings.Join(argv, " "))
	}
	if got, _ := flagValue(argv, "--memory"); got != "4g" {
		t.Errorf("dind --memory = %q, want 4g; argv:\n%s", got, strings.Join(argv, " "))
	}
	if got, _ := flagValue(argv, "--memory-swap"); got != "4g" {
		t.Errorf("dind --memory-swap = %q, want 4g; argv:\n%s", got, strings.Join(argv, " "))
	}
	if got, _ := flagValue(argv, "--pids-limit"); got != "1000" {
		t.Errorf("dind --pids-limit = %q, want 1000; argv:\n%s", got, strings.Join(argv, " "))
	}
}

// TestDryRun_Limits_MalformedConfig_FailsNamingTheKey is AC4's preview half
// at the engine level: a malformed key makes DryRun fail with a plain error
// naming it, before any runtime call.
func TestDryRun_Limits_MalformedConfig_FailsNamingTheKey(t *testing.T) {
	eng, callLog := limitsDryRunFixture(t, "docker", `{"sandbox":{"pidsLimit":-1}}`, nil)

	_, err := eng.DryRun(Options{Agent: "claude"})
	if err == nil {
		t.Fatal("DryRun with pidsLimit -1 = nil error, want a hard failure")
	}
	if IsUsage(err) {
		t.Errorf("malformed-limit error is a usage error (would exit 2), want a plain error (exit 1): %v", err)
	}
	if !strings.Contains(err.Error(), "pidsLimit") {
		t.Errorf("DryRun error = %q, want it to name pidsLimit", err.Error())
	}
	if lines := readCallLog(t, callLog); len(lines) != 0 {
		t.Errorf("want no runtime calls once the limits config hard-fails, got:\n%s", strings.Join(lines, "\n"))
	}
}

// -- LIMITS-001 keeps the real cause -------------------------------------------

// replaceFakeRuntime overwrites the fixture's fake runtime with script (which
// still logs every call) so a test can script stderr content or a hang that
// the shared fake has no knob for.
func replaceFakeRuntime(t *testing.T, callLog, runtime, script string) {
	t.Helper()
	body := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + exectest.ShellQuote(callLog) + "\n" + script
	exectest.WriteExecutable(t, filepath.Join(filepath.Dir(callLog), runtime), body)
}

// TestHostMemTotal_FailureCarriesRuntimeStderrOnOneBoundedLine pins that the
// reason the runtime gave for refusing `info` (not just "exit status 1")
// reaches the LIMITS-001 error, collapsed to one line and bounded, with the
// *exec.ExitError still reachable.
func TestHostMemTotal_FailureCarriesRuntimeStderrOnOneBoundedLine(t *testing.T) {
	eng, callLog := limitsDryRunFixture(t, "docker", "", nil)
	replaceFakeRuntime(t, callLog, "docker",
		"printf 'Cannot connect to the Docker daemon\\n  at unix:///var/run/docker.sock\\n' >&2\n"+
			"i=0; while [ $i -lt 100 ]; do printf 'xxxxxxxxxx' >&2; i=$((i+1)); done\n"+
			"exit 1\n")

	_, err := eng.hostMemTotal()
	if err == nil {
		t.Fatal("hostMemTotal with a failing runtime = nil error, want LIMITS-001")
	}

	msg := err.Error()
	if want := "Cannot connect to the Docker daemon at unix:///var/run/docker.sock"; !strings.Contains(msg, want) {
		t.Errorf("error missing the runtime's stderr collapsed to one line %q; got:\n%s", want, msg)
	}
	if strings.Contains(msg, strings.Repeat("x", maxStderrDetailBytes+1)) {
		t.Errorf("runtime stderr must be bounded to %d bytes; got a longer run of it:\n%s", maxStderrDetailBytes, msg)
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Errorf("the *exec.ExitError must stay reachable through the wrap; got %T", err)
	}
	for _, want := range []string{string(errcode.SandboxLimitsHostMemoryUnreadable), `"memoryLimit": "none"`, ".cenci/config.json"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error missing %q; got:\n%s", want, msg)
		}
	}
}

// TestHostMemTotal_BothTemplatesFail_ReportsTheFirstTemplatesErrorToo pins that
// the runtime-appropriate template's failure is not masked by the fallback's.
func TestHostMemTotal_BothTemplatesFail_ReportsTheFirstTemplatesErrorToo(t *testing.T) {
	eng, callLog := limitsDryRunFixture(t, "docker", "", nil)
	replaceFakeRuntime(t, callLog, "docker",
		"case \"$*\" in\n"+
			"*'.Host.MemTotal'*) echo 'podman template: unknown field Host' >&2 ;;\n"+
			"*) echo 'docker template: daemon is shutting down' >&2 ;;\n"+
			"esac\nexit 1\n")

	_, err := eng.hostMemTotal()
	if err == nil {
		t.Fatal("hostMemTotal with both templates failing = nil error, want LIMITS-001")
	}

	msg := err.Error()
	first := strings.Index(msg, "docker template: daemon is shutting down")
	second := strings.Index(msg, "podman template: unknown field Host")
	if first < 0 || second < 0 {
		t.Fatalf("error must carry both the first (docker) and the fallback (podman) failure; got:\n%s", msg)
	}
	if first > second {
		t.Errorf("the first template's error must be reported before the fallback's; got:\n%s", msg)
	}
}

// TestHostMemTotal_TimeoutSaysSoAndDoesNotRetryTheOtherTemplate pins that a
// wedged engine is reported as a timeout and is not queried a second time.
func TestHostMemTotal_TimeoutSaysSoAndDoesNotRetryTheOtherTemplate(t *testing.T) {
	eng, callLog := limitsDryRunFixture(t, "docker", "", nil)
	// `exec` so the context kill reaches the process holding the pipes.
	replaceFakeRuntime(t, callLog, "docker", "exec /bin/sleep 30\n")
	previous := hostMemQueryTimeout
	hostMemQueryTimeout = 200 * time.Millisecond
	t.Cleanup(func() { hostMemQueryTimeout = previous })

	start := time.Now()
	_, err := eng.hostMemTotal()
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("hostMemTotal against a hung runtime = nil error, want LIMITS-001")
	}
	if !strings.Contains(err.Error(), "timed out after 200ms") {
		t.Errorf("error must say the query timed out after 200ms; got:\n%s", err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("errors.Is(err, context.DeadlineExceeded) = false; err: %v", err)
	}
	if calls := memTotalCalls(readCallLog(t, callLog)); len(calls) != 1 {
		t.Errorf("a timed-out query must not be retried with the other template; got %d calls:\n%s", len(calls), strings.Join(calls, "\n"))
	}
	if elapsed > 10*time.Second {
		t.Errorf("the query took %v, want it cut off by the shortened timeout", elapsed)
	}
}
