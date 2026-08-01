package planfile

// Tests for the shared plan-inventory contract (#884): one authoritative
// directory enumeration + identity-based ticket selection, replacing the
// error-suppressing `filepath.Glob` discovery duplicated across
// internal/dispatch and internal/pipeline. Package-boundary ("white box")
// tests, mirroring frontmatter_test.go's own convention.
//
// This test file is the source of truth for inventory.go's shape (not yet
// written):
//
//	type DirState string
//	const (
//	    DirAbsent     DirState = "absent"
//	    DirEmpty      DirState = "empty"
//	    DirComplete   DirState = "complete"
//	    DirUnreadable DirState = "unreadable"
//	    DirPartial    DirState = "partial"
//	)
//
//	type Health string
//	const (
//	    HealthOK             Health = "ok"
//	    HealthUnreadable     Health = "unreadable"
//	    HealthMalformed      Health = "malformed"
//	    HealthIDMismatch     Health = "id_mismatch"
//	    HealthUnattributable Health = "unattributable"
//	    HealthPathAnomaly    Health = "path_anomaly"
//	)
//
//	type Entry struct {
//	    Path          string            // full path
//	    Base          string            // filepath.Base(Path)
//	    FilenameID    int               // leading ^\d+ run in Base; 0 = no match
//	    FrontMatterID int               // parsed ticketId front-matter field; 0 = absent/unparseable
//	    TicketID      int               // == FilenameID iff Health == HealthOK; 0 otherwise
//	    Health        Health
//	    Reason        string            // bounded: path + error text only, never plan/ticket body content
//	    FrontMatter   map[string]string // nil unless the file was read and its front matter parsed
//	}
//
//	type Inventory struct {
//	    Dir     string   // the scanned .plans directory (repoRoot/.plans)
//	    State   DirState
//	    Err     error    // set for DirUnreadable/DirPartial
//	    Entries []Entry  // sorted by Path; empty for absent/empty/unreadable
//	}
//
//	func (inv Inventory) Complete() bool // true for absent/empty/complete; false for unreadable/partial
//
//	type SelectResult string
//	const (
//	    SelectAbsent    SelectResult = ""
//	    SelectSingle    SelectResult = "single"
//	    SelectAmbiguous SelectResult = "ambiguous"
//	    SelectBroken    SelectResult = "broken"
//	)
//
//	type Selection struct {
//	    Result  SelectResult
//	    Entry   *Entry  // populated only for SelectSingle
//	    Entries []Entry // the claiming entries, for SelectAmbiguous/SelectBroken
//	    Reason  string  // bounded, populated for SelectBroken/SelectAmbiguous
//	}
//
//	func (inv Inventory) Select(ticketID int) Selection
//
//	func Read(repoRoot string) Inventory
//
// A "claim" on ticketID is any Entry that attributes itself to ticketID:
//   - Health == HealthOK && TicketID == ticketID (a healthy claim)
//   - Health == HealthUnreadable/HealthMalformed/HealthPathAnomaly &&
//     FilenameID == ticketID (a broken-but-attributable claim: the filename
//     numeric prefix still names the ticket even though the content
//     couldn't be trusted)
//   - Health == HealthIDMismatch && (FilenameID == ticketID ||
//     FrontMatterID == ticketID) (Q2: both the filename claim and the
//     front-matter claim are held)
//
// HealthUnattributable entries (no numeric filename prefix at all -- a
// legitimate ticketless plan, a malformed non-numeric name, or a
// dot-prefixed candidate file) never claim any ticket key (Q1).
//
// Select(ticketID) on an incomplete Inventory (State is DirUnreadable or
// DirPartial -- Complete() == false) always returns SelectBroken for every
// ticketID: a partial/unreadable read can never prove a ticket's plan
// inventory is either absent or a single healthy match.
//
// readDirEntries is the package-internal seam
// (`func(dir string) ([]os.DirEntry, error)`), overridable only from tests
// in this package, that lets a test force the DirPartial state -- which is
// not otherwise portably reproducible from the OS (os.ReadDir/File.ReadDir
// only return a partial slice + error on a genuine mid-enumeration failure).

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// -- fixtures -----------------------------------------------------------

