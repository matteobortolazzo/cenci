package main_test

// Docs/CLI consistency tests for the incident worker's identity tutorial
// (#1370, parent #1210: P36-P40, P54, P56-P59, P63).
//
// These are black-box checks of committed documents against the built `cenci`
// binary (binaryPath, built by TestMain in main_test.go) and against the Go
// test files themselves. Every document is read by climbing from this file's
// own path, and a document that cannot be read fails the test rather than
// skipping it: a moved or renamed doc must break CI, not pass with zero
// coverage (watch/docs/test-strategy.md, #912).
//
// Regenerating the tutorial's captured sample output. Step 2's expected output
// is never hand-written. Build the binary (`make build` in watch/), put a
// directory without `az` first on PATH, write a throwaway config under a 0700
// temp directory, and run the exact preview command shown in Step 2:
//
//	PATH=<dir-without-az> ./cenci incident setup --config <config> \
//	  --tenant <uuid> --subscription <uuid> \
//	  --certificate-path <absolute-pem-path> ...
//
// With no usable `az` the command exits 0 and renders every step with
// `<placeholder>` tokens. Paste the output, shortening only with `...`. The same
// command with `incident verify -h` / `incident setup -h` shows the flag lists
// these tests read.
//
// Phrases the tests pin in the documents (kept few on purpose; the writer of
// the docs must use them exactly):
//   - Tutorial H2:  "## Tutorial: a worker identity on a local host", with H3
//     headings "### Step N: ..." (N = 1..7); heading keywords per step are in
//     tutorialStepKeywords.
//   - Step 1 contains "Data Sender" and "separate from the worker identity".
//   - Step 6 contains "Data Sender", "Fired" and "Resolved".
//   - Step 7 has "#### Undo a setup apply" and the sentence "no command deletes
//     the incident ledger".
//   - Hosted H2: "## Azure-hosted worker identity", containing "managed
//     identity", "workload identity federation" and "does not work on an
//     arbitrary local Docker host".
//   - sandbox/README.md "### Azure CLI" contains "unrelated to the incident
//     worker".
//   - "## Automated verification" holds a table whose rows start with
//     "| P<n> |" for P1..P63, each once.

import (
	"bytes"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"unicode"
)

const (
	docsTutorialHeading = "Tutorial: a worker identity on a local host"
	docsHostedHeading   = "Azure-hosted worker identity"
	docsVerifyHeading   = "Automated verification"
	docsParentCriteria  = 63
)

// tutorialStepKeywords is the topic word each step heading must contain,
// indexed by step number minus one.
var tutorialStepKeywords = []string{
	"prerequisites",   // 1: prerequisites and the bridge/worker/agent distinction
	"preview",         // 2: setup preview
	"certificate",     // 3: identity, certificate and role creation
	"service account", // 4: service account and environment
	"verif",           // 5: verification
	"smoke",           // 6: staging smoke test
	"migrat",          // 7: migration, troubleshooting, renewal, revocation, undo
}

// --- file access ------------------------------------------------------------

// docsWatchDir is the watch/ module directory, resolved from this file's path.
func docsWatchDir(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller(0) failed to resolve this test file's own path")
	}
	return filepath.Dir(file)
}

// readRepoFile reads a repo-relative file, failing (never skipping) when it
// cannot be read.
func readRepoFile(t *testing.T, rel string) string {
	t.Helper()
	path := filepath.Join(docsWatchDir(t), "..", filepath.FromSlash(rel))
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("cannot read %s: %v (a missing or renamed doc must fail loudly, not be skipped)", path, err)
	}
	return string(content)
}

func incidentDoc(t *testing.T) string { return readRepoFile(t, "watch/docs/incident-worker.md") }
func watchReadme(t *testing.T) string { return readRepoFile(t, "watch/README.md") }

// --- markdown helpers -------------------------------------------------------

var (
	mdHeadingLine = regexp.MustCompile("^(#{1,6}) +(.+?) *$")
	mdFenceLine   = regexp.MustCompile("^\\s*```(.*)$")
)

type mdHeading struct {
	level int
	text  string
	line  int
}

