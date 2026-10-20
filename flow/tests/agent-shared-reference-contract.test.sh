#!/usr/bin/env bash
# Contract test for ticket #693 -- drop <example> dialogue blocks from the agent
# descriptions and give each piece of duplicated agent boilerplate one home:
#   - the Claude Code subagent tool-discipline rule lives in the shell-rules
#     skill; the 10 consumer agents preload it with `skills:` and keep a one-line
#     banner with their own Bash exception;
#   - the maintainers' finding schema, base redaction rule and output template
#     live in skills/maintain/finding-schema.md, which the maintain procedures
#     pass to every maintainer launch prompt by absolute path;
#   - frozen frontmatter grants, the 18 agent names and domain-specialist.md stay
#     byte-identical to main 8b3c9898.
#
# Follows the idiom of flow/tests/subagent-cwd-contract.test.sh (require_doc /
# assert_contains / failures= counter) and flow/docs/shell-scripting-gotchas.md:
# exact-marker assertions, `grep -oF | wc -l` for occurrence counts, existence
# guards before every negative assertion, anchors kept on one source line, and
# injected non-vacuity self-tests for the two scanners (dispatch resolution and
# skills: resolution). Greps the real committed files relative to FLOW_DIR.
set -uo pipefail
shopt -s nullglob

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)" || { echo "agent-shared-reference-contract.test.sh: failed to resolve script directory." >&2; exit 2; }
FLOW_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)" || { echo "agent-shared-reference-contract.test.sh: failed to resolve flow directory." >&2; exit 2; }
failures=0

# Safety net for the mktemp fixture directories below: removes only a recorded,
# non-empty, non-symlink directory, so an unset/empty path can never reach rm.
FIXTURE_DIRS=()
cleanup_fixtures() {
  local d
  for d in "${FIXTURE_DIRS[@]+"${FIXTURE_DIRS[@]}"}"; do
    if [[ -n "${d}" && -d "${d}" && ! -L "${d}" ]]; then
      rm -rf -- "${d}"
    fi
  done
  return 0
}
trap cleanup_fixtures EXIT

fail() { echo "FAIL: $1" >&2; failures=$((failures+1)); }

# require_doc <result-var> <flow-relative-path> -- assigns the committed file's
# content, or fails closed (and returns 1) when it is missing/unreadable so a
# negative assertion can never pass against a file that does not exist.
require_doc() {
  local -n _result="$1"
  local _relpath="$2"
  local _content
  if [[ ! -f "${FLOW_DIR}/${_relpath}" ]] || ! _content="$(cat "${FLOW_DIR}/${_relpath}")"; then
    fail "${_relpath}: file not found/unreadable: ${FLOW_DIR}/${_relpath}"
    _result=""
    return 1
  fi
  _result="${_content}"
}

assert_contains() {
  local content="$1" pattern="$2" label="$3"
  [[ -n "${pattern}" ]] || { fail "${label}: empty required phrase (test bug)"; return; }
  [[ "${content}" == *"${pattern}"* ]] || fail "${label}: expected text not found: [${pattern}]"
}

assert_not_contains() {
  local content="$1" pattern="$2" label="$3"
  [[ -n "${pattern}" ]] || { fail "${label}: empty forbidden phrase (test bug)"; return; }
  [[ "${content}" != *"${pattern}"* ]] || fail "${label}: forbidden text still present: [${pattern}]"
}

# count_in <content> <needle> -- occurrences (not lines) of a fixed string.
count_in() {
  printf '%s' "$1" | grep -oF -- "$2" | wc -l | tr -d '[:space:]'
}

assert_count() {
  local content="$1" needle="$2" expected="$3" label="$4" actual
  actual="$(count_in "${content}" "${needle}")"
  [[ "${actual}" -eq "${expected}" ]] || fail "${label}: expected ${expected} occurrence(s) of [${needle}], found ${actual}"
}

# --- pure extractors (no fail() side effects; safe inside $(...)) -----------

# fm_of <file> -- the YAML frontmatter body (between the first two --- lines).
fm_of() { awk 'NR==1 && $0=="---"{f=1;next} f&&$0=="---"{exit} f' "$1"; }

# fm_name <file> -- the frontmatter `name:` value.
fm_name() { fm_of "$1" | awk '/^name:/{sub(/^name:[ ]*/,""); print; exit}'; }

# fm_description <file> -- the lines of a block-scalar `description:`.
fm_description() { fm_of "$1" | awk '/^description:/{f=1;next} f&&/^[A-Za-z_-]+:/{exit} f'; }

# fm_skills <file> -- the entries of a `skills:` YAML list, one per line.
fm_skills() { fm_of "$1" | awk '/^skills:/{f=1;next} f&&/^  - /{sub(/^  - /,"");print;next} f{exit}'; }

