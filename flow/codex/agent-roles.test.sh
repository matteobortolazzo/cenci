#!/usr/bin/env bash
# agent-roles.test.sh -- Codex agent-role install, override and migration (#1191).
#
# Drives the real flow/codex/install-agents.sh (invoked as `sh`, like configure does)
# against temp roots and compares bytes with `cmp`, so a refusal or a skipped file is
# proven to leave the original bytes unchanged. Covers:
#   - the six normal role templates omit model/model_reasoning_effort and validate;
#     domain-specialist keeps its pinned escalation model
#   - this repo's .codex/agents copies are byte-identical to the templates
#   - --role-override writes model (and effort when given) into NEWLY created files
#     only; every malformed value, unknown or duplicate role is refused with no file
#     (and no directory) written
#   - a pre-existing role file stays byte-identical across install, override, a
#     migration preview and an unapproved or ineligible apply
#   - --migrate-preview lists only files whose model AND effort exactly match a
#     shipped default for that role; --migrate-apply removes exactly those two lines
#     and re-checks eligibility at apply time
#   - native-agent-roles.test.sh prints `not run:` and exits 0 when
#     CENCI_CODEX_NATIVE is unset or codex is not on PATH
set -uo pipefail
FLOW="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)" || { echo "agent-roles.test.sh: failed to resolve flow directory." >&2; exit 2; }
REPO_ROOT="$(cd "$FLOW/.." && pwd)" || { echo "agent-roles.test.sh: failed to resolve repo root." >&2; exit 2; }
INSTALL="$FLOW/codex/install-agents.sh"
VALIDATE="$FLOW/codex/validate-agent-roles.sh"
RUNNER="$FLOW/codex/native-agent-roles.test.sh"
TEMPLATES="$FLOW/templates/codex/agent-roles"
NORMAL_ROLES="planner implementer critical-reviewer acceptance-reviewer context-gatherer structure-analyzer"
ALL_ROLES="$NORMAL_ROLES domain-specialist"
T="$(mktemp -d)" || { echo "agent-roles.test.sh: mktemp failed." >&2; exit 2; }
trap 'rm -rf "$T"' EXIT
failures=0
n=0

fail() { echo "FAIL: $1" >&2; failures=$((failures+1)); }

# install <root> [args...] -- runs install-agents.sh as sh; sets RC, $T/out, $T/err.
install() {
  local root="$1"; shift
  PLUGIN_ROOT="$FLOW" sh "$INSTALL" "$root" "$@" >"$T/out" 2>"$T/err"
  RC=$?
}

# fresh_root -- a new empty temp root path (the directory exists, .codex does not).
fresh_root() {
  n=$((n+1))
  mkdir -p "$T/root$n" || { echo "agent-roles.test.sh: mkdir failed." >&2; exit 2; }
  ROOT="$T/root$n"
  AGENTS="$ROOT/.codex/agents"
}

expect_rc() { [[ "$RC" -eq "$2" ]] || fail "$1: expected exit $2, got $RC (stderr: $(cat "$T/err"))"; }
expect_nonzero() { [[ "$RC" -ne 0 ]] || fail "$1: expected a non-zero exit, got 0"; }
expect_same() { # <label> <file-a> <file-b> -- byte-identical, both present
  if [[ ! -f "$2" || ! -f "$3" ]]; then fail "$1: a compared file is missing ($2 / $3)"; return; fi
  cmp -s "$2" "$3" || fail "$1: bytes differ ($2 vs $3)"
}
expect_has_line() { # <label> <file> <exact line>
  if [[ ! -f "$2" ]]; then fail "$1: $2 is missing"; return; fi
  grep -qxF -- "$3" "$2" || fail "$1: $2 lacks the exact line: $3"
}
expect_stderr_has() { grep -qF -- "$2" "$T/err" || fail "$1: stderr lacks '$2' (got: $(cat "$T/err"))"; }
expect_stdout_has() { grep -qF -- "$2" "$T/out" || fail "$1: stdout lacks '$2' (got: $(cat "$T/out"))"; }
expect_stdout_lacks() { if grep -qF -- "$2" "$T/out"; then fail "$1: stdout must not contain '$2'"; fi; }
expect_no_agents_dir() { [[ ! -e "$ROOT/.codex" ]] || fail "$1: a refusal must not create $ROOT/.codex"; }
expect_no_stray_files() { # <label> -- only role *.toml files in the agents dir
  local stray
  stray="$(find "$AGENTS" -mindepth 1 ! -name '*.toml' -print)" || { fail "$1: could not scan $AGENTS"; return; }
  [[ -z "$stray" ]] || fail "$1: leftover non-role files in the agents dir: $stray"
}