// mdHeadings lists the headings outside code fences.
func mdHeadings(doc string) []mdHeading {
	var out []mdHeading
	inFence := false
	for i, line := range strings.Split(doc, "\n") {
		if mdFenceLine.MatchString(line) {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		if m := mdHeadingLine.FindStringSubmatch(line); m != nil {
			out = append(out, mdHeading{level: len(m[1]), text: m[2], line: i})
		}
	}
	return out
}

// mdSection returns the text of the first heading of the given level whose text
// satisfies match, from the heading line to the next heading of the same or a
// shallower level. ok is false when no such heading exists.
func mdSection(doc string, level int, match func(string) bool) (string, bool) {
	lines := strings.Split(doc, "\n")
	heads := mdHeadings(doc)
	for i, h := range heads {
		if h.level != level || !match(h.text) {
			continue
		}
		end := len(lines)
		for _, next := range heads[i+1:] {
			if next.level <= level {
				end = next.line
				break
			}
		}
		return strings.Join(lines[h.line:end], "\n"), true
	}
	return "", false
}

func requireSection(t *testing.T, doc string, level int, what string, match func(string) bool) string {
	t.Helper()
	section, ok := mdSection(doc, level, match)
	if !ok {
		t.Fatalf("missing level-%d heading for %s", level, what)
	}
	return section
}

func headingIs(want string) func(string) bool {
	return func(text string) bool { return text == want }
}

func headingStartsWith(prefix string) func(string) bool {
	return func(text string) bool { return strings.HasPrefix(text, prefix) }
}

func requireTutorial(t *testing.T, doc string) string {
	t.Helper()
	return requireSection(t, doc, 2, "the tutorial H2 "+strconv.Quote(docsTutorialHeading), headingIs(docsTutorialHeading))
}

func requireStep(t *testing.T, tutorial string, n int) string {
	t.Helper()
	prefix := "Step " + strconv.Itoa(n) + ":"
	return requireSection(t, tutorial, 3, strconv.Quote(prefix), headingStartsWith(prefix))
}

// collapseSpace folds every whitespace run to one space so a phrase pin survives
// hard-wrapped markdown.
func collapseSpace(s string) string { return strings.Join(strings.Fields(s), " ") }

func containsFold(s, sub string) bool {
	return strings.Contains(strings.ToLower(collapseSpace(s)), strings.ToLower(sub))
}

type mdBlock struct {
	path  []string // enclosing headings, outermost first
	lang  string   // fence language; empty for prose
	fence bool
	text  string
}

// mdBlocks splits a document into prose paragraphs and fenced code blocks, each
// tagged with the headings it sits under. Heading lines are not blocks.
func mdBlocks(doc string) []mdBlock {
	var (
		blocks []mdBlock
		stack  [7]string
		para   []string
		fence  []string
		lang   string
		inFen  bool
	)
	path := func() []string {
		var p []string
		for _, h := range stack {
			if h != "" {
				p = append(p, h)
			}
		}
		return p
	}
	flush := func() {
		if len(para) > 0 {
			blocks = append(blocks, mdBlock{path: path(), text: strings.Join(para, "\n")})
			para = nil
		}
	}
	for _, line := range strings.Split(doc, "\n") {
		if m := mdFenceLine.FindStringSubmatch(line); m != nil {
			if inFen {
				blocks = append(blocks, mdBlock{path: path(), lang: lang, fence: true, text: strings.Join(fence, "\n")})
				fence, inFen = nil, false
			} else {
				flush()
				lang, inFen = strings.TrimSpace(m[1]), true
			}
			continue
		}
		if inFen {
			fence = append(fence, line)
			continue
		}
		if m := mdHeadingLine.FindStringSubmatch(line); m != nil {
			flush()
			level := len(m[1])
			stack[level] = m[2]
			for deeper := level + 1; deeper < len(stack); deeper++ {
				stack[deeper] = ""
			}
			continue
		}
		if strings.TrimSpace(line) == "" {
			flush()
			continue
		}
		para = append(para, line)
	}
	flush()
	return blocks
}

// fenceCommands returns the logical shell commands of a code fence: backslash
// continuations joined, blank lines and comments dropped.
func fenceCommands(fence string) []string {
	var cmds []string
	var current string
	for _, raw := range strings.Split(fence, "\n") {
		line := strings.TrimSpace(raw)
		if current == "" && (line == "" || strings.HasPrefix(line, "#")) {
			continue
		}
		if strings.HasSuffix(line, "\\") {
			current += strings.TrimSpace(strings.TrimSuffix(line, "\\")) + " "
			continue
		}
		cmds = append(cmds, strings.TrimSpace(current+line))
		current = ""
	}
	if strings.TrimSpace(current) != "" {
		cmds = append(cmds, strings.TrimSpace(current))
	}
	return cmds
}

// mdSlug is the GitHub heading anchor: lowercase, punctuation dropped, spaces to
// hyphens.
func mdSlug(heading string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(heading) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_':
			b.WriteRune(r)
		case r == ' ':
			b.WriteRune('-')
		}
	}
	return b.String()
}

