#!/usr/bin/env bash
# ensure-issue.sh -- deterministic create/recover/repair/link helper for one
# refine manifest entry (a split-child or companion-design issue), ticket
# #876 (2/12 of #661). Invoked identically by refine/SKILL.md (Claude) and
# refine/codex.md (Codex) so a confirmed manifest entry resolves to at most
# one GitHub issue across timeouts, retries, crashes, and resumed
# refinement sessions -- never a duplicate create.
#
# HEADER INVARIANT: this script writes ONLY under .plans/ (a
# .plans/.refine-<parent>.checkpoint.json checkpoint file) and
# ${TMPDIR:-/tmp}/cenci/ (its own scratch files). guard-main-worktree.sh
# cannot see inside a single Bash invocation of this script, so this
# invariant is enforced here, by construction: --checkpoint is validated
# against exactly those two path shapes before any write, never accepted
# as an arbitrary destination.
#
# Usage:
#   ensure-issue.sh init   --repo <owner>/<repo> --parent <n> \
#                           --checkpoint <path> --manifest <path> \
#                           [--parent-meta <path>]
#   ensure-issue.sh ensure --checkpoint <path> --repo <owner>/<repo> \
#                           --slot <slot> --title <title-file> --body <body-file>
#   ensure-issue.sh link   --checkpoint <path> --repo <owner>/<repo> \
#                           --slot <slot> --parent <n>
#   ensure-issue.sh clear  --checkpoint <path>
#
# Exit codes:
#   0  success
#   1  a create/POST attempt could not be verified this run (retry recovers
#      it via marker scan on the next `ensure` call -- never a duplicate)
#   2  usage / preflight failure (bad args, missing jq/openssl, unreadable
#      or malformed manifest, invalid --checkpoint path shape, unknown slot)
#   10 multiple marker matches for one nonce -- fail closed, no write, a
#      partial-state report naming every candidate issue number
#   11 checkpoint missing or corrupt (bad JSON / wrong schemaVersion) on any
#      subcommand other than `init` -- fail closed, never silently re-POST
#   12 candidate-list call itself failed -- fail closed, no POST
#   13 an existing issue diverged from the manifest and the repair PATCH
#      itself failed -- unrepairable this run, verification recorded as
#      "mismatch" so a human can reconcile manually
#   14 the --parent sub-issue link call failed
#
# Checkpoint schema (v1), written atomically (temp file in the same
# directory as the destination, then `mv`, mirroring
# flow/skills/maintain/scripts/check.sh's write_report_atomically):
#   {
#     "schemaVersion": 1, "repo": "<owner>/<repo>", "parent": <n>,
#     "creatorLogin": "<login>", "createdAt": "<RFC3339>",
#     "entries": [
#       { "slot": "child-K-of-N" | "design", "nonce": "<32 hex>",
#         "titleHash": "<sha256>", "bodyHash": "<sha256>",
#         "labels": "<compact JSON array, stored as a string>",
#         "milestone": <n|null>, "issueNumber": <n|null>,
#         "verification": "pending" | "verified" | "mismatch",
#         "link": "unlinked" | "linked" | "verified" }
#     ]
#   }
# `labels` is stored as a compact-JSON-encoded STRING (via jq's `tojson`)
# rather than a nested array, so a plain `jq -r '.entries[N].labels'` read
# (the read pattern every caller of this checkpoint uses) returns the exact
# compact array text directly, with no pretty-printed reformatting to
# reverse.
#
# Identity: `init` resolves the creating login once via `gh api user --jq
# .login` and persists it as `creatorLogin`. Every later `ensure` call
# resumes using that PERSISTED login for both the `creator=` list filter and
# the per-candidate author check -- never a second identity lookup -- so a
# resumed run recovers the original creator's issues even if a different
# identity is authenticated in the resuming session.
#
# Read-back verification everywhere: a returned numeric ID from the create
# request is never itself proof the body landed -- this script always
# re-derives state by re-listing marker-bearing candidates rather than
# trusting the create response, so a client-side timeout or a malformed
# response body recovers cleanly via marker scan on the very next `ensure`
# call instead of creating a duplicate issue.
#
# No issue-search endpoint: candidates are listed via paginated REST
# (`repos/<o>/<r>/issues?state=all&creator=<login>&since=<ts>` with
# `--paginate`) rather than a dedicated search query, matching this script's
# documented REST-only surface (decision #3). `since=` floors at the
# checkpoint's `createdAt` minus a clock-skew margin; a list-call failure or
# an unparseable response is a hard stop (exit 12), never a silent
# fall-through to create.
#
# Canonical with-meta / no-meta label-inheritance forms (documentation --
# the real merge below is parameterized over the manifest's own seed
# array, but every site follows exactly this shape, ported from
# refine/SKILL.md's former Pass 1 / companion-design jq snippets, #876):
#   With-meta (child):
#     jq -n --rawfile title <title> --rawfile body <body> --slurpfile meta <parent-meta>
#       '{title: ($title | rtrimstr("\n")), body: $body,
#         labels: (["Refined"] + [$meta[0].labels[].name | select(. as $n |
#           (["Refined","Working","Planned","In Review","Implemented","Design","Designed","automerge:ok","Browser","ui:visual-check"] | index($n)) | not)])}'
#   With-meta (design ticket):
#     ... labels: (["Refined","Design"] + [$meta[0].labels[].name | select(...)]) ...
#   No-meta fallback (the parent-metadata fetch failed after its one retry
#   -- graceful degrade, never a STOP; see docs/pipeline-safety.md):
#     child:  '{title: ..., body: ..., labels: ["Refined"]}'
#     design: '{title: ..., body: ..., labels: ["Refined","Design"]}'
#   When the parent-metadata fetch fails, the caller passes no
#   --parent-meta and this script notes that milestone/label inheritance
#   was skipped rather than aborting -- a child created without the
#   parent's milestone is still a correct, complete ticket.
set -uo pipefail

SELF="ensure-issue.sh"

