package capacity

import "testing"

func heldEntry(repo string, ticket int, socket, window string) Entry {
	return Entry{HolderID: repo + "#" + window, Kind: KindImplement, Repo: repo, Ticket: ticket, Socket: socket, Window: window, State: StateConfirmed}
}

func TestCountUsageSumsHeldUnjoinedAndOrphans(t *testing.T) {
	entries := []Entry{heldEntry("o/a", 1, "s", "1-implement"), heldEntry("o/b", 2, "s", "2-implement")}
	obs := Observation{
		Sessions: []Session{{Socket: "s", Window: "manual", PaneID: "%9", Running: true}},
		Orphans:  2,
	}
	got := CountUsage(entries, obs)
	if got.Machine != 5 {
		t.Fatalf("Machine = %d, want 2 held + 1 manual + 2 orphans = 5", got.Machine)
	}
	if got.Held != 2 || got.Unjoined != 1 || got.Orphans != 2 {
		t.Errorf("breakdown = held %d unjoined %d orphans %d; want 2, 1, 2", got.Held, got.Unjoined, got.Orphans)
	}
}

func TestCountUsageCountsALedgerWorkerThatIsAlsoARunningSessionOnce(t *testing.T) {
	entries := []Entry{heldEntry("o/a", 1, "s", "1-implement")}
	obs := Observation{Sessions: []Session{{Socket: "s", Window: "1-implement", PaneID: "%3", Running: true}}}
	if got := CountUsage(entries, obs).Machine; got != 1 {
		t.Fatalf("Machine = %d, want 1: a held worker that also runs counts once", got)
	}
}

func TestCountUsageJoinsByPaneIDThroughTheInventory(t *testing.T) {
	entries := []Entry{heldEntry("o/a", 1, "s", "1-implement")}
	obs := Observation{
		// The daemon session carries only a pane id; the inventory ties that pane to
		// the entry's window.
		Sessions: []Session{{Socket: "s", PaneID: "%3", Running: true}},
		Inventory: Inventory{"s": {Complete: true, Windows: []Window{
			{Name: "1-implement", PaneIDs: []string{"%3"}},
		}}},
	}
	if got := CountUsage(entries, obs).Machine; got != 1 {
		t.Fatalf("Machine = %d, want 1: a pane id in the entry's window is the same worker", got)
	}
}

func TestCountUsageDoesNotJoinAcrossSockets(t *testing.T) {
	entries := []Entry{heldEntry("o/a", 1, "s1", "1-implement")}
	obs := Observation{Sessions: []Session{{Socket: "s2", Window: "1-implement", Running: true}}}
	if got := CountUsage(entries, obs).Machine; got != 2 {
		t.Fatalf("Machine = %d, want 2: the same window name on another socket is another worker", got)
	}
}

func TestCountUsageCountsAManualRunningSessionWithNoEntry(t *testing.T) {
	obs := Observation{Sessions: []Session{{Socket: "s", Window: "scratch", Running: true}}}
	if got := CountUsage(nil, obs).Machine; got != 1 {
		t.Fatalf("Machine = %d, want 1", got)
	}
}

func TestCountUsageIgnoresSessionsThatAreNotRunning(t *testing.T) {
	obs := Observation{Sessions: []Session{
		{Socket: "s", Window: "a", Running: false},
		{Socket: "s", Window: "b", Running: false},
	}}
	if got := CountUsage(nil, obs).Machine; got != 0 {
		t.Fatalf("Machine = %d, want 0: sessions in a non-running status count zero", got)
	}
}

func TestCountUsagePerRepoCountsOnlyThatRepoLedgerEntries(t *testing.T) {
	entries := []Entry{
		heldEntry("o/a", 1, "s", "1-implement"),
		heldEntry("o/a", 2, "s", "2-implement"),
		heldEntry("o/b", 3, "s", "3-implement"),
	}
	obs := Observation{
		Sessions: []Session{{Socket: "s", Window: "manual", Running: true}},
		Orphans:  4,
	}
	got := CountUsage(entries, obs)
	if got.Repo("o/a") != 2 || got.Repo("O/B") != 1 || got.Repo("o/none") != 0 {
		t.Fatalf("per-repo = a:%d b:%d none:%d; want 2, 1, 0 (manual and orphan sessions count against the machine only)",
			got.Repo("o/a"), got.Repo("O/B"), got.Repo("o/none"))
	}
}

func TestCountUsageNeverGoesNegative(t *testing.T) {
	// More entries than running sessions is the normal case right after a launch.
	entries := []Entry{heldEntry("o/a", 1, "s", "1-implement"), heldEntry("o/a", 2, "s", "2-implement")}
	if got := CountUsage(entries, Observation{}).Machine; got != 2 {
		t.Fatalf("Machine = %d, want 2", got)
	}
}