// mdAnchors is every heading anchor of the document; repeated headings get the
// -1, -2 suffixes GitHub assigns.
func mdAnchors(doc string) map[string]bool {
	anchors := map[string]bool{}
	seen := map[string]int{}
	for _, h := range mdHeadings(doc) {
		slug := mdSlug(h.text)
		if n := seen[slug]; n > 0 {
			anchors[slug+"-"+strconv.Itoa(n)] = true
		} else {
			anchors[slug] = true
		}
		seen[slug]++
	}
	return anchors
}

// unfenced blanks the lines inside code fences so link scans ignore examples.
func unfenced(doc string) string {
	lines := strings.Split(doc, "\n")
	inFence := false
	for i, line := range lines {
		if mdFenceLine.MatchString(line) {
			inFence = !inFence
			lines[i] = ""
			continue
		}
		if inFence {
			lines[i] = ""
		}
	}
	return strings.Join(lines, "\n")
}

// --- binary helpers ---------------------------------------------------------

// binaryOutput runs the built binary and returns its combined output. The exit
// code must equal wantExit: `-h` and `help` exit 0, and only the bare
// `cenci incident` usage hint exits 2, so a crash or a changed contract cannot
// pass as usage text.
func binaryOutput(t *testing.T, wantExit int, args ...string) string {
	t.Helper()
	var out bytes.Buffer
	cmd := exec.Command(binaryPath, args...)
	cmd.Stdout, cmd.Stderr = &out, &out
	got := 0
	if err := cmd.Run(); err != nil {
		exitErr, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("%v: %v", args, err)
		}
		got = exitErr.ExitCode()
	}
	if got != wantExit {
		t.Fatalf("cenci %v exited %d, want %d; output:\n%s", args, got, wantExit, out.String())
	}
	return out.String()
}

var flagDefaultLine = regexp.MustCompile(`(?m)^  -([a-z0-9-]+)\b`)

// registeredFlags reads the flag names the binary registers for a verb from the
// defaults block of `-h`, so the check follows real registration.
func registeredFlags(t *testing.T, verb string, mustInclude ...string) []string {
	t.Helper()
	var names []string
	for _, m := range flagDefaultLine.FindAllStringSubmatch(binaryOutput(t, 0, "incident", verb, "-h"), -1) {
		names = append(names, m[1])
	}
	slices.Sort(names)
	for _, want := range mustInclude {
		if !slices.Contains(names, want) {
			t.Fatalf("incident %s -h lists %v, missing known flag %q: the parse is vacuous or the flag was renamed", verb, names, want)
		}
	}
	return names
}

func mentionsFlag(text, name string) bool {
	return regexp.MustCompile("--" + regexp.QuoteMeta(name) + "([^a-z0-9-]|$)").MatchString(text)
}

// --- P39: flags and verbs ---------------------------------------------------

func TestIncidentDocsListEveryRegisteredSetupAndVerifyFlag(t *testing.T) {
	setupFlags := registeredFlags(t, "setup", "config", "apply", "workspace-resource-id")
	verifyFlags := registeredFlags(t, "verify", "config", "propagation-wait")

	doc, readme := incidentDoc(t), watchReadme(t)
	cliReference := requireSection(t, readme, 2, "README's Local incident worker", headingStartsWith("Local incident worker"))
	// A missing tutorial leaves its surfaces empty, which fails only those
	// subtests, so the README and help surfaces are still checked.
	tutorial, _ := mdSection(doc, 2, headingIs(docsTutorialHeading))

	var helpSetupLine string
	for _, line := range strings.Split(binaryOutput(t, 0, "help"), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "incident setup ") {
			helpSetupLine = line
		}
	}

	surfaces := []struct {
		name  string
		text  string
		flags []string
	}{
		{"README CLI reference setup flags", cliReference, setupFlags},
		{"README CLI reference verify flags", cliReference, verifyFlags},
		{"tutorial setup flags", tutorial, setupFlags},
		{"tutorial verify flags", tutorial, verifyFlags},
		{"cenci help incident setup line", helpSetupLine, setupFlags},
	}
	for _, s := range surfaces {
		t.Run(s.name, func(t *testing.T) {
			if strings.TrimSpace(s.text) == "" {
				t.Fatalf("%s: the surface is missing or empty (tutorial H2 absent, or no matching `cenci help` line)", s.name)
			}
			for _, name := range s.flags {
				if !mentionsFlag(s.text, name) {
					t.Errorf("--%s is registered but not documented here", name)
				}
			}
		})
	}
}