log_err() { printf '%s: %s\n' "${SELF}" "$*" >&2; }
die() {
  local code="$1"; shift
  log_err "$*"
  exit "${code}"
}

command -v jq >/dev/null 2>&1 || die 2 "jq is required but was not found on PATH."
command -v openssl >/dev/null 2>&1 || die 2 "openssl is required but was not found on PATH (needed to mint nonces; no weaker fallback)."

# resolve_path <path> -- canonicalize an *existing* path (symlinks resolved).
# Prefer realpath; fall back to GNU readlink -f (probed via a known-existing
# path so a BSD/other readlink lacking -f is rejected here, not at use time).
# Neither available -> fail closed. Deliberately does NOT use GNU realpath's
# `-m` extension (BSD/macOS realpath rejects -m outright, which would make
# every --checkpoint-taking subcommand die unconditionally on those hosts) --
# see canonicalize_path below, ported from
# flow/hooks/scripts/check-sensitive-files.sh's resolve_path/canonicalize_path
# (also mirrored in guard-main-worktree.sh's canonicalize_target), for the
# portable replacement that only ever feeds resolve_path an existing path.
if command -v realpath >/dev/null 2>&1; then
  resolve_path() { realpath -- "$1"; }
elif readlink -f / >/dev/null 2>&1; then
  resolve_path() { readlink -f -- "$1"; }
else
  die 2 "realpath or 'readlink -f' is required but neither was found on PATH (needed to canonicalize --checkpoint/--parent-meta paths)."
fi

usage() {
  cat >&2 <<'EOF'
usage:
  ensure-issue.sh init   --repo <owner>/<repo> --parent <n> --checkpoint <path> --manifest <path> [--parent-meta <path>]
  ensure-issue.sh ensure --checkpoint <path> --repo <owner>/<repo> --slot <slot> --title <title-file> --body <body-file>
  ensure-issue.sh link   --checkpoint <path> --repo <owner>/<repo> --slot <slot> --parent <n>
  ensure-issue.sh clear  --checkpoint <path>
EOF
  exit 2
}

MARKER_PREFIX="<!-- cenci-refine-create:"
MARKER_SUFFIX=" -->"

# LIFECYCLE_EXCLUSION -- carried over verbatim from refine/SKILL.md's former
# inline construction (#798/#848, retargeted here by #876): every label the
# parent currently has is inherited EXCEPT these 10 lifecycle/transient and
# per-ticket-grant markers, which are per-ticket state, never classification.
LIFECYCLE_EXCLUSION_JSON='["Refined","Working","Planned","In Review","Implemented","Design","Designed","automerge:ok","Browser","ui:visual-check"]'

# --- path safety --------------------------------------------------------
# lexical_collapse <path> -- collapses "." and ".." segments in an absolute
# path without touching the filesystem. Ported (bash-ified) from
# flow/hooks/scripts/check-sensitive-files.sh's lexical_collapse -- part of
# the portable replacement for GNU-only `realpath -m` (#876 follow-up).
lexical_collapse() {
  local path="$1" old_ifs="${IFS}" result="" seg
  local -a segs
  IFS='/'
  set -f
  segs=( ${path} )
  set +f
  IFS="${old_ifs}"
  for seg in "${segs[@]}"; do
    case "${seg}" in
      "" | ".") ;;
      "..") result="${result%/*}" ;;
      *) result="${result}/${seg}" ;;
    esac
  done
  [[ -z "${result}" ]] && result="/"
  printf '%s\n' "${result}"
}

# canonicalize_path <path> -- parent-anchored canonicalization: prints the
# canonical form (symlinks resolved via the nearest existing ancestor,
# "."/".." collapsed) to stdout and returns 0. Only ever feeds *existing*
# paths to resolve_path, sidestepping realpath/readlink -f divergence on
# non-existent trailing components -- this IS the portable replacement for
# `realpath -m -- <path>` (a GNU-only extension BSD/macOS realpath rejects
# outright). Returns 1 (prints nothing) when a path component is a symlink
# node ([-L], no dereference) that does not resolve to an existing target
# ([-e] false -- dangling target or symlink loop), or when resolve_path
# itself fails; callers must fail closed on that. Ported (bash-ified) from
# flow/hooks/scripts/check-sensitive-files.sh's canonicalize_path (also
# mirrored in guard-main-worktree.sh's canonicalize_target).
canonicalize_path() {
  local path="$1" ancestor tail seg resolved_ancestor
  if [[ -e "${path}" ]]; then
    resolve_path "${path}" 2>/dev/null || return 1
    return 0
  fi
  ancestor="${path}"
  tail=""
  while [[ -n "${ancestor}" && ! -d "${ancestor}" ]]; do
    if [[ -L "${ancestor}" && ! -e "${ancestor}" ]]; then
      return 1
    fi
    seg="${ancestor##*/}"
    if [[ -z "${tail}" ]]; then
      tail="${seg}"
    else
      tail="${seg}/${tail}"
    fi
    # Belt-and-braces (#876 follow-up): ${ancestor%/*} is a no-op on a
    # slash-free segment (e.g. a relative path whose non-existent portion
    # reaches its first component), which would otherwise spin forever.
    # Callers are expected to absolutize before calling in, but fail closed
    # here too so a future caller that forgets to can't reintroduce the hang.
    if [[ "${ancestor%/*}" == "${ancestor}" ]]; then
      return 1
    fi
    ancestor="${ancestor%/*}"
  done
  [[ -z "${ancestor}" ]] && ancestor="/"
  resolved_ancestor="$(resolve_path "${ancestor}" 2>/dev/null)" || return 1
  lexical_collapse "${resolved_ancestor}/${tail}"
}

