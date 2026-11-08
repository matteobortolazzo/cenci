package pipeline

// Tests for the plan-authorization record (#1464): the SHA-256-bound
// `authorization` field of .cenci/pipeline/<id>.json, its closed-set parser,
// the fingerprint, the unattended predicate, the persist rules of bare
// `plan --authorization K`, the `plan --approve` gate (unattended and
// attended), and the `dispatch approve` engine (ApproveHeldPlan).
//
// Everything runs through the public Run / ApproveHeldPlan entry points
// against real temp repos, real `.plans/<id>-*.md` files and real state files.
// Every test pins CENCI_ATTENDED and the origin marker itself (setAttended /
// setUnattended): the developer sandbox these run in sets CENCI_ATTENDED=0
// ambiently, so an unpinned test would silently change posture.

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func setAttended(t *testing.T) {
	t.Helper()
	t.Setenv(AttendedEnv, "1")
	t.Setenv(AttendedOriginEnv, "")
}

func setUnattended(t *testing.T) {
	t.Helper()
	t.Setenv(AttendedEnv, "0")
	t.Setenv(AttendedOriginEnv, "")
}

// authFixture is one ticket (#42) with a valid plan file, a state file at a
// chosen stage and the paths needed to inspect both.
type authFixture struct {
	repoRoot string
	stateDir string
	planPath string
}

func newAuthFixture(t *testing.T, stage Stage) *authFixture {
	t.Helper()
	f := &authFixture{repoRoot: t.TempDir(), stateDir: t.TempDir()}
	f.planPath = mustWriteValidAdoptPlan(t, f.repoRoot, "42", "add-thing")
	mustSeedState(t, f.stateDir, "42", stage)
	return f
}

func (f *authFixture) statePath() string { return filepath.Join(f.stateDir, "42.json") }

func (f *authFixture) hash(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(f.planPath)
	if err != nil {
		t.Fatalf("read plan: %v", err)
	}
	return PlanFingerprint(string(b))
}

func (f *authFixture) run(stage string, approve bool, kind string) (Output, error) {
	return Run(Opts{Stage: stage, ID: "42", Approve: approve, Authorization: kind, RepoRoot: f.repoRoot, StateDir: f.stateDir})
}

func (f *authFixture) approve() (Output, error) { return f.run("plan", true, "") }

func (f *authFixture) stateBytes(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile(f.statePath())
	if err != nil {
		t.Fatalf("read state file: %v", err)
	}
	return b
}

func (f *authFixture) state(t *testing.T) State {
	t.Helper()
	s, err := loadState(f.statePath())
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	return s
}

func (f *authFixture) record(t *testing.T) (Authorization, AuthorizationClass) {
	t.Helper()
	return ParseAuthorization(f.state(t).Authorization)
}

func (f *authFixture) seedRaw(t *testing.T, raw string) {
	t.Helper()
	s := f.state(t)
	s.Authorization = json.RawMessage(raw)
	if err := saveState(f.statePath(), s); err != nil {
		t.Fatalf("seed raw authorization: %v", err)
	}
}

func (f *authFixture) seedRecord(t *testing.T, kind AuthorizationKind, hash, source string) {
	t.Helper()
	raw, err := json.Marshal(Authorization{Kind: kind, PlanSHA256: hash, RecordedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), Source: source})
	if err != nil {
		t.Fatalf("marshal record: %v", err)
	}
	f.seedRaw(t, string(raw))
}

// seedSameHash seeds kind bound to the plan's current bytes, with the source
// its kind requires.
func (f *authFixture) seedSameHash(t *testing.T, kind AuthorizationKind) {
	t.Helper()
	source := ""
	if kind == AuthorizationHuman {
		source = AuthorizationSourceCLI
	}
	f.seedRecord(t, kind, f.hash(t), source)
}

// changePlan rewrites the plan so its fingerprint no longer matches any
// earlier record.
func (f *authFixture) changePlan(t *testing.T) {
	t.Helper()
	b, err := os.ReadFile(f.planPath)
	if err != nil {
		t.Fatalf("read plan: %v", err)
	}
	if err := os.WriteFile(f.planPath, append(b, []byte("\nextra line\n")...), 0o644); err != nil {
		t.Fatalf("rewrite plan: %v", err)
	}
}

const otherHash = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

var autonomousKinds = []AuthorizationKind{AuthorizationLean, AuthorizationTrivial, AuthorizationLeanResumed, AuthorizationLeanDecided}

// assertRefused pins a refused call: a non-nil error detectable as sentinel,
// a populated errors[], the reported stage unchanged and the state file
// byte-identical (a refusal never saves).
func assertRefused(t *testing.T, f *authFixture, before []byte, out Output, err error, sentinel error, wantStage Stage) {
	t.Helper()
	if err == nil {
		t.Fatalf("want an error wrapping %v, got nil (state now %s)", sentinel, f.stateBytes(t))
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("error = %v, want errors.Is(_, %v)", err, sentinel)
	}
	if len(out.Errors) == 0 {
		t.Errorf("Output.Errors = [], want the refusal in errors[]")
	}
	if out.State != string(wantStage) {
		t.Errorf("Output.State = %q, want %q", out.State, wantStage)
	}
	if after := f.stateBytes(t); !bytes.Equal(before, after) {
		t.Errorf("a refusal changed the state file:\nbefore: %s\nafter:  %s", before, after)
	}
}

// -- parser ----------------------------------------------------------------------

