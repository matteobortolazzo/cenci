package planfile

// Shared plan-inventory contract (#884): one authoritative directory
// enumeration + identity-based ticket selection, replacing the
// error-suppressing `filepath.Glob` discovery duplicated across
// internal/dispatch and internal/pipeline. See inventory_test.go's top-of-
// file doc comment for the full API contract this file implements.

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// DirState classifies the result of enumerating a repo's .plans directory.
type DirState string

const (
	DirAbsent     DirState = "absent"
	DirEmpty      DirState = "empty"
	DirComplete   DirState = "complete"
	DirUnreadable DirState = "unreadable"
	DirPartial    DirState = "partial"
)

// Health classifies a single .plans entry once identity/content has been
// resolved (or deliberately never read, for path anomalies).
type Health string

const (
	HealthOK             Health = "ok"
	HealthUnreadable     Health = "unreadable"
	HealthMalformed      Health = "malformed"
	HealthIDMismatch     Health = "id_mismatch"
	HealthUnattributable Health = "unattributable"
	HealthPathAnomaly    Health = "path_anomaly"
)

// Entry describes one file/path found inside .plans.
type Entry struct {
	Path          string            // full path
	Base          string            // filepath.Base(Path)
	FilenameID    int               // leading ^\d+ run in Base; 0 = no match
	FrontMatterID int               // parsed ticketId front-matter field; 0 = absent/unparseable
	TicketID      int               // == FilenameID iff Health == HealthOK; 0 otherwise
	Health        Health
	Reason        string            // bounded: path + error text only, never plan/ticket body content
	FrontMatter   map[string]string // nil unless the file was read and its front matter parsed
	// Content is the full raw file body, populated once here during Read
	// whenever the file was successfully opened and read (Health == HealthOK
	// or HealthMalformed). Consumers must read this field instead of
	// re-opening Path by path (Phase 6+7 review finding #3): a second
	// by-path read is a TOCTOU window -- identity/content is proven against
	// this read, and a later os.ReadFile(Path) may follow a symlink or read
	// whatever now sits at that path instead.
	Content string
}

// Inventory is the result of enumerating one repo's .plans directory.
type Inventory struct {
	Dir     string   // the scanned .plans directory (repoRoot/.plans)
	State   DirState
	Err     error   // set for DirUnreadable/DirPartial
	Entries []Entry // sorted by Path; empty for absent/empty/unreadable
}

// Complete reports whether the inventory is a proven complete enumeration
// (absent/empty/complete) as opposed to one that could not be fully read
// (unreadable/partial), which can never prove absence or a single match.
func (inv Inventory) Complete() bool {
	switch inv.State {
	case DirAbsent, DirEmpty, DirComplete:
		return true
	default:
		return false
	}
}

// SelectResult is the closed set of outcomes Select can return.
type SelectResult string

const (
	SelectAbsent    SelectResult = ""
	SelectSingle    SelectResult = "single"
	SelectAmbiguous SelectResult = "ambiguous"
	SelectBroken    SelectResult = "broken"
)

// Selection is the result of resolving one ticket id against an Inventory.
type Selection struct {
	Result  SelectResult
	Entry   *Entry  // populated only for SelectSingle
	Entries []Entry // the claiming entries, for SelectAmbiguous/SelectBroken
	Reason  string  // bounded, populated for SelectBroken/SelectAmbiguous
}

// filenameIDPattern anchors the numeric-prefix match at byte offset 0 of the
// base filename (#899 drift, see plan Assumptions): a dot-prefixed candidate
// file (.plans/.<id>-<slug>.candidate.md) must never yield a FilenameID
// match, regardless of what follows the dot. Do not special-case the
// ".candidate.md" suffix -- the exclusion falls out of this anchoring.
var filenameIDPattern = regexp.MustCompile(`^\d+`)

