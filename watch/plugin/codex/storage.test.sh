# shellcheck shell=bash
# Codex-specific behavior cases, run by test.sh through the shared fixture.

client_extra_cases() {
  local shell mode bindir data legacy cache baseline archive_dir tarball digest calls output attempts os arch
  for shell in bash dash; do
    command -v "$shell" >/dev/null 2>&1 || continue
    for mode in native legacy cache default; do
      hh_setup "$shell" "storage $mode"
      data="" legacy="" cache="$HH_HOME/cache"
      case "$mode" in
        native) data="$HH_ROOT-data"; legacy="$HH_ROOT-other-data"; bindir="$data/$VERSION/bin" ;;
        legacy) legacy="$HH_ROOT-legacy-data"; bindir="$legacy/$VERSION/bin" ;;
        cache) bindir="$cache/cenci/codex/$VERSION/bin" ;;
        default) cache=""; bindir="$HH_HOME/.cache/cenci/codex/$VERSION/bin" ;;
      esac
      baseline="$HH_TMPD/plugin-before"
      cp -R "$HH_ROOT" "$baseline"
      archive_dir="$HH_TMPD/release"
      mkdir -p "$archive_dir"
      calls="$HH_TMPD/calls"
      output="$HH_TMPD/payload"
      attempts="$HH_TMPD/downloads"
      cat >"$archive_dir/cenci" <<'STUB'
#!/bin/sh
[ "$1" = --version ] && exit 0
printf '%s\n' "$0" "$@" >"$STORAGE_CALLS"
cat >"$STORAGE_PAYLOAD"
STUB
      chmod +x "$archive_dir/cenci"
      case "$(uname -s)" in Darwin) os=darwin ;; *) os=linux ;; esac
      case "$(uname -m)" in arm64 | aarch64) arch=arm64 ;; *) arch=amd64 ;; esac
      tarball="cenci_${VERSION}_${os}_${arch}.tar.gz"
      tar -czf "$archive_dir/$tarball" -C "$archive_dir" cenci
      if command -v sha256sum >/dev/null 2>&1; then
        digest="$(sha256sum "$archive_dir/$tarball")"
      else
        digest="$(shasum -a 256 "$archive_dir/$tarball")"
      fi
      printf '%s  %s\n' "${digest%% *}" "$tarball" >"$archive_dir/checksums.txt"
      cat >"$HH_STUBS/curl" <<'STUB'
