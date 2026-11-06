#!/usr/bin/env bash
# dispatch-target-invocable.test.sh — contract test for ticket #1155:
# "babysit's ci-repair/babysit-attention dispatch fails: both skills are
# user-invocable: false".
#
# `cenci run` (and therefore `cenci babysit`) dispatches work by launching the
# agent CLI with a slash/`$`-prefixed skill invocation built from the workflow
# name in watch/internal/run/template.go's builtinConfig(). A skill marked
# `user-invocable: false` is never exposed under that invocation shape, so the
# launched session silently receives an unknown command: the supervisor sees a
# clean process spawn, advances its retry counters, and no repair happens.
# That is exactly what #1155 observed for ci-repair on PR #1154.
#
# This test closes the gap by cross-checking the two sides against each other
# rather than restating either: it parses the workflow names out of
# template.go's real committed builtinConfig() and asserts that every one of
# them that is a flow skill (flow/skills/<name>/SKILL.md exists) does not
# carry `user-invocable: false`. Workflow names with no flow skill (a
# Codex-only adapter, or a future non-skill workflow) are reported as skipped,
# never silently dropped.
#
# Section 3 (#1236) adds the Codex half: every `codexWF("<name>")` target that
# is a flow skill must be portable (PORTABLE_SKILLS in opencode/install-skills.sh)
# or carry a non-empty codex.md plus the SKILL.md `Client dispatch` line naming
# it. Fixture self-tests prove each branch of that predicate can fail.
#
# Section 4 (#1217) adds the OpenCode half: template.go ships no built-in OpenCode
# workflow today (no `openCodeWF` helper), and KNOWN_HELPERS makes a reintroduced
# one fail as unrecognized. Section 4 is the latent guard for its return: every
# `openCodeWF("<name>")` target must be in the OpenCode delivery set
# (PORTABLE_SKILLS minus OPENCODE_RETIRED_SKILLS, both in
# opencode/install-skills.sh), so a retired `babysit` or a non-portable skill
# can never be a built-in OpenCode workflow. Zero OpenCode targets is valid.
#
# Sections 1-2 follow the fixture-free idiom of babysit-host-supervisor-contract.test.sh
# (REPO_ROOT resolved from FLOW_DIR/.., a `failures=` counter, failing closed
# on an unreadable source) — it greps the real committed sources, never a copy.
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)" || { echo "dispatch-target-invocable.test.sh: failed to resolve script directory." >&2; exit 2; }
FLOW_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)" || { echo "dispatch-target-invocable.test.sh: failed to resolve flow directory." >&2; exit 2; }
REPO_ROOT="$(cd "${FLOW_DIR}/.." && pwd)" || { echo "dispatch-target-invocable.test.sh: failed to resolve repository root." >&2; exit 2; }
failures=0

fail() { echo "FAIL: $1" >&2; failures=$((failures+1)); }

TEMPLATE="${REPO_ROOT}/watch/internal/run/template.go"

# required_skill_problem <flow-dir> <target> — a required launcher-driven target
# must be a readable flow skill. Pure (prints the reason and returns 1; never
# calls fail()), so it runs against the real tree and against fixtures. Without
# it the generic "no flow skill, skipped" path would let a deleted or renamed
# ci-repair/babysit-attention SKILL.md pass both the invocable and Codex checks.
required_skill_problem() {
  local skill="$1/skills/$2/SKILL.md"
  if [[ ! -r "${skill}" ]]; then
    echo "required dispatch target [$2]: flow/skills/$2/SKILL.md is missing or unreadable, so it would be silently skipped instead of checked"
    return 1
  fi
  return 0
}

# =====================================================================
# 1. Extract the dispatch targets from template.go's builtinConfig().
#
# Every built-in workflow entry is written as `"<name>": <agent>WF("<name>")`
# (claudeWF/codexWF today; openCodeWF returns only with built-in OpenCode
# workflows, see section 4). The helper-call argument is the token that
# actually becomes the invoked skill name, so that is what is parsed — the map
# key is only a lookup label. An empty parse fails closed: a silent zero-target
# sweep would let this test pass vacuously after a refactor of template.go.
# =====================================================================
if [[ ! -r "${TEMPLATE}" ]]; then
  fail "watch/internal/run/template.go: file not found/unreadable (dispatch targets cannot be verified)"
  echo "dispatch-target-invocable.test.sh: failures=${failures}"
  exit 1
fi

# Recognized per-agent template helpers. A future agent added via a helper
# not listed here would otherwise have its dispatch targets silently skipped
# by the extraction below, so the set is asserted rather than assumed: any
# `<word>WF(...)`-shaped call with an unrecognized prefix fails loudly.
# openCodeWF is deliberately absent: template.go ships no built-in OpenCode
# workflow, so a reintroduced openCodeWF(...) must be reviewed against section 4.
KNOWN_HELPERS='claudeWF codexWF'