// readDirEntries is the package-internal seam letting tests force the
// DirPartial state, which is not otherwise portably reproducible from the
// OS: os.ReadDir/File.ReadDir(-1) only return a partial slice + error on a
// genuine mid-enumeration failure. Overridable only from tests in this
// package.
var readDirEntries = func(dir string) ([]os.DirEntry, error) {
	f, err := os.Open(dir)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return f.ReadDir(-1)
}

// Read enumerates repoRoot/.plans and classifies every entry found there.
func Read(repoRoot string) Inventory {
	// An empty repoRoot must never be enumerated (#884 review round 2 finding
	// #3), mirroring dispatch's probeStage dir == "" guard precedent
	// (collect.go): filepath.Join("", ".plans") would silently resolve to a
	// bare ".plans" relative to the daemon's own working directory, reading
	// an unrelated repo's state -- and, unlike probeStage (which treats an
	// unresolved dir as the permissive StageProbeAbsent), an empty repoRoot
	// here must fail closed as DirUnreadable, never as verified absence.
	// This defense-in-depth check makes the invariant hold by construction
	// for every caller of Read, not just the ones that remember to guard
	// before calling it.
	if repoRoot == "" {
		return Inventory{State: DirUnreadable, Err: fmt.Errorf("repo root is empty; refusing to resolve .plans against the process working directory")}
	}

	dir := filepath.Join(repoRoot, ".plans")
	inv := Inventory{Dir: dir}

	// Lstat (not Stat) on dir itself, first: Lstat on a symlink whose target
	// is missing still succeeds and reports the symlink entry, whereas Stat
	// (which follows the link) returns ENOENT indistinguishable from "no
	// .plans entry at all" -- this is what lets a dangling-symlink .plans be
	// told apart from true absence (Phase 6+7 review finding #1).
	linfo, lerr := os.Lstat(dir)
	if lerr != nil {
		if os.IsNotExist(lerr) {
			// No `.plans` entry exists at all. Before concluding verified
			// absence, the repo root itself must be provably readable: a
			// missing/unmounted repoRoot also yields ENOENT on dir, which is
			// otherwise indistinguishable from true absence and would
			// fail-open exactly the class of bug this ticket exists to fix.
			if _, rerr := os.Stat(repoRoot); rerr != nil {
				inv.State = DirUnreadable
				inv.Err = fmt.Errorf("repo root %s unreadable: %w", repoRoot, rerr)
				return inv
			}
			inv.State = DirAbsent
			return inv
		}
		inv.State = DirUnreadable
		inv.Err = lerr
		return inv
	}

	info := linfo
	if linfo.Mode()&os.ModeSymlink != 0 {
		// `.plans` itself being a symlink to a real directory stays legal
		// (Assumption #4, worktree layouts may rely on it), but the target
		// must actually resolve: a dangling symlink must never be treated
		// as verified absence.
		sinfo, serr := os.Stat(dir)
		if serr != nil {
			inv.State = DirUnreadable
			inv.Err = fmt.Errorf("%s: dangling symlink: %w", dir, serr)
			return inv
		}
		info = sinfo
	}
	if !info.IsDir() {
		inv.State = DirUnreadable
		inv.Err = fmt.Errorf("%s: not a directory", dir)
		return inv
	}

	dirEntries, err := readDirEntries(dir)
	if err != nil {
		if len(dirEntries) == 0 {
			inv.State = DirUnreadable
			inv.Err = err
			return inv
		}
		inv.State = DirPartial
		inv.Err = err
		inv.Entries = classifyEntries(dir, dirEntries)
		return inv
	}

	if len(dirEntries) == 0 {
		inv.State = DirEmpty
		return inv
	}

	inv.State = DirComplete
	inv.Entries = classifyEntries(dir, dirEntries)
	return inv
}