// mkPlansDir creates repoRoot/.plans and returns its path.
func mkPlansDir(t *testing.T, repoRoot string) string {
	t.Helper()
	dir := filepath.Join(repoRoot, ".plans")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// writeFile writes content to path, creating no parent directories (callers
// are expected to have already created .plans via mkPlansDir).
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// healthyPlan builds minimal front matter for a fully healthy claim: the
// numeric filename prefix (set by the caller's chosen file name) must equal
// ticketID for Health to resolve HealthOK.
func healthyPlan(ticketID int) string {
	return fmt.Sprintf("---\nticketId: %d\nstatus: planned\n---\nbody\n", ticketID)
}

// -- DirState: absent / empty / complete ---------------------------------

func TestRead_MissingPlansDir_ReturnsAbsent(t *testing.T) {
	repoRoot := t.TempDir() // .plans never created

	inv := Read(repoRoot)
	if inv.State != DirAbsent {
		t.Errorf("State = %q, want DirAbsent", inv.State)
	}
	if !inv.Complete() {
		t.Error("Complete() = false, want true (a missing directory is verified absence)")
	}
	if len(inv.Entries) != 0 {
		t.Errorf("Entries = %v, want none", inv.Entries)
	}
	if sel := inv.Select(42); sel.Result != SelectAbsent {
		t.Errorf("Select(42).Result = %q, want SelectAbsent", sel.Result)
	}
}

func TestRead_EmptyPlansDir_ReturnsEmpty(t *testing.T) {
	repoRoot := t.TempDir()
	mkPlansDir(t, repoRoot) // exists, but zero entries

	inv := Read(repoRoot)
	if inv.State != DirEmpty {
		t.Errorf("State = %q, want DirEmpty", inv.State)
	}
	if !inv.Complete() {
		t.Error("Complete() = false, want true (an existing empty directory is verified absence)")
	}
	if sel := inv.Select(42); sel.Result != SelectAbsent {
		t.Errorf("Select(42).Result = %q, want SelectAbsent", sel.Result)
	}
}

func TestRead_Complete_SingleHealthyPlan_ReturnsSingle(t *testing.T) {
	repoRoot := t.TempDir()
	plansDir := mkPlansDir(t, repoRoot)
	path := filepath.Join(plansDir, "42-add-thing.md")
	writeFile(t, path, healthyPlan(42))

	inv := Read(repoRoot)
	if inv.State != DirComplete {
		t.Fatalf("State = %q, want DirComplete", inv.State)
	}
	if !inv.Complete() {
		t.Error("Complete() = false, want true")
	}
	if len(inv.Entries) != 1 {
		t.Fatalf("Entries = %v, want exactly one", inv.Entries)
	}
	e := inv.Entries[0]
	if e.Health != HealthOK {
		t.Errorf("Health = %q, want HealthOK", e.Health)
	}
	if e.FilenameID != 42 || e.FrontMatterID != 42 || e.TicketID != 42 {
		t.Errorf("FilenameID/FrontMatterID/TicketID = %d/%d/%d, want 42/42/42", e.FilenameID, e.FrontMatterID, e.TicketID)
	}
	if e.Path != path || e.Base != "42-add-thing.md" {
		t.Errorf("Path/Base = %q/%q, want %q/%q", e.Path, e.Base, path, "42-add-thing.md")
	}

	sel := inv.Select(42)
	if sel.Result != SelectSingle {
		t.Fatalf("Select(42).Result = %q, want SelectSingle", sel.Result)
	}
	if sel.Entry == nil || sel.Entry.Path != path {
		t.Errorf("Select(42).Entry = %+v, want the single healthy entry at %s", sel.Entry, path)
	}
}

// -- DirState: unreadable -------------------------------------------------

// TestRead_PlansIsRegularFile_ReturnsUnreadable covers the ENOTDIR case: a
// non-permission-dependent unreadable-directory class that runs everywhere,
// including as root (mitigating the root-run-CI-bypasses-permissions risk
// noted in the plan).
func TestRead_PlansIsRegularFile_ReturnsUnreadable(t *testing.T) {
	repoRoot := t.TempDir()
	writeFile(t, filepath.Join(repoRoot, ".plans"), "not a directory\n")

	inv := Read(repoRoot)
	if inv.State != DirUnreadable {
		t.Errorf("State = %q, want DirUnreadable", inv.State)
	}
	if inv.Err == nil {
		t.Error("Err = nil, want a non-nil error naming the ENOTDIR failure")
	}
	if inv.Complete() {
		t.Error("Complete() = true, want false (an unreadable directory can never prove absence)")
	}
	if sel := inv.Select(42); sel.Result != SelectBroken {
		t.Errorf("Select(42).Result = %q, want SelectBroken", sel.Result)
	}
}

// TestRead_PermissionDenied_ReturnsUnreadable covers the chmod-000
// permission-denied class, skipped under root (which bypasses Unix file
// permission checks entirely), mirroring internal/pipeline/reset_test.go's
// TestReset_UnreadableFile_ReadFailureWarningStillDeletesExit0 guard.
func TestRead_PermissionDenied_ReturnsUnreadable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root bypasses Unix file permission checks; cannot simulate permission-denied read")
	}
	repoRoot := t.TempDir()
	plansDir := mkPlansDir(t, repoRoot)
	writeFile(t, filepath.Join(plansDir, "42-x.md"), healthyPlan(42))
	if err := os.Chmod(plansDir, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(plansDir, 0o755) })

	inv := Read(repoRoot)
	if inv.State != DirUnreadable {
		t.Errorf("State = %q, want DirUnreadable", inv.State)
	}
	if inv.Err == nil {
		t.Error("Err = nil, want a non-nil permission error")
	}
	if inv.Complete() {
		t.Error("Complete() = true, want false")
	}
}

// -- DirState: unreadable, repo-root/dangling-symlink classes (Phase 6+7
// review finding #1) --------------------------------------------------------

// TestRead_RepoRootDoesNotExist_ReturnsUnreadable covers the "repo root
// itself is gone/unmounted" class: os.Stat(repoRoot/.plans) also yields
// ENOENT here, which -- without the repo-root check -- would be
// indistinguishable from true absence and misclassify as DirAbsent
// (Complete() == true), fail-opening exactly the class of bug this ticket
// exists to fix.
func TestRead_RepoRootDoesNotExist_ReturnsUnreadable(t *testing.T) {
	repoRoot := filepath.Join(t.TempDir(), "does-not-exist")

	inv := Read(repoRoot)
	if inv.State != DirUnreadable {
		t.Errorf("State = %q, want DirUnreadable (a missing repo root must never be verified absence)", inv.State)
	}
	if inv.Err == nil {
		t.Error("Err = nil, want a non-nil error naming the repo-root failure")
	}
	if inv.Complete() {
		t.Error("Complete() = true, want false")
	}
}