// incidentHintPrefix is the fixed start of the bare `cenci incident` usage hint
// (incidentVerbHint in incident_cmd.go).
const incidentHintPrefix = "cenci incident: use"

// hintVerbs reads the verbs the bare `cenci incident` usage hint lists. The hint
// must start with incidentHintPrefix, so other output (a crash, a panic trace)
// is never scanned as if it were the usage text.
func hintVerbs(t *testing.T, hint string) []string {
	t.Helper()
	if !strings.HasPrefix(hint, incidentHintPrefix) {
		t.Fatalf("`cenci incident` output does not start with %q: %q", incidentHintPrefix, hint)
	}
	return regexp.MustCompile(`[a-z]+`).FindAllString(strings.TrimPrefix(hint, incidentHintPrefix), -1)
}

func TestIncidentUsageTextListsEveryVerb(t *testing.T) {
	verbs := []string{"run", "verify", "status", "cancel", "setup"}

	t.Run("bare incident usage", func(t *testing.T) {
		listed := hintVerbs(t, binaryOutput(t, 2, "incident"))
		for _, verb := range verbs {
			if !slices.Contains(listed, verb) {
				t.Errorf("`cenci incident` usage does not list %q (lists %v)", verb, listed)
			}
		}
	})

	t.Run("cenci help incident line", func(t *testing.T) {
		line := regexp.MustCompile(`(?m)^\s+incident ([a-z]+(?:\|[a-z]+)+)\s`).FindStringSubmatch(binaryOutput(t, 0, "help"))
		if line == nil {
			t.Fatal("`cenci help` has no `incident verb|verb|...` line")
		}
		listed := strings.Split(line[1], "|")
		for _, verb := range verbs {
			if !slices.Contains(listed, verb) {
				t.Errorf("`cenci help` incident line does not list %q (lists %v)", verb, listed)
			}
		}
	})
}

// --- P36: the seven headed steps --------------------------------------------

func TestIncidentTutorialHasTheSevenHeadedSteps(t *testing.T) {
	tutorial := requireTutorial(t, incidentDoc(t))

	stepHeading := regexp.MustCompile(`^Step ([0-9]+): `)
	var numbers []int
	var headings []string
	for _, h := range mdHeadings(tutorial) {
		if h.level == 3 {
			if m := stepHeading.FindStringSubmatch(h.text); m != nil {
				n, _ := strconv.Atoi(m[1])
				numbers = append(numbers, n)
				headings = append(headings, h.text)
			}
		}
	}
	if len(numbers) != len(tutorialStepKeywords) {
		t.Fatalf("tutorial has %d `### Step N:` headings, want exactly %d: %v", len(numbers), len(tutorialStepKeywords), headings)
	}
	for i, n := range numbers {
		if n != i+1 {
			t.Errorf("step heading %d is numbered %d, want %d in order", i, n, i+1)
		}
		if !containsFold(headings[i], tutorialStepKeywords[i]) {
			t.Errorf("step %d heading %q lacks its topic keyword %q", i+1, headings[i], tutorialStepKeywords[i])
		}
	}

	t.Run("step 4 shows the non-secret worker JSON", func(t *testing.T) {
		step4 := requireStep(t, tutorial, 4)
		secretField := regexp.MustCompile(`(?i)"[a-z_]*(secret|password|privatekey|private_key)[a-z_]*"\s*:`)
		found := false
		for _, b := range mdBlocks(step4) {
			if b.fence && b.lang == "json" && strings.Contains(b.text, `"auth"`) {
				found = true
				if secretField.MatchString(b.text) {
					t.Errorf("the auth JSON carries a secret-shaped field:\n%s", b.text)
				}
			}
		}
		if !found {
			t.Error("step 4 has no json fence containing \"auth\"")
		}
		if strings.Contains(step4, "PRIVATE KEY") {
			t.Error("step 4 contains PRIVATE KEY material")
		}
	})

	t.Run("step 6 covers the fired and resolved smoke test", func(t *testing.T) {
		step6 := requireStep(t, tutorial, 6)
		for _, want := range []string{"Data Sender", "Fired", "Resolved"} {
			if !strings.Contains(step6, want) {
				t.Errorf("step 6 does not mention %q", want)
			}
		}
	})
}

// --- P37, P56: credential chains only for the sender or setup operator -------

var credentialChain = regexp.MustCompile("DefaultAzureCredential|az login")