# validate_checkpoint_path <path> -- true only for a path that, once
# normalized, either lives under ${TMPDIR:-/tmp}/cenci/ or is exactly
# <cwd>/.plans/.refine-<digits>.checkpoint.json. Never accepts an arbitrary
# destination from a flag -- this is the self-restriction the header
# invariant documents.
#
# A literal ".." path segment is rejected outright before any resolution --
# `/tmp/cenci/../../../home/user/.claude/settings.json` is a bare-string
# prefix match against "${tmp_root}"* but resolves well outside tmp_root, so
# a pure string test alone is not sufficient (#876 review finding 1). Every
# real caller (refine/SKILL.md, refine/codex.md) always passes
# `.plans/.refine-<n>.checkpoint.json` relative to its own cwd -- never an
# arbitrary absolute path -- so anchoring the .plans/ branch to the
# resolved cwd (rather than accepting any directory named .plans/ anywhere
# on the filesystem, including the main worktree's) closes that hole too.
validate_checkpoint_path() {
  local path="$1"
  # No trailing slash: canonicalize_path's not-yet-existing-ancestor walk
  # strips one path segment per iteration, and a trailing "/" would make the
  # first iteration strip nothing (an empty segment) instead of "cenci" --
  # realpath -m tolerated this by normalizing the trailing slash away itself,
  # but canonicalize_path's fallback does not touch the filesystem, so the
  # slash must already be gone before it's called.
  local tmp_root="${TMPDIR:-/tmp}/cenci"

  case "${path}" in
    ../* | */../* | */.. | ..) return 1 ;;
  esac

  # Absolutize before canonicalize_path: its not-yet-existing-ancestor walk
  # only terminates on "" (an absolute-path property) -- a relative path
  # whose non-existent portion reaches a slash-free first segment would
  # otherwise spin (#876 review finding: infinite loop on a relative
  # --checkpoint, which is exactly the shape every real caller passes).
  case "${path}" in
    /*) ;;
    *) path="$(pwd)/${path}" || return 1 ;;
  esac

  local resolved_path resolved_tmp_root resolved_plans_dir
  resolved_path="$(canonicalize_path "${path}")" || return 1
  resolved_tmp_root="$(canonicalize_path "${tmp_root}")" || return 1
  resolved_plans_dir="$(canonicalize_path "./.plans")" || return 1

  case "${resolved_path}" in
    "${resolved_tmp_root}"/*) return 0 ;;
  esac

  if [[ "$(dirname -- "${resolved_path}")" == "${resolved_plans_dir}" ]]; then
    local base
    base="$(basename -- "${resolved_path}")"
    [[ "${base}" =~ ^\.refine-[0-9]+\.checkpoint\.json$ ]] && return 0
  fi

  return 1
}

require_checkpoint_path() {
  local path="$1"
  validate_checkpoint_path "${path}" \
    || die 2 "refusing to write outside .plans/ or \${TMPDIR:-/tmp}/cenci/: ${path}"
}

# validate_tmp_root_path <path> -- narrower sibling of
# validate_checkpoint_path for flags (e.g. --parent-meta) that are
# documented as living under ${TMPDIR:-/tmp}/cenci/ only -- never accepts
# the .plans/ checkpoint shape, and rejects the same ".." traversal
# attempts before resolution (#876 review finding 2).
validate_tmp_root_path() {
  local path="$1"
  # No trailing slash -- see validate_checkpoint_path's identical comment.
  local tmp_root="${TMPDIR:-/tmp}/cenci"

  case "${path}" in
    ../* | */../* | */.. | ..) return 1 ;;
  esac

  # Absolutize before canonicalize_path -- see validate_checkpoint_path's
  # identical comment (#876 review finding).
  case "${path}" in
    /*) ;;
    *) path="$(pwd)/${path}" || return 1 ;;
  esac

  local resolved_path resolved_tmp_root
  resolved_path="$(canonicalize_path "${path}")" || return 1
  resolved_tmp_root="$(canonicalize_path "${tmp_root}")" || return 1

  case "${resolved_path}" in
    "${resolved_tmp_root}"/*) return 0 ;;
  esac

  return 1
}

# --- atomic write --------------------------------------------------------
# write_atomically <destination> <content> -- temp-file-in-same-dir + mv,
# mirroring write_report_atomically in
# flow/skills/maintain/scripts/check.sh:403.
write_atomically() {
  local destination="$1" content="$2" parent tmp
  parent="$(dirname -- "${destination}")"
  if [[ ! -d "${parent}" ]] && ! mkdir -p -- "${parent}" 2>/dev/null; then
    log_err "failed to create checkpoint directory: ${parent}"
    return 1
  fi
  tmp="$(mktemp "${parent}/.ensure-issue.XXXXXX")" || {
    log_err "failed to create temporary checkpoint file next to: ${destination}"
    return 1
  }
  if ! printf '%s' "${content}" > "${tmp}"; then
    rm -f -- "${tmp}"
    log_err "failed to render checkpoint content: ${destination}"
    return 1
  fi
  if ! mv -f -- "${tmp}" "${destination}" 2>/dev/null; then
    rm -f -- "${tmp}"
    log_err "failed to write checkpoint atomically: ${destination}"
    return 1
  fi
}

# --- checkpoint read (pure extractor; no fail()/die() inside) ------------
# checkpoint_json_raw <path> -- prints the checkpoint's content on stdout
# and returns 0 only when the file exists, is readable, is valid JSON, and
# carries schemaVersion == 1; otherwise returns 1 with no stdout. Safe to
# call inside $(...) since it never itself terminates the script.
checkpoint_json_raw() {
  local path="$1" content
  [[ -f "${path}" ]] || return 1
  content="$(cat -- "${path}" 2>/dev/null)" || return 1
  [[ -n "${content}" ]] || return 1
  printf '%s' "${content}" | jq -e '.schemaVersion == 1' >/dev/null 2>&1 || return 1
  printf '%s' "${content}"
}

require_checkpoint_json() {
  # require_checkpoint_json <result-var> <path> -- nameref wrapper; die 11
  # (fail closed, never silently re-POST) on a missing/corrupt checkpoint.
  # Must NOT be invoked via $(...).
  local -n _result="$1"
  local _path="$2"
  local _content
  if ! _content="$(checkpoint_json_raw "${_path}")"; then
    die 11 "checkpoint missing or corrupt (bad JSON / wrong schemaVersion): ${_path}"
  fi
  _result="${_content}"
}

# --- hashing ---------------------------------------------------------------
sha256_file() {
  local file="$1" out
  out="$(openssl dgst -sha256 -r -- "${file}" 2>/dev/null)" || return 1
  printf '%s' "${out}" | awk '{print $1}'
}

# --- misc helpers ------------------------------------------------------
now_rfc3339() { date -u +%Y-%m-%dT%H:%M:%SZ; }

since_floor() {
  # since_floor <createdAt-RFC3339> -- createdAt minus a 5-minute
  # clock-skew margin; falls back to createdAt itself (still a bound, never
  # unbounded) if the arithmetic can't be done in this environment.
  local created_at="$1" epoch
  epoch="$(date -u -d "${created_at}" +%s 2>/dev/null)" || { printf '%s' "${created_at}"; return; }
  date -u -d "@$((epoch - 300))" +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || printf '%s' "${created_at}"
}

entry_index_for_slot() {
  local checkpoint_json="$1" slot="$2"
  jq -r --arg slot "${slot}" '[.entries[].slot] | index($slot) // "null"' <<<"${checkpoint_json}"
}

# =====================================================================
# init
# =====================================================================
cmd_init() {
  local repo="" parent="" checkpoint="" manifest="" parent_meta=""
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --repo) repo="${2:-}"; shift 2 || usage ;;
      --parent) parent="${2:-}"; shift 2 || usage ;;
      --checkpoint) checkpoint="${2:-}"; shift 2 || usage ;;
      --manifest) manifest="${2:-}"; shift 2 || usage ;;
      --parent-meta) parent_meta="${2:-}"; shift 2 || usage ;;
      *) die 2 "init: unknown argument: $1" ;;
    esac
  done
  [[ -n "${repo}" && "${repo}" =~ ^[A-Za-z0-9._-]+/[A-Za-z0-9._-]+$ ]] || die 2 "init: --repo must be <owner>/<repo>, got: ${repo}"
  [[ -n "${parent}" && "${parent}" =~ ^[0-9]+$ ]] || die 2 "init: --parent must be numeric, got: ${parent}"
  [[ -n "${checkpoint}" ]] || die 2 "init: --checkpoint is required"
  require_checkpoint_path "${checkpoint}"
  [[ -n "${manifest}" && -f "${manifest}" ]] || die 2 "init: --manifest is required and must be a readable file: ${manifest}"

  local manifest_raw
  manifest_raw="$(cat -- "${manifest}" 2>/dev/null)" || die 2 "init: failed to read manifest: ${manifest}"
  jq -e 'type == "array"' >/dev/null 2>&1 <<<"${manifest_raw}" \
    || die 2 "init: manifest is not valid JSON array: ${manifest}"

  local login
  login="$(gh api user --jq .login 2>/dev/null)" || die 2 "init: failed to resolve the authenticated login via gh api user --jq .login"
  [[ -n "${login}" ]] || die 2 "init: gh api user --jq .login returned an empty login"

  # --parent-meta (optional): consumed mechanically via --slurpfile, never
  # interpolated into a command line. Named -parent-meta.json to mirror
  # SKILL.md's own ${TMPDIR:-/tmp}/cenci/issue-<number>-<token>-parent-meta.json
  # fetch file -- this script now owns that file's lifecycle: once its
  # content has been consumed below, it removes the file itself rather than
  # leaving cleanup to the caller's step-13 rm -f list.
  local have_meta=0
  if [[ -n "${parent_meta}" && -f "${parent_meta}" ]] \
    && jq -e 'type == "object"' >/dev/null 2>&1 < "${parent_meta}"; then
    have_meta=1
  fi
  if [[ -n "${parent_meta}" && "${have_meta}" -eq 0 ]]; then
    log_err "init: parent-meta fetch was absent or invalid -- milestone/label inheritance was skipped for this init (graceful degrade, not a stop)"
  fi

  local entries_ndjson
  entries_ndjson="$(mktemp)" || die 2 "init: failed to create a scratch entries file"
  trap 'rm -f -- "${entries_ndjson}"' RETURN

  local manifest_lines
  manifest_lines="$(jq -c '.[]' <<<"${manifest_raw}" 2>/dev/null)" \
    || die 2 "init: failed to iterate manifest entries: ${manifest}"

  local entry slot title_file body_file seed_labels_json milestone_json
  local nonce title_hash body_hash final_labels_json final_milestone_json
  while IFS= read -r entry; do
    [[ -n "${entry}" ]] || continue
    slot="$(jq -r '.slot' <<<"${entry}")"
    title_file="$(jq -r '.titleFile' <<<"${entry}")"
    body_file="$(jq -r '.bodyFile' <<<"${entry}")"
    seed_labels_json="$(jq -c '.labels' <<<"${entry}")"
    milestone_json="$(jq -c '.milestone' <<<"${entry}")"
    [[ -n "${slot}" && "${slot}" != "null" ]] || die 2 "init: manifest entry missing slot"
    [[ -f "${title_file}" ]] || die 2 "init: manifest entry ${slot}: titleFile not readable: ${title_file}"
    [[ -f "${body_file}" ]] || die 2 "init: manifest entry ${slot}: bodyFile not readable: ${body_file}"

    nonce="$(openssl rand -hex 16)" || die 2 "init: openssl rand failed while minting a nonce for ${slot}"
    [[ "${nonce}" =~ ^[0-9a-f]{32}$ ]] || die 2 "init: minted nonce failed validation for ${slot} (got: ${nonce}) -- stopping, never a weaker fallback"

    title_hash="$(sha256_file "${title_file}")" || die 2 "init: failed to hash titleFile for ${slot}"
    body_hash="$(sha256_file "${body_file}")" || die 2 "init: failed to hash bodyFile for ${slot}"

    if [[ "${have_meta}" -eq 1 ]]; then
      final_labels_json="$(jq -c --argjson seed "${seed_labels_json}" --argjson excl "${LIFECYCLE_EXCLUSION_JSON}" \
        --slurpfile meta "${parent_meta}" \
        '$seed + [$meta[0].labels[]?.name | select(. as $n | ($excl | index($n)) | not) | select(. as $n | ($seed | index($n)) | not)]' \
        <<<'null')" || die 2 "init: failed to merge inherited labels for ${slot}"
      final_milestone_json="$(jq -c --argjson m "${milestone_json}" --slurpfile meta "${parent_meta}" \
        'if $m != null then $m elif ($meta[0].milestone.number // null) != null then $meta[0].milestone.number else null end' \
        <<<'null')" || die 2 "init: failed to resolve inherited milestone for ${slot}"
    else
      final_labels_json="${seed_labels_json}"
      final_milestone_json="${milestone_json}"
    fi

    jq -n \
      --arg slot "${slot}" --arg nonce "${nonce}" \
      --arg titleHash "${title_hash}" --arg bodyHash "${body_hash}" \
      --argjson labels "${final_labels_json}" --argjson milestone "${final_milestone_json}" \
      '{slot: $slot, nonce: $nonce, titleHash: $titleHash, bodyHash: $bodyHash,
        labels: ($labels | tojson), milestone: $milestone,
        issueNumber: null, verification: "pending", link: "unlinked"}' \
      >> "${entries_ndjson}" \
      || die 2 "init: failed to build checkpoint entry for ${slot}"
  done <<<"${manifest_lines}"

  [[ -s "${entries_ndjson}" ]] || die 2 "init: manifest produced zero entries: ${manifest}"

  local created_at checkpoint_content
  created_at="$(now_rfc3339)"
  checkpoint_content="$(jq -n \
    --arg repo "${repo}" --argjson parent "${parent}" \
    --arg creatorLogin "${login}" --arg createdAt "${created_at}" \
    --slurpfile entries "${entries_ndjson}" \
    '{schemaVersion: 1, repo: $repo, parent: $parent, creatorLogin: $creatorLogin,
      createdAt: $createdAt, entries: $entries}')" \
    || die 2 "init: failed to assemble the checkpoint document"

  write_atomically "${checkpoint}" "${checkpoint_content}" || die 2 "init: failed to write checkpoint: ${checkpoint}"

  # Only delete a --parent-meta path this script itself is scoped to write
  # under (${TMPDIR:-/tmp}/cenci/, matching the header's documented
  # location for this fetch file) -- an unvalidated path here would let a
  # single init call delete an arbitrary JSON-object file on disk (#876
  # review finding 2). Skip the deletion (never fail closed) on an
  # out-of-scope path: the checkpoint write above already succeeded, so
  # this is best-effort cleanup, not correctness-critical, and the caller's
  # own step-13 cleanup still covers a file this script declines to touch.
  if [[ "${have_meta}" -eq 1 ]]; then
    if validate_tmp_root_path "${parent_meta}"; then
      rm -f -- "${parent_meta}"
    else
      log_err "init: --parent-meta path is outside \${TMPDIR:-/tmp}/cenci/ -- leaving it for the caller's own cleanup: ${parent_meta}"
    fi
  fi

  printf 'checkpoint=%s\n' "${checkpoint}"
  printf 'entries=%s\n' "$(jq '.entries | length' <<<"${checkpoint_content}")"
}

# =====================================================================
# ensure
# =====================================================================
cmd_ensure() {
  local checkpoint="" repo="" slot="" title_file="" body_file=""
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --checkpoint) checkpoint="${2:-}"; shift 2 || usage ;;
      --repo) repo="${2:-}"; shift 2 || usage ;;
      --slot) slot="${2:-}"; shift 2 || usage ;;
      --title) title_file="${2:-}"; shift 2 || usage ;;
      --body) body_file="${2:-}"; shift 2 || usage ;;
      *) die 2 "ensure: unknown argument: $1" ;;
    esac
  done
  [[ -n "${checkpoint}" ]] || die 2 "ensure: --checkpoint is required"
  require_checkpoint_path "${checkpoint}"
  [[ -n "${repo}" ]] || die 2 "ensure: --repo is required"
  [[ -n "${slot}" ]] || die 2 "ensure: --slot is required"
  [[ -n "${title_file}" ]] || die 2 "ensure: --title is required"
  [[ -n "${body_file}" ]] || die 2 "ensure: --body is required"

  # Checkpoint validity (exit 11, fail closed) is checked before anything
  # else this subcommand does -- a missing/corrupt checkpoint must never
  # surface as a generic usage error, and no candidate list or POST may run.
  local checkpoint_json
  require_checkpoint_json checkpoint_json "${checkpoint}"

  [[ -f "${title_file}" ]] || die 2 "ensure: --title must be a readable file: ${title_file}"
  [[ -f "${body_file}" ]] || die 2 "ensure: --body must be a readable file: ${body_file}"

  local idx
  idx="$(entry_index_for_slot "${checkpoint_json}" "${slot}")"
  [[ "${idx}" != "null" ]] || die 2 "ensure: no checkpoint entry for slot: ${slot}"

  local nonce labels_json milestone_json login created_at
  nonce="$(jq -r ".entries[${idx}].nonce" <<<"${checkpoint_json}")"
  labels_json="$(jq -r ".entries[${idx}].labels" <<<"${checkpoint_json}")"
  milestone_json="$(jq -c ".entries[${idx}].milestone" <<<"${checkpoint_json}")"
  login="$(jq -r '.creatorLogin' <<<"${checkpoint_json}")"
  created_at="$(jq -r '.createdAt' <<<"${checkpoint_json}")"

  local marker="${MARKER_PREFIX}${nonce}${MARKER_SUFFIX}"
  local since
  since="$(since_floor "${created_at}")"

  local query="?state=all&creator=${login}&since=${since}"
  local candidates_raw
  if ! candidates_raw="$(gh api repos/"${repo}"/issues"${query}" --paginate 2>&1)"; then
    die 12 "ensure: candidate-list call failed for slot ${slot}: ${candidates_raw}"
  fi
  jq -e 'type == "array"' >/dev/null 2>&1 <<<"${candidates_raw}" \
    || die 12 "ensure: candidate-list response was not a JSON array for slot ${slot}"

  # Both jq calls below, and the resulting match_count shape, are checked
  # explicitly -- unlike every other jq/`gh` call in this file, an
  # unchecked failure here would silently evaluate both "-ge 2" and
  # "-eq 1" as false and fall through to ensure_create, i.e. the exact
  # silent-duplicate-creation bug this ticket exists to close (#876 review
  # finding 3).
  local matches_json
  matches_json="$(jq -c --arg login "${login}" --arg marker "${marker}" \
    '[.[] | select(.user.login == $login) | select(.body // "" | contains($marker))]' \
    <<<"${candidates_raw}")" || die 12 "ensure: failed to filter candidate matches for slot ${slot}"
  local match_count
  match_count="$(jq 'length' <<<"${matches_json}")" || die 12 "ensure: failed to count candidate matches for slot ${slot}"
  [[ "${match_count}" =~ ^[0-9]+$ ]] \
    || die 12 "ensure: candidate match count was not a single integer for slot ${slot} (got: ${match_count})"

  if [[ "${match_count}" -ge 2 ]]; then
    local numbers
    numbers="$(jq -r '[.[].number] | join(", ")' <<<"${matches_json}")"
    die 10 "ensure: partial-state report -- ${match_count} marker matches for slot ${slot} (nonce ${nonce}): candidate issue numbers [${numbers}] -- fail closed, no write, resolve the ambiguity manually before retrying"
  fi

  if [[ "${match_count}" -eq 1 ]]; then
    ensure_reconcile "${checkpoint}" "${checkpoint_json}" "${idx}" "${slot}" "${repo}" \
      "$(jq -c '.[0]' <<<"${matches_json}")" "${title_file}" "${body_file}" "${labels_json}" "${milestone_json}" "${marker}"
    return $?
  fi

  ensure_create "${checkpoint}" "${checkpoint_json}" "${idx}" "${slot}" "${repo}" \
    "${title_file}" "${body_file}" "${labels_json}" "${milestone_json}" "${marker}"
}

# ensure_create <checkpoint> <checkpoint_json> <idx> <slot> <repo> <title_file>
#   <body_file> <labels_json> <milestone_json> <marker>
ensure_create() {
  local checkpoint="$1" checkpoint_json="$2" idx="$3" slot="$4" repo="$5"
  local title_file="$6" body_file="$7" labels_json="$8" milestone_json="$9" marker="${10}"

  local payload
  payload="$(mktemp)" || die 2 "ensure: failed to create a scratch payload file for slot ${slot}"

  jq -n --rawfile title "${title_file}" --rawfile body "${body_file}" \
    --arg marker "${marker}" --argjson labels "${labels_json}" --argjson milestone "${milestone_json}" \
    '{title: ($title | rtrimstr("\n")), body: (($body | rtrimstr("\n")) + "\n\n" + $marker), labels: $labels}
     + (if $milestone != null then {milestone: $milestone} else {} end)' \
    > "${payload}" || { rm -f -- "${payload}"; die 2 "ensure: failed to compose the create payload for slot ${slot}"; }

  local post_out post_rc number
  post_out="$(gh api repos/"${repo}"/issues -X POST --input "${payload}" --jq .number 2>&1)"
  post_rc=$?
  rm -f -- "${payload}"

  if [[ "${post_rc}" -ne 0 ]]; then
    log_err "ensure: create attempt for slot ${slot} did not complete (the create request exited ${post_rc}); the server may still have accepted it -- re-run ensure to recover via marker scan, never re-run with a new nonce"
    return 1
  fi

  number="$(printf '%s' "${post_out}" | tr -d '[:space:]')"
  if [[ ! "${number}" =~ ^[0-9]+$ ]]; then
    log_err "ensure: create attempt for slot ${slot} returned a non-numeric response (${post_out}) -- not trusted as proof of creation; re-run ensure to recover via marker scan"
    return 1
  fi

  # Read-back verification: a numeric ID alone is never proof the body
  # landed -- re-fetch and confirm the marker is actually present.
  local fetched_body
  if ! fetched_body="$(gh api repos/"${repo}"/issues/"${number}" --jq .body 2>&1)"; then
    log_err "ensure: create attempt for slot ${slot} (issue #${number}) could not be read back for verification -- re-run ensure to recover via marker scan"
    return 1
  fi
  if [[ "${fetched_body}" != *"${marker}"* ]]; then
    log_err "ensure: create attempt for slot ${slot} (issue #${number}) did not carry the expected marker on read-back -- re-run ensure to recover via marker scan"
    return 1
  fi

  persist_entry_result "${checkpoint}" "${checkpoint_json}" "${idx}" "${number}" "verified" \
    || die 2 "ensure: failed to persist checkpoint after creating slot ${slot} (issue #${number})"

  printf 'slot=%s\n' "${slot}"
  printf 'issueNumber=%s\n' "${number}"
  printf 'verification=verified\n'
}

# ensure_reconcile <checkpoint> <checkpoint_json> <idx> <slot> <repo> <candidate_json>
#   <title_file> <body_file> <labels_json> <milestone_json> <marker>
ensure_reconcile() {
  local checkpoint="$1" checkpoint_json="$2" idx="$3" slot="$4" repo="$5" candidate="$6"
  local title_file="$7" body_file="$8" labels_json="$9" milestone_json="${10}" marker="${11}"

  local number
  number="$(jq -r '.number' <<<"${candidate}")"

  # Computed via jq --rawfile + rtrimstr("\n") -- the exact same
  # transformation ensure_create's payload applies -- rather than
  # `$(cat -- "${title_file}")`, whose surrounding `$(...)` strips ALL
  # trailing newlines (bash command-substitution behavior), not just the
  # one rtrimstr strips. A title/body file with more than one trailing
  # newline previously made this differ from what was actually stored,
  # spuriously flagging a correctly-created issue as diverged (#876 review
  # finding 5).
  local expected_title expected_body_core expected_body
  expected_title="$(jq -rn --rawfile title "${title_file}" '$title | rtrimstr("\n")')" \
    || die 2 "ensure: failed to compute expected title for slot ${slot}"
  expected_body_core="$(jq -rn --rawfile body "${body_file}" '$body | rtrimstr("\n")')" \
    || die 2 "ensure: failed to compute expected body for slot ${slot}"
  expected_body="${expected_body_core}"$'\n\n'"${marker}"

  local expected_labels_sorted actual_labels_sorted
  expected_labels_sorted="$(jq -c 'sort' <<<"${labels_json}")"
  actual_labels_sorted="$(jq -c '[.labels[].name] | sort' <<<"${candidate}")"

  local expected_milestone actual_milestone
  expected_milestone="$(jq -c '.' <<<"${milestone_json}")"
  actual_milestone="$(jq -c '.milestone.number // null' <<<"${candidate}")"

  local actual_title actual_body
  actual_title="$(jq -r '.title' <<<"${candidate}")"
  actual_body="$(jq -r '.body' <<<"${candidate}")"

  if [[ "${actual_title}" == "${expected_title}" \
        && "${actual_body}" == "${expected_body}" \
        && "${actual_labels_sorted}" == "${expected_labels_sorted}" \
        && "${actual_milestone}" == "${expected_milestone}" ]]; then
    persist_entry_result "${checkpoint}" "${checkpoint_json}" "${idx}" "${number}" "verified" \
      || die 2 "ensure: failed to persist checkpoint after verifying slot ${slot} (issue #${number})"
    printf 'slot=%s\n' "${slot}"
    printf 'issueNumber=%s\n' "${number}"
    printf 'verification=verified\n'
    return 0
  fi

  # Diverged from the manifest -- exactly repair (PATCH), then reverify.
  # Composed the same way ensure_create's payload is (jq -n --rawfile,
  # reading directly from the title/body file paths, never a
  # pre-materialized shell variable) -- the only payload-composition site
  # that previously didn't was this one (#876 review finding 5).
  local patch
  patch="$(mktemp)" || die 2 "ensure: failed to create a scratch patch file for slot ${slot}"
  jq -n --rawfile title "${title_file}" --rawfile body "${body_file}" \
    --arg marker "${marker}" --argjson labels "${labels_json}" --argjson milestone "${expected_milestone}" \
    '{title: ($title | rtrimstr("\n")), body: (($body | rtrimstr("\n")) + "\n\n" + $marker), labels: $labels, milestone: $milestone}' \
    > "${patch}" || { rm -f -- "${patch}"; die 2 "ensure: failed to compose the repair payload for slot ${slot}"; }

  if ! gh api repos/"${repo}"/issues/"${number}" -X PATCH --input "${patch}" >/dev/null 2>&1; then
    rm -f -- "${patch}"
    persist_entry_result "${checkpoint}" "${checkpoint_json}" "${idx}" "${number}" "mismatch" \
      || log_err "ensure: also failed to persist the mismatch state for slot ${slot} (issue #${number})"
    die 13 "ensure: existing issue #${number} for slot ${slot} diverged from the manifest and the repair PATCH itself failed -- unrepairable this run, reconcile #${number} manually"
  fi
  rm -f -- "${patch}"

  local reverify
  if ! reverify="$(gh api repos/"${repo}"/issues/"${number}" 2>&1)"; then
    persist_entry_result "${checkpoint}" "${checkpoint_json}" "${idx}" "${number}" "mismatch" \
      || log_err "ensure: also failed to persist the mismatch state for slot ${slot} (issue #${number})"
    die 13 "ensure: repair PATCH succeeded but re-fetch for slot ${slot} (issue #${number}) failed -- treating as unrepairable this run"
  fi
  # Re-checks title, body, labels, AND milestone -- matching the pre-repair
  # divergence check above exactly. The body was previously omitted here,
  # so a PATCH that silently failed to apply the body correctly (while
  # succeeding on the other fields) was recorded as "verified" with no
  # detection (#876 review finding 6).
  local reverify_title reverify_body reverify_labels reverify_milestone
  reverify_title="$(jq -r '.title' <<<"${reverify}")"
  reverify_body="$(jq -r '.body' <<<"${reverify}")"
  reverify_labels="$(jq -c '[.labels[].name] | sort' <<<"${reverify}")"
  reverify_milestone="$(jq -c '.milestone.number // null' <<<"${reverify}")"
  if [[ "${reverify_title}" != "${expected_title}" \
        || "${reverify_body}" != "${expected_body}" \
        || "${reverify_labels}" != "${expected_labels_sorted}" \
        || "${reverify_milestone}" != "${expected_milestone}" ]]; then
    persist_entry_result "${checkpoint}" "${checkpoint_json}" "${idx}" "${number}" "mismatch" \
      || log_err "ensure: also failed to persist the mismatch state for slot ${slot} (issue #${number})"
    die 13 "ensure: repair PATCH did not reconcile slot ${slot} (issue #${number}) -- unrepairable this run"
  fi

  persist_entry_result "${checkpoint}" "${checkpoint_json}" "${idx}" "${number}" "verified" \
    || die 2 "ensure: failed to persist checkpoint after repairing slot ${slot} (issue #${number})"
  printf 'slot=%s\n' "${slot}"
  printf 'issueNumber=%s\n' "${number}"
  printf 'verification=verified\n'
}

# persist_entry_result <checkpoint> <checkpoint_json> <idx> <number> <verification>
persist_entry_result() {
  local checkpoint="$1" checkpoint_json="$2" idx="$3" number="$4" verification="$5"
  local updated
  updated="$(jq --argjson idx "${idx}" --argjson number "${number}" --arg verification "${verification}" \
    '.entries[$idx].issueNumber = $number | .entries[$idx].verification = $verification' \
    <<<"${checkpoint_json}")" || return 1
  write_atomically "${checkpoint}" "${updated}"
}

# subissue_numbers_contain <raw-parent-json> <number> -- true only when
# <raw-parent-json> is a non-empty JSON object whose .subIssues.nodes[]
# carries .number == <number>. Shared by cmd_link's pre-link "already
# linked" check and its post-link "verified" check below -- both are the
# same membership test against a `gh issue view --json subIssues` response.
subissue_numbers_contain() {
  local raw="$1" number="$2"
  [[ -n "${raw}" ]] || return 1
  jq -e --argjson n "${number}" \
    'type == "object" and ((.subIssues.nodes // []) | map(.number) | index($n) != null)' \
    >/dev/null 2>&1 <<<"${raw}"
}

# =====================================================================
# link -- idempotent native --parent sub-issue linking. Link states:
# unlinked -> linked (edit call succeeded) -> verified (parent-side
# confirmed); a failed edit leaves the entry unlinked.
# =====================================================================
cmd_link() {
  local checkpoint="" repo="" slot="" parent=""
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --checkpoint) checkpoint="${2:-}"; shift 2 || usage ;;
      --repo) repo="${2:-}"; shift 2 || usage ;;
      --slot) slot="${2:-}"; shift 2 || usage ;;
      --parent) parent="${2:-}"; shift 2 || usage ;;
      *) die 2 "link: unknown argument: $1" ;;
    esac
  done
  [[ -n "${checkpoint}" ]] || die 2 "link: --checkpoint is required"
  require_checkpoint_path "${checkpoint}"
  [[ -n "${repo}" ]] || die 2 "link: --repo is required"
  [[ -n "${slot}" ]] || die 2 "link: --slot is required"
  [[ -n "${parent}" && "${parent}" =~ ^[0-9]+$ ]] || die 2 "link: --parent must be numeric, got: ${parent}"

  local checkpoint_json
  require_checkpoint_json checkpoint_json "${checkpoint}"

  local idx
  idx="$(entry_index_for_slot "${checkpoint_json}" "${slot}")"
  [[ "${idx}" != "null" ]] || die 2 "link: no checkpoint entry for slot: ${slot}"

  local number
  number="$(jq -r ".entries[${idx}].issueNumber" <<<"${checkpoint_json}")"
  [[ "${number}" != "null" ]] || die 2 "link: slot ${slot} has no issueNumber yet -- run ensure first"

  # parent_subissues_raw <parent> <repo> -- the raw (unfiltered) parent
  # object on stdout; returns 1 only when the `gh` call itself fails
  # (network/auth/genuine 404), never merely for an object the host
  # returned as empty.
  local existing_raw
  if ! existing_raw="$(gh issue view "${parent}" --repo "${repo}" --json subIssues 2>&1)"; then
    die 14 "link: failed to read parent #${parent}'s existing sub-issue list: ${existing_raw}"
  fi

  local already_linked=0
  subissue_numbers_contain "${existing_raw}" "${number}" && already_linked=1

  if [[ "${already_linked}" -eq 0 ]]; then
    if ! gh issue edit "${number}" --repo "${repo}" --parent "${parent}" >/dev/null 2>&1; then
      die 14 "link: failed to link issue #${number} (slot ${slot}) as a sub-issue of parent #${parent}"
    fi
  fi

  local reverify_raw
  if ! reverify_raw="$(gh issue view "${parent}" --repo "${repo}" --json subIssues 2>&1)"; then
    die 14 "link: linked issue #${number} but could not re-verify parent #${parent}'s sub-issue list: ${reverify_raw}"
  fi
  # Fail closed on an empty re-fetch, matching this script's fail-closed
  # posture everywhere else (#876 review finding 4) -- an empty result here
  # is ambiguous (cannot tell "zero sub-issues" from "object unavailable"),
  # and ambiguity must never be read as success just because the earlier
  # edit call reported success.
  [[ -n "${reverify_raw}" ]] \
    || die 14 "link: linked issue #${number} but parent #${parent}'s sub-issue re-fetch returned no data -- fail closed, cannot confirm the link took effect"
  local verified=0
  if subissue_numbers_contain "${reverify_raw}" "${number}"; then
    verified=1
  fi
  [[ "${verified}" -eq 1 ]] || die 14 "link: issue #${number} (slot ${slot}) is not present in parent #${parent}'s sub-issue list after linking"

  local updated
  updated="$(jq --argjson idx "${idx}" '.entries[$idx].link = "verified"' <<<"${checkpoint_json}")" \
    || die 14 "link: failed to compute the updated checkpoint for slot ${slot}"
  write_atomically "${checkpoint}" "${updated}" || die 14 "link: failed to persist checkpoint after linking slot ${slot}"

  printf 'slot=%s\n' "${slot}"
  printf 'parent=%s\n' "${parent}"
  printf 'child=%s\n' "${number}"
  printf 'link=verified\n'
}

# =====================================================================
# clear -- idempotent checkpoint removal.
# =====================================================================
cmd_clear() {
  local checkpoint=""
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --checkpoint) checkpoint="${2:-}"; shift 2 || usage ;;
      *) die 2 "clear: unknown argument: $1" ;;
    esac
  done
  [[ -n "${checkpoint}" ]] || die 2 "clear: --checkpoint is required"
  require_checkpoint_path "${checkpoint}"

  rm -f -- "${checkpoint}"
  printf 'checkpoint=%s\n' "${checkpoint}"
  printf 'cleared=true\n'
}

# =====================================================================
# dispatch
# =====================================================================
[[ $# -ge 1 ]] || usage
SUBCOMMAND="$1"
shift
case "${SUBCOMMAND}" in
  init) cmd_init "$@" ;;
  ensure) cmd_ensure "$@" ;;
  link) cmd_link "$@" ;;
  clear) cmd_clear "$@" ;;
  *) die 2 "unknown subcommand: ${SUBCOMMAND}" ;;
esac