// TestRead_PlansIsDanglingSymlink_ReturnsUnreadable covers a `.plans` entry
// that IS present (unlike the missing-entry case above) but is a symlink
// whose target does not exist: os.Stat would also report ENOENT here (it
// follows the link), so Read must use os.Lstat on the `.plans` path itself
// to tell "no entry at all" apart from "an entry exists but is broken".
func TestRead_PlansIsDanglingSymlink_ReturnsUnreadable(t *testing.T) {
	repoRoot := t.TempDir()
	target := filepath.Join(repoRoot, "nonexistent-target")
	if err := os.Symlink(target, filepath.Join(repoRoot, ".plans")); err != nil {
		t.Fatal(err)
	}

	inv := Read(repoRoot)
	if inv.State != DirUnreadable {
		t.Errorf("State = %q, want DirUnreadable (a dangling .plans symlink must never be verified absence)", inv.State)
	}
	if inv.Err == nil {
		t.Error("Err = nil, want a non-nil error naming the dangling-symlink failure")
	}
	if inv.Complete() {
		t.Error("Complete() = true, want false")
	}
}

// -- DirState: partial (forced via the readDirEntries seam) --------------

// TestRead_Partial_ForcedBySeam covers the DirPartial state, which is not
// otherwise portably reproducible from the OS: readDirEntries is overridden
// to return a short entry slice alongside a non-nil error, mimicking
// File.ReadDir(-1)'s documented "entries read before the error, plus the
// error" partial-read contract.
func TestRead_Partial_ForcedBySeam(t *testing.T) {
	repoRoot := t.TempDir()
	plansDir := mkPlansDir(t, repoRoot)
	writeFile(t, filepath.Join(plansDir, "1-a.md"), healthyPlan(1))
	writeFile(t, filepath.Join(plansDir, "2-b.md"), healthyPlan(2))

	original := readDirEntries
	t.Cleanup(func() { readDirEntries = original })
	readDirEntries = func(dir string) ([]os.DirEntry, error) {
		entries, err := original(dir)
		if err != nil {
			return entries, err
		}
		if len(entries) == 0 {
			return entries, nil
		}
		return entries[:1], fmt.Errorf("simulated mid-enumeration failure")
	}

	inv := Read(repoRoot)
	if inv.State != DirPartial {
		t.Fatalf("State = %q, want DirPartial", inv.State)
	}
	if inv.Err == nil {
		t.Error("Err = nil, want the simulated partial-read error")
	}
	if inv.Complete() {
		t.Error("Complete() = true, want false (a partial read can never prove no second file claims any ticket)")
	}
	if len(inv.Entries) != 1 {
		t.Errorf("Entries = %d, want exactly the 1 entry read before the simulated failure", len(inv.Entries))
	}
	for _, id := range []int{1, 2, 999} {
		if sel := inv.Select(id); sel.Result != SelectBroken {
			t.Errorf("Select(%d).Result = %q, want SelectBroken", id, sel.Result)
		}
	}
}

// TestSelect_IncompleteInventory_AlwaysBroken pins the general contract
// (both DirUnreadable and DirPartial): Select must never resolve
// SelectAbsent or SelectSingle against an inventory it could not fully
// enumerate, for ANY ticket id -- an incomplete read can never prove
// absence.
func TestSelect_IncompleteInventory_AlwaysBroken(t *testing.T) {
	repoRoot := t.TempDir()
	writeFile(t, filepath.Join(repoRoot, ".plans"), "not a directory\n")

	inv := Read(repoRoot)
	if inv.Complete() {
		t.Fatal("Complete() = true, want false")
	}
	for _, id := range []int{1, 42, 999} {
		if sel := inv.Select(id); sel.Result != SelectBroken {
			t.Errorf("Select(%d).Result = %q, want SelectBroken", id, sel.Result)
		}
	}
}

// -- entry-level: unreadable, malformed, removed mid-read -----------------

// TestRead_EntryRemovedBetweenEnumerationAndRead_ClassifiesUnreadable covers
// the mid-write/rename race: the directory listing itself succeeds
// (DirComplete), but by the time Read attempts to read one entry's content,
// it has vanished -- classified HealthUnreadable, not silently dropped or
// treated as though it never existed (the entry stays in Inventory.Entries,
// still attributed via its filename numeric prefix).
func TestRead_EntryRemovedBetweenEnumerationAndRead_ClassifiesUnreadable(t *testing.T) {
	repoRoot := t.TempDir()
	plansDir := mkPlansDir(t, repoRoot)
	path := filepath.Join(plansDir, "42-vanishing.md")
	writeFile(t, path, healthyPlan(42))

	original := readDirEntries
	t.Cleanup(func() { readDirEntries = original })
	readDirEntries = func(dir string) ([]os.DirEntry, error) {
		entries, err := original(dir)
		if err == nil {
			// Simulate a concurrent removal between enumeration and the
			// per-entry content read that follows.
			_ = os.Remove(path)
		}
		return entries, err
	}

	inv := Read(repoRoot)
	if inv.State != DirComplete {
		t.Fatalf("State = %q, want DirComplete (directory enumeration itself succeeded)", inv.State)
	}
	if len(inv.Entries) != 1 {
		t.Fatalf("Entries = %v, want exactly one (the vanished entry, still listed)", inv.Entries)
	}
	e := inv.Entries[0]
	if e.Health != HealthUnreadable {
		t.Errorf("Health = %q, want HealthUnreadable", e.Health)
	}
	if e.FilenameID != 42 {
		t.Errorf("FilenameID = %d, want 42 (still attributable via the filename prefix)", e.FilenameID)
	}
	if sel := inv.Select(42); sel.Result != SelectBroken {
		t.Errorf("Select(42).Result = %q, want SelectBroken", sel.Result)
	}
}