// credentialChainViolations lists every block that mentions an ambient credential
// chain without being in an allowed context.
func credentialChainViolations(doc string, allowed func(mdBlock) bool) (violations []string, allowedMentions int) {
	for _, b := range mdBlocks(doc) {
		if !credentialChain.MatchString(b.text) {
			continue
		}
		if allowed(b) {
			allowedMentions++
			continue
		}
		violations = append(violations, strings.Join(b.path, " > ")+": "+collapseSpace(b.text))
	}
	return violations, allowedMentions
}

// allowedUnderHeadings allows a block that sits under the tutorial steps where
// the setup operator or the staging sender legitimately logs in, or under the
// setup preview/apply reference sections.
func allowedUnderHeadings(b mdBlock) bool {
	for _, h := range b.path {
		for _, step := range []string{"Step 1:", "Step 2:", "Step 3:", "Step 6:"} {
			if strings.HasPrefix(h, step) {
				return true
			}
		}
		if h == "Setup preview" || h == "Setup apply" {
			return true
		}
	}
	return false
}

func TestIncidentDocsMentionCredentialChainsOnlyForTheSenderOrSetupOperator(t *testing.T) {
	t.Run("incident-worker.md", func(t *testing.T) {
		violations, allowedMentions := credentialChainViolations(incidentDoc(t), allowedUnderHeadings)
		for _, v := range violations {
			t.Errorf("credential chain outside the sender/setup-operator context: %s", v)
		}
		if allowedMentions == 0 {
			t.Error("no allowed mention found: the scan is vacuous or the sender/setup context vanished")
		}
	})

	t.Run("watch README", func(t *testing.T) {
		readme := requireSection(t, watchReadme(t), 2, "README's Local incident worker", headingStartsWith("Local incident worker"))
		violations, _ := credentialChainViolations(readme, func(b mdBlock) bool {
			return strings.Contains(b.text, "cenci incident setup")
		})
		for _, v := range violations {
			t.Errorf("README mentions a credential chain outside `cenci incident setup`: %s", v)
		}
	})

	t.Run("a worker runtime credential chain is rejected", func(t *testing.T) {
		synthetic := "## " + docsTutorialHeading + "\n\n" +
			"### Step 4: Service account and environment\n\n" +
			"The worker authenticates with `DefaultAzureCredential` at run time.\n"
		violations, allowedMentions := credentialChainViolations(synthetic, allowedUnderHeadings)
		if len(violations) != 1 || allowedMentions != 0 {
			t.Fatalf("checker accepted a worker runtime credential chain: violations=%v allowed=%d", violations, allowedMentions)
		}
		control := strings.Replace(synthetic, "Step 4: Service account and environment", "Step 6: Staging smoke test", 1)
		if violations, allowedMentions = credentialChainViolations(control, allowedUnderHeadings); len(violations) != 0 || allowedMentions != 1 {
			t.Fatalf("checker rejected the sender-only control: violations=%v allowed=%d", violations, allowedMentions)
		}
	})
}

// --- P38, P63: hosted identity and sandbox.azure are separate ---------------

func TestIncidentDocsSeparateHostedIdentityFromTheLocalTutorial(t *testing.T) {
	doc := incidentDoc(t)

	t.Run("hosted section is its own H2 and states the Docker limit", func(t *testing.T) {
		hosted := requireSection(t, doc, 2, "the hosted H2 "+strconv.Quote(docsHostedHeading), headingIs(docsHostedHeading))
		// The tutorial range ends at the next H2, so the hosted H2 is outside it by
		// construction; the real structural claim is that both are H2s and the
		// hosted one comes after the tutorial, never nested under it.
		var h2s []string
		for _, h := range mdHeadings(doc) {
			if h.level == 2 {
				h2s = append(h2s, h.text)
			}
		}
		tutorialAt, hostedAt := slices.Index(h2s, docsTutorialHeading), slices.Index(h2s, docsHostedHeading)
		if tutorialAt < 0 || hostedAt <= tutorialAt {
			t.Errorf("the hosted H2 must follow the tutorial H2 in heading order: tutorial at %d, hosted at %d in %v", tutorialAt, hostedAt, h2s)
		}
		if tutorial, ok := mdSection(doc, 2, headingIs(docsTutorialHeading)); ok && strings.Contains(tutorial, docsHostedHeading) {
			t.Error("the tutorial range mentions the hosted section's exact heading text")
		}
		for _, want := range []string{"managed identity", "workload identity federation", "does not work on an arbitrary local Docker host"} {
			if !containsFold(hosted, want) {
				t.Errorf("hosted section does not mention %q", want)
			}
		}
	})

	t.Run("sandbox README says sandbox.azure is unrelated", func(t *testing.T) {
		azure := requireSection(t, readRepoFile(t, "sandbox/README.md"), 3, "sandbox README's Azure CLI", headingIs("Azure CLI"))
		if !containsFold(azure, "unrelated to the incident worker") {
			t.Error("sandbox/README.md's Azure CLI section does not say it is unrelated to the incident worker")
		}
	})

	t.Run("step 1 keeps the bridge identity separate from the worker", func(t *testing.T) {
		step1 := requireStep(t, requireTutorial(t, doc), 1)
		for _, want := range []string{"Data Sender", "separate from the worker identity"} {
			if !containsFold(step1, want) {
				t.Errorf("step 1 does not state %q", want)
			}
		}
	})
}

