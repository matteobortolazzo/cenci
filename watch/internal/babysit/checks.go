package babysit

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// noChecksReportedPattern matches `gh pr checks`' stderr text when a PR
// genuinely has no checks configured yet -- fetchChecks' narrow exit-1-only
// fallback below (#923).
var noChecksReportedPattern = regexp.MustCompile(`(?i)no checks reported`)

// fetchChecks fetches a PR's checks, tolerating the two documented `gh pr
// checks` nonzero-exit shapes that are not genuine read failures (#923):
// exit 8 (checks pending) and exit 1 (a failing check, or -- narrowly --
// zero checks reported at all) both still print valid JSON to stdout in the
// success sub-case. ghJSON itself stays strictly fail-closed (its own doc
// comment); this helper is the caller that distinguishes these exits
// itself, calling execGh directly rather than ghJSON so stdout and stderr
// stay separated.
//
// The tolerance is gated on classifyGhFailure(err) == failureClassCommand
// (a plain nonzero exit, nothing else joined in) AND errors.As succeeding
// against *ghExitError specifically -- not a bare `interface{ ExitCode()
// int }`, which would also match a raw *exec.ExitError that never went
// through execGh's wrapping. Every other failure class (timeout, cancelled,
// truncated, parse) stays a hard failure even when the wrapped exit code
// would otherwise qualify.
func fetchChecks(pr, repo string) ([]check, error) {
	args := []string{"pr", "checks", pr, "--repo", repo, "--json", "bucket,name,state"}
	stdout, stderr, err := execGh(args...)
	if err == nil {
		var checks []check
		if decodeErr := json.Unmarshal([]byte(stdout), &checks); decodeErr != nil {
			return nil, fmt.Errorf("gh %s: decode: %w", strings.Join(args, " "), errors.Join(decodeErr, errGhDecode))
		}
		return checks, nil
	}

	wrapped := fmt.Errorf("gh %s: %s: %w", strings.Join(args, " "), strings.TrimSpace(stderr), err)

	if classifyGhFailure(err) != failureClassCommand {
		return nil, wrapped
	}
	var exitErr *ghExitError
	if !errors.As(err, &exitErr) {
		return nil, wrapped
	}

	exitCode := exitErr.ExitCode()
	if exitCode != 8 && exitCode != 1 {
		return nil, wrapped
	}
	var checks []check
	if decodeErr := json.Unmarshal([]byte(stdout), &checks); decodeErr == nil {
		return checks, nil
	}
	if exitCode == 1 && noChecksReportedPattern.MatchString(stderr) {
		return nil, nil
	}
	return nil, wrapped
}
