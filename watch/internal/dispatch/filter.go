package dispatch

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
)

// RepoFilter is one enrolled repo's machine-local pickup filter (#1385), the
// normalized form of a `dispatch.repos[].filters` object. Dimensions combine
// with AND; entries inside Milestones and inside LabelsAny combine with OR. An
// empty list imposes no restriction, so the zero value is "unrestricted".
//
// parseRepoFilter always returns non-nil lists so a marshaled filter carries
// `[]`, never `null` (watch/docs/go-gotchas.md). Labels are stored trimmed, in
// the spelling of their first occurrence; matching folds case (labelKey).
type RepoFilter struct {
	// Milestones are repo-local milestone numbers (never titles or global IDs).
	Milestones []int `json:"milestones"`
	// LabelsAll must all be carried by the issue.
	LabelsAll []string `json:"labelsAll"`
	// LabelsAny requires at least one of them when nonempty.
	LabelsAny []string `json:"labelsAny"`
	// LabelsNone must all be absent; an exclusion wins over an inclusion.
	LabelsNone []string `json:"labelsNone"`
}

// Restricted reports whether any dimension narrows pickup.
func (f RepoFilter) Restricted() bool {
	return len(f.Milestones) > 0 || len(f.LabelsAll) > 0 || len(f.LabelsAny) > 0 || len(f.LabelsNone) > 0
}

// withEmptyLists returns f with every nil list replaced by an empty one, so a
// filter built by hand (a Config literal) marshals like a parsed one.
func (f RepoFilter) withEmptyLists() RepoFilter {
	if f.Milestones == nil {
		f.Milestones = make([]int, 0)
	}
	if f.LabelsAll == nil {
		f.LabelsAll = make([]string, 0)
	}
	if f.LabelsAny == nil {
		f.LabelsAny = make([]string, 0)
	}
	if f.LabelsNone == nil {
		f.LabelsNone = make([]string, 0)
	}
	return f
}

// maxFilterErrValueBytes bounds a rejected config value echoed into a
// validation error, so a hostile or huge entry cannot flood the error string.
const maxFilterErrValueBytes = 100

// filterKeys are the only keys accepted inside a `filters` object.
var filterKeys = map[string]bool{
	"milestones": true,
	"labelsAll":  true,
	"labelsAny":  true,
	"labelsNone": true,
}