// --- P40: undo removes only journal-recorded roles and identity -------------

var (
	undoAllowed = []*regexp.Regexp{
		regexp.MustCompile(`^(cat|jq)\b.*setup-journal\.json`),
		regexp.MustCompile(`^az role assignment list\b`),
		regexp.MustCompile(`^az role assignment delete --ids\b`),
		regexp.MustCompile(`^az ad app show --id\b`),
		regexp.MustCompile(`^az ad app delete --id\b`),
	}
	undoForbidden = regexp.MustCompile(`\brm\b|\baz group\b|\baz servicebus\b|\baz monitor\b|stateDir`)
	// undoShellMeta is every construct that can chain, substitute or batch one
	// command into several: command substitution, backticks, `;`, `&&`, `||` and
	// `|` (a lone `&` too). A command carrying any of them is rejected, because
	// the allowlist only inspects the command's prefix.
	undoShellMeta = regexp.MustCompile("\\$\\(|`|[;&|]")
	// undoPlainJournalRead is the one command allowed to contain `|`: a jq filter
	// in single quotes (the pipe sits inside the quotes) over the absolute
	// setup-journal.json path, with nothing after it. `cat <journal>` has no
	// metacharacter and needs no exemption.
	undoPlainJournalRead = regexp.MustCompile(`^jq '[^'` + "`" + `$;&]*' /[^ '"` + "`" + `$;&|<>]*setup-journal\.json$`)
	// undoPlaceholder is a documentation token such as <principalId>; it is
	// stripped before undoRedirect rejects any remaining `<` or `>`.
	undoPlaceholder = regexp.MustCompile(`<[A-Za-z-]+>`)
	undoRedirect    = regexp.MustCompile(`[<>]`)
)

// undoViolations checks an `#### Undo a setup apply` section. It scans every
// fenced block whatever its info string (bash, sh, shell, console, text, none),
// so a command cannot hide in a fence the scan skips.
func undoViolations(undo string) []string {
	var problems []string
	var commands []string
	for _, b := range mdBlocks(undo) {
		if b.fence {
			commands = append(commands, fenceCommands(b.text)...)
		}
	}
	if len(commands) == 0 {
		return []string{"the undo section has no fenced commands"}
	}
	for _, cmd := range commands {
		if !slices.ContainsFunc(undoAllowed, func(re *regexp.Regexp) bool { return re.MatchString(cmd) }) {
			problems = append(problems, "outside the journal-derived allowlist: "+cmd)
		}
		if undoForbidden.MatchString(cmd) {
			problems = append(problems, "touches something the undo must never delete: "+cmd)
		}
		if undoShellMeta.MatchString(cmd) && !undoPlainJournalRead.MatchString(cmd) {
			problems = append(problems, "chains, pipes or substitutes commands (only a plain jq read of setup-journal.json may): "+cmd)
		}
		if undoRedirect.MatchString(undoPlaceholder.ReplaceAllString(cmd, "")) {
			problems = append(problems, "redirects input or output: "+cmd)
		}
	}
	showAt := slices.IndexFunc(commands, func(c string) bool { return strings.HasPrefix(c, "az ad app show") })
	deleteAt := slices.IndexFunc(commands, func(c string) bool { return strings.HasPrefix(c, "az ad app delete") })
	switch {
	case showAt < 0:
		problems = append(problems, "no `az ad app show` review before the app delete")
	case deleteAt >= 0 && showAt > deleteAt:
		problems = append(problems, "the `az ad app show` review comes after the first `az ad app delete`")
	}
	return problems
}