#!/bin/sh
printf '%s\n' "$2" >>"$STORAGE_DOWNLOADS"
cp "$STORAGE_RELEASE/${2##*/}" "$4"
STUB
      chmod +x "$HH_STUBS/curl"

      hh_env PLUGIN_DATA="$data" CLAUDE_PLUGIN_DATA="$legacy" XDG_CACHE_HOME="$cache" \
        STORAGE_RELEASE="$archive_dir" STORAGE_DOWNLOADS="$attempts" CENCI_SANDBOX=1 \
        "$HH_ROOT/codex/bootstrap.sh"
      hh_assert "[$shell][$mode] verified download installs outside the plugin tree" \
        test -x "$bindir/cenci" -a -f "$bindir/.cenci-version"
      hh_assert "[$shell][$mode] the installed binary is the verified archive contents" \
        cmp -s "$bindir/cenci" "$archive_dir/cenci"
      hh_assert "[$shell][$mode] bootstrap preserves every plugin file and directory" \
        diff -r "$baseline" "$HH_ROOT"
      hh_assert "[$shell][$mode] the command-line link targets the external binary" \
        test "$(readlink "$HH_HOME/.local/bin/cenci")" = "$bindir/cenci"

      hh_env PLUGIN_DATA="$data" CLAUDE_PLUGIN_DATA="$legacy" XDG_CACHE_HOME="$cache" \
        STORAGE_CALLS="$calls" STORAGE_PAYLOAD="$output" \
        "$HH_ROOT/codex/notify.sh" <"$HH_PAYLOAD"
      hh_assert "[$shell][$mode] notify executes the external binary with the Codex arguments" \
        test "$(cat "$calls" 2>/dev/null)" = "$(printf '%s\nnotify\n-agent\ncodex' "$bindir/cenci")"
      hh_assert "[$shell][$mode] notify preserves the incoming hook payload" cmp -s "$HH_PAYLOAD" "$output"

      hh_env PLUGIN_DATA="$data" CLAUDE_PLUGIN_DATA="$legacy" XDG_CACHE_HOME="$cache" \
        STORAGE_RELEASE="$archive_dir" STORAGE_DOWNLOADS="$attempts" CENCI_SANDBOX=1 \
        "$HH_ROOT/codex/bootstrap.sh"
      hh_assert "[$shell][$mode] a repeated startup reuses the matching release" \
        test "$(wc -l <"$attempts" | tr -d ' ')" -eq 2
      hh_assert "[$shell][$mode] repeated startup and notify keep the plugin tree unchanged" \
        diff -r "$baseline" "$HH_ROOT"
    done

    hh_setup "$shell" fallback-immutable
    baseline="$HH_TMPD/plugin-before"
    cp -R "$HH_ROOT" "$baseline"
    printf '#!/bin/sh\nexit 0\n' >"$HH_STUBS/fallback"
    chmod +x "$HH_STUBS/fallback"
    hh_env CENCI_BIN="$HH_STUBS/fallback" CENCI_SANDBOX=1 "$HH_ROOT/codex/bootstrap.sh"
    hh_assert "[$shell] failed download adopts a working fallback into external storage" \
      cmp -s "$HH_STUBS/fallback" "$HH_BIN_DIR/cenci"
    hh_assert "[$shell] fallback adoption also leaves the trusted tree unchanged" \
      diff -r "$baseline" "$HH_ROOT"

    # An old install remains usable before the new location is provisioned;
    # reading it must not rewrite its binary or version marker.
    hh_setup "$shell" legacy-binary
    mkdir -p "$HH_ROOT/bin"
    # shellcheck disable=SC2016 # expanded only when the generated fixture runs
    printf '#!/bin/sh\n[ "$1" = --version ] && exit 0\nprintf legacy >"$STORAGE_CALLS"\n' >"$HH_ROOT/bin/cenci"
    chmod +x "$HH_ROOT/bin/cenci"
    baseline="$HH_TMPD/plugin-before"
    cp -R "$HH_ROOT" "$baseline"
    hh_env STORAGE_CALLS="$HH_TMPD/calls" "$HH_ROOT/codex/notify.sh" <"$HH_PAYLOAD"
    hh_assert "[$shell] notify can read an existing plugin-local binary during migration" \
      test "$(cat "$HH_TMPD/calls" 2>/dev/null)" = legacy
    hh_assert "[$shell] migration never rewrites the old plugin-local binary" diff -r "$baseline" "$HH_ROOT"
    hh_env CENCI_SANDBOX=1 "$HH_ROOT/codex/bootstrap.sh"
    hh_assert "[$shell] offline migration copies the existing working binary into external storage" \
      cmp -s "$HH_ROOT/bin/cenci" "$HH_BIN_DIR/cenci"
    hh_assert "[$shell] successful offline migration records no bootstrap failure" \
      test ! -e "$HH_STORE/bootstrap-failure"
    hh_assert "[$shell] offline migration leaves the old binary and plugin tree unchanged" \
      diff -r "$baseline" "$HH_ROOT"

    # Different installed versions must not overwrite one another in the
    # persistent data directory.
    hh_setup "$shell" version-isolation
    mkdir -p "$HH_BIN_DIR"
    printf '#!/bin/sh\nexit 0\n' >"$HH_BIN_DIR/cenci"
    chmod +x "$HH_BIN_DIR/cenci"
    printf '%s\n' "$VERSION" >"$HH_BIN_DIR/.cenci-version"
    printf '{"version":"9.99.99"}\n' >"$HH_ROOT/.codex-plugin/plugin.json"
    printf '#!/bin/sh\nexit 0\n' >"$HH_STUBS/fallback"
    chmod +x "$HH_STUBS/fallback"
    baseline="$HH_TMPD/plugin-before"
    cp -R "$HH_ROOT" "$baseline"
    hh_env CENCI_BIN="$HH_STUBS/fallback" CENCI_SANDBOX=1 "$HH_ROOT/codex/bootstrap.sh"
    hh_assert "[$shell] a second version uses its own runtime directory" \
      test -x "$HH_ROOT-data/9.99.99/bin/cenci" -a -x "$HH_BIN_DIR/cenci"
    hh_assert "[$shell] the previous version marker remains intact" \
      test "$(cat "$HH_BIN_DIR/.cenci-version")" = "$VERSION"
    hh_assert "[$shell] a version upgrade leaves the new plugin tree unchanged" diff -r "$baseline" "$HH_ROOT"

    # GUI sessions may omit ~/.local/bin from PATH. The maintained launcher
    # must still make the previous external binary available offline.
    hh_setup "$shell" previous-external
    mkdir -p "$HH_ROOT-data/0.0.1/bin" "$HH_HOME/.local/bin"
    printf '#!/bin/sh\nexit 0\n' >"$HH_ROOT-data/0.0.1/bin/cenci"
    chmod +x "$HH_ROOT-data/0.0.1/bin/cenci"
    ln -s "$HH_ROOT-data/0.0.1/bin/cenci" "$HH_HOME/.local/bin/cenci"
    baseline="$HH_TMPD/plugin-before"
    cp -R "$HH_ROOT" "$baseline"
    hh_env CENCI_SANDBOX=1 "$HH_ROOT/codex/bootstrap.sh"
    hh_assert "[$shell] offline upgrade finds the previous external binary without ~/.local/bin on PATH" \
      cmp -s "$HH_ROOT-data/0.0.1/bin/cenci" "$HH_BIN_DIR/cenci"
    hh_assert "[$shell] previous external binary adoption records no bootstrap failure" \
      test ! -e "$HH_STORE/bootstrap-failure"
    hh_assert "[$shell] previous external binary adoption leaves the plugin tree unchanged" \
      diff -r "$baseline" "$HH_ROOT"

    for mode in relative inside symlink malformed missing-version; do
      hh_setup "$shell" "invalid-$mode"
      data="$HH_ROOT-data"
      case "$mode" in
        relative) data=relative-data ;;
        inside) data="$HH_ROOT/data" ;;
        symlink) ln -s "$HH_ROOT" "$data" ;;
        malformed) printf '{"version":"../../escape"}\n' >"$HH_ROOT/.codex-plugin/plugin.json" ;;
        missing-version) printf '{}\n' >"$HH_ROOT/.codex-plugin/plugin.json" ;;
      esac
      baseline="$HH_TMPD/plugin-before"
      cp -R "$HH_ROOT" "$baseline"
      hh_env PLUGIN_DATA="$data" CENCI_SANDBOX=1 "$HH_ROOT/codex/bootstrap.sh"
      hh_assert "[$shell][$mode] invalid storage input records a nonfatal bootstrap failure" \
        hh_record_valid "$HH_STORE/bootstrap-failure" codex
      hh_assert "[$shell][$mode] invalid storage input never falls back to writing the plugin tree" \
        diff -r "$baseline" "$HH_ROOT"
    done
  done
}