# section_of <file> <heading-line> -- the heading's section, up to the next
# heading of the same or a shallower level. Fence-aware: a heading-shaped line
# inside a fenced block never ends the section (flow/docs/shell-scripting-gotchas.md).
section_of() {
  awk -v h="$2" '
    BEGIN { match(h, /^#+/); lvl = RLENGTH }
    /^```/ { infence = !infence }
    !infence && /^#+ / {
      match($0, /^#+/); l = RLENGTH
      if (on && l <= lvl) exit
      if (!on && $0 == h) on = 1
    }
    on { print }
  ' "$1"
}

# --- dispatch-resolution scanner --------------------------------------------
# Claude surfaces only: skills/*/SKILL.md, skills/*/phases/*.md and
# skills/*/modes/*.md. skills/*/codex.md is deliberately NOT scanned: it
# dispatches Codex roles (e.g. critical-reviewer) that have no Claude agent.
DISPATCH_LINE_RE='[Ll]aunch|[Dd]elegat|[Dd]ispatch|[Ss]pawn|[Ii]nvoke|\bTask\b|[Aa]gent'
ROLE_TOKEN_RE='(`|\*\*)([a-z]+(-[a-z]+)*-(reviewer|maintainer|analyzer|hunter|collector|gatherer|specialist)|implementer|planner|refiner)(`|\*\*)'

# dispatch_tokens <flow-root> -- unique backticked/bold role tokens found on
# launch/delegate/Task/agent lines of the Claude surfaces, one per line.
dispatch_tokens() {
  local root="$1" f
  for f in "${root}"/skills/*/SKILL.md "${root}"/skills/*/phases/*.md "${root}"/skills/*/modes/*.md; do
    grep -E -- "${DISPATCH_LINE_RE}" "${f}" | grep -oE -- "${ROLE_TOKEN_RE}"
  done | tr -d '`*' | LC_ALL=C sort -u
}

# unresolved_dispatch <flow-root> -- every dispatched token that does NOT
# resolve to <root>/agents/<token>.md whose frontmatter `name:` matches.
unresolved_dispatch() {
  local root="$1" tok
  while IFS= read -r tok; do
    [[ -n "${tok}" ]] || continue
    if [[ ! -f "${root}/agents/${tok}.md" ]] || [[ "$(fm_name "${root}/agents/${tok}.md")" != "${tok}" ]]; then
      printf '%s\n' "${tok}"
    fi
  done < <(dispatch_tokens "${root}")
}

# --- skills: preload scanner --------------------------------------------------

# skill_entries <flow-root> -- "<agent-file-basename> <entry>" for every skills:
# entry of every agent under <root>/agents.
skill_entries() {
  local root="$1" f e
  for f in "${root}"/agents/*.md; do
    while IFS= read -r e; do
      [[ -n "${e}" ]] || continue
      printf '%s %s\n' "$(basename "${f}")" "${e}"
    done < <(fm_skills "${f}")
  done
}

# unresolved_skill_entries <flow-root> -- every skills: entry that is not a
# `cenci:<name>` reference to an existing skill whose frontmatter name matches
# and which does not set `disable-model-invocation: true` (Claude Code skips a
# missing or disabled preload silently, logging only to the debug log).
unresolved_skill_entries() {
  local root="$1" agent entry skill sfile
  while read -r agent entry; do
    [[ -n "${entry}" ]] || continue
    if [[ "${entry}" != cenci:* ]]; then
      printf '%s %s (not namespaced)\n' "${agent}" "${entry}"
      continue
    fi
    skill="${entry#cenci:}"
    sfile="${root}/skills/${skill}/SKILL.md"
    if [[ ! -f "${sfile}" ]]; then
      printf '%s %s (no such skill)\n' "${agent}" "${entry}"
    elif [[ "$(fm_name "${sfile}")" != "${skill}" ]]; then
      printf '%s %s (skill name mismatch)\n' "${agent}" "${entry}"
    elif fm_of "${sfile}" | grep -qE '^disable-model-invocation:[ ]*true'; then
      printf '%s %s (disable-model-invocation)\n' "${agent}" "${entry}"
    fi
  done < <(skill_entries "${root}")
}

# =====================================================================
# Fixed data, generated from main commit 8b3c9898.
# =====================================================================
AGENT_NAMES=(
  acceptance-reviewer backlog-maintainer code-reviewer context-gatherer
  docs-maintainer domain-specialist duplication-analyzer implementer
  lessons-collector planner portability-maintainer refiner rules-maintainer
  security-analyzer security-reviewer silent-failure-hunter structure-analyzer
  structure-maintainer
)
DOMAIN_SPECIALIST_BLOB="78ccd320b8be34530677c9fb02843275a27c5a62"

declare -A FM_PIN
# pin_fm <agent> <tools> <model> <effort|-> <color> <permissionMode>
# Records the exact frozen frontmatter lines, in file order; "-" = no effort line.
pin_fm() {
  local pin="tools: $2"$'\n'"model: $3"
  [[ "$4" == "-" ]] || pin+=$'\n'"effort: $4"
  pin+=$'\n'"color: $5"$'\n'"permissionMode: $6"
  FM_PIN["$1"]="${pin}"
}
pin_fm acceptance-reviewer     "Read, Grep, Glob, Bash" opus   high cyan    plan
pin_fm backlog-maintainer      "Read, Grep, Glob, Bash" sonnet medium orange plan
pin_fm code-reviewer           "Read, Grep, Glob, Bash" sonnet high yellow plan
pin_fm context-gatherer        "Read, Write, Grep, Glob, Bash" haiku - cyan acceptEdits
pin_fm docs-maintainer         "Read, Grep, Glob, Bash" sonnet medium yellow plan
pin_fm domain-specialist       "Read, Grep, Glob" fable high red plan
pin_fm duplication-analyzer    "Read, Grep, Glob" sonnet high magenta plan
pin_fm implementer             "Read, Write, Edit, Glob, Grep, Bash, Skill, mcp__context7__resolve-library-id, mcp__context7__query-docs" sonnet high green acceptEdits
pin_fm lessons-collector       "Read, Write, Edit, Grep, Glob" haiku - cyan acceptEdits
pin_fm planner                 "Read, Grep, Glob, Bash, mcp__context7__resolve-library-id, mcp__context7__query-docs" opus high blue plan
pin_fm portability-maintainer  "Read, Grep, Glob, Bash" sonnet medium purple plan
pin_fm refiner                 "Read, Grep, Glob, Bash, mcp__context7__resolve-library-id, mcp__context7__query-docs" opus high green plan
pin_fm rules-maintainer        "Read, Grep, Glob, Bash" sonnet medium green plan
pin_fm security-analyzer       "Read, Grep, Glob, mcp__context7__resolve-library-id, mcp__context7__query-docs" sonnet high red plan
pin_fm security-reviewer       "Read, Grep, Glob" opus high red plan
pin_fm silent-failure-hunter   "Read, Grep, Glob" sonnet high magenta plan
pin_fm structure-analyzer      "Read, Grep, Glob, Bash" haiku - cyan plan
pin_fm structure-maintainer    "Read, Grep, Glob, Bash" sonnet medium cyan plan

# cksum of each agent's `> **Output discipline**` paragraph at 8b3c9898 (the
# tailored banner must stay unchanged in every agent).
declare -A OUTPUT_DISCIPLINE_CKSUM=(
  [acceptance-reviewer]=321532103
  [backlog-maintainer]=1116462597
  [code-reviewer]=3790580220
  [context-gatherer]=438116571
  [docs-maintainer]=42699246
  [duplication-analyzer]=907601956
  [implementer]=4118837263
  [lessons-collector]=547605310
  [planner]=4092770159
  [portability-maintainer]=42699246
  [refiner]=1732145820
  [rules-maintainer]=42699246
  [security-analyzer]=3431697674
  [security-reviewer]=2925193210
  [silent-failure-hunter]=2914457869
  [structure-analyzer]=3045111482
  [structure-maintainer]=42699246
)

CANONICAL_FRAGMENT="do not inherit the invoking skill's \`allowed-tools\`"
GGR="\`Grep\`/\`Glob\`/\`Read\`"
SHELL_BANNER_PREFIX='> **Shell discipline**:'
INCOMPLETE_MARKER='INCOMPLETE AUDIT:'

# =====================================================================
# Load every agent file once; a missing file fails closed here and every
# later per-agent assertion is guarded on its presence.
# =====================================================================
declare -A DOC
for n in "${AGENT_NAMES[@]}"; do
  if require_doc CONTENT "agents/${n}.md"; then
    DOC["${n}"]="${CONTENT}"
  fi
done

# =====================================================================
# No dialogue in agents.
# =====================================================================
for n in "${AGENT_NAMES[@]}"; do
  [[ -n "${DOC[${n}]+x}" ]] || continue
  for needle in '<example>' '</example>' '<commentary>' '</commentary>'; do
    c="$(count_in "${DOC[${n}]}" "${needle}")"
    [[ "${c}" -eq 0 ]] || fail "agents/${n}.md: ${c} occurrence(s) of ${needle} (dialogue blocks must be removed)"
  done
done
[[ "${#DOC[@]}" -eq "${#AGENT_NAMES[@]}" ]] || fail "expected ${#AGENT_NAMES[@]} agent files readable, loaded ${#DOC[@]}"

# =====================================================================
# Agent names and dispatch resolution.
# =====================================================================
ACTUAL_NAMES="$(for f in "${FLOW_DIR}"/agents/*.md; do fm_name "${f}"; done | LC_ALL=C sort)"
EXPECTED_NAMES="$(printf '%s\n' "${AGENT_NAMES[@]}" | LC_ALL=C sort)"
[[ "${ACTUAL_NAMES}" == "${EXPECTED_NAMES}" ]] || fail "agent name: set differs from the 18 names at 8b3c9898. Expected: $(echo "${EXPECTED_NAMES}" | tr '\n' ' ') Actual: $(echo "${ACTUAL_NAMES}" | tr '\n' ' ')"
for n in "${AGENT_NAMES[@]}"; do
  [[ -z "${DOC[${n}]+x}" ]] || [[ "$(fm_name "${FLOW_DIR}/agents/${n}.md")" == "${n}" ]] || fail "agents/${n}.md: frontmatter name: does not match the file name"
done

DISPATCHED="$(dispatch_tokens "${FLOW_DIR}")"
UNRESOLVED="$(unresolved_dispatch "${FLOW_DIR}")"
[[ -z "${UNRESOLVED}" ]] || fail "dispatch lines name roles with no matching flow/agents/<name>.md: $(echo "${UNRESOLVED}" | tr '\n' ' ')"

# Non-vacuity floor: the scan must actually have collected the known dispatchers.
for tok in implementer planner refiner context-gatherer code-reviewer \
  duplication-analyzer security-analyzer structure-analyzer \
  structure-maintainer docs-maintainer portability-maintainer rules-maintainer backlog-maintainer
do
  printf '%s\n' "${DISPATCHED}" | grep -qxF -- "${tok}" || fail "dispatch scan floor: ${tok} not collected from any Claude launch/delegate line (scanner went blind?)"
done

# Injected self-test: a fixture with a ghost-reviewer dispatch line must fail,
# a resolvable one must not, and codex.md must never be scanned.
if FIX="$(mktemp -d)" && [[ -d "${FIX}" ]]; then
  FIXTURE_DIRS+=("${FIX}")
  mkdir -p "${FIX}/agents" "${FIX}/skills/demo"
  printf -- '---\nname: real-reviewer\n---\nbody\n' > "${FIX}/agents/real-reviewer.md"
  printf 'Launch the `real-reviewer` agent.\nDelegate to **ghost-reviewer** next.\n' > "${FIX}/skills/demo/SKILL.md"
  printf 'Launch the `critical-reviewer` worker.\n' > "${FIX}/skills/demo/codex.md"
  FIX_TOKENS="$(dispatch_tokens "${FIX}")"
  FIX_UNRESOLVED="$(unresolved_dispatch "${FIX}")"
  [[ "${FIX_UNRESOLVED}" == "ghost-reviewer" ]] || fail "self-test: injected ghost-reviewer was not the (only) unresolved dispatch token, got [${FIX_UNRESOLVED}]"
  printf '%s\n' "${FIX_TOKENS}" | grep -qxF -- "real-reviewer" || fail "self-test: resolvable real-reviewer dispatch token was not collected"
  printf '%s\n' "${FIX_TOKENS}" | grep -qxF -- "critical-reviewer" && fail "self-test: codex.md must not be scanned for Claude dispatch tokens"
  rm -rf "${FIX}"
else
  fail "self-test: could not create dispatch fixture directory"
fi

# =====================================================================
# Frontmatter pins, domain-specialist byte identity, descriptions.
# =====================================================================
for n in "${AGENT_NAMES[@]}"; do
  [[ -n "${DOC[${n}]+x}" ]] || continue
  ACTUAL_FM="$(fm_of "${FLOW_DIR}/agents/${n}.md" | grep -E '^(tools|model|effort|color|permissionMode):')"
  [[ "${ACTUAL_FM}" == "${FM_PIN[${n}]}" ]] || fail "agents/${n}.md: tools/model/effort/color/permissionMode lines differ from 8b3c9898. Expected [$(echo "${FM_PIN[${n}]}" | tr '\n' '|')] got [$(echo "${ACTUAL_FM}" | tr '\n' '|')]"

  # Tailored Output discipline banner unchanged.
  PARA="$(awk '/^> \*\*Output discipline\*\*:/{f=1} f&&/^$/{exit} f' "${FLOW_DIR}/agents/${n}.md")"
  if [[ -n "${OUTPUT_DISCIPLINE_CKSUM[${n}]+x}" ]]; then
    SUM="$(printf '%s\n' "${PARA}" | cksum | cut -d' ' -f1)"
    [[ "${SUM}" == "${OUTPUT_DISCIPLINE_CKSUM[${n}]}" ]] || fail "agents/${n}.md: the tailored '> **Output discipline**' banner changed (cksum ${SUM})"
  fi
done

if command -v git >/dev/null 2>&1 && [[ -f "${FLOW_DIR}/agents/domain-specialist.md" ]]; then
  if BLOB="$(git hash-object --no-filters "${FLOW_DIR}/agents/domain-specialist.md")"; then
    [[ "${BLOB}" == "${DOMAIN_SPECIALIST_BLOB}" ]] || fail "agents/domain-specialist.md: blob ${BLOB} differs from main 8b3c9898 (${DOMAIN_SPECIALIST_BLOB})"
  else
    fail "agents/domain-specialist.md: git hash-object failed"
  fi
else
  fail "agents/domain-specialist.md: cannot verify byte identity (git or file missing)"
fi

for n in "${AGENT_NAMES[@]}"; do
  [[ "${n}" != "domain-specialist" ]] || continue   # single-line description, byte-pinned above
  [[ -n "${DOC[${n}]+x}" ]] || continue
  DESC="$(fm_description "${FLOW_DIR}/agents/${n}.md")"
  if [[ -z "${DESC}" ]]; then
    fail "agents/${n}.md: description block is empty or not a block scalar"
    continue
  fi
  if printf '%s\n' "${DESC}" | grep -qE '^[[:space:]]*(user|assistant|Context):'; then
    fail "agents/${n}.md: description carries dialogue lines (user:/assistant:/Context:)"
  fi
  if printf '%s\n' "${DESC}" | grep -q '<'; then
    fail "agents/${n}.md: description contains a '<' markup line"
  fi
  assert_contains "${DESC}" 'Use ' "agents/${n}.md description"
done
# The five agents whose first line named no dispatcher now name the real one.
for n in implementer planner; do
  [[ -n "${DOC[${n}]+x}" ]] && assert_contains "$(fm_description "${FLOW_DIR}/agents/${n}.md")" '/cenci:implement' "agents/${n}.md description dispatcher"
done
for n in duplication-analyzer security-analyzer structure-analyzer; do
  [[ -n "${DOC[${n}]+x}" ]] && assert_contains "$(fm_description "${FLOW_DIR}/agents/${n}.md")" '/cenci:refactor' "agents/${n}.md description dispatcher"
done

# =====================================================================
# Canonical rule in shell-rules.
# =====================================================================
SR="skills/shell-rules/SKILL.md"
if require_doc SR_CONTENT "${SR}"; then
  assert_count "${SR_CONTENT}" "${CANONICAL_FRAGMENT}" 1 "${SR}"
  SR_SEARCH="$(section_of "${FLOW_DIR}/${SR}" '## Search and File Operations')"
  assert_contains "${SR_SEARCH}" '### Claude Code subagents' "${SR} (subsection nested under ## Search and File Operations)"
  SR_SUB="$(section_of "${FLOW_DIR}/${SR}" '### Claude Code subagents')"
  if [[ -z "${SR_SUB}" ]]; then
    fail "${SR}: no '### Claude Code subagents' section"
  else
    assert_contains "${SR_SUB}" "${CANONICAL_FRAGMENT}" "${SR} subagent section"
    assert_contains "${SR_SUB}" "${GGR}" "${SR} subagent section"
    assert_contains "${SR_SUB}" "\`grep\`, \`rg\`, \`find\`, \`ls\`, \`cat\`, or \`head\`" "${SR} subagent section forbidden-command list"
    assert_contains "${SR_SUB}" 'one command per call' "${SR} subagent section"
    assert_contains "${SR_SUB}" "no \`echo\` banners" "${SR} subagent section"
    assert_contains "${SR_SUB}" "no \`&&\`/\`;\` compounds" "${SR} subagent section"
  fi
  if fm_of "${FLOW_DIR}/${SR}" | grep -qE '^disable-model-invocation:[ ]*true'; then
    fail "${SR}: shell-rules must not set disable-model-invocation: true (a preload would be skipped)"
  fi
  [[ "$(fm_name "${FLOW_DIR}/${SR}")" == "shell-rules" ]] || fail "${SR}: frontmatter name is not shell-rules"
fi
for n in "${AGENT_NAMES[@]}"; do
  [[ -n "${DOC[${n}]+x}" ]] || continue
  assert_not_contains "${DOC[${n}]}" "${CANONICAL_FRAGMENT}" "agents/${n}.md (rationale lives only in shell-rules)"
done

# =====================================================================
# The 10 consumers.
# =====================================================================
CONSUMERS=(code-reviewer context-gatherer docs-maintainer implementer planner portability-maintainer refiner rules-maintainer structure-analyzer structure-maintainer)

# check_banner <agent> <exception-substring>... -- exactly one one-line
# '> **Shell discipline**:' banner holding the Grep/Glob/Read rule and the
# agent's own Bash exception (the designed fallback if the preload is skipped).
check_banner() {
  local n="$1" label line count sub
  shift
  label="agents/${n}.md"
  [[ -n "${DOC[${n}]+x}" ]] || return 0
  count="$(grep -c -F -- "${SHELL_BANNER_PREFIX}" "${FLOW_DIR}/${label}")"
  if [[ "${count}" -ne 1 ]]; then
    fail "${label}: expected exactly one '${SHELL_BANNER_PREFIX}' line, found ${count}"
    return 0
  fi
  line="$(grep -F -- "${SHELL_BANNER_PREFIX}" "${FLOW_DIR}/${label}")"
  assert_contains "${line}" "${GGR}" "${label} banner"
  # Codex workers are built from these same files and have no skills: preload,
  # so the banner must not assert an unconditional preload.
  assert_not_contains "${line}" 'The preloaded `shell-rules`' "${label} banner (no unconditional preload claim)"
  assert_contains "${line}" 'Claude Code also preloads the `shell-rules` skill' "${label} banner (client-neutral preload note)"
  for sub in "$@"; do
    assert_contains "${line}" "${sub}" "${label} banner exception"
  done
}

check_banner code-reviewer "\`git\` and the project's build/test commands"
check_banner implementer "\`git\` and the project's build/test commands"
check_banner context-gatherer "read-only \`gh\` calls" "git remote get-url"
for n in docs-maintainer portability-maintainer rules-maintainer structure-maintainer; do
  check_banner "${n}" "read-only commands such as \`wc -l\`"
done
check_banner structure-analyzer "\`wc -l\` line counts"
check_banner planner "\`git\`" "\`gh\`" "build/test commands" "no \`-exec\`"
check_banner refiner "\`git\`" "\`gh\`"

# planner step 5 and refiner Before Analyzing step 3 lose the Hard-rule prose.
for n in planner refiner; do
  [[ -n "${DOC[${n}]+x}" ]] && assert_not_contains "${DOC[${n}]}" 'Hard rule: all' "agents/${n}.md"
done

# implementer keeps its Timing exception banner.
[[ -n "${DOC[implementer]+x}" ]] && assert_contains "${DOC[implementer]}" '> **Timing exception to Shell discipline**:' "agents/implementer.md"

for n in "${CONSUMERS[@]}"; do
  [[ -n "${DOC[${n}]+x}" ]] || continue
  label="agents/${n}.md"
  SKILLS="$(fm_skills "${FLOW_DIR}/${label}")"
  [[ "${SKILLS}" == "cenci:shell-rules" ]] || fail "${label}: frontmatter skills: must list exactly cenci:shell-rules, got [$(echo "${SKILLS}" | tr '\n' ' ')]"
  fm_of "${FLOW_DIR}/${label}" | awk '/^permissionMode:/{p=NR} /^skills:/{s=NR} END{exit !(p && s && s>p)}' || fail "${label}: skills: must follow permissionMode: in the frontmatter"
done

# acceptance-reviewer and backlog-maintainer: no preload, and each keeps exactly
# its existing single banner (no new tool-discipline banner).
for n in acceptance-reviewer backlog-maintainer; do
  [[ -n "${DOC[${n}]+x}" ]] || continue
  [[ -z "$(fm_skills "${FLOW_DIR}/agents/${n}.md")" ]] || fail "agents/${n}.md: must not gain a skills: entry"
  fm_of "${FLOW_DIR}/agents/${n}.md" | grep -q '^skills:' && fail "agents/${n}.md: must not carry a skills: key"
  assert_count "${DOC[${n}]}" "${SHELL_BANNER_PREFIX}" 1 "agents/${n}.md"
done

# Every skills: entry in any agent resolves; consumers are the only users.
ENTRIES="$(skill_entries "${FLOW_DIR}")"
ENTRY_COUNT="$(printf '%s\n' "${ENTRIES}" | grep -c .)"
[[ "${ENTRY_COUNT}" -ge "${#CONSUMERS[@]}" ]] || fail "skills: scan floor: found ${ENTRY_COUNT} preload entries across agents, expected at least ${#CONSUMERS[@]} (scanner went blind?)"
BAD_ENTRIES="$(unresolved_skill_entries "${FLOW_DIR}")"
[[ -z "${BAD_ENTRIES}" ]] || fail "skills: preload entries that do not resolve to an existing, non-disabled skill: $(echo "${BAD_ENTRIES}" | tr '\n' ';')"

# Injected self-test: unresolved / disabled / non-namespaced entries must be caught.
if FIX="$(mktemp -d)" && [[ -d "${FIX}" ]]; then
  FIXTURE_DIRS+=("${FIX}")
  mkdir -p "${FIX}/agents" "${FIX}/skills/ok" "${FIX}/skills/off"
  printf -- '---\nname: ok\n---\n' > "${FIX}/skills/ok/SKILL.md"
  printf -- '---\nname: off\ndisable-model-invocation: true\n---\n' > "${FIX}/skills/off/SKILL.md"
  printf -- '---\nname: a1\npermissionMode: plan\nskills:\n  - cenci:ok\n  - cenci:off\n  - cenci:missing\n  - bare\n---\n' > "${FIX}/agents/a1.md"
  FIX_BAD="$(unresolved_skill_entries "${FIX}")"
  FIX_BAD_COUNT="$(printf '%s\n' "${FIX_BAD}" | grep -c .)"
  [[ "${FIX_BAD_COUNT}" -eq 3 ]] || fail "self-test: expected 3 unresolved fixture preload entries (off, missing, bare), got ${FIX_BAD_COUNT}: ${FIX_BAD}"
  assert_not_contains "${FIX_BAD}" 'cenci:ok' "self-test (resolvable preload must not be flagged)"
  rm -rf "${FIX}"
else
  fail "self-test: could not create skills fixture directory"
fi

# =====================================================================
# finding-schema.md content.
# =====================================================================
FS="skills/maintain/finding-schema.md"
if require_doc FS_CONTENT "${FS}"; then
  for field in ID Category Severity Location Evidence 'Proposed change' 'Repair confidence' 'Required tests'; do
    assert_contains "${FS_CONTENT}" "**${field}**" "${FS} field list"
  done
  assert_contains "${FS_CONTENT}" 'credential-like value' "${FS} redaction rule"
  assert_contains "${FS_CONTENT}" 'PII' "${FS} redaction rule"
  assert_contains "${FS_CONTENT}" 'stricter' "${FS} redaction precedence (role-specific restrictions add to the base rule; the stricter wins)"
  assert_contains "${FS_CONTENT}" '<Audit heading>' "${FS} output template"
  assert_contains "${FS_CONTENT}" '<PREFIX>' "${FS} output template"
  assert_contains "${FS_CONTENT}" '### Recommendations' "${FS} output template"
  FS_FINDINGS_HEADINGS="$(count_in "${FS_CONTENT}" '### Findings')"
  [[ "${FS_FINDINGS_HEADINGS}" -ge 2 ]] || fail "${FS}: expected the template and its no-findings variant (>= 2 '### Findings' headings), found ${FS_FINDINGS_HEADINGS}"
  printf '%s\n' "${FS_CONTENT}" | grep -qi 'no findings' || fail "${FS}: no-findings variant not described"
fi

# =====================================================================
# Maintainer files.
# =====================================================================
# check_maintainer <agent> <audit-heading> <no-findings-sentence> <prefix> <category>...
check_maintainer() {
  local n="$1" heading="$2" nofind="$3" prefix="$4" label sec cat
  shift 4
  label="agents/${n}.md"
  [[ -n "${DOC[${n}]+x}" ]] || return 0
  assert_not_contains "${DOC[${n}]}" 'Report every finding with exactly these fields' "${label}"
  assert_not_contains "${DOC[${n}]}" '## Output Format' "${label}"
  assert_not_contains "${DOC[${n}]}" '- **Severity** — Critical | High | Medium | Low' "${label}"
  assert_not_contains "${DOC[${n}]}" '## Finding Schema' "${label}"
  assert_count "${DOC[${n}]}" '## Finding Reference' 1 "${label}"
  sec="$(section_of "${FLOW_DIR}/${label}" '## Finding Reference')"
  if [[ -z "${sec}" ]]; then
    fail "${label}: no '## Finding Reference' section"
    return 0
  fi
  assert_contains "${sec}" 'finding-schema.md' "${label} Finding Reference (Read directive)"
  assert_contains "${sec}" 'absolute path' "${label} Finding Reference (Read directive)"
  assert_contains "${sec}" 'launch prompt' "${label} Finding Reference (Read directive)"
  assert_contains "${sec}" 'unreadable' "${label} Finding Reference (incomplete fallback)"
  assert_contains "${sec}" 'incomplete' "${label} Finding Reference (incomplete fallback)"
  assert_contains "${sec}" "${INCOMPLETE_MARKER}" "${label} Finding Reference (literal incomplete marker the procedures key on)"
  assert_contains "${sec}" 'Do NOT emit the no-findings sentence' "${label} Finding Reference (fail closed, never the clean sentence)"
  assert_not_contains "${DOC[${n}]}" 'report no findings: return' "${label} (ambiguous fail-closed wording)"
  assert_contains "${sec}" "${heading}" "${label} Finding Reference (audit heading)"
  assert_contains "${sec}" "${nofind}" "${label} Finding Reference (no-findings sentence)"
  assert_contains "${sec}" "${prefix}-" "${label} Finding Reference (ID prefix)"
  for cat in "$@"; do
    assert_contains "${sec}" "\`${cat}\`" "${label} Finding Reference (category)"
  done
}

check_maintainer structure-maintainer '## Structure & Test Gap Audit' 'No structural or test-gap drift found in the analyzed scope.' STR 'Structure' 'Test gap'
check_maintainer docs-maintainer '## Documentation & Generated Index Audit' 'No documentation or generated-index drift found in the analyzed scope.' DOC 'Documentation drift' 'Generated index drift'
check_maintainer portability-maintainer '## Client Portability Audit' 'No client-portability drift found in the analyzed scope.' POR 'Client mismatch'
check_maintainer rules-maintainer '## Rule Hygiene Audit' 'No rule-hygiene drift found in the analyzed scope.' RUL 'Rule hygiene'
check_maintainer backlog-maintainer '## Followup Backlog Audit' 'No consolidation opportunities found.' BKL 'Followup backlog'

if [[ -n "${DOC[backlog-maintainer]+x}" ]]; then
  BK_SEC="$(section_of "${FLOW_DIR}/agents/backlog-maintainer.md" '## Finding Reference')"
  assert_contains "${BK_SEC}" 'issue number' "agents/backlog-maintainer.md Finding Reference (issue-number Location)"
  for action in 'Keep' 'Flag duplicate' 'Promote' 'Batch'; do
    assert_contains "${BK_SEC}" "${action}" "agents/backlog-maintainer.md Finding Reference (Proposed change actions)"
  done
  assert_contains "${DOC[backlog-maintainer]}" 'never reproduce a credential-like value or PII from an issue body' "agents/backlog-maintainer.md (issue-body redaction rule)"
fi

# =====================================================================
# The reference path is actually supplied (not only described).
# =====================================================================
MS="skills/maintain/SKILL.md"
if require_doc MS_CONTENT "${MS}"; then
  MS_P3="$(section_of "${FLOW_DIR}/${MS}" '## Phase 3 — Parallel audit')"
  MS_P4="$(section_of "${FLOW_DIR}/${MS}" '## Phase 4 — Report')"
  CLAUDE_REF='${CLAUDE_PLUGIN_ROOT}/skills/maintain/finding-schema.md'
  if [[ -z "${MS_P3}" ]]; then
    fail "${MS}: no '## Phase 3 — Parallel audit' section"
  else
    assert_contains "${MS_P3}" "${CLAUDE_REF}" "${MS} Phase 3 (absolute path put in every launch prompt)"
    assert_contains "${MS_P3}" 'Read before analysis' "${MS} Phase 3"
    assert_contains "${MS_P3}" 'unreadable' "${MS} Phase 3 (Agent-failure distinction covers an unreadable reference)"
    assert_contains "${MS_P3}" "${INCOMPLETE_MARKER}" "${MS} Phase 3 (Agent-failure distinction keys on the literal marker)"
  fi
  assert_contains "${MS_P4}" 'finding-schema.md' "${MS} Phase 4 (pointer to the shared schema)"
  assert_not_contains "${MS_CONTENT}" 'the schema each analyzer agent documents in its own file' "${MS}"
  # Wiring: the path the prompt supplies must resolve to a real file.
  [[ -f "${FLOW_DIR}/${CLAUDE_REF#'${CLAUDE_PLUGIN_ROOT}'/}" ]] || fail "${MS}: supplied path does not resolve to an existing file under the plugin root: ${CLAUDE_REF}"
fi

CX="skills/maintain/codex.md"
if require_doc CX_CONTENT "${CX}"; then
  CX_P3="$(section_of "${FLOW_DIR}/${CX}" '## Phase 3 — Worker audit')"
  CODEX_REF='${PLUGIN_ROOT}/skills/maintain/finding-schema.md'
  if [[ -z "${CX_P3}" ]]; then
    fail "${CX}: no '## Phase 3 — Worker audit' section"
  else
    assert_contains "${CX_P3}" "${CODEX_REF}" "${CX} Phase 3 (plugin-root-resolved path in every worker prompt)"
    assert_contains "${CX_P3}" 'absolute path' "${CX} Phase 3"
    assert_contains "${CX_P3}" "does not inherit the parent's loaded skills" "${CX} Phase 3 (no-inheritance statement)"
    assert_contains "${CX_P3}" 'unreadable' "${CX} Phase 3 (incomplete-run clause)"
    assert_contains "${CX_P3}" "${INCOMPLETE_MARKER}" "${CX} Phase 3 (incomplete-run clause keys on the literal marker)"
    assert_contains "${CX_P3}" 'cannot be resolved to an existing' "${CX} Phase 3 (unresolvable plugin root marks workers incomplete)"
  fi
  assert_not_contains "${CX_CONTENT}" 'eight-field finding schema documented by its' "${CX}"
  [[ -f "${FLOW_DIR}/${CODEX_REF#'${PLUGIN_ROOT}'/}" ]] || fail "${CX}: supplied path does not resolve to an existing file under the plugin root: ${CODEX_REF}"
fi

echo "agent-shared-reference-contract.test.sh: failures=${failures}"
[[ "${failures}" -eq 0 ]]