// TestRead_TruncatedFrontMatter_ClassifiesMalformed covers a mid-write file
// whose front-matter fence never closes.
func TestRead_TruncatedFrontMatter_ClassifiesMalformed(t *testing.T) {
	repoRoot := t.TempDir()
	plansDir := mkPlansDir(t, repoRoot)
	writeFile(t, filepath.Join(plansDir, "42-truncated.md"), "---\nticketId: 42\nstatus: planned\n")

	inv := Read(repoRoot)
	if len(inv.Entries) != 1 {
		t.Fatalf("Entries = %v, want exactly one", inv.Entries)
	}
	e := inv.Entries[0]
	if e.Health != HealthMalformed {
		t.Errorf("Health = %q, want HealthMalformed", e.Health)
	}
	if e.FilenameID != 42 {
		t.Errorf("FilenameID = %d, want 42", e.FilenameID)
	}
	if sel := inv.Select(42); sel.Result != SelectBroken {
		t.Errorf("Select(42).Result = %q, want SelectBroken", sel.Result)
	}
}

// TestEntry_Reason_NeverLeaksPlanBodyContent covers the plan's bounded-
// diagnostics constraint: Reason carries paths and error text only, never
// plan/ticket body content.
func TestEntry_Reason_NeverLeaksPlanBodyContent(t *testing.T) {
	repoRoot := t.TempDir()
	plansDir := mkPlansDir(t, repoRoot)
	secret := "SECRET-TICKET-BODY-CONTENT-MUST-NEVER-BE-LOGGED"
	writeFile(t, filepath.Join(plansDir, "42-x.md"), "---\nticketId: 42\n"+secret+"\n")

	inv := Read(repoRoot)
	if len(inv.Entries) != 1 {
		t.Fatalf("Entries = %v, want exactly one", inv.Entries)
	}
	if strings.Contains(inv.Entries[0].Reason, secret) {
		t.Errorf("Reason leaked plan body content: %q", inv.Entries[0].Reason)
	}
}

// -- path anomalies: symlink, directory ------------------------------------

// TestRead_SymlinkEntry_ClassifiesPathAnomaly_NeverRead covers a symlink
// sitting inside .plans (Assumption #4): classified from os.DirEntry.Type()
// alone, without ever being opened/read -- its target need not even exist.
// The numeric filename prefix still attributes it as a broken claim to that
// ticket (never silently absent), consistent with unreadable/malformed
// entries above.
func TestRead_SymlinkEntry_ClassifiesPathAnomaly_NeverRead(t *testing.T) {
	repoRoot := t.TempDir()
	plansDir := mkPlansDir(t, repoRoot)
	linkPath := filepath.Join(plansDir, "42-link.md")
	if err := os.Symlink(filepath.Join(plansDir, "nonexistent-target"), linkPath); err != nil {
		t.Fatal(err)
	}

	inv := Read(repoRoot)
	if len(inv.Entries) != 1 {
		t.Fatalf("Entries = %v, want exactly one", inv.Entries)
	}
	e := inv.Entries[0]
	if e.Health != HealthPathAnomaly {
		t.Errorf("Health = %q, want HealthPathAnomaly", e.Health)
	}
	if e.FrontMatter != nil {
		t.Errorf("FrontMatter = %v, want nil (a path anomaly must never be read)", e.FrontMatter)
	}
	if e.FilenameID != 42 {
		t.Errorf("FilenameID = %d, want 42", e.FilenameID)
	}
	if sel := inv.Select(42); sel.Result != SelectBroken {
		t.Errorf("Select(42).Result = %q, want SelectBroken", sel.Result)
	}
}

// TestRead_DirectoryEntry_ClassifiesPathAnomaly_NeverRead covers a
// subdirectory sitting inside .plans with a plan-shaped name.
func TestRead_DirectoryEntry_ClassifiesPathAnomaly_NeverRead(t *testing.T) {
	repoRoot := t.TempDir()
	plansDir := mkPlansDir(t, repoRoot)
	subdir := filepath.Join(plansDir, "42-subdir.md")
	if err := os.Mkdir(subdir, 0o755); err != nil {
		t.Fatal(err)
	}

	inv := Read(repoRoot)
	if len(inv.Entries) != 1 {
		t.Fatalf("Entries = %v, want exactly one", inv.Entries)
	}
	if inv.Entries[0].Health != HealthPathAnomaly {
		t.Errorf("Health = %q, want HealthPathAnomaly", inv.Entries[0].Health)
	}
}

// TestRead_PlansDirItselfIsSymlink_EnumeratedNormally covers the other half
// of Assumption #4: a .plans directory that is ITSELF a symlink to a real
// directory stays legal and is enumerated normally (worktree layouts may
// rely on it) -- distinct from a symlinked ENTRY inside .plans.
func TestRead_PlansDirItselfIsSymlink_EnumeratedNormally(t *testing.T) {
	repoRoot := t.TempDir()
	realDir := filepath.Join(repoRoot, "real-plans")
	if err := os.MkdirAll(realDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(realDir, "42-x.md"), healthyPlan(42))
	if err := os.Symlink(realDir, filepath.Join(repoRoot, ".plans")); err != nil {
		t.Fatal(err)
	}

	inv := Read(repoRoot)
	if inv.State != DirComplete {
		t.Fatalf("State = %q, want DirComplete (a symlinked .plans directory itself stays legal)", inv.State)
	}
	if sel := inv.Select(42); sel.Result != SelectSingle {
		t.Errorf("Select(42).Result = %q, want SelectSingle", sel.Result)
	}
}

