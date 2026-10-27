#!/bin/sh
# Prerequisite gate for native Codex normal-mode workflows (#1233): the first step
# of every normal-mode run, before any checkpoint, workflow, or GitHub mutation.
# Silent and exit 0 when `node --version` reports a major version of MIN_NODE_MAJOR
# or higher; otherwise one actionable line on stderr and exit 2. POSIX sh, shell
# builtins only (no external tools), and it never creates or changes any state.
#
# install.sh `doctor_codex_support` repeats this threshold inline as node_min_major;
# flow/codex/require-runtime.test.sh fails when the two differ.
MIN_NODE_MAJOR=18

found=none
if command -v node >/dev/null 2>&1; then
  found=unparseable
  raw=$(node --version 2>/dev/null) || raw=""
  # Accept only v<major>.<minor>.<patch> with an optional -pre-release or +build tail,
  # and rebuild the reported version from the validated digits, so version text from a
  # broken `node` is never echoed or evaluated.
  v=${raw#v}
  if [ "$v" != "$raw" ] && [ "${v#*.}" != "$v" ]; then
    major=${v%%.*}
    rest=${v#*.}
    if [ "${rest#*.}" != "$rest" ]; then
      minor=${rest%%.*}
      rest=${rest#*.}
      patch=${rest%%[!0-9]*}
      suffix=${rest#"$patch"}
      case $major in '' | *[!0-9]*) major="" ;; esac
      case $minor in '' | *[!0-9]*) minor="" ;; esac
      case $patch in '' | *[!0-9]*) patch="" ;; esac
      case $suffix in '' | -* | +*) ;; *) patch="" ;; esac
      if [ -n "$major" ] && [ -n "$minor" ] && [ -n "$patch" ] \
        && [ "${#major}" -le 6 ] && [ "${#minor}" -le 6 ] && [ "${#patch}" -le 6 ]; then
        found="$major.$minor.$patch"
        if [ "$major" -ge "$MIN_NODE_MAJOR" ] 2>/dev/null; then
          exit 0
        fi
      fi
    fi
  fi
fi

printf 'cenci: Codex workflow runtime unavailable — Node.js %s+ not found on PATH (found: %s). Install Node.js %s or newer, then rerun; no checkpoint, workflow, or GitHub state was changed.\n' \
  "$MIN_NODE_MAJOR" "$found" "$MIN_NODE_MAJOR" >&2
exit 2