func TestIncidentTutorialUndoRemovesOnlyJournalRecordedRolesAndIdentityAfterReview(t *testing.T) {
	step7 := requireStep(t, requireTutorial(t, incidentDoc(t)), 7)
	undo := requireSection(t, step7, 4, "`#### Undo a setup apply`", headingIs("Undo a setup apply"))

	for _, problem := range undoViolations(undo) {
		t.Errorf("undo section: %s", problem)
	}

	t.Run("the guard rejects commands that hide from the prefix allowlist", func(t *testing.T) {
		fence := func(lang, body string) string { return "```" + lang + "\n" + body + "\n```\n" }
		const (
			review = "jq '{identity, steps: [.steps[] | {name}]}' /home/incident/.local/state/cenci/incidents/setup-journal.json"
			list   = "az role assignment list --assignee-object-id <principalId> --scope <scope> --role <role> -o json"
			del    = "az role assignment delete --ids <assignment-id>"
			show   = "az ad app show --id <clientId> --query displayName -o tsv"
			appDel = "az ad app delete --id <clientId>"

			forbiddenMessage = "touches something the undo must never delete"
		)
		control := fence("bash", review) + fence("bash", list) + fence("bash", del) + fence("bash", show) + fence("bash", appDel)
		if problems := undoViolations(control); len(problems) != 0 {
			t.Fatalf("the guard rejected a valid undo: %v", problems)
		}
		negatives := map[string]struct{ doc, want string }{
			"a shell-tagged group delete": {
				control + fence("shell", "az group delete --name app --yes"), forbiddenMessage,
			},
			"an untagged rm": {
				control + fence("", "rm -r /home/incident/.local/state/cenci/incidents"), forbiddenMessage,
			},
			"an allowlisted prefix that names stateDir": {
				control + fence("bash", "az role assignment list --scope /home/incident/stateDir"), forbiddenMessage,
			},
			"a jq read piped into a shell": {
				control + fence("bash", "jq '.' /home/incident/.local/state/cenci/incidents/setup-journal.json | sh"), "chains, pipes or substitutes",
			},
			"a redirect over the ledger": {
				control + fence("bash", "cat /home/incident/.local/state/cenci/incidents/setup-journal.json > /home/incident/.local/state/cenci/incidents/ledger.json"), "redirects input or output",
			},
			"a batch delete by command substitution": {
				fence("bash", review) + fence("bash", list) +
					fence("bash", `az role assignment delete --ids $(az role assignment list --scope <scope> --query "[].id" -o tsv)`) +
					fence("bash", show) + fence("bash", appDel), "chains, pipes or substitutes",
			},
			"a batch delete through a pipe": {
				control + fence("sh", "az role assignment list --scope <scope> -o tsv | xargs az role assignment delete --ids"), "chains, pipes or substitutes",
			},
			"an app delete before its review": {
				fence("bash", review) + fence("bash", list) + fence("bash", del) + fence("bash", appDel) + fence("bash", show),
				"comes after the first `az ad app delete`",
			},
		}
		for name, n := range negatives {
			problems := strings.Join(undoViolations(n.doc), "\n")
			if !strings.Contains(problems, n.want) {
				t.Errorf("%s: the guard did not flag it (want a problem mentioning %q): %q", name, n.want, problems)
			}
		}
	})

	t.Run("a review comes before the first delete", func(t *testing.T) {
		deleteAt := regexp.MustCompile(`az (role assignment|ad app) delete`).FindStringIndex(undo)
		if deleteAt == nil {
			t.Fatal("the undo section has no delete command")
		}
		before := undo[:deleteAt[0]]
		if !strings.Contains(strings.ToLower(before), "review") {
			t.Error("no review instruction precedes the first delete command")
		}
		if !strings.Contains(before, "az role assignment list") {
			t.Error("the exact assignment is not read with `az role assignment list` before the first delete")
		}
	})

	t.Run("states that no command deletes the ledger", func(t *testing.T) {
		if !containsFold(undo, "no command deletes the incident ledger") {
			t.Error(`the undo section does not say "no command deletes the incident ledger"`)
		}
	})
}

// --- P54: every parent criterion maps to a defined test ---------------------

var (
	criterionRow   = regexp.MustCompile(`(?m)^\|\s*P([0-9]+)\s*\|.*$`)
	backtickedTest = regexp.MustCompile("`(Test[A-Za-z0-9_]+)`")
	testFuncDef    = regexp.MustCompile(`(?m)^func (Test[A-Za-z0-9_]+)\(`)
)