// -- identity: filename/front-matter mismatch (Q2) -------------------------

// TestSelect_FilenameFrontMatterMismatch_BothKeysHeldWithSameReason covers
// Q2: a plan file named for one ticket but whose front matter claims
// another must hold BOTH keys, with the identical "plan file ticket identity
// mismatch" reason on each side.
func TestSelect_FilenameFrontMatterMismatch_BothKeysHeldWithSameReason(t *testing.T) {
	repoRoot := t.TempDir()
	plansDir := mkPlansDir(t, repoRoot)
	writeFile(t, filepath.Join(plansDir, "900-foo.md"), "---\nticketId: 884\nstatus: planned\n---\nbody\n")

	inv := Read(repoRoot)
	if len(inv.Entries) != 1 {
		t.Fatalf("Entries = %v, want exactly one", inv.Entries)
	}
	e := inv.Entries[0]
	if e.Health != HealthIDMismatch {
		t.Errorf("Health = %q, want HealthIDMismatch", e.Health)
	}
	if e.FilenameID != 900 || e.FrontMatterID != 884 {
		t.Errorf("FilenameID/FrontMatterID = %d/%d, want 900/884", e.FilenameID, e.FrontMatterID)
	}

	selFilename := inv.Select(900)
	selFrontMatter := inv.Select(884)
	if selFilename.Result != SelectBroken {
		t.Errorf("Select(900).Result = %q, want SelectBroken", selFilename.Result)
	}
	if selFrontMatter.Result != SelectBroken {
		t.Errorf("Select(884).Result = %q, want SelectBroken", selFrontMatter.Result)
	}
	if selFilename.Reason == "" || selFrontMatter.Reason == "" {
		t.Fatalf("Reason must be populated on both sides, got %q / %q", selFilename.Reason, selFrontMatter.Reason)
	}
	if selFilename.Reason != selFrontMatter.Reason {
		t.Errorf("Reason must be the SAME identity-mismatch reason on both sides, got %q vs %q", selFilename.Reason, selFrontMatter.Reason)
	}

	// A ticket unrelated to either claim must remain verified absent.
	if sel := inv.Select(1); sel.Result != SelectAbsent {
		t.Errorf("Select(1).Result = %q, want SelectAbsent", sel.Result)
	}
}

// -- ambiguity: duplicate claims, both sort orders (AC3) -------------------

// TestSelect_TwoHealthyDuplicateClaims_AmbiguousRegardlessOfSortOrder covers
// AC3: two healthy plan files claiming the same ticket must resolve
// ambiguous, proven with the "winning" filename swapped across the two
// sub-tests so the result can never depend on directory sort order.
func TestSelect_TwoHealthyDuplicateClaims_AmbiguousRegardlessOfSortOrder(t *testing.T) {
	cases := []struct {
		name          string
		first, second string
	}{
		{"alphabetically-first-name-a", "42-aaa.md", "42-zzz.md"},
		{"alphabetically-first-name-b", "42-bbb.md", "42-yyy.md"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repoRoot := t.TempDir()
			plansDir := mkPlansDir(t, repoRoot)
			writeFile(t, filepath.Join(plansDir, tc.first), healthyPlan(42))
			writeFile(t, filepath.Join(plansDir, tc.second), healthyPlan(42))

			inv := Read(repoRoot)
			sel := inv.Select(42)
			if sel.Result != SelectAmbiguous {
				t.Fatalf("Select(42).Result = %q, want SelectAmbiguous", sel.Result)
			}
			if len(sel.Entries) != 2 {
				t.Errorf("Entries = %d, want 2 claiming entries", len(sel.Entries))
			}
		})
	}
}

// TestSelect_BrokenPlusHealthyDuplicate_HealthyFirst_AmbiguousNeverHealthy
// covers AC4: a broken-plus-healthy duplicate must never silently resolve
// healthy -- this deliberately supersedes #852's success-overwrites-error
// first-wins rule for duplicate claims (plan Assumptions).
func TestSelect_BrokenPlusHealthyDuplicate_HealthyFirst_AmbiguousNeverHealthy(t *testing.T) {
	repoRoot := t.TempDir()
	plansDir := mkPlansDir(t, repoRoot)
	writeFile(t, filepath.Join(plansDir, "42-a-healthy.md"), healthyPlan(42))
	writeFile(t, filepath.Join(plansDir, "42-b-broken.md"), "---\nticketId: 42\n") // unterminated front matter

	inv := Read(repoRoot)
	sel := inv.Select(42)
	if sel.Result != SelectAmbiguous {
		t.Fatalf("Select(42).Result = %q, want SelectAmbiguous (a broken duplicate must never be silently absorbed into a healthy single)", sel.Result)
	}
}

// TestSelect_BrokenPlusHealthyDuplicate_BrokenFirst_AmbiguousNeverHealthy is
// the reverse-sort-order sibling of the case above.
func TestSelect_BrokenPlusHealthyDuplicate_BrokenFirst_AmbiguousNeverHealthy(t *testing.T) {
	repoRoot := t.TempDir()
	plansDir := mkPlansDir(t, repoRoot)
	writeFile(t, filepath.Join(plansDir, "42-a-broken.md"), "---\nticketId: 42\n") // unterminated front matter
	writeFile(t, filepath.Join(plansDir, "42-b-healthy.md"), healthyPlan(42))

	inv := Read(repoRoot)
	sel := inv.Select(42)
	if sel.Result != SelectAmbiguous {
		t.Fatalf("Select(42).Result = %q, want SelectAmbiguous (order must never matter, per AC3/AC4)", sel.Result)
	}
}