func TestParseAuthorization_Classes(t *testing.T) {
	good := `"planSha256":"` + otherHash + `","recordedAt":"2026-01-02T03:04:05Z"`
	cases := []struct {
		name string
		raw  string
		want AuthorizationClass
	}{
		{"nil is absent", "", AuthorizationAbsent},
		{"whitespace is absent", "  \n", AuthorizationAbsent},
		{"held", `{"kind":"held",` + good + `}`, AuthorizationRecorded},
		{"lean", `{"kind":"lean",` + good + `}`, AuthorizationRecorded},
		{"trivial", `{"kind":"trivial",` + good + `}`, AuthorizationRecorded},
		{"lean-resumed", `{"kind":"lean-resumed",` + good + `}`, AuthorizationRecorded},
		{"lean-decided", `{"kind":"lean-decided",` + good + `}`, AuthorizationRecorded},
		{"human cli", `{"kind":"human","source":"cli",` + good + `}`, AuthorizationRecorded},
		{"human attended-launch", `{"kind":"human","source":"attended-launch",` + good + `}`, AuthorizationRecorded},
		{"not json", `not json`, AuthorizationUnreadable},
		{"json null", `null`, AuthorizationUnreadable},
		{"json array", `[]`, AuthorizationUnreadable},
		{"json string", `"held"`, AuthorizationUnreadable},
		{"empty object", `{}`, AuthorizationUnreadable},
		{"unknown kind", `{"kind":"bogus",` + good + `}`, AuthorizationUnreadable},
		{"empty kind", `{"kind":"",` + good + `}`, AuthorizationUnreadable},
		{"kind wrong type", `{"kind":7,` + good + `}`, AuthorizationUnreadable},
		{"kind wrong case", `{"kind":"HELD",` + good + `}`, AuthorizationUnreadable},
		{"missing planSha256", `{"kind":"held"}`, AuthorizationUnreadable},
		{"short planSha256", `{"kind":"held","planSha256":"abc123"}`, AuthorizationUnreadable},
		{"uppercase planSha256", `{"kind":"held","planSha256":"` + strings.ToUpper(otherHash) + `"}`, AuthorizationUnreadable},
		{"non-hex planSha256", `{"kind":"held","planSha256":"` + strings.Repeat("z", 64) + `"}`, AuthorizationUnreadable},
		{"65-char planSha256", `{"kind":"held","planSha256":"` + otherHash + `a"}`, AuthorizationUnreadable},
		{"human without source", `{"kind":"human",` + good + `}`, AuthorizationUnreadable},
		{"human unknown source", `{"kind":"human","source":"alice",` + good + `}`, AuthorizationUnreadable},
		{"autonomous kind with a source", `{"kind":"lean","source":"cli",` + good + `}`, AuthorizationUnreadable},
		{"bad recordedAt", `{"kind":"held","planSha256":"` + otherHash + `","recordedAt":"yesterday"}`, AuthorizationUnreadable},
		{"trailing data", `{"kind":"held",` + good + `} {}`, AuthorizationUnreadable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, got := ParseAuthorization(json.RawMessage(tc.raw))
			if got != tc.want {
				t.Errorf("ParseAuthorization(%q) class = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}

func TestParseAuthorization_ReturnsTheRecordedFields(t *testing.T) {
	raw := `{"kind":"human","planSha256":"` + otherHash + `","recordedAt":"2026-01-02T03:04:05Z","source":"attended-launch"}`
	a, class := ParseAuthorization(json.RawMessage(raw))
	if class != AuthorizationRecorded {
		t.Fatalf("class = %q, want recorded", class)
	}
	if a.Kind != AuthorizationHuman || a.PlanSHA256 != otherHash || a.Source != AuthorizationSourceAttendedLaunch {
		t.Errorf("record = %+v, want human/%s/attended-launch", a, otherHash)
	}
	if !a.RecordedAt.Equal(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)) {
		t.Errorf("RecordedAt = %v", a.RecordedAt)
	}
}

func TestPlanFingerprint_IsSHA256OfTheExactBytes(t *testing.T) {
	const abc = "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
	if got := PlanFingerprint("abc"); got != abc {
		t.Errorf("PlanFingerprint(abc) = %q, want %q", got, abc)
	}
	if PlanFingerprint("abc\n") == PlanFingerprint("abc") {
		t.Error("a one-byte change (trailing newline) must change the fingerprint")
	}
}

func TestUnattended_Matrix(t *testing.T) {
	// An empty value reads as unset (os.Getenv cannot tell them apart), so
	// "" stands for both.
	cases := []struct {
		name             string
		attended, origin string
		want             bool
	}{
		{"CENCI_ATTENDED unset", "", "", false},
		{"CENCI_ATTENDED=1", "1", "", false},
		{"CENCI_ATTENDED=0 and no origin marker", "0", "", true},
		{"CENCI_ATTENDED=0 with a pinned origin", "0", "pinned", true},
		{"CENCI_ATTENDED=0 with an unrecognized origin fails closed", "0", "anything-else", true},
		{"CENCI_ATTENDED=0 from host config is a human session", "0", AttendedOriginHostConfig, false},
		{"CENCI_ATTENDED=1 from host config", "1", AttendedOriginHostConfig, false},
		{"any other value is not an unattended pin", "false", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(AttendedEnv, tc.attended)
			t.Setenv(AttendedOriginEnv, tc.origin)
			if got := Unattended(); got != tc.want {
				t.Errorf("Unattended() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestOriginMarkerConstants(t *testing.T) {
	if AttendedEnv != "CENCI_ATTENDED" || AttendedOriginEnv != "CENCI_ATTENDED_ORIGIN" || AttendedOriginHostConfig != "host-config" {
		t.Errorf("marker names drifted: %q %q %q (the launcher mirrors them)", AttendedEnv, AttendedOriginEnv, AttendedOriginHostConfig)
	}
}

// An agent-writable record must round-trip through every other state writer
// untouched, even when this binary cannot parse it: a bad field becomes
// "plan authorization unreadable" for the reader, never a state-load failure
// and never a silent drop.
func TestStateAuthorization_RoundTripsRawBytesThroughOtherWriters(t *testing.T) {
	setAttended(t)
	f := newAuthFixture(t, StagePlanApproved)
	f.seedRaw(t, `{"kind":"from-the-future","planSha256":"x","extra":[1,2]}`)

	if _, err := Run(Opts{Stage: "execute", ID: "42", StateDir: f.stateDir}); err != nil {
		t.Fatalf("execute over an unparseable authorization record: %v (it must not fail state load)", err)
	}
	got := f.state(t).Authorization
	var compact bytes.Buffer
	if err := json.Compact(&compact, got); err != nil {
		t.Fatalf("authorization no longer valid JSON after another writer saved: %s", got)
	}
	if want := `{"kind":"from-the-future","planSha256":"x","extra":[1,2]}`; compact.String() != want {
		t.Errorf("authorization = %s, want it preserved verbatim as %s", compact.String(), want)
	}
	if _, class := ParseAuthorization(got); class != AuthorizationUnreadable {
		t.Errorf("class = %q, want unreadable", class)
	}
}

// -- flag misuse -----------------------------------------------------------------

func TestRun_AuthorizationMisuse_IsAUsageError(t *testing.T) {
	setAttended(t)
	cases := []struct {
		name  string
		stage string
		appr  bool
		kind  string
	}{
		{"human is never writable through plan", "plan", false, "human"},
		{"unknown kind", "plan", false, "bogus"},
		{"together with --approve", "plan", true, "lean"},
		{"on a non-plan stage", "execute", false, "lean"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newAuthFixture(t, StageWaitingForPlanApproval)
			before := f.stateBytes(t)
			out, err := f.run(tc.stage, tc.appr, tc.kind)
			assertRefused(t, f, before, out, err, ErrAuthorizationUsage, StageWaitingForPlanApproval)
		})
	}
}

// -- bare `plan --authorization K` -----------------------------------------------

func TestRun_PersistHeld_RecordsHeldBoundToThePlanHash(t *testing.T) {
	setUnattended(t)
	f := newAuthFixture(t, StagePrepared)
	before := time.Now().Add(-time.Minute)

	out, err := f.run("plan", false, "held")
	if err != nil {
		t.Fatalf("plan --authorization held: %v", err)
	}
	if out.State != string(StageWaitingForPlanApproval) {
		t.Errorf("State = %q, want waiting_for_plan_approval", out.State)
	}
	rec, class := f.record(t)
	if class != AuthorizationRecorded || rec.Kind != AuthorizationHeld {
		t.Fatalf("record = %+v (%s), want held", rec, class)
	}
	if rec.PlanSHA256 != f.hash(t) {
		t.Errorf("planSha256 = %q, want the plan file's SHA-256 %q", rec.PlanSHA256, f.hash(t))
	}
	if rec.Source != "" {
		t.Errorf("source = %q, want none for a non-human kind (no usernames, no source)", rec.Source)
	}
	if rec.RecordedAt.Before(before) {
		t.Errorf("recordedAt = %v, want a current timestamp", rec.RecordedAt)
	}
}

func TestRun_PersistAutonomousKinds_RecordEachKindUnattended(t *testing.T) {
	setUnattended(t)
	for _, kind := range autonomousKinds {
		t.Run(string(kind), func(t *testing.T) {
			f := newAuthFixture(t, StagePrepared)
			if _, err := f.run("plan", false, string(kind)); err != nil {
				t.Fatalf("plan --authorization %s: %v", kind, err)
			}
			rec, class := f.record(t)
			if class != AuthorizationRecorded || rec.Kind != kind || rec.PlanSHA256 != f.hash(t) || rec.Source != "" {
				t.Errorf("record = %+v (%s), want %s bound to the plan hash with no source", rec, class, kind)
			}
		})
	}
}

func TestRun_PersistHeld_ReplacesAnEarlierHumanApproval(t *testing.T) {
	setUnattended(t)
	f := newAuthFixture(t, StageWaitingForPlanApproval)
	f.seedSameHash(t, AuthorizationHuman)

	if _, err := f.run("plan", false, "held"); err != nil {
		t.Fatalf("re-plan with held: %v", err)
	}
	if rec, _ := f.record(t); rec.Kind != AuthorizationHeld {
		t.Errorf("kind = %q, want held: a re-plan replaces the earlier human approval", rec.Kind)
	}
}

func TestRun_PersistWritesEvenWhenTheStageStepIsANoOp(t *testing.T) {
	setUnattended(t)
	f := newAuthFixture(t, StageExecuted)
	f.seedSameHash(t, AuthorizationHuman)

	out, err := f.run("plan", false, "held")
	if err != nil {
		t.Fatalf("plan --authorization held over executed: %v", err)
	}
	if out.State != string(StageExecuted) {
		t.Errorf("State = %q, want the unchanged executed (a no-op never rewinds)", out.State)
	}
	if rec, _ := f.record(t); rec.Kind != AuthorizationHeld {
		t.Errorf("kind = %q, want held written even though the stage step was a no-op", rec.Kind)
	}
}

func TestRun_PersistAutonomous_UnattendedOverHeldSameHash_IsRefused(t *testing.T) {
	setUnattended(t)
	for _, kind := range autonomousKinds {
		t.Run(string(kind), func(t *testing.T) {
			f := newAuthFixture(t, StageWaitingForPlanApproval)
			f.seedSameHash(t, AuthorizationHeld)
			before := f.stateBytes(t)

			out, err := f.run("plan", false, string(kind))
			assertRefused(t, f, before, out, err, ErrAuthorizationDowngrade, StageWaitingForPlanApproval)
			if rec, _ := f.record(t); rec.Kind != AuthorizationHeld {
				t.Errorf("kind = %q, want the record left held", rec.Kind)
			}
		})
	}
}

func TestRun_PersistAutonomous_UnattendedOverHeldChangedHash_IsAdmitted(t *testing.T) {
	setUnattended(t)
	f := newAuthFixture(t, StageWaitingForPlanApproval)
	f.seedRecord(t, AuthorizationHeld, otherHash, "")

	if _, err := f.run("plan", false, "lean"); err != nil {
		t.Fatalf("lean over a held record for different plan bytes: %v (the defence is content-exact)", err)
	}
	rec, _ := f.record(t)
	if rec.Kind != AuthorizationLean || rec.PlanSHA256 != f.hash(t) {
		t.Errorf("record = %+v, want lean bound to the current plan", rec)
	}
}

func TestRun_PersistAutonomous_AttendedOverHeldSameHash_IsAdmitted(t *testing.T) {
	setAttended(t)
	f := newAuthFixture(t, StageWaitingForPlanApproval)
	f.seedSameHash(t, AuthorizationHeld)

	if _, err := f.run("plan", false, "lean"); err != nil {
		t.Fatalf("attended lean over held: %v", err)
	}
	if rec, _ := f.record(t); rec.Kind != AuthorizationLean {
		t.Errorf("kind = %q, want lean", rec.Kind)
	}
}

func TestRun_PersistAutonomous_OverHumanSameHash_LeavesTheRecordAndWarns(t *testing.T) {
	setUnattended(t)
	f := newAuthFixture(t, StageWaitingForPlanApproval)
	f.seedSameHash(t, AuthorizationHuman)
	before := f.state(t).Authorization

	out, err := f.run("plan", false, "lean")
	if err != nil {
		t.Fatalf("lean over a matching human record: %v", err)
	}
	if !bytes.Equal(before, f.state(t).Authorization) {
		t.Errorf("authorization = %s, want the human record unchanged (%s)", f.state(t).Authorization, before)
	}
	named := 0
	for _, w := range out.Warnings {
		if strings.Contains(w, "approved by a human") {
			named++
		}
	}
	if named != 1 {
		t.Errorf("Warnings = %v, want exactly one warning naming the kept human approval", out.Warnings)
	}
}

func TestRun_PersistAutonomous_OverHumanChangedHash_Writes(t *testing.T) {
	setUnattended(t)
	f := newAuthFixture(t, StageWaitingForPlanApproval)
	f.seedRecord(t, AuthorizationHuman, otherHash, AuthorizationSourceCLI)

	if _, err := f.run("plan", false, "trivial"); err != nil {
		t.Fatalf("trivial over a stale human record: %v", err)
	}
	rec, _ := f.record(t)
	if rec.Kind != AuthorizationTrivial || rec.PlanSHA256 != f.hash(t) {
		t.Errorf("record = %+v, want trivial bound to the current plan", rec)
	}
}

// An unreadable record may be a held one this binary cannot parse, so an
// unattended autonomous persist must not overwrite it (default-deny): the
// no-downgrade defence would otherwise be bypassed by corrupting the record.
func TestRun_PersistAutonomous_UnattendedOverUnreadableRecord_IsRefused(t *testing.T) {
	setUnattended(t)
	for _, kind := range autonomousKinds {
		t.Run(string(kind), func(t *testing.T) {
			f := newAuthFixture(t, StageWaitingForPlanApproval)
			f.seedRaw(t, `{"kind":"held","planSha256":"bogus"}`)
			before := f.stateBytes(t)

			out, err := f.run("plan", false, string(kind))
			assertRefused(t, f, before, out, err, ErrAuthorizationUnreadable, StageWaitingForPlanApproval)
			if !strings.Contains(err.Error(), "cenci dispatch approve 42") {
				t.Errorf("error = %q, want it to name the release command", err)
			}
		})
	}
}

func TestRun_PersistHeld_UnattendedOverUnreadableRecord_Overwrites(t *testing.T) {
	setUnattended(t)
	f := newAuthFixture(t, StageWaitingForPlanApproval)
	f.seedRaw(t, `"garbage"`)

	if _, err := f.run("plan", false, "held"); err != nil {
		t.Fatalf("held over an unreadable record: %v", err)
	}
	if rec, class := f.record(t); class != AuthorizationRecorded || rec.Kind != AuthorizationHeld || rec.PlanSHA256 != f.hash(t) {
		t.Errorf("record = %+v (%s), want held bound to the current plan", rec, class)
	}
}

func TestRun_PersistAutonomous_AttendedOverUnreadableRecord_OverwritesAndWarns(t *testing.T) {
	cases := []struct {
		name, raw, wantKind string
	}{
		{"plain raw kind", `{"kind":"held","planSha256":"bogus"}`, "held"},
		{"not an object", `"garbage"`, "unreadable"},
		{"hostile kind text is never echoed", "{\"kind\":\"a\\u001b[31mb\"}", "unreadable"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setAttended(t)
			f := newAuthFixture(t, StageWaitingForPlanApproval)
			f.seedRaw(t, tc.raw)

			out, err := f.run("plan", false, "lean")
			if err != nil {
				t.Fatalf("attended lean over an unreadable record: %v", err)
			}
			if rec, class := f.record(t); class != AuthorizationRecorded || rec.Kind != AuthorizationLean || rec.PlanSHA256 != f.hash(t) {
				t.Errorf("record = %+v (%s), want lean bound to the current plan", rec, class)
			}
			named := 0
			for _, w := range out.Warnings {
				if strings.Contains(w, "unreadable plan authorization") && strings.Contains(w, "(kind "+tc.wantKind+")") {
					named++
				}
				if strings.Contains(w, "\x1b") {
					t.Errorf("warning %q echoes hostile record text", w)
				}
			}
			if named != 1 {
				t.Errorf("Warnings = %v, want exactly one warning naming the replaced kind %q", out.Warnings, tc.wantKind)
			}
		})
	}
}

func TestRun_PersistAutonomous_OverAbsentRecord_WritesWithoutWarning(t *testing.T) {
	setUnattended(t)
	f := newAuthFixture(t, StageWaitingForPlanApproval)

	out, err := f.run("plan", false, "lean")
	if err != nil {
		t.Fatalf("lean with no record: %v", err)
	}
	if rec, class := f.record(t); class != AuthorizationRecorded || rec.Kind != AuthorizationLean {
		t.Errorf("record = %+v (%s), want lean", rec, class)
	}
	for _, w := range out.Warnings {
		if strings.Contains(w, "unreadable") {
			t.Errorf("Warnings = %v, want no replaced-record warning over an absent record", out.Warnings)
		}
	}
}

func TestRun_PlanWithoutAuthorizationFlag_LeavesTheRecordUnchanged(t *testing.T) {
	setUnattended(t)
	f := newAuthFixture(t, StageWaitingForPlanApproval)
	f.seedSameHash(t, AuthorizationHuman)
	before := f.state(t).Authorization

	if _, err := f.run("plan", false, ""); err != nil {
		t.Fatalf("bare plan, no flag: %v", err)
	}
	if !bytes.Equal(before, f.state(t).Authorization) {
		t.Errorf("authorization = %s, want it unchanged when the flag is omitted", f.state(t).Authorization)
	}
}

func TestRun_Persist_PlanFileCannotBeResolved_FailsWithoutWriting(t *testing.T) {
	setUnattended(t)
	f := newAuthFixture(t, StagePrepared)
	if err := os.Remove(f.planPath); err != nil {
		t.Fatalf("remove plan: %v", err)
	}
	before := f.stateBytes(t)

	out, err := f.run("plan", false, "held")
	assertRefused(t, f, before, out, err, ErrPlanNotFound, StagePrepared)
}

// -- `plan --approve`, unattended, a real transition -----------------------------

func TestRun_ApproveUnattended_FromWaiting_Matrix(t *testing.T) {
	setUnattended(t)
	type seed func(t *testing.T, f *authFixture)
	cases := []struct {
		name     string
		seed     seed
		sentinel error // nil: the approve must succeed
	}{
		{"held", func(t *testing.T, f *authFixture) { f.seedSameHash(t, AuthorizationHeld) }, ErrAuthorizationHeld},
		{"human, plan changed since approval", func(t *testing.T, f *authFixture) {
			f.seedRecord(t, AuthorizationHuman, otherHash, AuthorizationSourceCLI)
		}, ErrAuthorizationChanged},
		{"no record (legacy)", func(t *testing.T, f *authFixture) {}, ErrAuthorizationUnrecorded},
		{"unreadable record", func(t *testing.T, f *authFixture) { f.seedRaw(t, `{"kind":"bogus"}`) }, ErrAuthorizationUnreadable},
		{"human, same plan", func(t *testing.T, f *authFixture) { f.seedSameHash(t, AuthorizationHuman) }, nil},
	}
	for _, k := range autonomousKinds {
		cases = append(cases,
			struct {
				name     string
				seed     seed
				sentinel error
			}{string(k) + ", same plan", func(t *testing.T, f *authFixture) { f.seedSameHash(t, k) }, nil},
			struct {
				name     string
				seed     seed
				sentinel error
			}{string(k) + ", plan changed (a mismatch on any non-held kind is refused)", func(t *testing.T, f *authFixture) {
				f.seedRecord(t, k, otherHash, "")
			}, ErrAuthorizationChanged},
		)
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newAuthFixture(t, StageWaitingForPlanApproval)
			tc.seed(t, f)
			before := f.stateBytes(t)

			out, err := f.approve()
			if tc.sentinel != nil {
				assertRefused(t, f, before, out, err, tc.sentinel, StageWaitingForPlanApproval)
				if got := f.state(t).Stage; got != StageWaitingForPlanApproval {
					t.Errorf("persisted stage = %q, want it left at waiting_for_plan_approval", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("plan --approve: %v", err)
			}
			if out.State != string(StagePlanApproved) || len(out.Errors) != 0 {
				t.Errorf("Output = state %q errors %v, want plan_approved with no errors", out.State, out.Errors)
			}
		})
	}
}

func TestRun_ApproveUnattended_RefusalCarriesTheReleaseCommand(t *testing.T) {
	setUnattended(t)
	f := newAuthFixture(t, StageWaitingForPlanApproval)
	f.seedSameHash(t, AuthorizationHeld)
	out, _ := f.approve()
	if len(out.Errors) != 1 || !strings.Contains(out.Errors[0], "cenci dispatch approve 42") {
		t.Errorf("Errors = %v, want one entry naming `cenci dispatch approve 42`", out.Errors)
	}
}

func TestRun_ApproveUnattended_PlanFileCannotBeResolved_IsRefused(t *testing.T) {
	setUnattended(t)
	f := newAuthFixture(t, StageWaitingForPlanApproval)
	f.seedSameHash(t, AuthorizationLean)
	if err := os.Remove(f.planPath); err != nil {
		t.Fatalf("remove plan: %v", err)
	}
	before := f.stateBytes(t)

	out, err := f.approve()
	assertRefused(t, f, before, out, err, ErrPlanNotFound, StageWaitingForPlanApproval)
}

// -- `plan --approve`, unattended, a no-op at or past plan_approved ---------------

func TestRun_ApproveUnattended_NoOp_Matrix(t *testing.T) {
	setUnattended(t)
	for _, stage := range []Stage{StagePlanApproved, StageExecuted, StageReviewed} {
		t.Run(string(stage), func(t *testing.T) {
			t.Run("non-held record is admitted without a hash re-check", func(t *testing.T) {
				f := newAuthFixture(t, stage)
				f.seedRecord(t, AuthorizationLean, otherHash, "")
				out, err := f.approve()
				if err != nil {
					t.Fatalf("plan --approve no-op: %v", err)
				}
				if out.State != string(stage) {
					t.Errorf("State = %q, want unchanged %q", out.State, stage)
				}
			})
			t.Run("no record is admitted (legacy in-flight execution)", func(t *testing.T) {
				f := newAuthFixture(t, stage)
				if _, err := f.approve(); err != nil {
					t.Fatalf("plan --approve no-op with no record: %v", err)
				}
				if _, class := f.record(t); class != AuthorizationAbsent {
					t.Errorf("class = %q, want absent: a no-op must not invent a record", class)
				}
			})
			t.Run("held record is refused", func(t *testing.T) {
				f := newAuthFixture(t, stage)
				f.seedSameHash(t, AuthorizationHeld)
				before := f.stateBytes(t)
				out, err := f.approve()
				assertRefused(t, f, before, out, err, ErrAuthorizationHeld, stage)
			})
			t.Run("unreadable record is refused", func(t *testing.T) {
				f := newAuthFixture(t, stage)
				f.seedRaw(t, `{"kind":"bogus"}`)
				before := f.stateBytes(t)
				out, err := f.approve()
				assertRefused(t, f, before, out, err, ErrAuthorizationUnreadable, stage)
			})
		})
	}
}

// -- `plan --approve`, attended --------------------------------------------------

func TestRun_ApproveAttended_RecordsHumanForTheCurrentPlan(t *testing.T) {
	setAttended(t)
	cases := []struct {
		name string
		seed func(t *testing.T, f *authFixture)
	}{
		{"held record", func(t *testing.T, f *authFixture) { f.seedSameHash(t, AuthorizationHeld) }},
		{"absent record", func(t *testing.T, f *authFixture) {}},
		{"human record for other plan bytes", func(t *testing.T, f *authFixture) {
			f.seedRecord(t, AuthorizationHuman, otherHash, AuthorizationSourceCLI)
		}},
		{"autonomous record for other plan bytes", func(t *testing.T, f *authFixture) {
			f.seedRecord(t, AuthorizationLean, otherHash, "")
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newAuthFixture(t, StageWaitingForPlanApproval)
			tc.seed(t, f)

			out, err := f.approve()
			if err != nil {
				t.Fatalf("attended plan --approve: %v", err)
			}
			if out.State != string(StagePlanApproved) {
				t.Errorf("State = %q, want plan_approved", out.State)
			}
			rec, class := f.record(t)
			if class != AuthorizationRecorded || rec.Kind != AuthorizationHuman || rec.Source != AuthorizationSourceAttendedLaunch || rec.PlanSHA256 != f.hash(t) {
				t.Errorf("record = %+v (%s), want human/attended-launch bound to the current plan hash", rec, class)
			}
		})
	}
}

func TestRun_ApproveAttended_MatchingAutonomousRecord_IsLeftUnchanged(t *testing.T) {
	setAttended(t)
	for _, kind := range autonomousKinds {
		t.Run(string(kind), func(t *testing.T) {
			f := newAuthFixture(t, StageWaitingForPlanApproval)
			f.seedSameHash(t, kind)
			before := f.state(t).Authorization

			if _, err := f.approve(); err != nil {
				t.Fatalf("attended plan --approve: %v", err)
			}
			if !bytes.Equal(before, f.state(t).Authorization) {
				t.Errorf("authorization = %s, want the matching %s record unchanged", f.state(t).Authorization, kind)
			}
		})
	}
}

func TestRun_ApproveAttended_MatchingHumanRecord_IsLeftUnchanged(t *testing.T) {
	setAttended(t)
	f := newAuthFixture(t, StageWaitingForPlanApproval)
	f.seedSameHash(t, AuthorizationHuman)
	before := f.state(t).Authorization
	if _, err := f.approve(); err != nil {
		t.Fatalf("attended plan --approve: %v", err)
	}
	if !bytes.Equal(before, f.state(t).Authorization) {
		t.Errorf("authorization = %s, want the cli-approved record kept as is", f.state(t).Authorization)
	}
}

func TestRun_ApproveAttended_UnreadableRecord_IsRefusedPointingAtDispatchApprove(t *testing.T) {
	setAttended(t)
	f := newAuthFixture(t, StageWaitingForPlanApproval)
	f.seedRaw(t, `{"kind":"bogus"}`)
	before := f.stateBytes(t)

	out, err := f.approve()
	assertRefused(t, f, before, out, err, ErrAuthorizationUnreadable, StageWaitingForPlanApproval)
	if !strings.Contains(out.Errors[0], "cenci dispatch approve 42") {
		t.Errorf("Errors = %v, want it to point at `cenci dispatch approve 42`", out.Errors)
	}
}

func TestRun_ApproveAttended_NoOp(t *testing.T) {
	setAttended(t)
	t.Run("held record is released to human for the current plan", func(t *testing.T) {
		f := newAuthFixture(t, StageExecuted)
		f.seedSameHash(t, AuthorizationHeld)
		if _, err := f.approve(); err != nil {
			t.Fatalf("attended no-op approve: %v", err)
		}
		rec, _ := f.record(t)
		if rec.Kind != AuthorizationHuman || rec.Source != AuthorizationSourceAttendedLaunch || rec.PlanSHA256 != f.hash(t) {
			t.Errorf("record = %+v, want human/attended-launch for the current plan", rec)
		}
	})
	t.Run("absent record stays absent", func(t *testing.T) {
		f := newAuthFixture(t, StageExecuted)
		if _, err := f.approve(); err != nil {
			t.Fatalf("attended no-op approve: %v", err)
		}
		if _, class := f.record(t); class != AuthorizationAbsent {
			t.Errorf("class = %q, want absent", class)
		}
	})
	t.Run("unreadable record is refused", func(t *testing.T) {
		f := newAuthFixture(t, StageExecuted)
		f.seedRaw(t, `[]`)
		before := f.stateBytes(t)
		out, err := f.approve()
		assertRefused(t, f, before, out, err, ErrAuthorizationUnreadable, StageExecuted)
	})
}

func TestRun_ApproveFromHostConfigOrigin_CountsAsAttended(t *testing.T) {
	t.Setenv(AttendedEnv, "0")
	t.Setenv(AttendedOriginEnv, AttendedOriginHostConfig)
	f := newAuthFixture(t, StageWaitingForPlanApproval)
	f.seedSameHash(t, AuthorizationHeld)

	if _, err := f.approve(); err != nil {
		t.Fatalf("plan --approve from a host-config sandbox session: %v", err)
	}
	if rec, _ := f.record(t); rec.Kind != AuthorizationHuman {
		t.Errorf("kind = %q, want human (a human `cenci open` session)", rec.Kind)
	}
}

func TestRun_ApproveFromPinnedUnattended_IsNotAttended(t *testing.T) {
	t.Setenv(AttendedEnv, "0")
	t.Setenv(AttendedOriginEnv, "pinned")
	f := newAuthFixture(t, StageWaitingForPlanApproval)
	f.seedSameHash(t, AuthorizationHeld)
	before := f.stateBytes(t)

	out, err := f.approve()
	assertRefused(t, f, before, out, err, ErrAuthorizationHeld, StageWaitingForPlanApproval)
}

// -- ApproveHeldPlan (`cenci dispatch approve`) -----------------------------------

func approveOpts(f *authFixture) ApproveOpts {
	return ApproveOpts{ID: "42", RepoRoot: f.repoRoot, StateDir: f.stateDir}
}

func TestApproveHeldPlan_ReleasesAHeldPlan(t *testing.T) {
	setAttended(t)
	f := newAuthFixture(t, StageWaitingForPlanApproval)
	f.seedSameHash(t, AuthorizationHeld)

	res, err := ApproveHeldPlan(approveOpts(f))
	if err != nil {
		t.Fatalf("ApproveHeldPlan: %v", err)
	}
	if !res.Released || res.PlanPath != f.planPath || res.Fingerprint != f.hash(t) {
		t.Errorf("result = %+v, want released for %s with fingerprint %s", res, f.planPath, f.hash(t))
	}
	rec, class := f.record(t)
	if class != AuthorizationRecorded || rec.Kind != AuthorizationHuman || rec.Source != AuthorizationSourceCLI || rec.PlanSHA256 != f.hash(t) {
		t.Errorf("record = %+v (%s), want human/cli bound to the plan hash", rec, class)
	}
	if got := f.state(t).Stage; got != StageWaitingForPlanApproval {
		t.Errorf("stage = %q, want unchanged: approve records authorization, it does not advance the pipeline", got)
	}
	if res.ReplacedKind != string(AuthorizationHeld) {
		t.Errorf("ReplacedKind = %q, want held", res.ReplacedKind)
	}
}

func TestApproveHeldPlan_ThenUnattendedApproveAdvances(t *testing.T) {
	setAttended(t)
	f := newAuthFixture(t, StageWaitingForPlanApproval)
	f.seedSameHash(t, AuthorizationHeld)
	if _, err := ApproveHeldPlan(approveOpts(f)); err != nil {
		t.Fatalf("ApproveHeldPlan: %v", err)
	}

	setUnattended(t)
	out, err := f.approve()
	if err != nil || out.State != string(StagePlanApproved) {
		t.Fatalf("unattended plan --approve after release: state %q err %v, want plan_approved", out.State, err)
	}
}

func TestApproveHeldPlan_ChangedPlanNeedsFreshApproval(t *testing.T) {
	setAttended(t)
	f := newAuthFixture(t, StageWaitingForPlanApproval)
	f.seedSameHash(t, AuthorizationHeld)
	if _, err := ApproveHeldPlan(approveOpts(f)); err != nil {
		t.Fatalf("first approve: %v", err)
	}
	f.changePlan(t)

	setUnattended(t)
	before := f.stateBytes(t)
	out, err := f.approve()
	assertRefused(t, f, before, out, err, ErrAuthorizationChanged, StageWaitingForPlanApproval)

	setAttended(t)
	res, err := ApproveHeldPlan(approveOpts(f))
	if err != nil || !res.Released {
		t.Fatalf("second approve: released=%v err=%v, want the changed plan released again", res.Released, err)
	}
	if res.ReplacedKind != string(AuthorizationHuman) {
		t.Errorf("ReplacedKind = %q, want human (the stale approval)", res.ReplacedKind)
	}
	if rec, _ := f.record(t); rec.PlanSHA256 != f.hash(t) {
		t.Errorf("planSha256 = %q, want the new plan hash %q", rec.PlanSHA256, f.hash(t))
	}
}

func TestApproveHeldPlan_NoRecord_WritesHumanAndReplacesNothing(t *testing.T) {
	setAttended(t)
	f := newAuthFixture(t, StageWaitingForPlanApproval)

	res, err := ApproveHeldPlan(approveOpts(f))
	if err != nil || !res.Released {
		t.Fatalf("approve with no record: released=%v err=%v", res.Released, err)
	}
	if res.ReplacedKind != "" {
		t.Errorf("ReplacedKind = %q, want none", res.ReplacedKind)
	}
	if rec, _ := f.record(t); rec.Kind != AuthorizationHuman {
		t.Errorf("kind = %q, want human", rec.Kind)
	}
}

func TestApproveHeldPlan_NoStateFile_CreatesOneAtTheCanonicalPath(t *testing.T) {
	setAttended(t)
	repoRoot := t.TempDir()
	mustWriteValidAdoptPlan(t, repoRoot, "42", "add-thing")

	res, err := ApproveHeldPlan(ApproveOpts{ID: "42", RepoRoot: repoRoot})
	if err != nil || !res.Released {
		t.Fatalf("approve with no state file: released=%v err=%v", res.Released, err)
	}
	s, lerr := loadState(filepath.Join(repoRoot, ".cenci", "pipeline", "42.json"))
	if lerr != nil {
		t.Fatalf("state file: %v", lerr)
	}
	if rec, class := ParseAuthorization(s.Authorization); class != AuthorizationRecorded || rec.Kind != AuthorizationHuman {
		t.Errorf("record = %+v (%s), want human", rec, class)
	}
	if s.ID != "42" || s.SchemaVersion != CurrentSchemaVersion {
		t.Errorf("state = %+v, want id 42 at the current schema version", s)
	}
}

func TestApproveHeldPlan_UnreadableRecord_IsOverwrittenAndTheRawKindReported(t *testing.T) {
	setAttended(t)
	cases := []struct {
		name, raw, wantKind string
	}{
		{"unknown kind", `{"kind":"bogus"}`, "bogus"},
		{"malformed hash", `{"kind":"lean","planSha256":"nothex"}`, "lean"},
		{"not an object", `"x"`, "unreadable"},
		{"hostile kind text is never echoed", "{\"kind\":\"a\\u001b[31mb\"}", "unreadable"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newAuthFixture(t, StageWaitingForPlanApproval)
			f.seedRaw(t, tc.raw)

			res, err := ApproveHeldPlan(approveOpts(f))
			if err != nil || !res.Released {
				t.Fatalf("approve: released=%v err=%v", res.Released, err)
			}
			if res.ReplacedKind != tc.wantKind {
				t.Errorf("ReplacedKind = %q, want %q", res.ReplacedKind, tc.wantKind)
			}
			if rec, class := f.record(t); class != AuthorizationRecorded || rec.Kind != AuthorizationHuman {
				t.Errorf("record = %+v (%s), want it overwritten with human", rec, class)
			}
		})
	}
}

func TestApproveHeldPlan_NotHeld_IsANoOpAndLeavesTheFileByteIdentical(t *testing.T) {
	setAttended(t)
	for _, kind := range append([]AuthorizationKind{AuthorizationHuman}, autonomousKinds...) {
		t.Run(string(kind), func(t *testing.T) {
			f := newAuthFixture(t, StageWaitingForPlanApproval)
			f.seedSameHash(t, kind)
			before := f.stateBytes(t)

			res, err := ApproveHeldPlan(approveOpts(f))
			if err != nil {
				t.Fatalf("approve: %v", err)
			}
			if res.Released || res.Kind != kind {
				t.Errorf("result = %+v, want not released with kind %s", res, kind)
			}
			if after := f.stateBytes(t); !bytes.Equal(before, after) {
				t.Errorf("state file changed:\nbefore: %s\nafter:  %s", before, after)
			}
		})
	}
}

func TestApproveHeldPlan_UnattendedIsRefusedWithNoWriteAtAll(t *testing.T) {
	setUnattended(t)
	t.Run("existing state is byte-identical", func(t *testing.T) {
		f := newAuthFixture(t, StageWaitingForPlanApproval)
		f.seedSameHash(t, AuthorizationHeld)
		before := f.stateBytes(t)

		_, err := ApproveHeldPlan(approveOpts(f))
		if !errors.Is(err, ErrAttendedRequired) {
			t.Fatalf("error = %v, want errors.Is(_, ErrAttendedRequired)", err)
		}
		if after := f.stateBytes(t); !bytes.Equal(before, after) {
			t.Errorf("state file changed on a refusal:\nbefore: %s\nafter:  %s", before, after)
		}
	})
	t.Run("nothing is created for a ticket with no state", func(t *testing.T) {
		repoRoot := t.TempDir()
		mustWriteValidAdoptPlan(t, repoRoot, "42", "add-thing")
		if _, err := ApproveHeldPlan(ApproveOpts{ID: "42", RepoRoot: repoRoot}); !errors.Is(err, ErrAttendedRequired) {
			t.Fatalf("error = %v, want ErrAttendedRequired", err)
		}
		if _, err := os.Stat(filepath.Join(repoRoot, ".cenci")); !errors.Is(err, os.ErrNotExist) {
			t.Errorf(".cenci stat err = %v, want not-exist: a refused approve writes nothing", err)
		}
	})
	t.Run("a pinned-unattended origin marker is still unattended", func(t *testing.T) {
		t.Setenv(AttendedOriginEnv, "pinned")
		f := newAuthFixture(t, StageWaitingForPlanApproval)
		if _, err := ApproveHeldPlan(approveOpts(f)); !errors.Is(err, ErrAttendedRequired) {
			t.Fatalf("error = %v, want ErrAttendedRequired", err)
		}
	})
}

func TestApproveHeldPlan_PlanFileProblems_WriteNothing(t *testing.T) {
	setAttended(t)
	t.Run("no plan file", func(t *testing.T) {
		f := newAuthFixture(t, StageWaitingForPlanApproval)
		if err := os.Remove(f.planPath); err != nil {
			t.Fatalf("remove plan: %v", err)
		}
		before := f.stateBytes(t)
		_, err := ApproveHeldPlan(approveOpts(f))
		if !errors.Is(err, ErrPlanNotFound) {
			t.Fatalf("error = %v, want errors.Is(_, ErrPlanNotFound)", err)
		}
		if err.Error() != "no plan file for #42" {
			t.Errorf("message = %q, want exactly %q", err.Error(), "no plan file for #42")
		}
		if !bytes.Equal(before, f.stateBytes(t)) {
			t.Error("state file changed")
		}
	})
	t.Run("two plan files name both paths", func(t *testing.T) {
		f := newAuthFixture(t, StageWaitingForPlanApproval)
		second := mustWriteValidAdoptPlan(t, f.repoRoot, "42", "add-thing-v2")
		before := f.stateBytes(t)
		_, err := ApproveHeldPlan(approveOpts(f))
		if !errors.Is(err, ErrMultiplePlans) {
			t.Fatalf("error = %v, want errors.Is(_, ErrMultiplePlans)", err)
		}
		if !strings.Contains(err.Error(), f.planPath) || !strings.Contains(err.Error(), second) {
			t.Errorf("message = %q, want both plan paths named", err.Error())
		}
		if !bytes.Equal(before, f.stateBytes(t)) {
			t.Error("state file changed")
		}
	})
	t.Run("awaiting-input draft", func(t *testing.T) {
		f := newAuthFixture(t, StageWaitingForInput)
		fields := defaultPlanFields("42", "add-thing", "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef", "2026-07-20T20:00:00Z")
		fields["status"] = "awaiting-input"
		writePlanFile(t, f.repoRoot, "42", "add-thing", fields, validPlanBody)
		before := f.stateBytes(t)
		_, err := ApproveHeldPlan(approveOpts(f))
		if !errors.Is(err, ErrPlanAwaitingInput) {
			t.Fatalf("error = %v, want errors.Is(_, ErrPlanAwaitingInput)", err)
		}
		if !strings.Contains(err.Error(), "answer the escalation on the ticket") {
			t.Errorf("message = %q, want it to say to answer the escalation on the ticket", err.Error())
		}
		if !bytes.Equal(before, f.stateBytes(t)) {
			t.Error("state file changed")
		}
	})
}

func TestApproveHeldPlan_StateAccessFailures_WriteNothing(t *testing.T) {
	setAttended(t)
	t.Run("unreadable state file", func(t *testing.T) {
		f := newAuthFixture(t, StageWaitingForPlanApproval)
		if err := os.WriteFile(f.statePath(), []byte("{not json"), 0o644); err != nil {
			t.Fatalf("corrupt state: %v", err)
		}
		before := f.stateBytes(t)
		if _, err := ApproveHeldPlan(approveOpts(f)); err == nil {
			t.Fatal("want an error for an unreadable state file, got nil")
		}
		if !bytes.Equal(before, f.stateBytes(t)) {
			t.Error("state file changed")
		}
	})
	t.Run("symlinked .cenci/pipeline level", func(t *testing.T) {
		repoRoot := t.TempDir()
		mustWriteValidAdoptPlan(t, repoRoot, "42", "add-thing")
		outside := t.TempDir()
		if err := os.MkdirAll(filepath.Join(repoRoot, ".cenci"), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.Symlink(outside, filepath.Join(repoRoot, ".cenci", "pipeline")); err != nil {
			t.Fatalf("symlink: %v", err)
		}
		if _, err := ApproveHeldPlan(ApproveOpts{ID: "42", RepoRoot: repoRoot}); err == nil {
			t.Fatal("want an error for a symlinked .cenci/pipeline, got nil")
		}
		if entries, _ := os.ReadDir(outside); len(entries) != 0 {
			t.Errorf("the symlink target now holds %d entries, want none written", len(entries))
		}
	})
}

func TestApproveHeldPlan_InvalidID_IsRejected(t *testing.T) {
	setAttended(t)
	if _, err := ApproveHeldPlan(ApproveOpts{ID: "../x", RepoRoot: t.TempDir()}); err == nil {
		t.Fatal("want an error for a non-numeric id, got nil")
	}
}