func classifyEntries(dir string, dirEntries []os.DirEntry) []Entry {
	entries := make([]Entry, 0, len(dirEntries))
	for _, de := range dirEntries {
		entries = append(entries, classifyEntry(dir, de))
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	return entries
}

func classifyEntry(dir string, de os.DirEntry) Entry {
	base := de.Name()
	e := Entry{Path: filepath.Join(dir, base), Base: base}
	if m := filenameIDPattern.FindString(base); m != "" {
		e.FilenameID = AtoiSafe(m)
	}

	// Path anomalies are classified from os.DirEntry.Type() alone and are
	// never opened/read -- a symlink's target need not even exist.
	switch {
	case de.Type()&os.ModeSymlink != 0:
		e.Health = HealthPathAnomaly
		e.Reason = fmt.Sprintf("%s: symlink entry inside .plans, not read", e.Path)
		return e
	case de.IsDir():
		e.Health = HealthPathAnomaly
		e.Reason = fmt.Sprintf("%s: directory entry inside .plans, not read", e.Path)
		return e
	case !de.Type().IsRegular():
		e.Health = HealthPathAnomaly
		e.Reason = fmt.Sprintf("%s: non-regular entry inside .plans, not read", e.Path)
		return e
	}

	// No numeric filename prefix at all: a legitimate ticketless plan, a
	// malformed non-numeric name, or a dot-prefixed candidate file (#899
	// drift). Unattributable regardless of content -- it can never claim a
	// ticket key (Q1), so its content is never even inspected.
	if e.FilenameID == 0 {
		e.Health = HealthUnattributable
		e.Reason = fmt.Sprintf("%s: no numeric filename prefix; unattributable", e.Path)
		return e
	}

	// A numeric-prefixed entry that is not a .md file (a stray editor
	// backup, a .bak, ...) is never opened at all (Phase 6+7 review finding
	// #4): the old filepath.Glob(".plans/<id>-*.md") this package replaced
	// implicitly restricted discovery to .md files, and that restriction
	// must be preserved explicitly and by construction, not lost.
	if !strings.HasSuffix(base, ".md") {
		e.Health = HealthUnattributable
		e.Reason = fmt.Sprintf("%s: not a .md file; unattributable", e.Path)
		return e
	}

	content, err := readBoundedFile(e.Path, maxPlanFileBytes)
	if err != nil {
		e.Health = HealthUnreadable
		e.Reason = fmt.Sprintf("%s: %v", e.Path, err)
		return e
	}
	e.Content = string(content)

	fm, ok := ParseFrontMatter(e.Content)
	if !ok {
		e.Health = HealthMalformed
		e.Reason = fmt.Sprintf("%s: malformed or unterminated front matter", e.Path)
		return e
	}
	e.FrontMatter = fm
	e.FrontMatterID = AtoiSafe(fm["ticketId"])

	return finalizeIdentity(e)
}

// maxPlanFileBytes bounds classifyEntry's content read (Phase 6+7 review
// finding #4): an oversize file (or one still growing mid-race) is never
// read unbounded into memory -- it classifies HealthUnreadable instead.
// Overridable only from tests in this package, mirroring readDirEntries'
// seam, so a test can exercise the cap without writing a real 1MiB fixture.
var maxPlanFileBytes int64 = 1 << 20 // 1MiB: a generous plan-file-sized ceiling

// readBoundedFile reads path's full content, refusing to read more than
// limit+1 bytes so a file at or under limit is read exactly once and in
// full, while an oversize (or concurrently growing) file is never buffered
// unbounded into memory.
func readBoundedFile(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%s: exceeds %d byte read cap", path, limit)
	}
	return data, nil
}

// finalizeIdentity resolves Health/TicketID for an entry whose content was
// successfully read and whose front matter parsed, and whose filename
// already carries a numeric prefix (callers filter FilenameID == 0 earlier,
// per Q1). The numeric filename prefix and the front-matter ticketId must
// agree for the entry to be healthy; a missing or disagreeing front-matter
// ticketId is an identity mismatch (Q2, Q4 -- no legacy exemption for a
// missing ticketId).
func finalizeIdentity(e Entry) Entry {
	if e.FrontMatterID == 0 || e.FrontMatterID != e.FilenameID {
		e.Health = HealthIDMismatch
		e.Reason = fmt.Sprintf("%s: plan file ticket identity mismatch (filename ticket=%d, front matter ticketId=%d)", e.Path, e.FilenameID, e.FrontMatterID)
		return e
	}
	e.Health = HealthOK
	e.TicketID = e.FilenameID
	return e
}