// -- unattributable / ticketless (Q1) --------------------------------------

// TestSelect_UnattributableTicketlessFile_HoldsNothing_SiblingTicketStillResolves
// covers Q1: a legitimate ticketless plan file (no numeric filename prefix)
// is logged as a bounded anomaly but must never gate a real ticket -- a
// sibling ticket in the same repo dispatches normally.
func TestSelect_UnattributableTicketlessFile_HoldsNothing_SiblingTicketStillResolves(t *testing.T) {
	repoRoot := t.TempDir()
	plansDir := mkPlansDir(t, repoRoot)
	writeFile(t, filepath.Join(plansDir, "some-notes.md"), "just some notes, no front matter\n")
	writeFile(t, filepath.Join(plansDir, "42-sibling.md"), healthyPlan(42))

	inv := Read(repoRoot)
	if inv.State != DirComplete {
		t.Fatalf("State = %q, want DirComplete", inv.State)
	}

	var anomaly *Entry
	for i := range inv.Entries {
		if inv.Entries[i].Base == "some-notes.md" {
			anomaly = &inv.Entries[i]
		}
	}
	if anomaly == nil {
		t.Fatal("some-notes.md entry not found")
	}
	if anomaly.Health != HealthUnattributable {
		t.Errorf("Health = %q, want HealthUnattributable", anomaly.Health)
	}
	if anomaly.FilenameID != 0 {
		t.Errorf("FilenameID = %d, want 0 (no numeric prefix)", anomaly.FilenameID)
	}

	if sel := inv.Select(42); sel.Result != SelectSingle {
		t.Errorf("Select(42).Result = %q, want SelectSingle (sibling ticket unaffected by the anomaly)", sel.Result)
	}
	if sel := inv.Select(999); sel.Result != SelectAbsent {
		t.Errorf("Select(999).Result = %q, want SelectAbsent (the unattributable file claims no ticket at all)", sel.Result)
	}
}

// -- missing ticketId: legacy exemption dropped (Q4) -----------------------

// TestRead_MissingTicketIDInFrontMatter_LegacyFileUnhealthy covers Q4: a
// numeric-prefixed plan file whose front matter carries no ticketId at all
// (pre-dating the field) is no longer exempt -- it becomes unhealthy
// everywhere, consistent with dispatch's existing PlanProbeTicketIDError
// handling.
func TestRead_MissingTicketIDInFrontMatter_LegacyFileUnhealthy(t *testing.T) {
	repoRoot := t.TempDir()
	plansDir := mkPlansDir(t, repoRoot)
	writeFile(t, filepath.Join(plansDir, "42-legacy.md"), "---\nstatus: planned\n---\nbody\n")

	inv := Read(repoRoot)
	if len(inv.Entries) != 1 {
		t.Fatalf("Entries = %v, want exactly one", inv.Entries)
	}
	e := inv.Entries[0]
	if e.Health != HealthIDMismatch {
		t.Errorf("Health = %q, want HealthIDMismatch (Q4: no legacy exemption)", e.Health)
	}
	if e.FilenameID != 42 {
		t.Errorf("FilenameID = %d, want 42", e.FilenameID)
	}
	if e.FrontMatterID != 0 {
		t.Errorf("FrontMatterID = %d, want 0 (front matter carries no ticketId)", e.FrontMatterID)
	}
	if sel := inv.Select(42); sel.Result != SelectBroken {
		t.Errorf("Select(42).Result = %q, want SelectBroken (never silently healthy)", sel.Result)
	}
}

// -- #899 drift: dot-prefixed candidate files must never be attributed -----

// TestFilenameID_DotPrefixedCandidateFile_NeverAttributed_PreservesSingleMatchInvariant
// pins the #899-drift mitigation (plan Assumptions, updated 2026-08-01):
// Entry.FilenameID's numeric-prefix match must be anchored at byte offset 0
// of the base filename, so a dot-prefixed candidate file
// (.plans/.<id>-<slug>.candidate.md, #880's atomic-replace mechanism) never
// yields a FilenameID match regardless of what follows the dot. It is
// classified HealthUnattributable (Q1: holds nothing) BY CONSTRUCTION, not
// via special-casing the ".candidate.md" suffix -- so it can never be
// selected as a claim and never flips a single real healthy claim to
// ambiguous. See internal/pipeline/planfile_test.go's
// TestCheckPlan_CandidateFileAlongsideDraft_StillSingleMatch_AwaitingInput,
// the CheckPlan-level regression guard for the same invariant once CheckPlan
// is rewired onto this package's Read/Select (#884 Phase 4).
func TestFilenameID_DotPrefixedCandidateFile_NeverAttributed_PreservesSingleMatchInvariant(t *testing.T) {
	repoRoot := t.TempDir()
	plansDir := mkPlansDir(t, repoRoot)
	realPath := filepath.Join(plansDir, "42-add-thing.md")
	writeFile(t, realPath, healthyPlan(42))
	// Even with front matter that WOULD otherwise resolve healthy for the
	// same ticket -- proving the exclusion is anchoring-based, not a
	// content/health difference.
	candidatePath := filepath.Join(plansDir, ".42-add-thing.candidate.md")
	writeFile(t, candidatePath, healthyPlan(42))

	inv := Read(repoRoot)
	if inv.State != DirComplete {
		t.Fatalf("State = %q, want DirComplete", inv.State)
	}
	if len(inv.Entries) != 2 {
		t.Fatalf("Entries = %d, want 2 (the real draft and the dot-prefixed candidate)", len(inv.Entries))
	}

	var candidate *Entry
	for i := range inv.Entries {
		if inv.Entries[i].Base == ".42-add-thing.candidate.md" {
			candidate = &inv.Entries[i]
		}
	}
	if candidate == nil {
		t.Fatal("candidate entry not found in Inventory.Entries")
	}
	if candidate.FilenameID != 0 {
		t.Errorf("candidate FilenameID = %d, want 0 (a leading '.' must never match the ^\\d+ anchor, regardless of the digits that follow)", candidate.FilenameID)
	}
	if candidate.Health != HealthUnattributable {
		t.Errorf("candidate Health = %q, want HealthUnattributable", candidate.Health)
	}

	sel := inv.Select(42)
	if sel.Result != SelectSingle {
		t.Fatalf("Select(42).Result = %q, want SelectSingle (the dot-prefixed candidate must never flip a single real claim to ambiguous)", sel.Result)
	}
	if sel.Entry == nil || sel.Entry.Path != realPath {
		t.Errorf("Select(42).Entry = %+v, want the real draft at %s", sel.Entry, realPath)
	}
}