# wf_calls_of <template.go> — every `<agent>WF("<name>")` call in the template,
# one per line. No match is valid (prints nothing, returns 0); only a grep error
# (exit > 1) prints the reason and returns 1.
wf_calls_of() {
  local template="$1" raw rc
  raw="$(grep -oE '\b[A-Za-z]+WF\("[a-z-]+"\)' "${template}")"
  rc=$?
  if [[ "${rc}" -gt 1 ]]; then
    echo "grep failed (exit ${rc}) reading ${template}"
    return 1
  fi
  [[ -z "${raw}" ]] || echo "${raw}"
}

# unknown_helpers_of <template.go> — the `<agent>WF` helper names used in the
# template that are not in KNOWN_HELPERS, one per line. Pure: prints them and
# returns 1 when any exist (or when grep itself errors); prints nothing and
# returns 0 when every helper is recognized.
unknown_helpers_of() {
  local template="$1" raw helper unknown=""
  raw="$(wf_calls_of "${template}")" || { echo "${raw}"; return 1; }
  while IFS= read -r helper; do
    [[ -n "${helper}" ]] || continue
    [[ " ${KNOWN_HELPERS} " == *" ${helper} "* ]] || unknown+="${helper}"$'\n'
  done < <(sed -E 's/^([A-Za-z]+WF)\(.*/\1/' <<<"${raw}" | sort -u)
  if [[ -n "${unknown}" ]]; then
    printf '%s' "${unknown}"
    return 1
  fi
  return 0
}

# all_targets_of <template.go> — every `<agent>WF("<name>")` target across all
# agents, one per line. Fails closed (prints the reason, returns 1) on a grep
# error or an empty parse.
all_targets_of() {
  local template="$1" raw targets
  raw="$(wf_calls_of "${template}")" || { echo "${raw}"; return 1; }
  targets="$(sed -E 's/.*\("([a-z-]+)"\)/\1/' <<<"${raw}" | sort -u | sed '/^$/d')"
  if [[ -z "${targets}" ]]; then
    echo "no <agent>WF(\"<name>\") dispatch targets parsed from ${template}"
    return 1
  fi
  echo "${targets}"
}

if ! UNKNOWN_HELPERS="$(unknown_helpers_of "${TEMPLATE}")"; then
  while IFS= read -r helper; do
    [[ -n "${helper}" ]] || continue
    fail "watch/internal/run/template.go: unrecognized workflow-template helper [${helper}()] — add it to this test's KNOWN_HELPERS so its dispatch targets are checked too (a new agent's targets must not slip past unchecked)"
  done <<<"${UNKNOWN_HELPERS}"
fi

if ! TARGETS_RAW="$(all_targets_of "${TEMPLATE}")"; then
  fail "watch/internal/run/template.go: ${TARGETS_RAW} — the built-in workflow shape changed and this test needs updating (failing closed rather than passing vacuously)"
  TARGETS=()
else
  mapfile -t TARGETS <<<"${TARGETS_RAW}"
fi

# Sanity floor: the two launcher-driven targets #1155 is about must be among
# the parsed set, so a parser that silently narrows can never hide them.
for required in ci-repair babysit-attention; do
  found=0
  for t in "${TARGETS[@]:-}"; do
    [[ "${t}" == "${required}" ]] && found=1
  done
  [[ "${found}" -eq 1 ]] || fail "watch/internal/run/template.go: expected dispatch target [${required}] not found among parsed targets [${TARGETS[*]:-}]"
  if ! problem="$(required_skill_problem "${FLOW_DIR}" "${required}")"; then
    fail "${problem}"
  fi
done

# =====================================================================
# 2. Every parsed target that is a flow skill must be user-invocable.
# =====================================================================
skipped=()
for wf in "${TARGETS[@]:-}"; do
  [[ -n "${wf}" ]] || continue
  skill="${FLOW_DIR}/skills/${wf}/SKILL.md"
  if [[ ! -r "${skill}" ]]; then
    skipped+=("${wf}")
    continue
  fi
  # Frontmatter field only: the check is anchored to a bare `user-invocable:`
  # line so a mention of the field inside prose or a fenced example can
  # neither satisfy nor trip it.
  if grep -qE '^user-invocable:[[:space:]]*false[[:space:]]*$' "${skill}"; then
    fail "flow/skills/${wf}/SKILL.md: 'user-invocable: false' but the skill is dispatched as a slash/\$-prefixed invocation by watch/internal/run/template.go — the launched session cannot call it (#1155)"
  fi
done

if [[ "${#skipped[@]}" -gt 0 ]]; then
  echo "dispatch-target-invocable.test.sh: note — dispatch targets with no flow skill (not checked): ${skipped[*]}"
fi

