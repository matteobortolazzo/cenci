#!/usr/bin/env bash
# contract-lib.sh — shared library for flow/tests/parity/parity.test.sh (ticket #524).
#
# Shared library backing the property checks documented in
# flow/docs/adapter-contract.md. See that doc for the full property table
# this library checks against, and flow/tests/parity/parity.test.sh for
# exactly how each function is called and what result it must report.
#
# ---------------------------------------------------------------------------
# Expected contract (8 properties; IDs used throughout parity.test.sh):
#
#   P1 baseline-gate            — real script: hooks/scripts/run-gate.sh
#   P2 worktree-isolation       — real script: hooks/scripts/guard-main-worktree.sh
#   P3 red-before-green         — no backing script: harness-defined event-sequence model
#   P4 gate-result-integrity    — real script: hooks/scripts/run-gate.sh (GATE_STATUS
#                                  interpretation) + procedure-text-only facet
#                                  ("quality gates are mandatory", no script backs review
#                                  gating itself)
#   P5 planning-immutability    — real script: hooks/scripts/guard-main-worktree.sh
#                                  (main-worktree write guard, planning-session scenario)
#   P6 sensitive-file-refusal   — real script: hooks/scripts/check-sensitive-files.sh
#   P7 verification-locality    — real script: codex/checkpoint.mjs (worktree identity
#                                  recorded at init, compared against claimed verification
#                                  target)
#   P8 push-policy              — no backing script: harness-defined command-string model
#
# None of these functions modify flow/skills/implement/**, flow/hooks/scripts/**, or
# flow/codex/** — every real script/doc listed above is read-only from this file's
# point of view.