// Select resolves ticketID against the inventory. A "claim" on ticketID is
// any Entry that attributes itself to ticketID -- see the doc comment atop
// inventory_test.go for the exact per-Health claim rules.
//
// Memory (#884 review round 2 finding #2): classifyEntry reads and retains
// every readable numeric-prefixed .md entry's full Content (up to
// maxPlanFileBytes each), but only the single entry Select ever resolves via
// SelectSingle is ever consumed downstream (sel.Entry.Content). To avoid a
// repo with many plan files retaining every one of their Contents for the
// Inventory's whole lifetime, Select clears Content on every entry it
// resolves this call (in inv.Entries' shared backing array, which every
// Select call against this Inventory shares) once it has copied out
// whatever it needs into the returned Selection -- so after this call
// returns, at most one entry anywhere (the winning SelectSingle copy, owned
// by the caller) still retains its Content. This is safe across repeated
// Select calls for different ticketIDs against the same Inventory (as
// dispatch.ReadPlans makes, once per distinct claimed id): a call only ever
// touches entries that claim ITS ticketID, never another id's entries, so
// content an earlier call didn't need is left untouched for a later call
// that does.
func (inv Inventory) Select(ticketID int) Selection {
	if !inv.Complete() {
		return Selection{
			Result: SelectBroken,
			Reason: fmt.Sprintf("%s could not be fully enumerated: %v", inv.Dir, inv.Err),
		}
	}

	var claimingIdx []int
	for i, e := range inv.Entries {
		if entryClaims(e, ticketID) {
			claimingIdx = append(claimingIdx, i)
		}
	}

	switch len(claimingIdx) {
	case 0:
		return Selection{Result: SelectAbsent}
	case 1:
		i := claimingIdx[0]
		e := inv.Entries[i]
		if e.Health == HealthOK {
			entryCopy := e
			// The copy above already owns whatever Content e carries
			// (strings are immutable, so clearing the original field below
			// cannot affect entryCopy's independently-held string value) --
			// drop it from the shared backing array now that Select is
			// done with this entry.
			inv.Entries[i].Content = ""
			return Selection{Result: SelectSingle, Entry: &entryCopy}
		}
		broken := e
		broken.Content = ""
		inv.Entries[i].Content = ""
		return Selection{Result: SelectBroken, Entries: []Entry{broken}, Reason: e.Reason}
	default:
		claiming := make([]Entry, len(claimingIdx))
		for j, i := range claimingIdx {
			e := inv.Entries[i]
			e.Content = ""
			inv.Entries[i].Content = ""
			claiming[j] = e
		}
		return Selection{Result: SelectAmbiguous, Entries: claiming, Reason: ambiguityReason(ticketID, claiming)}
	}
}

func entryClaims(e Entry, ticketID int) bool {
	switch e.Health {
	case HealthOK:
		return e.TicketID == ticketID
	case HealthUnreadable, HealthMalformed, HealthPathAnomaly:
		return e.FilenameID == ticketID
	case HealthIDMismatch:
		return e.FilenameID == ticketID || e.FrontMatterID == ticketID
	default: // HealthUnattributable never claims any ticket key (Q1).
		return false
	}
}

func ambiguityReason(ticketID int, entries []Entry) string {
	paths := make([]string, len(entries))
	for i, e := range entries {
		paths[i] = e.Path
	}
	return fmt.Sprintf("ticket %d claimed by %d plan files: %s", ticketID, len(entries), strings.Join(paths, ", "))
}