# =====================================================================
# 3. Every Codex dispatch target that is a flow skill must be runnable in
#    Codex (#1236): either portable (shipped as-is, no companion needed) or
#    carrying BOTH a non-empty codex.md companion and the SKILL.md dispatch
#    line naming it. Without one of those, `$cenci:<skill>` launches a Codex
#    session that either cannot find the skill or falls into the Claude-only
#    procedure. The check lives here, flow-side, because path-filtered CI
#    would not run watch tests on a flow-only PR.
#
# The predicate helpers below are pure (they print a reason and return 1;
# they never call fail()), so they run against the real tree AND against
# mktemp fixtures, and each caller records the failure in the parent shell
# (docs/shell-scripting-gotchas.md: no fail() from a command substitution).
# =====================================================================

# codex_targets_of <template.go> — the codexWF("<name>") targets, one per
# line. Parsed on their own (not from the all-agent TARGETS set) so a target
# that is only a claudeWF entry can never satisfy the Codex check.
codex_targets_of() {
  local template="$1" raw targets
  if ! raw="$(grep -oE '\bcodexWF\("[a-z-]+"\)' "${template}")"; then
    echo "no codexWF(\"<name>\") targets parsed from ${template}"
    return 1
  fi
  targets="$(sed -E 's/.*\("([a-z-]+)"\)/\1/' <<<"${raw}" | sort -u)"
  if [[ -z "${targets}" ]]; then
    echo "codexWF targets parsed empty from ${template}"
    return 1
  fi
  echo "${targets}"
}