// verificationStepCriteria are the rows whose evidence is a gate run, a live
// staging record or the plan-fidelity cross-check rather than a test (P52, P53,
// P55). P53 in particular forbids claiming mocked tests prove live Azure.
var verificationStepCriteria = map[int]bool{52: true, 53: true, 55: true}

// definedTestNames is every `func TestXxx(` in a watch/**/*_test.go file.
func definedTestNames(t *testing.T) map[string]bool {
	t.Helper()
	root := docsWatchDir(t)
	defined := map[string]bool{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, m := range testFuncDef.FindAllStringSubmatch(string(src), -1) {
			defined[m[1]] = true
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s for test definitions: %v", root, err)
	}
	if len(defined) == 0 {
		t.Fatalf("no test functions found under %s: the scan is vacuous", root)
	}
	return defined
}

func TestIncidentAutomatedVerificationMapsEveryParentCriterionToADefinedTest(t *testing.T) {
	section := requireSection(t, incidentDoc(t), 2, "## "+docsVerifyHeading, headingIs(docsVerifyHeading))
	defined := definedTestNames(t)

	t.Run("criterion table", func(t *testing.T) {
		rows := map[int][]string{}
		for _, m := range criterionRow.FindAllStringSubmatch(section, -1) {
			n, _ := strconv.Atoi(m[1])
			rows[n] = append(rows[n], m[0])
		}
		if len(rows) == 0 {
			t.Fatalf("%q has no `| P<n> |` criterion rows (P1..P%d expected)", docsVerifyHeading, docsParentCriteria)
		}
		for n := 1; n <= docsParentCriteria; n++ {
			switch len(rows[n]) {
			case 0:
				t.Errorf("criterion P%d has no row", n)
				continue
			case 1:
			default:
				t.Errorf("criterion P%d has %d rows, want exactly one", n, len(rows[n]))
				continue
			}
			row := rows[n][0]
			isVerificationStep := strings.Contains(strings.ToLower(row), "verification step")
			switch {
			case verificationStepCriteria[n] && !isVerificationStep:
				t.Errorf("P%d must be marked `verification step` (its evidence is not a test): %s", n, row)
			case !verificationStepCriteria[n] && isVerificationStep:
				t.Errorf("P%d is marked `verification step`; only P52, P53 and P55 may be: %s", n, row)
			case !verificationStepCriteria[n] && len(backtickedTest.FindAllString(row, -1)) == 0:
				t.Errorf("P%d names no test: %s", n, row)
			}
		}
		for n := range rows {
			if n < 1 || n > docsParentCriteria {
				t.Errorf("row P%d is outside P1..P%d", n, docsParentCriteria)
			}
		}
	})

	t.Run("every named test is defined", func(t *testing.T) {
		names := backtickedTest.FindAllStringSubmatch(section, -1)
		if len(names) == 0 {
			t.Fatal("the section names no tests: the scan is vacuous")
		}
		for _, m := range names {
			if !defined[m[1]] {
				t.Errorf("%s is named in %q but no watch/**/*_test.go defines it", m[1], docsVerifyHeading)
			}
		}
	})
}

// --- anchors ----------------------------------------------------------------

var (
	inDocLink     = regexp.MustCompile(`\]\(#([^)\s]+)\)`)
	readmeDocLink = regexp.MustCompile(`incident-worker\.md#([A-Za-z0-9_-]+)`)
)

func TestIncidentDocsAnchorsResolve(t *testing.T) {
	doc := incidentDoc(t)
	anchors := mdAnchors(doc)

	// The anchors other files and the `run` diagnostic link to must survive any
	// restructure of the doc.
	for _, want := range []string{"worker-identity", "setup-preview", "setup-apply", "verifying-the-identity"} {
		if !anchors[want] {
			t.Errorf("anchor #%s no longer resolves (README and the run diagnostic link to it)", want)
		}
	}

	inDoc := inDocLink.FindAllStringSubmatch(unfenced(doc), -1)
	if len(inDoc) == 0 {
		t.Error("no in-doc links found: the scan is vacuous")
	}
	for _, m := range inDoc {
		if !anchors[m[1]] {
			t.Errorf("incident-worker.md links to #%s, which is not a heading", m[1])
		}
	}

	fromReadme := readmeDocLink.FindAllStringSubmatch(unfenced(watchReadme(t)), -1)
	if len(fromReadme) == 0 {
		t.Error("no README links into incident-worker.md found: the scan is vacuous")
	}
	for _, m := range fromReadme {
		if !anchors[m[1]] {
			t.Errorf("watch/README.md links to incident-worker.md#%s, which is not a heading", m[1])
		}
	}
}
