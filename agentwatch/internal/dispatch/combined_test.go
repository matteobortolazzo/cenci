package dispatch

import "testing"

func TestFailedWindowsMapping(t *testing.T) {
	got := failedWindows([]Ticket{
		{Repo: "o/r", Number: 42, Title: "Fix the thing!"},
		{Repo: "o/r", Number: 7, Title: ""},
	})
	if len(got) != 2 {
		t.Fatalf("got %d windows, want 2", len(got))
	}
	if got[0].WindowName != "42-fix-the-thing" || got[0].Status != "failed" {
		t.Errorf("window[0] = %+v, want 42-fix-the-thing/failed", got[0])
	}
	// No title ⇒ bare number.
	if got[1].WindowName != "7" || got[1].Status != "failed" {
		t.Errorf("window[1] = %+v, want 7/failed", got[1])
	}
}

func TestSlugify(t *testing.T) {
	cases := map[string]string{
		"Fix the thing!":       "fix-the-thing",
		"  Trim  spaces  ":     "trim-spaces",
		"already-slugged":      "already-slugged",
		"Multiple   ---dashes": "multiple-dashes",
		"":                     "",
		"!!!":                  "",
		"CamelCase123":         "camelcase123",
	}
	for in, want := range cases {
		if got := slugify(in); got != want {
			t.Errorf("slugify(%q) = %q, want %q", in, got, want)
		}
	}
}