# ---------------------------------------------------------------------------
# read_doc <path> [base-dir]
#
# Fail-closed doc reader (mirrors flow/tests/subagent-cwd-contract.test.sh's
# idiom): prints file content to stdout; returns 1 and prints nothing on a
# missing/unreadable file so a dangling reference can never vacuously pass.
# <path> may be absolute (used as-is) or relative to <base-dir> (defaults to
# the global FLOW_DIR set by parity.test.sh before it sources this file).
read_doc() {
  local path="$1" base="${2:-${FLOW_DIR:-}}" full content
  case "${path}" in
    /*) full="${path}" ;;
    *) full="${base}/${path}" ;;
  esac
  if ! content="$(cat "${full}" 2>/dev/null)"; then
    return 1
  fi
  printf '%s' "${content}"
  return 0
}

# ---------------------------------------------------------------------------
# print_run_header <flow-dir>
#
# Prints the commit SHA, the plugin version, and a config fingerprint, to
# stdout only (no persisted artifact file).
print_run_header() {
  local flow_dir="$1" repo_root sha version config_fp
  if repo_root="$(cd "${flow_dir}/.." 2>/dev/null && pwd)"; then
    sha="$(git -C "${repo_root}" rev-parse HEAD 2>/dev/null)" || sha="unknown"
  else
    sha="unknown"
  fi
  version="$(jq -r '.version // "unknown"' "${flow_dir}/.claude-plugin/plugin.json" 2>/dev/null)"
  [[ -n "${version}" ]] || version="unknown"
  if command -v sha256sum >/dev/null 2>&1; then
    config_fp="$(cat "${flow_dir}/hooks/hooks.json" "${flow_dir}/codex/hooks.json" 2>/dev/null | sha256sum | cut -d' ' -f1)"
    [[ -n "${config_fp}" ]] || config_fp="unavailable"
  else
    config_fp="unavailable"
  fi
  echo "commit: ${sha}"
  echo "plugin version: ${version}"
  echo "config fingerprint: ${config_fp}"
}

# ---------------------------------------------------------------------------
# Internal helper: check_push_policy_text <content>
# Token-based scan (never a bare substring match) so a legitimate
# "--force-with-lease" push line is never mistaken for a bare "--force"/"-f"
# push. Only lines that mention "push" at all are scanned for bad tokens,
# so an unrelated "-f" elsewhere in the doc (e.g. "rm -f") never false-flags.
# Returns 0 only if at least one push line uses --force-with-lease and no
# push line uses a bare --force / -f / --no-verify token.
_check_push_policy_text() {
  local content="$1" line token bad=0 saw_force_with_lease=0
  while IFS= read -r line; do
    case "${line}" in
      *push*)
        # set -f disables pathname/glob expansion for this unquoted
        # word-split (a literal '*' token in scanned text must never expand
        # against the CWD's file list and skew the verdict).
        set -f
        for token in ${line}; do
          case "${token}" in
            --force) bad=1 ;;
            -f) bad=1 ;;
            --no-verify) bad=1 ;;
            --force-with-lease) saw_force_with_lease=1 ;;
          esac
        done
        set +f
        ;;
    esac
  done <<< "${content}"
  [[ "${bad}" -eq 0 && "${saw_force_with_lease}" -eq 1 ]]
}

# _prop <property-id> <ok (0|1)> <reason-if-failed> -- prints the
# "<property-id>:pass" / "<property-id>:fail:<reason>" line contract.
# Returns the same 0/1 it was given, for the caller's overall accumulator.
_prop() {
  local id="$1" ok="$2" reason="${3:-}"
  if [[ "${ok}" -eq 0 ]]; then
    echo "${id}:pass"
    return 0
  else
    echo "${id}:fail:${reason}"
    return 1
  fi
}

# _marker_offset <content> <marker> -- prints the byte offset of <marker>'s
# first occurrence within <content> to stdout and returns 0, or returns 1
# (prints nothing) if <marker> is absent. <marker> is always used quoted
# inside a bash pattern context (both here and in the "presence" check
# below), so glob metacharacters in the marker text are matched literally,
# never pathname-expanded.
_marker_offset() {
  local content="$1" marker="$2" prefix
  case "${content}" in
    *"${marker}"*)
      prefix="${content%%"${marker}"*}"
      printf '%s' "${#prefix}"
      return 0
      ;;
    *) return 1 ;;
  esac
}

# _markers_strictly_increasing <content> <marker1> <marker2> [<marker3> ...]
#
# Returns 0 only if every listed marker is present in <content> AND their
# first-occurrence byte offsets are strictly increasing in the given
# argument order; returns 1 if any marker is missing or the offsets are not
# strictly increasing (no distinction between the two -- callers that need
# a presence-vs-order-specific failure reason, like check_claude_adapter and
# check_codex_adapter below, check each marker's presence individually first
# and only call this for the final ordering verdict). Exposed as its own
# function (rather than inlined) so parity.test.sh can call it directly
# against a synthetic, deliberately-reordered COPY of real-doc anchor text
# and prove the ordering logic itself catches a reordering -- without
# needing a whole fixture/flow-dir, and without ever touching the real,
# already-correctly-ordered docs.
_markers_strictly_increasing() {
  local content="$1"
  shift
  local marker prev=-1 offset
  for marker in "$@"; do
    if ! offset="$(_marker_offset "${content}" "${marker}")"; then
      return 1
    fi
    if [[ "${prev}" -ge 0 ]] && [[ "${offset}" -le "${prev}" ]]; then
      return 1
    fi
    prev="${offset}"
  done
  return 0
}

# Property registry for check_synthetic_adapter: property id, its
# good-adapter anchor marker, and its presence-fail reason, all in the
# fixed order flow/docs/adapter-contract.md's table and every fixture (good
# and bad-*) use. This single ordered list backs BOTH checks below:
# presence (the marker exists at all) and ordering (each property's marker
# must appear strictly after the previous property's, in this same order) --
# see check_synthetic_adapter.
_SYNTH_PROP_IDS=(
  baseline-gate worktree-isolation red-before-green gate-result-integrity
  planning-immutability sensitive-file-refusal verification-locality push-policy
)
_SYNTH_PROP_MARKERS=(
  "invoke run-gate.sh and require"
  ".worktrees/<id>-<desc>/, never in the main worktree."
  "procedure then make the failing tests pass with the simplest correct implementation."
  "GATE_STATUS=red is always treated as a failed gate, never as success, and a run"
  "The planning session never begins implementation and only writes to .plans/"
  "The procedure refuses to write environment files, credentials, secrets, or key"
  "Build and tests run only inside the assigned worktree, matching the worktree"
  "Pushes use git push or --force-with-lease only. Never force-push or bypass"
)
_SYNTH_PROP_REASONS=(
  "baseline gate omitted (no run-gate.sh invocation found)"
  "wrong worktree used (writes not confined to .worktrees/)"
  "green without an observed prior red"
  "failed probe/gate treated as success"
  "planning session writes a source file"
  "sensitive file written without refusal"
  "verification ran outside the assigned worktree"
  "force-push or gate bypass detected"
)

# ---------------------------------------------------------------------------
# check_synthetic_adapter <fixture-dir>
#
# Runs the static procedure-text checker against fixture-dir/procedure.md for
# all 8 properties. Prints one line per property: "<property-id>:pass" or
# "<property-id>:fail:<reason>". Returns 0 only if every property passes.
#
# Checks TWO things per property, not just one: (1) presence -- the marker
# exists in the doc at all; (2) ORDER -- the marker's byte offset is
# strictly greater than the immediately-preceding property's offset, i.e.
# the 8 properties must appear in _SYNTH_PROP_IDS' registry order, matching
# flow/docs/adapter-contract.md's table order and every fixture's numbered
# sections (1..8). A fixture that merely omits a marker fails on presence; a
# fixture where every marker is present but two sections were swapped fails
# specifically on order, with a distinct "out of order" reason -- see
# fixtures/bad-wrong-order/procedure.md and its self-test in parity.test.sh.
check_synthetic_adapter() {
  local fixture_dir="$1" content overall=0

  if ! content="$(read_doc "${fixture_dir}/procedure.md")"; then
    local prop
    for prop in "${_SYNTH_PROP_IDS[@]}"; do
      echo "${prop}:fail:procedure.md not found/unreadable: ${fixture_dir}/procedure.md"
    done
    return 1
  fi

  local i prop marker reason offset prev_offset=-1 prev_prop=""
  for i in "${!_SYNTH_PROP_IDS[@]}"; do
    prop="${_SYNTH_PROP_IDS[$i]}"
    marker="${_SYNTH_PROP_MARKERS[$i]}"
    reason="${_SYNTH_PROP_REASONS[$i]}"

    if ! offset="$(_marker_offset "${content}" "${marker}")"; then
      _prop "${prop}" 1 "${reason}"
      overall=$((overall + $?))
      continue
    fi

    if [[ "${prev_offset}" -ge 0 ]] && [[ "${offset}" -le "${prev_offset}" ]]; then
      _prop "${prop}" 1 "out of order: '${prop}' must appear after '${prev_prop}' (property-registry order), but its anchor is at or before the '${prev_prop}' anchor"
      overall=$((overall + $?))
    else
      _prop "${prop}" 0
      overall=$((overall + $?))
    fi
    prev_offset="${offset}"
    prev_prop="${prop}"
  done

  [[ "${overall}" -eq 0 ]]
}

# Anchor markers for the real Claude phase-2-worktree.md doc, shared between
# check_claude_adapter's presence/ordering checks below and parity.test.sh's
# real-adapter ordering self-test, so both sides can never drift apart.
# Verified verbatim against phase-2-worktree.md (grepped, not guessed).
_CLAUDE_WT_MARKER="git worktree add .worktrees/"
_CLAUDE_GATE_MARKER="hooks/scripts/run-gate.sh"
_CLAUDE_STATUS_MARKER='`GATE_STATUS=green` or `GATE_STATUS=unset` → this target passes.'

# ---------------------------------------------------------------------------
# check_claude_adapter <flow-dir>
#
# Same per-property pass/fail reporting, against the real committed Claude
# phase docs plus hooks/hooks.json wiring.
check_claude_adapter() {
  local flow_dir="$1" overall=0
  # Read every doc up front and capture read_doc's own return status per
  # call site (mirroring check_synthetic_adapter's fail-closed pattern) so a
  # missing/renamed file is reported as "<path> not found/unreadable"
  # instead of the misleadingly generic "does not document X" content-gap
  # reason -- read_doc's fail-closed contract (empty content on a missing
  # file) already made every property fail closed; this only makes the
  # REASON honestly distinguish "missing file" from "wrong content".
  local phase2 phase2_ok phase3 phase3_ok phase4 phase4_ok phase1 phase1_ok
  local hooks_json hooks_json_ok implementer_doc implementer_ok phase9 phase9_ok phase67 phase67_ok
  phase2="$(read_doc "skills/implement/phases/phase-2-worktree.md" "${flow_dir}")"; phase2_ok=$?
  phase3="$(read_doc "skills/implement/phases/phase-3-test-red.md" "${flow_dir}")"; phase3_ok=$?
  phase4="$(read_doc "skills/implement/phases/phase-4-implement-green.md" "${flow_dir}")"; phase4_ok=$?
  phase1="$(read_doc "skills/implement/phases/phase-1-plan.md" "${flow_dir}")"; phase1_ok=$?
  hooks_json="$(read_doc "hooks/hooks.json" "${flow_dir}")"; hooks_json_ok=$?
  implementer_doc="$(read_doc "agents/implementer.md" "${flow_dir}")"; implementer_ok=$?
  phase9="$(read_doc "skills/implement/phases/phase-9-pr.md" "${flow_dir}")"; phase9_ok=$?
  phase67="$(read_doc "skills/implement/phases/phase-6-7-review.md" "${flow_dir}")"; phase67_ok=$?

  # P1 baseline-gate: phase-2-worktree.md invokes run-gate.sh.
  if [[ "${phase2_ok}" -ne 0 ]]; then
    _prop "baseline-gate" 1 "skills/implement/phases/phase-2-worktree.md not found/unreadable"
  elif [[ "${phase2}" == *"${_CLAUDE_GATE_MARKER}"* ]]; then
    _prop "baseline-gate" 0
  else
    _prop "baseline-gate" 1 "phase-2-worktree.md does not invoke hooks/scripts/run-gate.sh"
  fi
  overall=$((overall + $?))

  # P2 worktree-isolation: phase-2-worktree.md creates the worktree, and
  # hooks/hooks.json wires guard-main-worktree.sh to enforce it at runtime.
  # Beyond mere presence, this also asserts the real doc's ACTUAL order:
  # worktree creation must physically precede the baseline-gate invocation,
  # which must physically precede its GATE_STATUS being interpreted. This is
  # NOT flow/docs/adapter-contract.md's table row order (baseline-gate is
  # listed before worktree-isolation there) -- a blanket "table order" check
  # against the real doc would be wrong, since the doc correctly documents a
  # different, narrower order than the table's listing order. This instead
  # guards only this specific, already-true-today pairwise ordering, so a
  # future silent reordering (e.g. running the gate before the worktree
  # exists, or trusting GATE_STATUS before the gate has run) can no longer
  # slip past a presence-only check undetected.
  if [[ "${phase2_ok}" -ne 0 ]]; then
    _prop "worktree-isolation" 1 "skills/implement/phases/phase-2-worktree.md not found/unreadable"
  elif [[ "${hooks_json_ok}" -ne 0 ]]; then
    _prop "worktree-isolation" 1 "hooks/hooks.json not found/unreadable"
  elif [[ "${phase2}" != *"${_CLAUDE_WT_MARKER}"* ]] \
      || [[ "${hooks_json}" != *"guard-main-worktree.sh"* ]]; then
    _prop "worktree-isolation" 1 "phase-2-worktree.md/hooks.json do not confine writes to .worktrees/"
  elif [[ "${phase2}" != *"${_CLAUDE_GATE_MARKER}"* ]] || [[ "${phase2}" != *"${_CLAUDE_STATUS_MARKER}"* ]]; then
    _prop "worktree-isolation" 1 "phase-2-worktree.md is missing the run-gate.sh/GATE_STATUS anchors needed for the worktree-creation-ordering check"
  elif ! _markers_strictly_increasing "${phase2}" \
      "${_CLAUDE_WT_MARKER}" "${_CLAUDE_GATE_MARKER}" "${_CLAUDE_STATUS_MARKER}"; then
    _prop "worktree-isolation" 1 "out of order in phase-2-worktree.md: worktree creation must precede the baseline-gate invocation (hooks/scripts/run-gate.sh), which must precede GATE_STATUS interpretation"
  else
    _prop "worktree-isolation" 0
  fi
  overall=$((overall + $?))

  # P3 red-before-green: phase-3 says tests should fail, phase-4 makes them pass.
  if [[ "${phase3_ok}" -ne 0 ]]; then
    _prop "red-before-green" 1 "skills/implement/phases/phase-3-test-red.md not found/unreadable"
  elif [[ "${phase4_ok}" -ne 0 ]]; then
    _prop "red-before-green" 1 "skills/implement/phases/phase-4-implement-green.md not found/unreadable"
  elif [[ "${phase3}" == *"Tests should fail."* ]] && [[ "${phase4}" == *"make failing tests pass."* ]]; then
    _prop "red-before-green" 0
  else
    _prop "red-before-green" 1 "phase-3/phase-4 do not document a red-then-green sequence"
  fi
  overall=$((overall + $?))

  # P4 gate-result-integrity: phase-2-worktree.md's Interpret section must
  # never let a red gate resolve to a pass.
  if [[ "${phase2_ok}" -ne 0 ]]; then
    _prop "gate-result-integrity" 1 "skills/implement/phases/phase-2-worktree.md not found/unreadable"
  elif [[ "${phase2}" == *"${_CLAUDE_STATUS_MARKER}"* ]] \
      && [[ "${phase2}" == *'`GATE_STATUS=red` → this target fails'* ]]; then
    _prop "gate-result-integrity" 0
  else
    _prop "gate-result-integrity" 1 "phase-2-worktree.md does not document correct GATE_STATUS interpretation"
  fi
  overall=$((overall + $?))

  # P5 planning-immutability: phase-1-plan.md forbids starting Phase 2 in a
  # planning session, and guard-main-worktree.sh enforces it at runtime.
  if [[ "${phase1_ok}" -ne 0 ]]; then
    _prop "planning-immutability" 1 "skills/implement/phases/phase-1-plan.md not found/unreadable"
  elif [[ "${hooks_json_ok}" -ne 0 ]]; then
    _prop "planning-immutability" 1 "hooks/hooks.json not found/unreadable"
  elif [[ "${phase1}" == *"Never begin Phase 2 in a session that created a new plan"* ]] \
      && [[ "${hooks_json}" == *"guard-main-worktree.sh"* ]]; then
    _prop "planning-immutability" 0
  else
    _prop "planning-immutability" 1 "phase-1-plan.md/hooks.json do not enforce planning-session write immutability"
  fi
  overall=$((overall + $?))

  # P6 sensitive-file-refusal: hooks.json wires check-sensitive-files.sh.
  if [[ "${hooks_json_ok}" -ne 0 ]]; then
    _prop "sensitive-file-refusal" 1 "hooks/hooks.json not found/unreadable"
  elif [[ "${hooks_json}" == *"check-sensitive-files.sh"* ]]; then
    _prop "sensitive-file-refusal" 0
  else
    _prop "sensitive-file-refusal" 1 "hooks.json does not wire check-sensitive-files.sh"
  fi
  overall=$((overall + $?))

  # P7 verification-locality: implementer.md targets the worktree explicitly
  # on every command, and guard-main-worktree.sh backs the write half.
  if [[ "${implementer_ok}" -ne 0 ]]; then
    _prop "verification-locality" 1 "agents/implementer.md not found/unreadable"
  elif [[ "${hooks_json_ok}" -ne 0 ]]; then
    _prop "verification-locality" 1 "hooks/hooks.json not found/unreadable"
  elif [[ "${implementer_doc}" == *"target it explicitly on every command"* ]] \
      && [[ "${hooks_json}" == *"guard-main-worktree.sh"* ]]; then
    _prop "verification-locality" 0
  else
    _prop "verification-locality" 1 "implementer.md/hooks.json do not confine verification to the assigned worktree"
  fi
  overall=$((overall + $?))

  # P8 push-policy: phase-9-pr.md uses --force-with-lease and never a bare
  # force/no-verify push; phase-6-7-review.md states quality gates are
  # mandatory (the "never bypass a gate" facet).
  if [[ "${phase9_ok}" -ne 0 ]]; then
    _prop "push-policy" 1 "skills/implement/phases/phase-9-pr.md not found/unreadable"
  elif [[ "${phase67_ok}" -ne 0 ]]; then
    _prop "push-policy" 1 "skills/implement/phases/phase-6-7-review.md not found/unreadable"
  elif _check_push_policy_text "${phase9}" && [[ "${phase67}" == *"quality gates are mandatory"* ]]; then
    _prop "push-policy" 0
  else
    _prop "push-policy" 1 "phase-9-pr.md/phase-6-7-review.md do not document force-push refusal / mandatory gates"
  fi
  overall=$((overall + $?))

  [[ "${overall}" -eq 0 ]]
}

# Anchor markers for the real Codex codex.md doc, shared between
# check_codex_adapter's presence/ordering checks below and parity.test.sh's
# real-adapter ordering self-test, so both sides can never drift apart.
# Verified verbatim against codex.md (grepped, not guessed).
_CODEX_STOP_MARKER="Stop before mutations"
_CODEX_CREATE_MARKER="create the worktree"

# ---------------------------------------------------------------------------
# check_codex_adapter <flow-dir>
#
# Same per-property pass/fail reporting, against the real committed
# skills/implement/codex.md, skills/codex-runtime/SKILL.md, and codex/hooks.json
# wiring. P1 (baseline-gate) and the gate-interpretation facet of P4 are
# KNOWN, currently-failing checks here -- codex.md does not call run-gate.sh
# yet. The caller (parity.test.sh) wraps those two lines through `xfail`,
# never asserts them as plain passes.
check_codex_adapter() {
  local flow_dir="$1" overall=0
  local codex_doc codex_ok runtime_doc runtime_ok hooks_json hooks_ok

  codex_doc="$(read_doc "skills/implement/codex.md" "${flow_dir}")"; codex_ok=$?
  runtime_doc="$(read_doc "skills/codex-runtime/SKILL.md" "${flow_dir}")"; runtime_ok=$?
  hooks_json="$(read_doc "codex/hooks.json" "${flow_dir}")"; hooks_ok=$?

  # P1 baseline-gate: KNOWN GAP -- codex.md never invokes run-gate.sh.
  if [[ "${codex_ok}" -ne 0 && "${runtime_ok}" -ne 0 ]]; then
    _prop "baseline-gate" 1 "skills/implement/codex.md and skills/codex-runtime/SKILL.md not found/unreadable"
  elif [[ "${codex_doc}" == *"run-gate.sh"* ]] || [[ "${runtime_doc}" == *"run-gate.sh"* ]]; then
    _prop "baseline-gate" 0
  else
    _prop "baseline-gate" 1 "codex.md never invokes hooks/scripts/run-gate.sh (#517 Codex implement gate parity)"
  fi
  overall=$((overall + $?))

  # P2 worktree-isolation: codex.md creates the worktree, codex/hooks.json
  # wires guard-main-worktree.sh. Beyond mere presence, this also asserts
  # the real doc's ACTUAL order: the planning-mode "Stop before mutations"
  # instruction must physically precede the apply-mode "create the
  # worktree" instruction -- the one pairwise ordering guarantee that
  # already holds true in codex.md today, guarding against a future silent
  # reordering (e.g. creating the worktree before planning mode stops)
  # slipping past a presence-only check undetected.
  if [[ "${codex_ok}" -ne 0 ]]; then
    _prop "worktree-isolation" 1 "skills/implement/codex.md not found/unreadable"
  elif [[ "${hooks_ok}" -ne 0 ]]; then
    _prop "worktree-isolation" 1 "codex/hooks.json not found/unreadable"
  elif [[ "${codex_doc}" != *"${_CODEX_CREATE_MARKER}"* ]] \
      || [[ "${hooks_json}" != *"guard-main-worktree.sh"* ]]; then
    _prop "worktree-isolation" 1 "codex.md/hooks.json do not confine writes to .worktrees/"
  elif [[ "${codex_doc}" != *"${_CODEX_STOP_MARKER}"* ]]; then
    _prop "worktree-isolation" 1 "codex.md is missing the 'Stop before mutations' anchor needed for the planning-before-worktree-creation ordering check"
  elif ! _markers_strictly_increasing "${codex_doc}" "${_CODEX_STOP_MARKER}" "${_CODEX_CREATE_MARKER}"; then
    _prop "worktree-isolation" 1 "out of order in codex.md: planning-mode 'Stop before mutations' must precede apply-mode worktree creation ('create the worktree')"
  else
    _prop "worktree-isolation" 0
  fi
  overall=$((overall + $?))

  # P3 red-before-green: codex.md's "implement test-first" step.
  if [[ "${codex_ok}" -ne 0 ]]; then
    _prop "red-before-green" 1 "skills/implement/codex.md not found/unreadable"
  elif [[ "${codex_doc}" == *"test-first"* ]]; then
    _prop "red-before-green" 0
  else
    _prop "red-before-green" 1 "codex.md does not document a test-first sequence"
  fi
  overall=$((overall + $?))

  # P4 gate-result-integrity: KNOWN GAP -- no run-gate.sh output to interpret.
  if [[ "${codex_ok}" -ne 0 && "${runtime_ok}" -ne 0 ]]; then
    _prop "gate-result-integrity" 1 "skills/implement/codex.md and skills/codex-runtime/SKILL.md not found/unreadable"
  elif [[ "${codex_doc}" == *"GATE_STATUS"* ]] || [[ "${runtime_doc}" == *"GATE_STATUS"* ]]; then
    _prop "gate-result-integrity" 0
  else
    _prop "gate-result-integrity" 1 "no run-gate.sh output for codex.md to interpret (#517 Codex implement gate parity)"
  fi
  overall=$((overall + $?))

  # P5 planning-immutability: codex.md's /plan stops before mutations;
  # guard-main-worktree.sh enforces it at runtime.
  if [[ "${codex_ok}" -ne 0 ]]; then
    _prop "planning-immutability" 1 "skills/implement/codex.md not found/unreadable"
  elif [[ "${hooks_ok}" -ne 0 ]]; then
    _prop "planning-immutability" 1 "codex/hooks.json not found/unreadable"
  elif [[ "${codex_doc}" == *"${_CODEX_STOP_MARKER}"* ]] && [[ "${hooks_json}" == *"guard-main-worktree.sh"* ]]; then
    _prop "planning-immutability" 0
  else
    _prop "planning-immutability" 1 "codex.md/hooks.json do not enforce planning-session write immutability"
  fi
  overall=$((overall + $?))

  # P6 sensitive-file-refusal: codex/hooks.json wires check-sensitive-files.sh.
  if [[ "${hooks_ok}" -ne 0 ]]; then
    _prop "sensitive-file-refusal" 1 "codex/hooks.json not found/unreadable"
  elif [[ "${hooks_json}" == *"check-sensitive-files.sh"* ]]; then
    _prop "sensitive-file-refusal" 0
  else
    _prop "sensitive-file-refusal" 1 "codex/hooks.json does not wire check-sensitive-files.sh"
  fi
  overall=$((overall + $?))

  # P7 verification-locality: codex-runtime/SKILL.md's checkpoint records
  # worktree identity.
  if [[ "${runtime_ok}" -ne 0 ]]; then
    _prop "verification-locality" 1 "skills/codex-runtime/SKILL.md not found/unreadable"
  elif [[ "${runtime_doc}" == *"plan path, worktree, last completed gate, and status"* ]]; then
    _prop "verification-locality" 0
  else
    _prop "verification-locality" 1 "codex-runtime/SKILL.md's checkpoint does not record worktree identity"
  fi
  overall=$((overall + $?))

  # P8 push-policy: codex.md's literal force-push/gate-bypass refusal sentence.
  if [[ "${codex_ok}" -ne 0 ]]; then
    _prop "push-policy" 1 "skills/implement/codex.md not found/unreadable"
  elif [[ "${codex_doc}" == *"Never force-push or bypass security/design/approval gates."* ]]; then
    _prop "push-policy" 0
  else
    _prop "push-policy" 1 "codex.md does not state the force-push/gate-bypass refusal"
  fi
  overall=$((overall + $?))

  [[ "${overall}" -eq 0 ]]
}

# ---------------------------------------------------------------------------
# drive_baseline_gate <root-dir> [slug]
#
# Thin wrapper over hooks/scripts/run-gate.sh; prints "green"|"red"|"unset"|"error".
drive_baseline_gate() {
  local root_dir="$1" slug="${2:-}" out
  if [[ -n "${slug}" ]]; then
    out="$(cd "${root_dir}" && sh "${FLOW_DIR}/hooks/scripts/run-gate.sh" "${slug}" 2>/dev/null)"
  else
    out="$(cd "${root_dir}" && sh "${FLOW_DIR}/hooks/scripts/run-gate.sh" 2>/dev/null)"
  fi
  case "${out}" in
    *GATE_STATUS=green*) echo "green" ;;
    *GATE_STATUS=red*) echo "red" ;;
    *GATE_STATUS=unset*) echo "unset" ;;
    *) echo "error" ;;
  esac
}

# ---------------------------------------------------------------------------
# drive_worktree_guard <repo-root> <abs-file-path>
#
# Thin wrapper over hooks/scripts/guard-main-worktree.sh; prints
# "allowed"|"blocked"|"error". guard-main-worktree.sh uses exit 2 for BOTH a
# genuine policy block AND every infra-error case (missing jq, missing
# realpath/readlink, unparseable JSON, an unresolvable symlink) -- all of
# those also print a "BLOCKED: ..." stderr line, so exit code alone cannot
# tell them apart. Only the literal, genuine-block message text ("targets
# the main worktree, not a feature worktree") maps to "blocked"; any other
# non-zero exit (an infra failure that never reached the real guard logic)
# maps to "error" instead, mirroring drive_baseline_gate's "error" state for
# run-gate.sh -- so a broken environment can never masquerade as a passing
# "bad sim" assertion.
drive_worktree_guard() {
  local repo_root="$1" file_path="$2" input rc err
  input="$(jq -n --arg fp "${file_path}" '{tool_input:{file_path:$fp}}')"
  err="$( (cd "${repo_root}" && printf '%s' "${input}" | sh "${FLOW_DIR}/hooks/scripts/guard-main-worktree.sh") 2>&1 1>/dev/null )"
  rc=$?
  if [[ "${rc}" -eq 0 ]]; then
    echo "allowed"
  elif [[ "${err}" == *"targets the main worktree, not a feature worktree"* ]]; then
    echo "blocked"
  else
    echo "error"
  fi
}

# ---------------------------------------------------------------------------
# drive_sensitive_files_guard <abs-file-path>
#
# Thin wrapper over hooks/scripts/check-sensitive-files.sh; prints
# "allowed"|"blocked"|"error". Same rationale as drive_worktree_guard above:
# check-sensitive-files.sh's infra-error paths (missing jq, parse failure,
# unresolvable path) also exit 2 with a "BLOCKED: ..." stderr line, so only
# the three genuine-refusal message texts ("Refusing to write to environment
# file:", "Refusing to write to sensitive file:", "Refusing to write to key
# file:") map to "blocked"; every other non-zero exit maps to "error".
drive_sensitive_files_guard() {
  local file_path="$1" input rc err
  input="$(jq -n --arg fp "${file_path}" '{tool_input:{file_path:$fp}}')"
  err="$(printf '%s' "${input}" | sh "${FLOW_DIR}/hooks/scripts/check-sensitive-files.sh" 2>&1 1>/dev/null)"
  rc=$?
  if [[ "${rc}" -eq 0 ]]; then
    echo "allowed"
  elif [[ "${err}" == *"Refusing to write to environment file:"* ]] \
      || [[ "${err}" == *"Refusing to write to sensitive file:"* ]] \
      || [[ "${err}" == *"Refusing to write to key file:"* ]]; then
    echo "blocked"
  else
    echo "error"
  fi
}

# ---------------------------------------------------------------------------
# drive_checkpoint <init|advance|block|complete|get|clear> <path> [workflow] [target] [phase]
#
# Thin wrapper over codex/checkpoint.mjs.
drive_checkpoint() {
  local cmd="$1" path="$2"
  shift 2
  node "${FLOW_DIR}/codex/checkpoint.mjs" "${cmd}" "${path}" "$@"
}

# ---------------------------------------------------------------------------
# verify_worktree_match <checkpoint-path> <claimed-verification-path>
#
# Reads the worktree/target recorded in the checkpoint at <checkpoint-path>
# (via `drive_checkpoint get`) and compares it against
# <claimed-verification-path>. Returns 0 if they match (verification ran
# where the checkpoint says the worktree is), non-zero otherwise
# (verification ran elsewhere).
verify_worktree_match() {
  local ckpt="$1" claimed="$2" state target
  state="$(drive_checkpoint get "${ckpt}" 2>/dev/null)" || return 1
  target="$(printf '%s' "${state}" | jq -r '.target // empty' 2>/dev/null)"
  [[ -n "${target}" ]] || return 1
  case "${claimed}" in
    *"/.worktrees/${target}") return 0 ;;
    *) return 1 ;;
  esac
}

# ---------------------------------------------------------------------------
# verify_gate_interpretation <gate-status-line> <exit-code> <claimed-verdict>
#
# Deterministic model, no script: given a captured GATE_STATUS=<...> line and
# the gate command's exit code, checks whether <claimed-verdict>
# ("pass"|"fail") is the CORRECT interpretation -- GATE_STATUS=red or a
# non-zero exit with no GATE_STATUS line at all must always resolve to
# "fail", never "pass". Returns 0 if the claimed verdict is correct,
# non-zero if it silently treats a failed probe as a success.
verify_gate_interpretation() {
  local line="$1" exit_code="$2" claimed="$3" correct
  case "${line}" in
    *GATE_STATUS=red*) correct="fail" ;;
    *GATE_STATUS=green*) correct="pass" ;;
    *GATE_STATUS=unset*) correct="pass" ;;
    *)
      if [[ "${exit_code}" -ne 0 ]]; then
        correct="fail"
      else
        correct="pass"
      fi
      ;;
  esac
  [[ "${claimed}" == "${correct}" ]]
}

# ---------------------------------------------------------------------------
# verify_red_before_green <space-separated-event-sequence>
#
# Deterministic model, no script: "red green" -> pass, "green" -> fail,
# "green red" -> fail (green must never appear before an observed red).
verify_red_before_green() {
  local seq="$1" event seen_red=0
  # set -f disables pathname/glob expansion for the unquoted word-split
  # below -- a '*' token in the sequence must never expand against the CWD.
  set -f
  for event in ${seq}; do
    case "${event}" in
      red) seen_red=1 ;;
      green)
        if [[ "${seen_red}" -ne 1 ]]; then
          set +f
          return 1
        fi
        ;;
    esac
  done
  set +f
  return 0
}

# ---------------------------------------------------------------------------
# verify_push_policy <git-push-command-string>
#
# Deterministic model, no script: accepts "git push" and
# "git push --force-with-lease ..."; rejects any bare
# "--force"/"-f"/"--no-verify".
verify_push_policy() {
  local cmd="$1" token
  # set -f disables pathname/glob expansion for the unquoted word-split
  # below -- a '*' token in the command string must never expand against
  # the CWD.
  set -f
  for token in ${cmd}; do
    case "${token}" in
      --force) set +f; return 1 ;;
      -f) set +f; return 1 ;;
      --no-verify) set +f; return 1 ;;
    esac
  done
  set +f
  return 0
}

# ---------------------------------------------------------------------------
# xfail <label> <check-exit-code> <reference-note>
#
#   check-exit-code != 0 (failed, as expected) -> prints "XFAIL: <label> (<reference-note>)",
#     returns 0 (caller must NOT count this as a failure).
#   check-exit-code == 0 (unexpectedly passing) -> prints "XPASS: <label> — check
#     unexpectedly passed; flip the xfail marker in parity.test.sh (<reference-note>)",
#     returns 1 (caller MUST count this as a failure).
xfail() {
  local label="$1" code="$2" note="$3"
  if [[ "${code}" -ne 0 ]]; then
    echo "XFAIL: ${label} (${note})"
    return 0
  else
    echo "XPASS: ${label} — check unexpectedly passed; flip the xfail marker in parity.test.sh (${note})"
    return 1
  fi
}