// -- non-.md entries and the size cap (Phase 6+7 review finding #4) ---------

// TestClassifyEntry_NonMdRegularFile_ExcludedUnattributable covers a stray
// editor backup (or .bak, or any non-.md file) with a leading numeric
// prefix: the old filepath.Glob(".plans/<id>-*.md") this package replaced
// implicitly restricted discovery to .md files, and that restriction must be
// preserved by construction -- such a file is never opened at all and never
// claims its ticket.
func TestClassifyEntry_NonMdRegularFile_ExcludedUnattributable(t *testing.T) {
	repoRoot := t.TempDir()
	plansDir := mkPlansDir(t, repoRoot)
	// If this were ever opened/parsed it would resolve HealthOK for ticket
	// 42 too, which would make Select(42) ambiguous -- proving it below that
	// Select(42) stays SelectSingle proves the file was never opened.
	writeFile(t, filepath.Join(plansDir, "42-foo.md~"), healthyPlan(42))
	writeFile(t, filepath.Join(plansDir, "42-sibling.md"), healthyPlan(42))

	inv := Read(repoRoot)
	var backup *Entry
	for i := range inv.Entries {
		if inv.Entries[i].Base == "42-foo.md~" {
			backup = &inv.Entries[i]
		}
	}
	if backup == nil {
		t.Fatal("42-foo.md~ entry not found")
	}
	if backup.Health != HealthUnattributable {
		t.Errorf("Health = %q, want HealthUnattributable (non-.md files must never be opened/classified)", backup.Health)
	}
	if backup.FrontMatter != nil || backup.Content != "" {
		t.Errorf("FrontMatter/Content = %v/%q, want nil/empty (a non-.md file must never be opened)", backup.FrontMatter, backup.Content)
	}

	if sel := inv.Select(42); sel.Result != SelectSingle {
		t.Errorf("Select(42).Result = %q, want SelectSingle (a non-.md file must never contest the claim)", sel.Result)
	}
}

// TestClassifyEntry_OversizeFile_ClassifiesUnreadableWithoutFullRead covers
// the read-size bound: an oversize file classifies HealthUnreadable rather
// than being read unbounded into memory. Uses the test-only maxPlanFileBytes
// seam so the case is exercised without a real 1MiB fixture.
func TestClassifyEntry_OversizeFile_ClassifiesUnreadableWithoutFullRead(t *testing.T) {
	original := maxPlanFileBytes
	maxPlanFileBytes = 8
	t.Cleanup(func() { maxPlanFileBytes = original })

	repoRoot := t.TempDir()
	plansDir := mkPlansDir(t, repoRoot)
	writeFile(t, filepath.Join(plansDir, "42-big.md"), "this content is longer than the test cap\n")

	inv := Read(repoRoot)
	if len(inv.Entries) != 1 {
		t.Fatalf("Entries = %v, want exactly one", inv.Entries)
	}
	e := inv.Entries[0]
	if e.Health != HealthUnreadable {
		t.Errorf("Health = %q, want HealthUnreadable (an oversize file must never be read unbounded)", e.Health)
	}
	if e.FrontMatter != nil || e.Content != "" {
		t.Errorf("FrontMatter/Content = %v/%q, want nil/empty", e.FrontMatter, e.Content)
	}
	if sel := inv.Select(42); sel.Result != SelectBroken {
		t.Errorf("Select(42).Result = %q, want SelectBroken", sel.Result)
	}
}

// -- Complete() across every DirState ---------------------------------------

func TestComplete_ReflectsDirState(t *testing.T) {
	cases := []struct {
		state DirState
		want  bool
	}{
		{DirAbsent, true},
		{DirEmpty, true},
		{DirComplete, true},
		{DirUnreadable, false},
		{DirPartial, false},
	}
	for _, tc := range cases {
		inv := Inventory{State: tc.state}
		if got := inv.Complete(); got != tc.want {
			t.Errorf("Inventory{State: %q}.Complete() = %v, want %v", tc.state, got, tc.want)
		}
	}
}

// -- repoRoot == "" guard (#884 review round 2 finding #3) -------------------