# snapshot <dir> <snapdir> / expect_snapshot -- byte-identity of every role file.
snapshot() { rm -rf "$2"; mkdir -p "$2" && cp -p "$1"/*.toml "$2"/; }
expect_snapshot() { # <label> <dir> <snapdir>
  local f base
  for f in "$3"/*.toml; do
    base="$(basename "$f")"
    expect_same "$1 ($base)" "$f" "$2/$base"
  done
}

# write_role <dir> <name> <pin-lines|""> -- a user-owned role file; pin lines go after description.
write_role() {
  local dir="$1" name="$2" pins="$3"
  {
    printf 'name = "%s"\n' "$name"
    printf 'description = "user-owned %s"\n' "$name"
    if [[ -n "$pins" ]]; then printf '%s\n' "$pins"; fi
    printf 'developer_instructions = "user text for %s"\n' "$name"
  } > "$dir/$name.toml"
}

# ── 1. Templates omit the pins and validate ───────────────────────────
for role in $NORMAL_ROLES; do
  f="$TEMPLATES/$role.toml"
  if [[ ! -f "$f" ]]; then fail "template $role: $f is missing"; continue; fi
  if grep -qE '^model(_reasoning_effort)?[[:space:]]*=' "$f"; then
    fail "template $role: must omit model and model_reasoning_effort so the role inherits"
  fi
done
expect_has_line "template domain-specialist keeps its pinned model" "$TEMPLATES/domain-specialist.toml" 'model = "gpt-6-astra"'
expect_has_line "template domain-specialist keeps its pinned effort" "$TEMPLATES/domain-specialist.toml" 'model_reasoning_effort = "high"'
bash "$VALIDATE" "$TEMPLATES" >"$T/out" 2>"$T/err"; RC=$?
expect_rc "templates validate" 0

# ── 2. This repo's installed copies are byte-identical to the templates ─
for role in $ALL_ROLES; do
  expect_same "repo .codex/agents/$role.toml matches its template" "$REPO_ROOT/.codex/agents/$role.toml" "$TEMPLATES/$role.toml"
done

# ── 3. Plain install: templates verbatim, no model keys on the normal roles ─
fresh_root
install "$ROOT"
expect_rc "plain install" 0
for role in $ALL_ROLES; do
  expect_same "plain install $role" "$AGENTS/$role.toml" "$TEMPLATES/$role.toml"
done
expect_no_stray_files "plain install"
snapshot "$AGENTS" "$T/snap-plain"
install "$ROOT"
expect_rc "second plain install" 0
expect_snapshot "second plain install leaves every file" "$AGENTS" "$T/snap-plain"

# ── 4. Overrides: written to newly created files only ─────────────────
fresh_root
install "$ROOT" --role-override planner=gpt-test.1:low --role-override implementer=custom-model_2
expect_rc "override install" 0
{
  sed -n '1,2p' "$TEMPLATES/planner.toml"
  printf 'model = "gpt-test.1"\nmodel_reasoning_effort = "low"\n'
  sed -n '3,$p' "$TEMPLATES/planner.toml"
} > "$T/expected-planner.toml"
expect_same "planner override renders model and effort after description" "$AGENTS/planner.toml" "$T/expected-planner.toml"
{
  sed -n '1,2p' "$TEMPLATES/implementer.toml"
  printf 'model = "custom-model_2"\n'
  sed -n '3,$p' "$TEMPLATES/implementer.toml"
} > "$T/expected-implementer.toml"
expect_same "model-only override writes no effort key" "$AGENTS/implementer.toml" "$T/expected-implementer.toml"
for role in critical-reviewer acceptance-reviewer context-gatherer structure-analyzer domain-specialist; do
  expect_same "role without an override is the plain template ($role)" "$AGENTS/$role.toml" "$TEMPLATES/$role.toml"
done
bash "$VALIDATE" "$AGENTS" >"$T/out" 2>"$T/err"; RC=$?
expect_rc "overridden roles still validate" 0

# An override replaces an existing template pin (domain-specialist) instead of duplicating it.
fresh_root
install "$ROOT" --role-override domain-specialist=other-model:medium
expect_rc "override of a pinned template" 0
expect_has_line "domain-specialist override model" "$AGENTS/domain-specialist.toml" 'model = "other-model"'
expect_has_line "domain-specialist override effort" "$AGENTS/domain-specialist.toml" 'model_reasoning_effort = "medium"'
[[ "$(grep -c '^model = ' "$AGENTS/domain-specialist.toml")" -eq 1 ]] || fail "domain-specialist override must replace the pin, not add a second model line"
[[ "$(grep -c '^model_reasoning_effort = ' "$AGENTS/domain-specialist.toml")" -eq 1 ]] || fail "domain-specialist override must replace the effort, not add a second effort line"

# A model-only override of a pinned template keeps the template's own effort.
fresh_root
install "$ROOT" --role-override domain-specialist=other-model
expect_rc "model-only override of a pinned template" 0
expect_has_line "model-only domain-specialist override model" "$AGENTS/domain-specialist.toml" 'model = "other-model"'
expect_has_line "model-only domain-specialist override keeps the template effort" "$AGENTS/domain-specialist.toml" 'model_reasoning_effort = "high"'

# ── 5. An override for an existing file is skipped with a notice ──────
fresh_root
mkdir -p "$AGENTS"
write_role "$AGENTS" planner ""
snapshot "$AGENTS" "$T/snap-existing"
install "$ROOT" --role-override planner=gpt-test:high --role-override implementer=gpt-test2
expect_rc "override on an existing file" 0
expect_stderr_has "override on an existing file" "planner: role file already exists; override not applied"
expect_snapshot "existing planner.toml survives an override" "$AGENTS" "$T/snap-existing"
expect_has_line "the new role still gets its override" "$AGENTS/implementer.toml" 'model = "gpt-test2"'

# ── 6. Refusals: non-zero, no file and no directory written ───────────
refuse() { # <label> <install args...>
  local label="$1"; shift
  fresh_root
  install "$ROOT" "$@"
  expect_nonzero "refuse: $label"
  expect_no_agents_dir "refuse: $label"
}
refuse "no equals sign" --role-override planner
refuse "empty model" --role-override planner=
refuse "empty model with effort" --role-override planner=:high
refuse "unknown role" --role-override reviewer=gpt-test
refuse "empty role" --role-override =gpt-test
refuse "model with a space" --role-override "planner=gpt test"
refuse "model with a quote" --role-override 'planner=gpt"test'
refuse "model with a shell expansion" --role-override 'planner=$(id)'
refuse "model with a newline" --role-override "planner=gpt
injected = 1"
refuse "model with a TOML injection" --role-override 'planner=x"
injected = "1'
refuse "uppercase effort" --role-override planner=gpt-test:High
refuse "effort with a digit" --role-override planner=gpt-test:high1
refuse "effort with a dash" --role-override planner=gpt-test:hi-gh
refuse "empty effort" --role-override planner=gpt-test:
refuse "second colon in effort" --role-override planner=gpt-test:high:low
refuse "duplicate role" --role-override planner=gpt-a --role-override planner=gpt-b
refuse "valid override followed by a malformed one" --role-override implementer=gpt-ok --role-override planner='bad value'
refuse "missing override argument" --role-override
refuse "unknown option" --bogus-flag
refuse "override combined with a preview" --migrate-preview --role-override planner=gpt-test
# A refusal never touches an existing agents directory either.
fresh_root
mkdir -p "$AGENTS"
write_role "$AGENTS" planner ""
snapshot "$AGENTS" "$T/snap-refuse"
install "$ROOT" --role-override implementer=ok-model --role-override planner='no good'
expect_nonzero "refusal beside an existing dir"
expect_snapshot "refusal beside an existing dir" "$AGENTS" "$T/snap-refuse"
[[ ! -e "$AGENTS/implementer.toml" ]] || fail "a refusal must not create implementer.toml from the valid override"

# ── 7. User files stay byte-identical across every mode ───────────────
fresh_root
mkdir -p "$AGENTS"
write_role "$AGENTS" planner 'model = "gpt-5.6"
model_reasoning_effort = "high"'
write_role "$AGENTS" implementer 'model = "my-own-model"
model_reasoning_effort = "low"'
write_role "$AGENTS" critical-reviewer ""
write_role "$AGENTS" domain-specialist 'model = "gpt-6-astra"
model_reasoning_effort = "high"'
snapshot "$AGENTS" "$T/snap-user"
install "$ROOT"
expect_rc "user files: install" 0
expect_snapshot "install" "$AGENTS" "$T/snap-user"
install "$ROOT" --role-override planner=a-model:low --role-override implementer=b-model --role-override critical-reviewer=c-model --role-override domain-specialist=d-model
expect_rc "user files: override install" 0
expect_snapshot "override install" "$AGENTS" "$T/snap-user"
install "$ROOT" --migrate-preview
expect_rc "user files: preview" 0
expect_snapshot "preview" "$AGENTS" "$T/snap-user"
install "$ROOT" --migrate-apply implementer
expect_rc "user files: apply of a user-chosen pin" 2
expect_snapshot "unapproved apply of a user-chosen pin" "$AGENTS" "$T/snap-user"
install "$ROOT" --migrate-apply planner implementer
expect_rc "user files: apply with one ineligible role" 2
expect_snapshot "apply with one ineligible role writes nothing" "$AGENTS" "$T/snap-user"
install "$ROOT" --migrate-apply
expect_nonzero "user files: apply without roles"
expect_snapshot "apply without roles" "$AGENTS" "$T/snap-user"
# The files the install created (the other roles) were not part of the snapshot; only they are new.
for role in acceptance-reviewer context-gatherer structure-analyzer; do
  [[ -f "$AGENTS/$role.toml" ]] || fail "install must still create the missing role $role"
done

# ── 8. Migration fixtures ─────────────────────────────────────────────
fresh_root
mkdir -p "$AGENTS"
write_role "$AGENTS" planner 'model = "gpt-5.6"
model_reasoning_effort = "high"'                       # eligible (earlier shipped pair)
write_role "$AGENTS" implementer 'model = "gpt-6.1-sol"
model_reasoning_effort = "high"'                       # eligible (current shipped pair)
write_role "$AGENTS" critical-reviewer 'model = "my-own-model"
model_reasoning_effort = "high"'                       # user-chosen model: never offered
write_role "$AGENTS" acceptance-reviewer 'model = "gpt-5.6"
model_reasoning_effort = "high"'                       # acceptance-reviewer never shipped gpt-5.6: never offered
write_role "$AGENTS" context-gatherer 'model = "gpt-5.6-terra"
model_reasoning_effort = "medium"'                     # eligible
write_role "$AGENTS" structure-analyzer ""             # no pin: never offered
write_role "$AGENTS" domain-specialist 'model = "gpt-6-astra"
model_reasoning_effort = "high"'                       # no shipped default: never offered
snapshot "$AGENTS" "$T/snap-mig"
install "$ROOT" --migrate-preview
expect_rc "preview" 0
for role in planner implementer context-gatherer; do
  expect_stdout_has "preview lists $role" "--- a/$role.toml"
done
for role in critical-reviewer acceptance-reviewer structure-analyzer domain-specialist; do
  expect_stdout_lacks "preview omits $role" "$role.toml"
done
expect_stdout_has "preview shows the model line as removed" '-model = "gpt-5.6"'
expect_stdout_has "preview shows the effort line as removed" '-model_reasoning_effort = "high"'
expect_snapshot "preview is read-only" "$AGENTS" "$T/snap-mig"
expect_no_stray_files "preview"

# A pin only counts at the top level, and a mismatched effort is a user choice.
fresh_root
mkdir -p "$AGENTS"
write_role "$AGENTS" structure-analyzer ""
printf '[mcp_servers.x]\nmodel = "gpt-5.6-terra"\nmodel_reasoning_effort = "medium"\n' >> "$AGENTS/structure-analyzer.toml"
write_role "$AGENTS" context-gatherer 'model = "gpt-5.6-terra"
model_reasoning_effort = "high"'
snapshot "$AGENTS" "$T/snap-mig2"
install "$ROOT" --migrate-preview
expect_rc "preview of ineligible shapes" 0
[[ ! -s "$T/out" ]] || fail "preview of ineligible shapes must print nothing (got: $(cat "$T/out"))"
install "$ROOT" --migrate-apply structure-analyzer
expect_rc "apply of a table-only pin" 2
install "$ROOT" --migrate-apply context-gatherer
expect_rc "apply of a mismatched effort" 2
expect_snapshot "ineligible shapes stay byte-identical" "$AGENTS" "$T/snap-mig2"

# Apply: removes exactly the two pin lines of the named roles and nothing else.
fresh_root
mkdir -p "$AGENTS"
write_role "$AGENTS" planner 'model = "gpt-5.6"
model_reasoning_effort = "high"'
write_role "$AGENTS" implementer 'model = "gpt-6.1-sol"
model_reasoning_effort = "high"'
write_role "$AGENTS" context-gatherer 'model = "gpt-5.6-terra"
model_reasoning_effort = "medium"'
write_role "$AGENTS" critical-reviewer 'model = "my-own-model"
model_reasoning_effort = "high"'
snapshot "$AGENTS" "$T/snap-apply"
install "$ROOT" --migrate-apply planner context-gatherer
expect_rc "apply of two eligible roles" 0
for role in planner context-gatherer; do
  sed -e '3,4d' "$T/snap-apply/$role.toml" > "$T/expected-$role.toml"
  expect_same "apply removes only the two pin lines ($role)" "$AGENTS/$role.toml" "$T/expected-$role.toml"
done
expect_same "apply leaves the unnamed eligible role (implementer)" "$AGENTS/implementer.toml" "$T/snap-apply/implementer.toml"
expect_same "apply leaves the user-chosen pin (critical-reviewer)" "$AGENTS/critical-reviewer.toml" "$T/snap-apply/critical-reviewer.toml"
expect_no_stray_files "apply"
install "$ROOT" --migrate-preview
expect_stdout_has "implementer is still offered after the partial apply" "--- a/implementer.toml"
expect_stdout_lacks "an applied role is no longer offered (planner)" "planner.toml"
expect_stdout_lacks "an applied role is no longer offered (context-gatherer)" "context-gatherer.toml"
# Re-applying a role that is no longer eligible is refused.
install "$ROOT" --migrate-apply planner
expect_rc "re-apply of a migrated role" 2

# Eligibility is re-checked at apply time: a file changed after the preview is refused.
sed -e '3s/.*/model = "changed-after-preview"/' "$AGENTS/implementer.toml" > "$T/implementer-late.toml"
cp "$T/implementer-late.toml" "$AGENTS/implementer.toml"
snapshot "$AGENTS" "$T/snap-late"
install "$ROOT" --migrate-apply implementer
expect_rc "apply after the file changed" 2
expect_snapshot "apply after the file changed writes nothing" "$AGENTS" "$T/snap-late"
# Unknown, duplicate and unpinned-by-design roles are refused with nothing written.
install "$ROOT" --migrate-apply bogus
expect_rc "apply of an unknown role" 2
install "$ROOT" --migrate-apply domain-specialist
expect_rc "apply of domain-specialist (no shipped default)" 2
install "$ROOT" --migrate-apply critical-reviewer critical-reviewer
expect_rc "apply of a duplicate role" 2
expect_snapshot "refused applies write nothing" "$AGENTS" "$T/snap-late"
# Migration modes never create the agents directory.
fresh_root
install "$ROOT" --migrate-preview
expect_rc "preview with no agents dir" 0
expect_no_agents_dir "preview with no agents dir"
install "$ROOT" --migrate-apply planner
expect_rc "apply with no agents dir" 2
expect_no_agents_dir "apply with no agents dir"

# ── 9. The native runner is skipped, not failed, in CI ────────────────
if [[ ! -f "$RUNNER" ]]; then
  fail "runner: $RUNNER is missing"
else
  bash -n "$RUNNER" || fail "runner: bash -n failed"
  BASH_BIN="$(command -v bash)" || { echo "agent-roles.test.sh: bash not found." >&2; exit 2; }
  env -u CENCI_CODEX_NATIVE "$BASH_BIN" "$RUNNER" >"$T/out" 2>"$T/err"; RC=$?
  expect_rc "runner with CENCI_CODEX_NATIVE unset" 0
  expect_stdout_has "runner with CENCI_CODEX_NATIVE unset" "not run: "
  env CENCI_CODEX_NATIVE=0 "$BASH_BIN" "$RUNNER" >"$T/out" 2>"$T/err"; RC=$?
  expect_rc "runner with CENCI_CODEX_NATIVE=0" 0
  expect_stdout_has "runner with CENCI_CODEX_NATIVE=0" "not run: "
  # A PATH holding no codex: only dirname (the one tool the gate path may need) is linked in.
  NOCODEX="$T/nocodex-bin"
  mkdir -p "$NOCODEX"
  for tool in dirname; do
    real="$(command -v "$tool")" && ln -s "$real" "$NOCODEX/$tool"
  done
  env CENCI_CODEX_NATIVE=1 PATH="$NOCODEX" "$BASH_BIN" "$RUNNER" >"$T/out" 2>"$T/err"; RC=$?
  expect_rc "runner with codex absent" 0
  expect_stdout_has "runner with codex absent" "not run: codex is not on PATH"
fi

echo "agent-roles.test.sh: failures=${failures}"
[[ "$failures" -eq 0 ]]
