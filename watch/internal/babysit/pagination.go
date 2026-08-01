package babysit

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// maxFeedbackPages bounds fetchPaged's per_page=100&page=N loop -- mirrors
// feedback.go's maxThreadPages discipline (#854): a fetch still returning
// full pages after this many pages fails closed (complete=false) rather
// than silently treating a partial traversal as complete.
const maxFeedbackPages = 10

// feedbackPageSize is the per_page value fetchPaged requests -- GitHub
// REST's maximum, so completeness ("this page came back short") is detected
// as early as possible.
const feedbackPageSize = 100

// errPaginationFailed is fetchPaged's sentinel for a mid-pagination `gh`
// failure or a non-JSON page body -- distinct from the cap-exhaustion
// "complete=false" signal: a mid-pagination failure means the read itself
// broke, not merely that more pages exist beyond the cap
// (watch/docs/error-handling.md #850's "truncation means unreliable, never
// no data" rule; #412's direct errors.Is boundary test applies here too).
var errPaginationFailed = errors.New("paginated fetch failed")

// fetchPaged fetches every page of path via `gh api
// <path>?per_page=100&page=N`, assembling items across pages with an
// explicit per_page=100&page=N loop -- never `gh api --paginate`, which
// returns a nested array-of-page-arrays and hides the completeness signal
// (the plan's rejected alternative). complete is true when the traversal
// terminated on a short (< feedbackPageSize) or empty page; false when the
// page cap (maxFeedbackPages) was hit while a page was still full-sized,
// meaning further items may exist beyond what was fetched. Strict, like
// ghJSON's own #886 rewrite: any `gh` failure or non-JSON page body during
// pagination is an error, never partial-as-complete.
func fetchPaged[T any](path string) (items []T, complete bool, err error) {
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	for page := 1; page <= maxFeedbackPages; page++ {
		pageURL := fmt.Sprintf("%s%sper_page=%d&page=%d", path, sep, feedbackPageSize, page)
		stdout, stderr, err := execGh("api", pageURL)
		if err != nil {
			// Both %w verbs matter (#886): the first preserves
			// errPaginationFailed itself, the second preserves err's own
			// wrapped sentinel chain (errGhTimeout, errGhCancelled, ...) --
			// the previous "%w: %s: %s" form stringified err with %s,
			// severing that chain so classifyGhFailure could never see past
			// errPaginationFailed to the underlying cause.
			return nil, false, fmt.Errorf("%w: %s: %w", errPaginationFailed, strings.TrimSpace(stderr), err)
		}
		var pageItems []T
		if err := json.Unmarshal([]byte(stdout), &pageItems); err != nil {
			return nil, false, fmt.Errorf("%w: non-JSON page body: %w", errPaginationFailed, errors.Join(err, errGhDecode))
		}
		items = append(items, pageItems...)
		if len(pageItems) < feedbackPageSize {
			return items, true, nil
		}
	}
	return items, false, nil
}