// parseRepoFilter validates and normalizes a `filters` value (#1385). A nil or
// empty raw is the absent key: unrestricted. An explicit null, a non-object, an
// unknown key, a null or non-array list, a milestone that is not a positive
// JSON integer, and a label that is not a non-blank string are all errors --
// never a silent unrestricted fallback, so a typo like `milestone` can never
// widen pickup. Duplicates collapse (milestones by value, labels after trim and
// case-fold). Each error starts mid-path (".milestones[1] must be ...", or " must
// be ...") so LoadConfig can prefix it with the config path, index and repo.
func parseRepoFilter(raw json.RawMessage) (RepoFilter, error) {
	if len(raw) == 0 {
		return RepoFilter{}.withEmptyLists(), nil
	}
	if isJSONNull(raw) {
		return RepoFilter{}, errors.New(" must be a JSON object, got null")
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return RepoFilter{}, fmt.Errorf(" must be a JSON object: %w", err)
	}
	var unknown []string
	for key := range obj {
		if !filterKeys[key] {
			unknown = append(unknown, key)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return RepoFilter{}, fmt.Errorf(".%s: unknown key (known: milestones, labelsAll, labelsAny, labelsNone)",
			boundFilterValue(unknown[0]))
	}

	f := RepoFilter{}.withEmptyLists()
	var err error
	if f.Milestones, err = parseMilestoneList(obj, "milestones"); err != nil {
		return RepoFilter{}, err
	}
	if f.LabelsAll, err = parseLabelList(obj, "labelsAll"); err != nil {
		return RepoFilter{}, err
	}
	if f.LabelsAny, err = parseLabelList(obj, "labelsAny"); err != nil {
		return RepoFilter{}, err
	}
	if f.LabelsNone, err = parseLabelList(obj, "labelsNone"); err != nil {
		return RepoFilter{}, err
	}
	return f, nil
}

// filterList returns the JSON array under key, or (nil, nil) when the key is
// absent. A null or non-array value is an error.
func filterList(obj map[string]json.RawMessage, key string) ([]json.RawMessage, error) {
	raw, ok := obj[key]
	if !ok {
		return nil, nil
	}
	if isJSONNull(raw) {
		return nil, fmt.Errorf(".%s must be a JSON array, got null", key)
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, fmt.Errorf(".%s must be a JSON array, got %s", key, boundFilterValue(string(raw)))
	}
	return items, nil
}

// parseMilestoneList returns the deduplicated milestone numbers under key, in
// order of first appearance. Entries must be positive JSON integers: decoding
// into an int rejects fractions (including 3.0), exponents, strings, booleans,
// objects and overflow; null and non-positive values are rejected explicitly.
func parseMilestoneList(obj map[string]json.RawMessage, key string) ([]int, error) {
	items, err := filterList(obj, key)
	if err != nil {
		return nil, err
	}
	out := make([]int, 0, len(items))
	seen := make(map[int]bool, len(items))
	for i, item := range items {
		var n int
		if isJSONNull(item) || json.Unmarshal(item, &n) != nil || n < 1 {
			return nil, fmt.Errorf(".%s[%d] must be a positive integer milestone number, got %s",
				key, i, boundFilterValue(string(item)))
		}
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	return out, nil
}

// parseLabelList returns the deduplicated labels under key, trimmed, in order of
// first appearance. Entries must be non-blank JSON strings.
func parseLabelList(obj map[string]json.RawMessage, key string) ([]string, error) {
	items, err := filterList(obj, key)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(items))
	seen := make(map[string]bool, len(items))
	for i, item := range items {
		var s string
		if isJSONNull(item) || json.Unmarshal(item, &s) != nil || strings.TrimSpace(s) == "" {
			return nil, fmt.Errorf(".%s[%d] must be a non-blank string label, got %s",
				key, i, boundFilterValue(string(item)))
		}
		name := strings.TrimSpace(s)
		if k := labelKey(name); !seen[k] {
			seen[k] = true
			out = append(out, name)
		}
	}
	return out, nil
}

// labelKey is the comparison form of a label: literal, trimmed and
// case-folded. Dedupe at load and matching at decision time share it, so the
// two can never disagree about which labels are equal.
func labelKey(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

// boundFilterValue renders a rejected value for an error message: one line and
// at most maxFilterErrValueBytes.
func boundFilterValue(s string) string {
	return truncateDetail(collapseLines(s), maxFilterErrValueBytes)
}

// buildFilterByRepo maps each enrolled repo to its resolved filter. A repo that
// appears twice takes its last entry, whole -- the same last-wins rule as
// buildSessionByRepo -- so a later entry without filters is unrestricted.
func buildFilterByRepo(repos []RepoConfig) map[string]RepoFilter {
	byRepo := make(map[string]RepoFilter, len(repos))
	for _, rc := range repos {
		byRepo[rc.Repo] = rc.Filter
	}
	return byRepo
}

// FilterForRepo returns repo's resolved filter (last entry wins on duplicates).
// A repo that is not enrolled is unrestricted. Lists are never nil.
func FilterForRepo(repos []RepoConfig, repo string) RepoFilter {
	return buildFilterByRepo(repos)[repo].withEmptyLists()
}

// filterSkip is the pure machine-local pickup gate (#1385). It returns
// (reason, true) when filter excludes t from this machine's queue and ("",
// false) otherwise. Dimensions are checked in a fixed order -- milestones,
// labelsAll, labelsAny, labelsNone -- and the first failure wins. The reason
// starts with "filter:", names the failed dimension with its expected and
// observed values, and only ever echoes configured label names (quoted), never
// issue-sourced label lists; the whole reason is one line and bounded.
//
// When a milestone constraint is set, unknown milestone metadata holds the
// ticket rather than counting as a match; without one the milestone is never
// consulted.
func filterSkip(filter RepoFilter, t Ticket) (string, bool) {
	if len(filter.Milestones) > 0 {
		switch t.MilestoneProbe {
		case MilestoneProbePresent:
			if t.Milestone < 1 {
				return filterReason("filter: milestone metadata unavailable"), true
			}
			if !slices.Contains(filter.Milestones, t.Milestone) {
				return filterReason(fmt.Sprintf("filter: milestone %d not in %v", t.Milestone, filter.Milestones)), true
			}
		case MilestoneProbeNone:
			return filterReason(fmt.Sprintf("filter: no milestone assigned (milestones %v)", filter.Milestones)), true
		default:
			// MilestoneProbeUnknown (the zero value) and any unrecognized probe
			// value: default-deny with the unavailable reason, never a match
			// (watch/docs/go-gotchas.md closed-set rule).
			return filterReason("filter: milestone metadata unavailable"), true
		}
	}

	have := make(map[string]bool, len(t.Labels))
	for _, l := range t.Labels {
		have[labelKey(l)] = true
	}
	for _, want := range filter.LabelsAll {
		if !have[labelKey(want)] {
			return filterReason(fmt.Sprintf("filter: required label %q missing", want)), true
		}
	}
	if len(filter.LabelsAny) > 0 {
		matched := false
		for _, want := range filter.LabelsAny {
			if have[labelKey(want)] {
				matched = true
				break
			}
		}
		if !matched {
			return filterReason(fmt.Sprintf("filter: no labelsAny match among %q", filter.LabelsAny)), true
		}
	}
	for _, banned := range filter.LabelsNone {
		if have[labelKey(banned)] {
			return filterReason(fmt.Sprintf("filter: excluded label %q present", banned)), true
		}
	}
	return "", false
}

// filterReason bounds a skip reason to one line of at most
// maxProbeLogDetailBytes, keeping the "filter:" prefix (truncation cuts the
// tail only).
func filterReason(reason string) string {
	return truncateDetail(collapseLines(reason), maxProbeLogDetailBytes)
}