// TestRead_EmptyRepoRoot_ReturnsUnreadable pins the plan's Architectural
// Context Constraints section verbatim: "probeStage's dir == "" guard
// precedent (collect.go:187): never resolve a repo root from the daemon's
// cwd -- an empty Dir must not be enumerated, and must not be reported as
// verified absence either." Unlike probeStage (which treats dir == "" as the
// permissive StageProbeAbsent), Read must fail closed here: DirUnreadable,
// never DirAbsent.
func TestRead_EmptyRepoRoot_ReturnsUnreadable(t *testing.T) {
	inv := Read("")
	if inv.State != DirUnreadable {
		t.Errorf("State = %q, want DirUnreadable (an empty repo root must never resolve against the process cwd, and must never be verified absence)", inv.State)
	}
	if inv.Err == nil {
		t.Error("Err = nil, want a non-nil error naming the empty-repo-root failure")
	}
	if inv.Complete() {
		t.Error("Complete() = true, want false (an empty repo root can never prove absence)")
	}
	if sel := inv.Select(42); sel.Result != SelectBroken {
		t.Errorf("Select(42).Result = %q, want SelectBroken", sel.Result)
	}
}

// -- Select's Content memory bound (#884 review round 2 finding #2) ---------

// TestSelect_SingleMatch_ClearsContentFromSharedEntriesArray covers the
// memory fix: after Select resolves a SelectSingle match, only the returned
// Selection.Entry copy retains the winning entry's full Content -- the
// original entry inside inv.Entries (the shared backing array every Select
// call against this Inventory reads from) must have its Content cleared, so
// a repo with many plan files never retains every one of their Contents for
// the Inventory's whole lifetime.
func TestSelect_SingleMatch_ClearsContentFromSharedEntriesArray(t *testing.T) {
	repoRoot := t.TempDir()
	plansDir := mkPlansDir(t, repoRoot)
	writeFile(t, filepath.Join(plansDir, "1-a.md"), healthyPlan(1))
	writeFile(t, filepath.Join(plansDir, "2-b.md"), healthyPlan(2))

	inv := Read(repoRoot)
	entry1 := findEntry(t, inv, "1-a.md")
	entry2 := findEntry(t, inv, "2-b.md")
	if entry1.Content == "" || entry2.Content == "" {
		t.Fatal("both entries must carry Content right after Read, before Select ever runs")
	}
	entry2ContentBeforeSelect := entry2.Content

	sel := inv.Select(1)
	if sel.Result != SelectSingle {
		t.Fatalf("Select(1).Result = %q, want SelectSingle", sel.Result)
	}
	if sel.Entry.Content == "" {
		t.Error("the returned Selection.Entry must still carry Content -- it is the only copy meant to survive")
	}

	// The ORIGINAL entry (inv.Entries, the shared backing array) must have
	// had its Content cleared -- Select already copied out whatever the
	// caller needs into sel.Entry.
	afterEntry1 := findEntry(t, inv, "1-a.md")
	if afterEntry1.Content != "" {
		t.Errorf("original entry 1-a.md's Content = %q, want empty (cleared after Select(1) resolved it)", afterEntry1.Content)
	}

	// Entry 2 was never claimed by Select(1) and must be left untouched, so
	// a later Select(2) call against this same Inventory can still consume
	// it.
	afterEntry2 := findEntry(t, inv, "2-b.md")
	if afterEntry2.Content != entry2ContentBeforeSelect {
		t.Errorf("unrelated entry 2-b.md's Content changed after Select(1): got %q, want unchanged %q", afterEntry2.Content, entry2ContentBeforeSelect)
	}

	sel2 := inv.Select(2)
	if sel2.Result != SelectSingle || sel2.Entry.Content == "" {
		t.Fatalf("Select(2) = %+v, want SelectSingle with non-empty Content", sel2)
	}
	afterEntry2Selected := findEntry(t, inv, "2-b.md")
	if afterEntry2Selected.Content != "" {
		t.Errorf("original entry 2-b.md's Content = %q, want empty (cleared after Select(2) resolved it)", afterEntry2Selected.Content)
	}
}

// TestSelect_Ambiguous_ClearsContentFromEveryClaimingEntry covers the same
// memory bound for a SelectAmbiguous result: no consumer ever reads
// Selection.Entries[i].Content (only sel.Entry.Content, populated solely for
// SelectSingle), so every claiming entry's Content must be cleared, both in
// the returned Selection.Entries and in the shared inv.Entries backing
// array.
func TestSelect_Ambiguous_ClearsContentFromEveryClaimingEntry(t *testing.T) {
	repoRoot := t.TempDir()
	plansDir := mkPlansDir(t, repoRoot)
	writeFile(t, filepath.Join(plansDir, "1-a.md"), healthyPlan(1))
	writeFile(t, filepath.Join(plansDir, "1-b.md"), healthyPlan(1))

	inv := Read(repoRoot)
	sel := inv.Select(1)
	if sel.Result != SelectAmbiguous {
		t.Fatalf("Select(1).Result = %q, want SelectAmbiguous", sel.Result)
	}
	for _, e := range sel.Entries {
		if e.Content != "" {
			t.Errorf("Selection.Entries[%s].Content = %q, want empty (never consumed for a non-SelectSingle result)", e.Path, e.Content)
		}
	}
	for _, base := range []string{"1-a.md", "1-b.md"} {
		e := findEntry(t, inv, base)
		if e.Content != "" {
			t.Errorf("original entry %s's Content = %q, want empty (cleared after the ambiguous Select(1))", base, e.Content)
		}
	}
}

// findEntry locates the Entry in inv.Entries whose Base matches, failing the
// test if not found.
func findEntry(t *testing.T, inv Inventory, base string) Entry {
	t.Helper()
	for _, e := range inv.Entries {
		if e.Base == base {
			return e
		}
	}
	t.Fatalf("entry %s not found in %+v", base, inv.Entries)
	return Entry{}
}