# portable_skills_of <flow-dir> — the PORTABLE_SKILLS list of
# opencode/install-skills.sh, space-separated. Same source as
# skills/maintain/scripts/check.sh's portable_skills_list, but read fail-closed:
# the declaration must exist exactly once, be a closed double-quoted value on
# one line, and name at least one skill.
portable_skills_of() {
  local installer="$1/opencode/install-skills.sh" count decl rc list
  if [[ ! -r "${installer}" ]]; then
    echo "opencode/install-skills.sh unreadable"
    return 1
  fi
  count="$(grep -c '^PORTABLE_SKILLS=' "${installer}")"
  rc=$?
  if [[ "${rc}" -gt 1 ]]; then
    echo "opencode/install-skills.sh: grep failed (exit ${rc})"
    return 1
  fi
  if [[ "${count}" -eq 0 ]]; then
    echo "opencode/install-skills.sh: no ^PORTABLE_SKILLS= line (portable status unknown)"
    return 1
  fi
  if [[ "${count}" -gt 1 ]]; then
    echo "opencode/install-skills.sh: ${count} ^PORTABLE_SKILLS= lines (portable status ambiguous)"
    return 1
  fi
  decl="$(grep '^PORTABLE_SKILLS=' "${installer}")" || { echo "opencode/install-skills.sh: PORTABLE_SKILLS line unreadable"; return 1; }
  if [[ ! "${decl}" =~ ^PORTABLE_SKILLS=\"([^\"]*)\" ]]; then
    echo "opencode/install-skills.sh: PORTABLE_SKILLS is not a closed double-quoted single-line value"
    return 1
  fi
  list="${BASH_REMATCH[1]//[[:space:]]/ }"
  if [[ -z "${list// /}" ]]; then
    echo "opencode/install-skills.sh: PORTABLE_SKILLS is empty"
    return 1
  fi
  echo "${list}"
}

# codex_dispatch_needle <skill> — the standard dispatch line's fixed prefix.
codex_dispatch_needle() {
  printf '%s' "> **Client dispatch**: In Codex, read \`codex-runtime\` and \`${1}/codex.md\`"
}

# codex_target_problem <flow-dir> <target> <portable-list> — prints why the
# target is not runnable in Codex and returns 1; prints nothing and returns 0
# when it is portable, fully wired, or not a flow skill at all.
codex_target_problem() {
  local flow_dir="$1" target="$2" portable="$3"
  local skill="${flow_dir}/skills/${target}/SKILL.md"
  local companion="${flow_dir}/skills/${target}/codex.md"
  local needle line found=0
  [[ -n "${target}" ]] || { echo "empty Codex target name"; return 1; }
  [[ -r "${skill}" ]] || return 0
  [[ " ${portable} " == *" ${target} "* ]] && return 0
  if [[ ! -s "${companion}" ]] || ! grep -q '[^[:space:]]' "${companion}"; then
    echo "${target}: flow/skills/${target}/codex.md missing or empty, and ${target} is not in PORTABLE_SKILLS — \$cenci:${target} has no Codex procedure"
    return 1
  fi
  needle="$(codex_dispatch_needle "${target}")"
  while IFS= read -r line; do
    [[ "${line}" == "${needle}"* ]] && found=1
  done < <(grep -F -- "${needle}" "${skill}")
  if [[ "${found}" -ne 1 ]]; then
    echo "${target}: flow/skills/${target}/SKILL.md lacks the line starting [${needle}] — Codex would not be routed to codex.md"
    return 1
  fi
  return 0
}

# --- 3a. The real tree ------------------------------------------------
if ! CODEX_RAW="$(codex_targets_of "${TEMPLATE}")"; then
  fail "watch/internal/run/template.go: ${CODEX_RAW} — the built-in workflow shape changed and this test needs updating (failing closed rather than passing vacuously)"
  CODEX_TARGETS=()
else
  mapfile -t CODEX_TARGETS <<<"${CODEX_RAW}"
fi

for required in ci-repair babysit-attention; do
  found=0
  for t in "${CODEX_TARGETS[@]:-}"; do
    [[ "${t}" == "${required}" ]] && found=1
  done
  [[ "${found}" -eq 1 ]] || fail "watch/internal/run/template.go: expected Codex dispatch target [${required}] not found among parsed codexWF targets [${CODEX_TARGETS[*]:-}]"
done

if ! PORTABLE_LIST="$(portable_skills_of "${FLOW_DIR}")"; then
  fail "${PORTABLE_LIST} — cannot decide which Codex targets are portable (failing closed)"
else
  for t in "${CODEX_TARGETS[@]:-}"; do
    [[ -n "${t}" ]] || continue
    if ! problem="$(codex_target_problem "${FLOW_DIR}" "${t}" "${PORTABLE_LIST}")"; then
      fail "Codex dispatch target ${problem}"
    fi
  done
fi

# --- 3b. Fixture self-tests (non-vacuity of the predicate) ---------------
# Each case builds a throwaway flow tree; the real tree is never mutated.
FIXTURE_ROOT="$(mktemp -d)" || { echo "dispatch-target-invocable.test.sh: failed to create fixture directory." >&2; exit 2; }
trap 'rm -rf "${FIXTURE_ROOT}"' EXIT

# make_flow <name> <portable-decl-line> — a one-skill flow tree (`demo`) with a
# SKILL.md that has no dispatch line and no companion. Prints its path.
make_flow() {
  local dir="${FIXTURE_ROOT}/$1"
  mkdir -p "${dir}/skills/demo" "${dir}/opencode" || return 1
  printf -- '---\nname: demo\n---\n\n# Demo\n' > "${dir}/skills/demo/SKILL.md" || return 1
  printf '#!/usr/bin/env bash\n%s\n' "$2" > "${dir}/opencode/install-skills.sh" || return 1
  echo "${dir}"
}

expect_problem() { # <label> <flow-dir> <target> <portable> — must fail, naming the target
  local out
  if out="$(codex_target_problem "$2" "$3" "$4")"; then
    fail "self-test [$1]: expected a failure for target ${3} but the predicate passed (vacuous)"
  elif [[ "${out}" != *"${3}"* ]]; then
    fail "self-test [$1]: the failure does not name target ${3}: ${out}"
  fi
}

expect_clean() { # <label> <flow-dir> <target> <portable> — must pass
  local out
  if ! out="$(codex_target_problem "$2" "$3" "$4")"; then
    fail "self-test [$1]: expected target ${3} to pass but got: ${out}"
  fi
}

expect_portable_error() { # <label> <flow-dir> <needle> — portable_skills_of must fail closed
  local out
  if out="$(portable_skills_of "$2")"; then
    fail "self-test [$1]: portable_skills_of passed (${out}) but must fail closed"
  elif [[ "${out}" != *"$3"* ]]; then
    fail "self-test [$1]: expected reason containing [$3], got: ${out}"
  fi
}

if F_BARE="$(make_flow bare 'PORTABLE_SKILLS="other"')"; then
  expect_problem "no companion, not portable" "${F_BARE}" demo "other"
else
  fail "self-test setup: could not build the bare fixture"
fi

if F_NODISPATCH="$(make_flow nodispatch 'PORTABLE_SKILLS="other"')"; then
  printf '# Demo codex procedure\n\nDo the thing.\n' > "${F_NODISPATCH}/skills/demo/codex.md" || fail "self-test setup: could not write nodispatch companion"
  expect_problem "companion without dispatch line" "${F_NODISPATCH}" demo "other"
else
  fail "self-test setup: could not build the nodispatch fixture"
fi

if F_EMPTYCOMP="$(make_flow emptycomp 'PORTABLE_SKILLS="other"')"; then
  : > "${F_EMPTYCOMP}/skills/demo/codex.md" || fail "self-test setup: could not write empty companion"
  codex_dispatch_needle demo >> "${F_EMPTYCOMP}/skills/demo/SKILL.md" || fail "self-test setup: could not write dispatch line"
  printf '\n' >> "${F_EMPTYCOMP}/skills/demo/SKILL.md"
  expect_problem "dispatch line but empty companion" "${F_EMPTYCOMP}" demo "other"
else
  fail "self-test setup: could not build the emptycomp fixture"
fi

if F_WRONGNAME="$(make_flow wrongname 'PORTABLE_SKILLS="other"')"; then
  printf '# Demo codex procedure\n\nDo the thing.\n' > "${F_WRONGNAME}/skills/demo/codex.md" || fail "self-test setup: could not write wrongname companion"
  { codex_dispatch_needle other; printf '\n'; } >> "${F_WRONGNAME}/skills/demo/SKILL.md" || fail "self-test setup: could not write wrong dispatch line"
  expect_problem "dispatch line names a different skill" "${F_WRONGNAME}" demo "other"
else
  fail "self-test setup: could not build the wrongname fixture"
fi

if F_WIRED="$(make_flow wired 'PORTABLE_SKILLS="other"')"; then
  printf '# Demo codex procedure\n\nDo the thing.\n' > "${F_WIRED}/skills/demo/codex.md" || fail "self-test setup: could not write wired companion"
  { codex_dispatch_needle demo; printf ', execute that native procedure.\n'; } >> "${F_WIRED}/skills/demo/SKILL.md" || fail "self-test setup: could not write dispatch line"
  expect_clean "companion plus dispatch line (positive control)" "${F_WIRED}" demo "other"
else
  fail "self-test setup: could not build the wired fixture"
fi

if F_PORTABLE="$(make_flow portable 'PORTABLE_SKILLS="other demo"')"; then
  if PL="$(portable_skills_of "${F_PORTABLE}")"; then
    expect_clean "portable, no companion" "${F_PORTABLE}" demo "${PL}"
  else
    fail "self-test [portable, no companion]: portable_skills_of failed on a valid declaration: ${PL}"
  fi
else
  fail "self-test setup: could not build the portable fixture"
fi

# A required target (ci-repair, babysit-attention) whose SKILL.md is unreadable must FAIL, never be
# skipped like an arbitrary non-skill workflow name.
if F_REQ="$(make_flow required 'PORTABLE_SKILLS="other"')"; then
  if out="$(required_skill_problem "${F_REQ}" no-such-required)"; then
    fail "self-test [required target without SKILL.md]: predicate passed but a missing required SKILL.md must fail"
  elif [[ "${out}" != *"no-such-required"* ]]; then
    fail "self-test [required target without SKILL.md]: the failure does not name the target: ${out}"
  fi
  if ! out="$(required_skill_problem "${F_REQ}" demo)"; then
    fail "self-test [required target with SKILL.md] (positive control): expected pass but got: ${out}"
  fi
else
  fail "self-test setup: could not build the required fixture"
fi

# A target with no flow skill is skipped by the predicate (not a failure).
expect_clean "non-flow-skill target is skipped" "${F_BARE:-${FIXTURE_ROOT}}" no-such-skill "other"

# PORTABLE_SKILLS is read fail-closed: missing, duplicated and empty all fail.
if F_NOLINE="$(make_flow noline '# no declaration here')"; then
  expect_portable_error "PORTABLE_SKILLS line missing" "${F_NOLINE}" "no ^PORTABLE_SKILLS= line"
else
  fail "self-test setup: could not build the noline fixture"
fi
if F_DUP="$(make_flow dup 'PORTABLE_SKILLS="demo"')"; then
  printf 'PORTABLE_SKILLS="demo"\n' >> "${F_DUP}/opencode/install-skills.sh" || fail "self-test setup: could not duplicate the declaration"
  expect_portable_error "PORTABLE_SKILLS line duplicated" "${F_DUP}" "2 ^PORTABLE_SKILLS= lines"
else
  fail "self-test setup: could not build the dup fixture"
fi
if F_EMPTY="$(make_flow empty 'PORTABLE_SKILLS=""')"; then
  expect_portable_error "PORTABLE_SKILLS empty" "${F_EMPTY}" "PORTABLE_SKILLS is empty"
else
  fail "self-test setup: could not build the empty fixture"
fi

# codexWF parsing: an empty parse fails closed, and only codexWF entries count.
printf 'x := claudeWF("ci-repair")\ny := claudeWF("babysit-attention")\n' > "${FIXTURE_ROOT}/no-codex.go" || fail "self-test setup: could not write no-codex.go"
if out="$(codex_targets_of "${FIXTURE_ROOT}/no-codex.go")"; then
  fail "self-test [no codexWF targets]: parse succeeded (${out}) but an empty codexWF parse must fail closed"
fi
printf 'a := codexWF("alpha")\nb := claudeWF("beta")\nc := codexWF("gamma")\n' > "${FIXTURE_ROOT}/mixed.go" || fail "self-test setup: could not write mixed.go"
if out="$(codex_targets_of "${FIXTURE_ROOT}/mixed.go")"; then
  [[ "${out}" == $'alpha\ngamma' ]] || fail "self-test [codexWF-only parse]: expected alpha and gamma only, got: ${out}"
else
  fail "self-test [codexWF-only parse]: parse failed on a valid template: ${out}"
fi

# =====================================================================
# 4. Every OpenCode dispatch target must be in the OpenCode delivery set
#    (#1217): PORTABLE_SKILLS minus OPENCODE_RETIRED_SKILLS in
#    opencode/install-skills.sh. A built-in OpenCode workflow must name a skill
#    OpenCode actually receives; checking raw PORTABLE_SKILLS would accept a
#    retired `babysit` workflow. template.go ships no OpenCode workflow today
#    (matching #1222), so zero targets passes — section 1's KNOWN_HELPERS makes
#    a reintroduced openCodeWF fail as unrecognized until its owner re-adds the
#    helper AND this delivery-set check passes.
#
# Same pure-predicate shape as section 3: print the reason and return 1;
# callers record the failure in the parent shell.
# =====================================================================

# opencode_retired_skills_of <flow-dir> — the OPENCODE_RETIRED_SKILLS list of
# opencode/install-skills.sh, space-separated. Fail-closed like
# portable_skills_of (exactly one closed double-quoted single-line value), but an
# empty value is valid: retiring nothing is a legitimate future state.
opencode_retired_skills_of() {
  local installer="$1/opencode/install-skills.sh" count decl rc
  if [[ ! -r "${installer}" ]]; then
    echo "opencode/install-skills.sh unreadable"
    return 1
  fi
  count="$(grep -c '^OPENCODE_RETIRED_SKILLS=' "${installer}")"
  rc=$?
  if [[ "${rc}" -gt 1 ]]; then
    echo "opencode/install-skills.sh: grep failed (exit ${rc})"
    return 1
  fi
  if [[ "${count}" -eq 0 ]]; then
    echo "opencode/install-skills.sh: no ^OPENCODE_RETIRED_SKILLS= line (OpenCode delivery set unknown)"
    return 1
  fi
  if [[ "${count}" -gt 1 ]]; then
    echo "opencode/install-skills.sh: ${count} ^OPENCODE_RETIRED_SKILLS= lines (OpenCode delivery set ambiguous)"
    return 1
  fi
  decl="$(grep '^OPENCODE_RETIRED_SKILLS=' "${installer}")" || { echo "opencode/install-skills.sh: OPENCODE_RETIRED_SKILLS line unreadable"; return 1; }
  if [[ ! "${decl}" =~ ^OPENCODE_RETIRED_SKILLS=\"([^\"]*)\" ]]; then
    echo "opencode/install-skills.sh: OPENCODE_RETIRED_SKILLS is not a closed double-quoted single-line value"
    return 1
  fi
  echo "${BASH_REMATCH[1]//[[:space:]]/ }"
}

# opencode_delivery_set <portable-list> <retired-list> — the skills OpenCode
# receives, one per line: every portable skill that is not retired.
opencode_delivery_set() {
  local skill
  for skill in $1; do
    [[ " $2 " == *" ${skill} "* ]] || echo "${skill}"
  done
}

# opencode_targets_of <template.go> — the openCodeWF("<name>") targets, one per
# line. Zero targets is valid (prints nothing, returns 0); only a grep error
# (exit > 1) fails closed.
opencode_targets_of() {
  local template="$1" raw rc
  raw="$(grep -oE '\bopenCodeWF\("[a-z-]+"\)' "${template}")"
  rc=$?
  if [[ "${rc}" -gt 1 ]]; then
    echo "grep failed (exit ${rc}) reading ${template}"
    return 1
  fi
  [[ -z "${raw}" ]] || sed -E 's/.*\("([a-z-]+)"\)/\1/' <<<"${raw}" | sort -u
}

# opencode_target_problem <target> <portable-list> <retired-list> — prints why
# the target is not delivered to OpenCode (naming it) and returns 1; prints
# nothing and returns 0 when it is in the delivery set.
opencode_target_problem() {
  local target="$1" portable="$2" retired="$3"
  [[ -n "${target}" ]] || { echo "empty OpenCode target name"; return 1; }
  if [[ " ${retired} " == *" ${target} "* ]]; then
    echo "${target}: retired for OpenCode (listed in OPENCODE_RETIRED_SKILLS), so OpenCode never receives it"
    return 1
  fi
  if [[ " ${portable} " != *" ${target} "* ]]; then
    echo "${target}: not in PORTABLE_SKILLS, so OpenCode never receives it"
    return 1
  fi
  return 0
}

# --- 4a. The real tree ------------------------------------------------
if ! OC_TARGETS_RAW="$(opencode_targets_of "${TEMPLATE}")"; then
  fail "watch/internal/run/template.go: ${OC_TARGETS_RAW} (cannot verify the OpenCode dispatch targets)"
elif [[ -z "${OC_TARGETS_RAW}" ]]; then
  echo "dispatch-target-invocable.test.sh: note — no built-in OpenCode workflows in template.go (matches #1222); delivery-set check has no targets"
else
  if ! OC_RETIRED="$(opencode_retired_skills_of "${FLOW_DIR}")"; then
    fail "${OC_RETIRED} — cannot decide which OpenCode targets are delivered (failing closed)"
  elif [[ -z "${PORTABLE_LIST:-}" ]]; then
    fail "PORTABLE_SKILLS unavailable — cannot decide which OpenCode targets are delivered (failing closed)"
  else
    while IFS= read -r t; do
      [[ -n "${t}" ]] || continue
      if ! problem="$(opencode_target_problem "${t}" "${PORTABLE_LIST}" "${OC_RETIRED}")"; then
        fail "OpenCode dispatch target ${problem}"
      fi
    done <<<"${OC_TARGETS_RAW}"
  fi
fi

# The real installer must itself declare a readable, well-formed retired list
# even with zero targets, so a dropped line cannot hide until a target returns.
if ! OC_RETIRED_REAL="$(opencode_retired_skills_of "${FLOW_DIR}")"; then
  fail "${OC_RETIRED_REAL}"
elif [[ -n "${PORTABLE_LIST:-}" ]]; then
  OC_DELIVERY="$(opencode_delivery_set "${PORTABLE_LIST}" "${OC_RETIRED_REAL}" | tr '\n' ' ')"
  [[ " ${OC_DELIVERY} " != *" babysit "* ]] || fail "OpenCode delivery set still contains babysit: ${OC_DELIVERY}"
  [[ " ${OC_DELIVERY} " == *" project-core "* ]] || fail "OpenCode delivery set lost project-core: ${OC_DELIVERY}"
  [[ "$(wc -w <<<"${OC_DELIVERY}" | tr -d '[:space:]')" -eq 12 ]] || fail "OpenCode delivery set is not 12 skills: ${OC_DELIVERY}"
fi

# --- 4b. Fixture self-tests (non-vacuity) ----------------------------------
expect_opencode_problem() { # <label> <target> <portable> <retired> — must fail, naming the target
  local out
  if out="$(opencode_target_problem "$2" "$3" "$4")"; then
    fail "self-test [$1]: expected a failure for OpenCode target ${2} but the predicate passed (vacuous)"
  elif [[ "${out}" != *"${2}"* ]]; then
    fail "self-test [$1]: the failure does not name OpenCode target ${2}: ${out}"
  fi
}

expect_retired_error() { # <label> <flow-dir> <needle> — opencode_retired_skills_of must fail closed
  local out
  if out="$(opencode_retired_skills_of "$2")"; then
    fail "self-test [$1]: opencode_retired_skills_of passed (${out}) but must fail closed"
  elif [[ "${out}" != *"$3"* ]]; then
    fail "self-test [$1]: expected reason containing [$3], got: ${out}"
  fi
}

# Three targets: delivered passes, retired fails and is named, non-portable
# fails and is named.
if out="$(opencode_target_problem demo "babysit demo other" "babysit")"; then
  :
else
  fail "self-test [delivered OpenCode target] (positive control): expected pass but got: ${out}"
fi
expect_opencode_problem "retired target babysit" babysit "babysit demo other" "babysit"
expect_opencode_problem "target outside PORTABLE_SKILLS" ghost "babysit demo other" "babysit"

# The delivery set is the portable list minus the retired list.
if out="$(opencode_delivery_set "babysit demo other" "babysit" | tr '\n' ' ')"; then
  [[ "${out}" == "demo other " ]] || fail "self-test [delivery set]: expected [demo other ], got [${out}]"
else
  fail "self-test [delivery set]: helper failed: ${out}"
fi

# The parser picks up openCodeWF targets, ignores other agents, and treats a
# template with none as valid; only a grep error fails closed.
printf 'a := openCodeWF("demo")\nb := codexWF("other")\nc := openCodeWF("babysit")\n' > "${FIXTURE_ROOT}/with-opencode.go" || fail "self-test setup: could not write with-opencode.go"
if out="$(opencode_targets_of "${FIXTURE_ROOT}/with-opencode.go")"; then
  [[ "${out}" == $'babysit\ndemo' ]] || fail "self-test [openCodeWF parse]: expected babysit and demo only, got: ${out}"
else
  fail "self-test [openCodeWF parse]: parse failed on a valid template: ${out}"
fi
if out="$(opencode_targets_of "${FIXTURE_ROOT}/mixed.go")"; then
  [[ -z "${out}" ]] || fail "self-test [zero openCodeWF targets]: expected no targets, got: ${out}"
else
  fail "self-test [zero openCodeWF targets]: zero targets must pass, got: ${out}"
fi
if out="$(opencode_targets_of "${FIXTURE_ROOT}/does-not-exist.go" 2>/dev/null)"; then
  fail "self-test [openCodeWF grep error]: unreadable template passed (${out}) but a grep error must fail closed"
fi

# OPENCODE_RETIRED_SKILLS is read fail-closed: missing, duplicated and an
# unclosed quote fail; empty and a valid list pass.
if F_NORETIRED="$(make_flow noretired 'PORTABLE_SKILLS="demo"')"; then
  expect_retired_error "OPENCODE_RETIRED_SKILLS line missing" "${F_NORETIRED}" "no ^OPENCODE_RETIRED_SKILLS= line"
else
  fail "self-test setup: could not build the noretired fixture"
fi
if F_DUPRETIRED="$(make_flow dupretired $'PORTABLE_SKILLS="demo"\nOPENCODE_RETIRED_SKILLS="demo"')"; then
  printf 'OPENCODE_RETIRED_SKILLS="demo"\n' >> "${F_DUPRETIRED}/opencode/install-skills.sh" || fail "self-test setup: could not duplicate the retired declaration"
  expect_retired_error "OPENCODE_RETIRED_SKILLS line duplicated" "${F_DUPRETIRED}" "2 ^OPENCODE_RETIRED_SKILLS= lines"
else
  fail "self-test setup: could not build the dupretired fixture"
fi
if F_OPENQUOTE="$(make_flow openquote $'PORTABLE_SKILLS="demo"\nOPENCODE_RETIRED_SKILLS="demo')"; then
  expect_retired_error "OPENCODE_RETIRED_SKILLS unclosed quote" "${F_OPENQUOTE}" "not a closed double-quoted single-line value"
else
  fail "self-test setup: could not build the openquote fixture"
fi
if F_EMPTYRETIRED="$(make_flow emptyretired $'PORTABLE_SKILLS="demo"\nOPENCODE_RETIRED_SKILLS=""')"; then
  if out="$(opencode_retired_skills_of "${F_EMPTYRETIRED}")"; then
    [[ -z "${out}" ]] || fail "self-test [OPENCODE_RETIRED_SKILLS empty]: expected an empty list, got: ${out}"
  else
    fail "self-test [OPENCODE_RETIRED_SKILLS empty]: an empty value is valid but the reader failed: ${out}"
  fi
else
  fail "self-test setup: could not build the emptyretired fixture"
fi
if F_GOODRETIRED="$(make_flow goodretired $'PORTABLE_SKILLS="demo other"\nOPENCODE_RETIRED_SKILLS="demo"')"; then
  if out="$(opencode_retired_skills_of "${F_GOODRETIRED}")"; then
    [[ "${out}" == "demo" ]] || fail "self-test [OPENCODE_RETIRED_SKILLS valid]: expected [demo], got: ${out}"
  else
    fail "self-test [OPENCODE_RETIRED_SKILLS valid]: reader failed on a valid declaration: ${out}"
  fi
else
  fail "self-test setup: could not build the goodretired fixture"
fi

# KNOWN_HELPERS: a reintroduced openCodeWF is flagged and named; a template
# using only recognized helpers is clean.
if out="$(unknown_helpers_of "${FIXTURE_ROOT}/with-opencode.go")"; then
  fail "self-test [unknown helper openCodeWF]: expected openCodeWF to be flagged but the check passed"
elif [[ "${out}" != *openCodeWF* ]]; then
  fail "self-test [unknown helper openCodeWF]: the failure does not name openCodeWF: ${out}"
fi
if ! out="$(unknown_helpers_of "${FIXTURE_ROOT}/mixed.go")"; then
  fail "self-test [known helpers] (positive control): claudeWF/codexWF were flagged: ${out}"
fi

# Zero targets overall still fails closed: a template with no <agent>WF("<name>")
# call at all must not pass vacuously.
printf 'package run\n\nfunc builtinConfig() {}\n' > "${FIXTURE_ROOT}/no-targets.go" || fail "self-test setup: could not write no-targets.go"
if out="$(all_targets_of "${FIXTURE_ROOT}/no-targets.go")"; then
  fail "self-test [no <agent>WF targets]: parse succeeded (${out}) but an empty overall parse must fail closed"
fi
if out="$(all_targets_of "${FIXTURE_ROOT}/mixed.go")"; then
  [[ "${out}" == $'alpha\nbeta\ngamma' ]] || fail "self-test [all-agent parse]: expected alpha, beta and gamma, got: ${out}"
else
  fail "self-test [all-agent parse]: parse failed on a valid template: ${out}"
fi

echo "dispatch-target-invocable.test.sh: failures=${failures}"
[[ "${failures}" -eq 0 ]]
