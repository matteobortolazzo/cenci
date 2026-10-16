package sandbox

// parentEnvironScript recovers ownership for a non-dumpable child (notably
// native Codex) through its still-live, same-user launcher. It is only called
// after an actual environ read error. A readable non-match never falls back.
// Stop at the first readable ancestor, require launch ownership metadata, and
// never cross PID 1 or a user boundary. Before returning, recheck every link's
// parent PID and start time: an exited/reparented/reused process is not evidence
// of ownership. Unresolved processes retain the existing unreadable diagnostic.
// Both cleanup scans use this helper; no extra container capability is needed.
const parentEnvironScript = `
cenci_proc_identity() {
  cpi_stat=$(cat "/proc/$1/stat" 2>/dev/null) || return 1
  cpi_stat=${cpi_stat##*) }
  set -f
  set -- $cpi_stat
  [ "$#" -ge 20 ] || return 1
  cpi_parent=$2
  shift 19
  case "$cpi_parent:$1" in *[!0123456789:]*) return 1;; esac
  [ -n "$cpi_parent" ] && [ -n "$1" ] || return 1
  printf '%s:%s' "$cpi_parent" "$1"
}
cenci_parent_environ() {
  cpe_pid=$1
  cpe_kind=${2:-orphan}
  cpe_uid=$(id -u 2>/dev/null) || return 1
  cpe_links=""
  cpe_depth=0
  while [ "$cpe_depth" -lt 32 ]; do
    [ "$cpe_pid" -gt 1 ] || return 1
    cpe_owner=$(sed -n 's/^Uid:[[:space:]]*\([0-9][0-9]*\).*/\1/p' "/proc/$cpe_pid/status" 2>/dev/null) || return 1
    [ "$cpe_owner" = "$cpe_uid" ] || return 1
    cpe_identity=$(cenci_proc_identity "$cpe_pid") || return 1
    cpe_links="$cpe_links $cpe_pid:$cpe_identity"
    if [ "$cpe_depth" -gt 0 ]; then
      grep --null-data --text --quiet --line-regexp '^CENCI_LAUNCH_ID=[0123456789abcdef]\{32\}' "/proc/$cpe_pid/environ" 2>/dev/null
      cpe_rc=$?
      if [ "$cpe_rc" -eq 1 ] && [ "$cpe_kind" = orphan ]; then
        # Legacy launchers have only socket/pane ownership. Both must be
        # genuine entries; the orphan scanner still validates their values.
        if grep --null-data --text --quiet --line-regexp '^TMUX_PANE=%[0123456789][0123456789]*' "/proc/$cpe_pid/environ" 2>/dev/null &&
           grep --null-data --text --quiet --line-regexp '^CENCI_TMUX_SOCKET=/.*' "/proc/$cpe_pid/environ" 2>/dev/null; then
          cpe_rc=0
        fi
      fi
      if [ "$cpe_rc" -eq 0 ]; then
        for cpe_link in $cpe_links; do
          cpe_check_pid=${cpe_link%%:*}
          cpe_check=$(cenci_proc_identity "$cpe_check_pid") || return 1
          [ "$cpe_check" = "${cpe_link#*:}" ] || return 1
        done
        printf '/proc/%s/environ' "$cpe_pid"
        return 0
      fi
      [ "$cpe_rc" -eq 2 ] || return 1
    fi
    cpe_pid=${cpe_identity%%:*}
    cpe_depth=$((cpe_depth + 1))
  done
  return 1
}
`
