#!/usr/bin/env bash
# cenci installer — one command for the whole package.
#
# Installs or updates the three cenci plugins (cenci, cenci-watch,
# cenci-sandbox) as a single system: registers the marketplace, installs the
# plugins, and runs the post-install setup that used to be manual (cn launcher
# link + sandbox image build, macOS menu bar and Linux desktop bar-widget
# wiring). Supports Claude Code and Codex as marketplace-installable clients;
# when OpenCode is also detected on the host, offers to wire it up too
# (portable skills + watch plugin registration) — OpenCode-only hosts are not
# supported on their own, since it has no marketplace CLI of its own.
#
# Usage:
#   ./install.sh                interactive wizard (install)
#   cenci-installer update      update installed plugins (+ optional rebuild)
#   cenci-installer doctor      check prerequisites, change nothing
#   cenci-installer uninstall   remove plugins, PATH links, daemon, config,
#                                and every cenci sandbox container/image/
#                                volume on this machine
#
#   The canonical install is a download-verify-run sequence: fetch install.sh
#   and its cosign sign-blob bundle from the latest `watch/vX.Y.Z` release
#   asset, verify the bundle against cenci's pinned release identity with
#   `cosign verify-blob`, then run only the verified bytes (see
#   docs/getting-started.md for the exact commands). cosign
#   (https://docs.sigstore.dev/system_config/installation/) is required —
#   verification fails closed with no fallback to any unverified ref.
#
#   The legacy one-liner (`curl -fsSL .../main/install.sh | bash`) still
#   works: piped runs with CENCI_REF unset re-exec themselves through the
#   same verified download-verify-run path, from the newest `<layer>/vX.Y.Z`
#   release tag's install.sh — never live main. That same resolved ref then
#   pins the client marketplace registration and all three installed
#   plugins' content — not just which install.sh runs — on every path
#   (initial install, `cenci-installer update`, repair, dev-from-clone). Set
#   CENCI_REF=main (or pass --ref main) to explicitly opt into bleeding-edge,
#   unverified main instead (unsafe; development use only) — it is the only
#   path that intentionally tracks main.
#
# Flags:
#   --yes                                 accept defaults, never prompt
#   --build / --no-build                  force / skip the sandbox image build
#   --lazyboards / --no-lazyboards        force / skip lazyboards install
#                                          (uninstall mode: --lazyboards also removes it)
#   --ref <ref>                           marketplace + plugin content, and the installer
#                                          itself, all come from this ref instead of the
#                                          resolved latest release tag (e.g. --ref main);
#                                          same as CENCI_REF=<ref>
#   --help                                this text

set -u

MARKETPLACE_REPO="matteobortolazzo/cenci"
MARKETPLACE_NAME="cenci"
ALL_PLUGINS="cenci cenci-watch cenci-sandbox"
# RESOLVED_REF is the single validated ref that governs marketplace
# registration (and, through it, all three plugins' content) for this run —
# resolved once in main and consumed by step_marketplace. Empty until main
# resolves it (see resolve_install_ref / validate_ref_format / ref_available
# near cenci_latest_ref).
RESOLVED_REF=""
LAZYBOARDS_REPO="matteobortolazzo/lazyboards"
CODEX_MARKETPLACE_READY=0
CLAUDE_MARKETPLACE_READY=0
HAS_CLAUDE=0
HAS_CODEX=0
HAS_OPENCODE=0
OPENCODE_MIN_VERSION="1.18.3"

# ---------------------------------------------------------------- output ----

if [ -t 1 ]; then
	BOLD=$'\033[1m' DIM=$'\033[2m' RED=$'\033[31m' GREEN=$'\033[32m' YELLOW=$'\033[33m' BLUE=$'\033[34m' RESET=$'\033[0m'
else
	BOLD='' DIM='' RED='' GREEN='' YELLOW='' BLUE='' RESET=''
fi

say()  { printf '%s\n' "$*"; }
ok()   { printf '  %s✓%s %s\n' "$GREEN" "$RESET" "$*"; }
warn() { printf '  %s!%s %s\n' "$YELLOW" "$RESET" "$*"; }
fail() { printf '  %s✗%s %s\n' "$RED" "$RESET" "$*"; }
step() { printf '\n%s==>%s %s%s%s\n' "$BLUE" "$RESET" "$BOLD" "$*" "$RESET"; }
die()  { printf '%sError:%s %s\n' "$RED" "$RESET" "$*" >&2; exit 1; }

# ---------------------------------------------------------- interactivity ----

# The script must work through `curl | bash`, where stdin is the pipe — so all
# prompts read from /dev/tty. No tty (CI, --yes) means defaults are used.
INTERACTIVE=0
if [ -r /dev/tty ] && [ -w /dev/tty ]; then
	INTERACTIVE=1
fi

ASSUME_YES=0

# ask_yn <question> <default y|n> — returns 0 for yes.
ask_yn() {
	local q="$1" def="$2" hint ans
	if [ "$INTERACTIVE" -eq 0 ] || [ "$ASSUME_YES" -eq 1 ]; then
		[ "$def" = y ]
		return
	fi
	if [ "$def" = y ]; then hint="[Y/n]"; else hint="[y/N]"; fi
	while :; do
		printf '%s %s ' "$q" "$hint" >/dev/tty
		read -r ans </dev/tty || ans=""
		case "${ans:-$def}" in
		[Yy]*) return 0 ;;
		[Nn]*) return 1 ;;
		esac
	done
}

# ---------------------------------------------------------------- platform ----

OS="" ARCH="" IS_WSL=0
detect_platform() {
	case "$(uname -s)" in
	Linux) OS=linux ;;
	Darwin) OS=macos ;;
	*) die "unsupported OS '$(uname -s)' — cenci supports Linux, macOS, and WSL2" ;;
	esac
	case "$(uname -m)" in
	x86_64 | amd64) ARCH=amd64 ;;
	arm64 | aarch64) ARCH=arm64 ;;
	*) ARCH="$(uname -m)" ;;
	esac
	if [ "$OS" = linux ] && grep -qi microsoft /proc/version 2>/dev/null; then
		IS_WSL=1
	fi
}

platform_label() {
	if [ "$IS_WSL" -eq 1 ]; then
		printf 'WSL2 (%s)' "$ARCH"
	elif [ "$OS" = macos ]; then
		printf 'macOS (%s)' "$ARCH"
	else
		printf 'Linux (%s)' "$ARCH"
	fi
}

have() { command -v "$1" >/dev/null 2>&1; }

# run_bounded <seconds> <cmd...> — run cmd with stdin detached and a hard
# timeout. Probes of binaries this installer doesn't own (e.g. `lazyboards
# --version`) must never block the run: a build that doesn't recognize the
# flag can start its TUI and wait forever for terminal input — and under
# `curl | bash` it would inherit the script's own stdin pipe as that input.
run_bounded() {
	local secs="$1" pid watchdog rc
	shift
	if have timeout; then
		timeout "$secs" "$@" </dev/null
		return $?
	fi
	"$@" </dev/null &
	pid=$!
	{ sleep "$secs" && kill "$pid"; } 2>/dev/null &
	watchdog=$!
	wait "$pid" 2>/dev/null
	rc=$?
	{ kill "$watchdog" && wait "$watchdog"; } 2>/dev/null
	return "$rc"
}

detect_clients() {
	have claude && HAS_CLAUDE=1
	have codex && HAS_CODEX=1
	have opencode && HAS_OPENCODE=1
}

# have_supported_client gates on Claude/Codex only — OpenCode has no
# marketplace CLI of its own, so an OpenCode-only host can't bootstrap the
# marketplace checkout step_opencode_setup depends on. It's an opt-in add-on
# once one of the two marketplace-capable clients is present, never a
# standalone install target.
have_supported_client() {
	[ "$HAS_CLAUDE" -eq 1 ] || [ "$HAS_CODEX" -eq 1 ]
}

container_runtime() {
	if have podman; then
		echo podman
	elif have docker; then
		echo docker
	else
		return 1
	fi
}

# ------------------------------------------------------------- plugin CLI ----

# claude plugin names can be qualified with the marketplace; try the qualified
# form first, fall back to the bare name for older CLIs.
plugin_cmd() {
	local action="$1" name="$2"
	claude plugin "$action" "${name}@${MARKETPLACE_NAME}" >/dev/null 2>&1 ||
		claude plugin "$action" "$name" >/dev/null 2>&1
}

plugin_installed() {
	local list
	list="$(claude plugin list 2>/dev/null)" || return 2
	printf '%s\n' "$list" |
		grep -Eq "(^|[[:space:]])$1@${MARKETPLACE_NAME}([[:space:]]|$)"
}

marketplace_registered() {
	local list
	list="$(claude plugin marketplace list 2>/dev/null)" || return 2
	printf '%s\n' "$list" |
		grep -Eq "(^|[[:space:]])${MARKETPLACE_NAME}([[:space:]]|$)"
}

codex_plugin_installed() {
	local list
	list="$(codex plugin list 2>/dev/null)" || return 2
	printf '%s\n' "$list" |
		grep -Eq "^$1@${MARKETPLACE_NAME}[[:space:]]+installed([[:space:]]|$)"
}

codex_marketplace_registered() {
	local list
	list="$(codex plugin marketplace list 2>/dev/null)" || return 2
	printf '%s\n' "$list" |
		grep -Eq "^${MARKETPLACE_NAME}([[:space:]]|$)"
}

# claude_plugin_version <plugin> — version of the exact <plugin>@cenci record
# in Claude's authoritative active-install state
# (~/.claude/plugins/installed_plugins.json), preferring the "user"-scope
# record when multiple coexist, else the first record. Prints nothing (and
# never fails) when the file, jq, or a valid semver version is unavailable —
# a missing/corrupt authoritative record falls back to ok_updated's non-fatal
# wording rather than breaking the update.
claude_plugin_version() {
	local plugin="$1" meta version
	meta="$HOME/.claude/plugins/installed_plugins.json"
	[ -f "$meta" ] || return 0
	have jq || return 0
	version="$(jq -r --arg key "${plugin}@${MARKETPLACE_NAME}" \
		'(.plugins[$key] // []) as $r | ([$r[] | select(.scope=="user") | .version][0] // $r[0].version // empty)' \
		"$meta" 2>/dev/null)" || return 0
	[[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || return 0
	printf '%s\n' "$version"
}

# codex_plugin_version <plugin> — version of the exact <plugin>@cenci entry
# from Codex's authoritative active-install state (`codex plugin list
# --json`'s `.installed[]`, matched by `.pluginId`), mirroring
# update_codex_plugins' parsing in sandbox/lib/migrate-settings.sh. Prints
# nothing (and never fails) when jq is unavailable, the command fails, its
# output isn't valid JSON, or no valid semver version is found.
codex_plugin_version() {
	local plugin="$1" versions_json version
	have jq || return 0
	versions_json="$(codex plugin list --json 2>/dev/null)" || return 0
	jq -e . <<<"$versions_json" >/dev/null 2>&1 || return 0
	version="$(jq -r --arg id "${plugin}@${MARKETPLACE_NAME}" \
		'[.installed[]? | select(.pluginId == $id) | .version][0] // empty' \
		<<<"$versions_json" 2>/dev/null)" || return 0
	[[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || return 0
	printf '%s\n' "$version"
}

# installed_plugin_version <claude|codex> <plugin> — active installed version
# of <plugin>, resolved from the client's own authoritative install state
# (never plugin-cache manifest mtimes, which are not authoritative — see the
# resolvers above). Prints nothing when that state is absent, unreadable, or
# yields no valid semver.
installed_plugin_version() {
	local client="$1" plugin="$2"
	case "$client" in
	claude) claude_plugin_version "$plugin" ;;
	codex) codex_plugin_version "$plugin" ;;
	esac
}

# ok_updated <label> <plugin> <old-version> <new-version> — success line for a
# plugin update, showing the version transition when the client's
# authoritative install state (installed_plugin_version) reveals it ("1.2.3 →
# 1.2.4", or "(already up to date)" when nothing new was pulled). Falls back
# to the plain wording when no version is known.
ok_updated() {
	local label="$1" plugin="$2" old="$3" new="$4"
	if [ -n "$old" ] && [ -n "$new" ] && [ "$old" != "$new" ]; then
		ok "$label: $plugin $old → $new"
	elif [ -n "$new" ] && [ "$old" = "$new" ]; then
		ok "$label: $plugin $new (already up to date)"
	elif [ -n "$new" ]; then
		ok "$label: $plugin updated to $new"
	else
		ok "$label: $plugin updated"
	fi
}

# find_plugin_path <relative-path> — resolve a file inside the installed
# marketplace checkout (stable across plugin updates), falling back to the
# versioned plugin cache.
find_plugin_path() {
	local rel="$1" f
	for f in \
		"$HOME/.claude/plugins/marketplaces/$MARKETPLACE_NAME/$rel" \
		"$HOME/.codex/plugins/marketplaces/$MARKETPLACE_NAME/$rel"; do
		[ -e "$f" ] && { printf '%s\n' "$f"; return 0; }
	done
	case "$rel" in
	sandbox/*)
		rel=${rel#sandbox/}
		for f in \
			"$HOME"/.claude/plugins/cache/*/cenci-sandbox/*/"$rel" \
			"$HOME"/.codex/plugins/cache/*/cenci-sandbox/*/"$rel"; do
			[ -e "$f" ] && { printf '%s\n' "$f"; return 0; }
		done
		;;
	watch/plugin/*)
		rel=${rel#watch/plugin/}
		for f in \
			"$HOME"/.claude/plugins/cache/*/cenci-watch/*/"$rel" \
			"$HOME"/.codex/plugins/cache/*/cenci-watch/*/"$rel"; do
			[ -e "$f" ] && { printf '%s\n' "$f"; return 0; }
		done
		;;
	flow/*)
		rel=${rel#flow/}
		for f in \
			"$HOME"/.claude/plugins/cache/*/cenci/*/"$rel" \
			"$HOME"/.codex/plugins/cache/*/cenci/*/"$rel"; do
			[ -e "$f" ] && { printf '%s\n' "$f"; return 0; }
		done
		;;
	esac
	return 1
}

# ------------------------------------------------------------------ doctor ----

# check <label> <required|optional> <hint> <cmd...>
check() {
	local label="$1" kind="$2" hint="$3"
	shift 3
	if "$@" >/dev/null 2>&1; then
		ok "$label"
		return 0
	fi
	if [ "$kind" = required ]; then
		fail "$label — $hint"
		DOCTOR_FAILED=1
	else
		warn "$label — $hint"
	fi
	return 1
}

# cenci_binary_for_doctor resolves the installed cenci binary the
# same way a running agent session would: a bare `cenci` on PATH first
# (the bootstrap-maintained ~/.local/bin link, or the Codex-only manual-install
# case), falling back to the version-pinned plugin cache directly so doctor can
# still report daemon state before that PATH link exists (e.g. right after a
# fresh install, before the first agent session bootstraps it). cached_cenci_binary
# is defined further down this file but, like every function here, is available
# by the time run_doctor actually runs (MODE dispatch happens after the whole
# script is parsed).
cenci_binary_for_doctor() {
	if have cenci; then
		command -v cenci
		return 0
	fi
	cached_cenci_binary
}

# doctor_daemon_status reports whether the cenci daemon is alive via
# `cenci daemon status` (exit 0 = running, exit 1 = not running — see
# runDaemonStatus in watch/main.go). A missing binary is reported like any
# other missing component (warn, not a hard doctor failure) since the daemon
# self-bootstraps on the first agent session; an idle daemon is equally
# expected and not treated as an error either.
doctor_daemon_status() {
	local bin
	if ! bin="$(cenci_binary_for_doctor)"; then
		warn "cenci daemon — binary not found yet (bootstraps on your first agent session)"
		return 0
	fi
	if "$bin" daemon status >/dev/null 2>&1; then
		ok "cenci daemon: running"
	else
		warn "cenci daemon: not running (starts automatically on your first agent session, or run: cenci daemon start)"
	fi
}

# check_stale_tmux_vars greps the user's own tmux config(s) — never touched by
# this installer — for the pre-rename @agentwatch-* tmux user variables. tmux
# format lookups against a variable that no longer exists silently fall back
# to the format's default (no error), so a stale window-status-format override
# left over from a pre-rename Custom status-format setup just looks like
# nothing happened rather than a visible break. This is read-only: doctor's
# contract is to change nothing, so it warns instead of rewriting the dotfile.
check_stale_tmux_vars() {
	local conf found=0
	for conf in "$HOME/.tmux.conf" "${XDG_CONFIG_HOME:-$HOME/.config}/tmux/tmux.conf"; do
		[ -f "$conf" ] || continue
		if grep -qE '@agentwatch-(symbol|style|headroom-)' "$conf" 2>/dev/null; then
			found=1
		fi
	done
	if [ "$found" -eq 1 ]; then
		warn "tmux config references old @agentwatch-* variables — see docs/migrating-to-cenci.md#tmux-user-variables"
	fi
}

doctor_codex_support() {
	[ "$HAS_CODEX" -eq 1 ] || return 0
	local raw version config method notifications project_root
	raw="$(codex --version 2>/dev/null || true)"
	version="$(printf '%s' "$raw" | grep -oE '[0-9]+\.[0-9]+\.[0-9]+' | head -1)"
	if [ -n "$version" ] && printf '%s\n%s\n' "0.144.1" "$version" | sort -V -C 2>/dev/null; then
		ok "Codex ${version} supports cenci hooks"
	else
		warn "Codex ${version:-unknown} is unsupported — cenci requires 0.144.1 or newer"
	fi
	config="${CODEX_HOME:-$HOME/.codex}/config.toml"
	project_root="$(git rev-parse --show-toplevel 2>/dev/null || true)"
	method="$(grep -E '^[[:space:]]*notification_method[[:space:]]*=' "$config" 2>/dev/null | tail -1 | cut -d= -f2- | tr -d ' "')"
	notifications="$(grep -E '^[[:space:]]*notifications[[:space:]]*=' "$config" 2>/dev/null | tail -1 | cut -d= -f2- | sed 's/^[[:space:]]*//')"
	[ -n "$notifications" ] && say "    Codex tui.notifications: $notifications"
	if [ -n "$method" ]; then say "    Codex notification_method: $method"; else say "    Codex notification_method: default (terminal-dependent)"; fi
	if [ "$method" = "bel" ] || [ -z "$method" ]; then
		warn "Codex BEL notifications may trigger tmux's native red-background bell; set notification_method = \"osc9\" yourself if unwanted"
	fi
	if [ -n "$project_root" ] && [ -f "$project_root/.cenci/config.json" ]; then ok "Neutral project config: $project_root/.cenci/config.json"
	elif [ -n "$project_root" ] && [ -f "$project_root/.claude/config.json" ]; then warn "Legacy-only project config: run cenci:configure to migrate to .cenci/config.json"
	elif [ -n "$project_root" ]; then warn "No .cenci/config.json in this project"
	fi
	warn "Codex hook trust state is not exposed non-interactively; inspect /hooks after plugin updates (cenci never changes trust)"
}

# sysbox_runc_version <path> — resolves the sysbox-runc version string per
# #630's Q2 fallback chain: first exec <path> (the runtime path docker info's
# Runtimes JSON reported for the sysbox-runc entry) with --version, then fall
# back to a bare `sysbox-runc --version` on PATH if <path> is empty/
# unusable/yields no version, else report "unknown". The candidate version
# token (the last whitespace-separated field of the version output's first
# line) is validated with a whole-string `[[ =~ ]]` regex — not line-oriented
# grep — before being trusted for display (per the project's own
# whole-string-validation lesson), so a truncated/malformed version string
# can never masquerade as a real one.
sysbox_runc_version() {
	local path="$1" raw="" line token
	if [ -n "$path" ] && [ -x "$path" ]; then
		raw="$("$path" --version 2>/dev/null)" || raw=""
	fi
	if [ -z "$raw" ] && have sysbox-runc; then
		raw="$(sysbox-runc --version 2>/dev/null)" || raw=""
	fi
	line="$(printf '%s\n' "$raw" | head -1)"
	token="${line##* }"
	if [[ "$token" =~ ^[0-9]+\.[0-9]+\.[0-9]+([.-][0-9A-Za-z.-]+)*$ ]]; then
		printf '%s' "$token"
	else
		printf 'unknown'
	fi
}

# doctor_sysbox reports whether Docker has the sysbox-runc container runtime
# registered for nested Docker (dind) sandbox sessions, and (#630) its
# registered version. This is an informational mirror of Go's
# sandbox.SysboxRegistered (watch/internal/sandbox/sandbox.go), which probes
# the exact same `docker info --format '{{json .Runtimes}}'` output for the
# same "sysbox-runc" key — keep both detections in sync if either changes
# (Q4: the Go helper itself stays a 3-state present/absent/error probe with
# no version; only this bash doctor check gets the 4-state + version
# treatment). Never sets DOCTOR_FAILED: sysbox is an opt-in capability,
# expected to be absent on hosts/CI that don't need it (#587). Distinguishes
# four states:
#   1. query failure — `docker info` itself failed (daemon down, permission
#      denied, misconfigured context) — reported distinctly from absence so
#      an operator doesn't mistake a broken Docker for a genuinely
#      unregistered sysbox-runc.
#   2. malformed — docker info answered, but its Runtimes JSON is
#      unparsable (validated via `jq -e .`, a real parser rather than a
#      grep-based guess, when jq is available; falls back to the old
#      presence-only grep check when jq is absent, since a curl|bash install
#      may not have jq — #630's Risks).
#   3. absent — well-formed JSON with no "sysbox-runc" key.
#   4. present — resolves and reports the sysbox-runc version via
#      sysbox_runc_version.
doctor_sysbox() {
	local runtimes
	if ! runtimes="$(docker info --format '{{json .Runtimes}}' 2>/dev/null)" || [ -z "$runtimes" ]; then
		say "    could not query docker info — sysbox-runc status unknown"
		return 0
	fi
	if ! have jq; then
		# No jq: fall back to the pre-#630 presence-only substring probe.
		# Cannot distinguish malformed JSON from genuine absence without a
		# real parser, but this never fails doctor either way.
		if printf '%s' "$runtimes" | grep -q '"sysbox-runc"'; then
			ok "sysbox-runc registered — nested Docker (dind) available"
		else
			say "    sysbox-runc not registered — nested Docker (dind) unavailable (see sandbox/README.md#nested-docker-sysbox)"
		fi
		return 0
	fi
	if ! printf '%s' "$runtimes" | jq -e . >/dev/null 2>&1; then
		say "    docker info returned malformed runtimes data — sysbox-runc status unknown"
		return 0
	fi
	if [ "$(printf '%s' "$runtimes" | jq -r 'has("sysbox-runc")' 2>/dev/null)" != "true" ]; then
		say "    sysbox-runc not registered — nested Docker (dind) unavailable (see sandbox/README.md#nested-docker-sysbox)"
		return 0
	fi
	local sysbox_path version
	sysbox_path="$(printf '%s' "$runtimes" | jq -r '."sysbox-runc".path // empty' 2>/dev/null)"
	version="$(sysbox_runc_version "$sysbox_path")"
	ok "sysbox-runc registered — nested Docker (dind) available (version ${version})"
}

# opencode_config_dir — OpenCode's config directory, matching the exact
# expression flow/opencode/install-skills.sh uses for its own TARGET_DIR, so
# doctor's file-based checks below stay in sync with what that script (and
# sandbox/lib/opencode-config.sh's seed_opencode_config) actually writes to.
opencode_config_dir() {
	printf '%s' "${XDG_CONFIG_HOME:-$HOME/.config}/opencode"
}

# opencode_skills_linked — true when at least one entry exists under
# OpenCode's skills dir. Unlike Claude/Codex's plugin_installed (which queries
# the client's own CLI), OpenCode has no marketplace CLI to ask, so this is a
# direct filesystem check mirroring what flow/opencode/install-skills.sh
# itself symlinks into. Uses bash's own globbing rather than `ls`/`find` so
# it has no extra tool dependency.
opencode_skills_linked() {
	local dir entry
	dir="$(opencode_config_dir)/skills"
	[ -d "$dir" ] || return 1
	for entry in "$dir"/*; do
		if [ -e "$entry" ] || [ -L "$entry" ]; then
			return 0
		fi
	done
	return 1
}

# OPENCODE_PLUGIN_SPEC_PATTERN — an extended-regex fragment matching the
# cenci-watch OpenCode plugin's file:// spec regardless of which
# find_plugin_path branch resolved it when step_opencode_setup registered it:
# the marketplace-checkout branch's path ends in watch/plugin/opencode, while
# the plugin-cache fallback branch strips the watch/plugin/ prefix before
# searching, producing .../cenci-watch/<version>/opencode instead — with no
# watch/plugin/opencode substring in it at all. A single hardcoded literal
# would miss that second shape, so both are matched here. Shared by
# opencode_plugin_registered and uninstall_opencode_cleanup's jq filter so
# both registration shapes are recognized consistently.
OPENCODE_PLUGIN_SPEC_PATTERN='watch/plugin/opencode|cenci-watch/[^/"]+/opencode'

# opencode_plugin_registered — true when the cenci-watch OpenCode plugin's
# file:// spec (registered by seed_opencode_config) is present in
# opencode.json's .plugin array.
opencode_plugin_registered() {
	local config
	config="$(opencode_config_dir)/opencode.json"
	[ -f "$config" ] && grep -qE "$OPENCODE_PLUGIN_SPEC_PATTERN" "$config" 2>/dev/null
}

# doctor_opencode_support reports whether the detected OpenCode is new enough
# to support cenci integration (>= $OPENCODE_MIN_VERSION, mirroring
# doctor_codex_support's semver-grep + sort -V -C comparison), and whether a
# provider is authenticated — checked via env vars or the presence of
# OpenCode's own auth.json, never a live API call. Cenci-integration health
# (skills linked / plugin registered) is reported separately under "Installed
# stack components" below, alongside the other per-client install state; the
# daemon-reachability piece of that same "is the integration healthy"
# question is already covered once, for every client, by doctor_daemon_status
# — not duplicated here.
doctor_opencode_support() {
	[ "$HAS_OPENCODE" -eq 1 ] || return 0
	local raw version
	raw="$(opencode --version 2>/dev/null || true)"
	version="$(printf '%s' "$raw" | grep -oE '[0-9]+\.[0-9]+\.[0-9]+' | head -1)"
	if [ -n "$version" ] && printf '%s\n%s\n' "$OPENCODE_MIN_VERSION" "$version" | sort -V -C 2>/dev/null; then
		ok "OpenCode ${version} supports cenci integration"
	else
		warn "OpenCode ${version:-unknown} is unsupported — cenci requires ${OPENCODE_MIN_VERSION} or newer"
	fi
	if [ -n "${ANTHROPIC_API_KEY:-}" ] || [ -n "${OPENAI_API_KEY:-}" ] || [ -f "${XDG_DATA_HOME:-$HOME/.local/share}/opencode/auth.json" ]; then
		ok "OpenCode provider authenticated"
	else
		warn "OpenCode provider not authenticated"
	fi
}

run_doctor() {
	DOCTOR_FAILED=0
	# Hints must match how doctor was reached: standalone `cenci doctor` can
	# only point at the installer, but the install-mode preflight runs before
	# the very steps that create these items — telling a first-time installer
	# to "re-run the installer" mid-install reads as a failure loop.
	local launcher_hint="re-run the installer to create it"
	local image_hint="build it with: cenci sandbox build"
	local lazyboards_hint="optional; install with: cenci-installer --lazyboards"
	local seed_hint="re-run the installer to seed the default, or see docs/orchestration.md"
	if [ "$MODE" = install ]; then
		launcher_hint="created later in this run"
		[ "$BUILD_IMAGE" = no ] || image_hint="you'll be offered the build later in this run"
		case "$LAZYBOARDS" in
		ask) lazyboards_hint="optional; you'll be asked later in this run" ;;
		yes) lazyboards_hint="optional; installed later in this run" ;;
		esac
		# Mirrors step_lazyboards_setup's guard: only an explicit --no-lazyboards
		# skips seeding for an already-installed binary.
		if ! { [ "$LAZYBOARDS" = no ] && [ "$LAZYBOARDS_EXPLICIT" -eq 1 ]; }; then
			seed_hint="seeded later in this run (see docs/orchestration.md)"
		fi
	fi
	step "Checking your system ($(platform_label))"

	say "  ${BOLD}Required platform dependencies${RESET}"
	check "git" required "install git from your package manager" command -v git
	check "curl" required "install curl from your package manager" command -v curl
	if [ "$OS" = macos ]; then
		check "Docker or Podman" required \
			"install Docker Desktop (https://docker.com/products/docker-desktop) or Podman" \
			container_runtime
	else
		check "Docker or Podman" required \
			"install docker or podman from your package manager" container_runtime
	fi

	say ""
	say "  ${BOLD}Supported clients (at least one required)${RESET}"
	if [ "$HAS_CLAUDE" -eq 1 ]; then ok "Claude Code detected"; else warn "Claude Code not detected"; fi
	if [ "$HAS_CODEX" -eq 1 ]; then ok "Codex detected"; else warn "Codex not detected"; fi
	doctor_codex_support
	# Unlike Claude/Codex (marketplace-capable clients, always reported), a
	# missing OpenCode is only worth a line in standalone `cenci doctor` runs —
	# it's a bonus, opt-in integration, and printing "not detected" during
	# every plain install/update preflight would add noise for the many hosts
	# that never have it. Its *presence* is always worth surfacing though.
	if [ "$HAS_OPENCODE" -eq 1 ]; then
		ok "OpenCode detected"
	elif [ "$MODE" = doctor ]; then
		warn "OpenCode not detected"
	fi
	doctor_opencode_support
	if ! have_supported_client; then
		fail "no supported client — install Claude Code, Codex, or both"
		DOCTOR_FAILED=1
	fi

	say ""
	say "  ${BOLD}For cenci (workflow)${RESET}"
	check "gh (GitHub CLI)" optional \
		"needed for issues/PRs: https://cli.github.com" command -v gh
	if have gh; then
		check "gh authenticated" optional "run: gh auth login" gh auth status
	fi

	say ""
	say "  ${BOLD}For cenci-watch (attention)${RESET}"
	check "tmux" optional \
		"cenci's main frontend is the tmux status bar; other surfaces (waybar, macOS menu bar) still work without it" \
		command -v tmux
	check_stale_tmux_vars
	if [ "$OS" = macos ]; then
		check "SwiftBar (menu bar widget)" optional \
			"optional: brew install swiftbar — for live status in the macOS menu bar" \
			test -d /Applications/SwiftBar.app
	else
		for de in gnome plasma dms noctalia; do
			if de_detected "$de"; then
				ok "$(de_label "$de") detected — widget installable"
			fi
		done
		if have waybar; then
			ok "waybar detected — add the Cenci Watch module (see watch/README.md)"
		fi
	fi
	doctor_daemon_status

	say ""
	say "  ${BOLD}Installed stack components${RESET}"
	if [ "$HAS_CLAUDE" -eq 1 ]; then
		for p in $ALL_PLUGINS; do
			if plugin_installed "$p"; then
				ok "Claude: $p"
			elif [ "$?" -eq 2 ]; then
				fail "Claude: could not query installed plugins"
				DOCTOR_FAILED=1
				break
			else
				warn "Claude: $p not installed"
			fi
		done
	fi
	if [ "$HAS_CODEX" -eq 1 ]; then
		for p in $ALL_PLUGINS; do
			if codex_plugin_installed "$p"; then
				ok "Codex: $p"
			elif [ "$?" -eq 2 ]; then
				fail "Codex: could not query installed plugins"
				DOCTOR_FAILED=1
				break
			else
				warn "Codex: $p not installed"
			fi
		done
	fi
	if [ "$HAS_OPENCODE" -eq 1 ]; then
		# No plugin-CLI query — OpenCode has none — so this is the direct
		# filesystem check the doctor_opencode_support comment above refers to.
		if opencode_skills_linked; then ok "OpenCode skills linked"; else warn "OpenCode skills not linked"; fi
		if opencode_plugin_registered; then ok "OpenCode plugin registered"; else warn "OpenCode plugin not registered"; fi
	fi

	say ""
	say "  ${BOLD}Launchers and container image${RESET}"
	check "cenci-installer utility" optional "$launcher_hint" test -x "$HOME/.local/bin/cenci-installer"
	if [ "$HAS_CLAUDE" -eq 1 ] || [ "$HAS_CODEX" -eq 1 ]; then
		check "cn launcher (cenci open)" optional "$launcher_hint" test -x "$HOME/.local/bin/cn"
	fi
	if runtime="$(container_runtime 2>/dev/null)"; then
		check "cenci-sandbox:latest image" optional "$image_hint" \
			"$runtime" image inspect cenci-sandbox:latest
	fi
	# Sysbox/dind requires Docker specifically (not Podman) as the outer
	# runtime, so this probe is gated on `have docker` independently of
	# container_runtime()'s podman-first preference for ordinary launches —
	# a dual-runtime host preferring Podman must still see Docker's sysbox
	# registration status (#630).
	if have docker; then
		doctor_sysbox
	fi
	# cosign is a new mandatory prerequisite for the default verified piped
	# install path (#626): the release-pin block fails closed if it's
	# absent, with no fallback to an unverified ref. Warn here (not a hard
	# doctor failure) so a fresh install with cosign missing is caught by
	# doctor before the piped install would fail closed anyway.
	check "cosign (installer verification)" optional \
		"install cosign (https://docs.sigstore.dev/system_config/installation/) — required for the default installer's fail-closed release verification" \
		command -v cosign

	say ""
	say "  ${BOLD}Board orchestration (optional)${RESET}"
	local lb_ver
	if lazyboards_managed_binary >/dev/null 2>&1; then
		if lb_ver="$(lazyboards_installed_version)" && [ -n "$lb_ver" ]; then
			ok "lazyboards installed (v${lb_ver})"
		else
			ok "lazyboards installed"
		fi
		local lb_config="${XDG_CONFIG_HOME:-$HOME/.config}/lazyboards/config.yml"
		if [ -f "$lb_config" ]; then
			ok "board config present (~/.config/lazyboards/config.yml)"
			if grep -q 'git fetch origin {pr_branch}' "$lb_config" 2>/dev/null; then
				warn "Checkout PR action uses the vulnerable git fetch/git switch pattern — update to gh pr checkout {pr_number}"
			fi
		else
			warn "no board config — $seed_hint"
		fi
		if ! verify_lazyboards_resolution; then
			warn "lazyboards PATH resolution needs attention"
		fi
	elif lazyboards_path_binary >/dev/null 2>&1; then
		warn "unmanaged lazyboards found at $(lazyboards_path_binary) — reconcile it with: cenci update --lazyboards"
	else
		warn "lazyboards not installed — $lazyboards_hint"
	fi

	say ""
	if [ "$DOCTOR_FAILED" -eq 1 ]; then
		say "${RED}Missing required tools — fix the ✗ items above, then re-run.${RESET}"
		return 1
	fi
	say "${GREEN}Required tools are present.${RESET} Optional warnings only affect the noted feature."
	return 0
}

# ----------------------------------------------------------------- wizard ----

SELECTED="$ALL_PLUGINS"

selected() { case " $SELECTED " in *" $1 "*) return 0 ;; *) return 1 ;; esac; }

# ------------------------------------------------------------ install steps ----

step_marketplace() {
	local state
	step "Reconciling the cenci marketplace"
	if [ "$HAS_CLAUDE" -eq 0 ]; then
		:
	elif marketplace_registered; then
		# Registered, possibly at a different (or unpinned) ref. Neither
		# client's in-place add/update semantics for repointing an existing
		# registration are verified, so remove then re-add at RESOLVED_REF —
		# the only mechanism that deterministically moves the registration
		# (#625, Q4). Destructive-prerequisite convention: verify the remove
		# succeeded before re-adding; don't proceed optimistically.
		if claude plugin marketplace remove "$MARKETPLACE_NAME" >/dev/null 2>&1; then
			if claude plugin marketplace add "${MARKETPLACE_REPO}@${RESOLVED_REF}" >/dev/null 2>&1; then
				ok "Claude: marketplace '$MARKETPLACE_NAME' repointed to $RESOLVED_REF"
				CLAUDE_MARKETPLACE_READY=1
			else
				fail "Claude: removed marketplace '$MARKETPLACE_NAME' but could not re-register it at $RESOLVED_REF. Run manually:
  claude plugin marketplace add ${MARKETPLACE_REPO}@${RESOLVED_REF}"
				INSTALL_FAILED=1
			fi
		else
			fail "Claude: could not remove the existing marketplace registration before repointing it to $RESOLVED_REF. Run manually:
  claude plugin marketplace remove $MARKETPLACE_NAME
  claude plugin marketplace add ${MARKETPLACE_REPO}@${RESOLVED_REF}"
			INSTALL_FAILED=1
		fi
	else
		state=$?
		if [ "$state" -eq 2 ]; then
			fail "Claude: could not query registered marketplaces"
			INSTALL_FAILED=1
		elif claude plugin marketplace add "${MARKETPLACE_REPO}@${RESOLVED_REF}" >/dev/null 2>&1; then
			ok "Claude: registered ${MARKETPLACE_REPO}@${RESOLVED_REF}"
			CLAUDE_MARKETPLACE_READY=1
		else
			fail "Claude: could not register the marketplace. Run manually:
  claude plugin marketplace add ${MARKETPLACE_REPO}@${RESOLVED_REF}"
			INSTALL_FAILED=1
		fi
	fi

	[ "$HAS_CODEX" -eq 1 ] || return 0
	if codex_marketplace_registered; then
		# Same remove+re-add contract as Claude above (#625, Q4).
		if codex plugin marketplace remove "$MARKETPLACE_NAME" >/dev/null 2>&1; then
			if codex plugin marketplace add "$MARKETPLACE_REPO" --ref "$RESOLVED_REF" >/dev/null 2>&1; then
				ok "Codex: marketplace '$MARKETPLACE_NAME' repointed to $RESOLVED_REF"
				CODEX_MARKETPLACE_READY=1
			else
				fail "Codex: removed marketplace '$MARKETPLACE_NAME' but could not re-register it at $RESOLVED_REF. Run manually:
  codex plugin marketplace add $MARKETPLACE_REPO --ref $RESOLVED_REF"
				INSTALL_FAILED=1
			fi
		else
			fail "Codex: could not remove the existing marketplace registration before repointing it to $RESOLVED_REF. Run manually:
  codex plugin marketplace remove $MARKETPLACE_NAME
  codex plugin marketplace add $MARKETPLACE_REPO --ref $RESOLVED_REF"
			INSTALL_FAILED=1
		fi
	else
		state=$?
		if [ "$state" -eq 2 ]; then
			fail "Codex: could not query registered marketplaces"
			INSTALL_FAILED=1
		elif codex plugin marketplace add "$MARKETPLACE_REPO" --ref "$RESOLVED_REF" >/dev/null 2>&1; then
			ok "Codex: registered $MARKETPLACE_REPO --ref $RESOLVED_REF"
			CODEX_MARKETPLACE_READY=1
		else
			fail "Codex marketplace registration failed. Run manually: codex plugin marketplace add $MARKETPLACE_REPO --ref $RESOLVED_REF"
			INSTALL_FAILED=1
		fi
	fi
}

step_reconcile_plugins() {
	local p old state
	step "Reconciling plugins"
	# No ref argument needed here: step_marketplace already pinned the
	# marketplace registration to RESOLVED_REF, so `plugin install`/`plugin
	# update`/`plugin add` below pull from that pinned checkout for every
	# plugin. Do not reintroduce a ref-less registration path upstream of
	# this step (#625) — the pin flows through the marketplace, not per-plugin.
	if [ "$CLAUDE_MARKETPLACE_READY" -eq 1 ]; then
		for p in $SELECTED; do
			if plugin_installed "$p"; then
				old="$(installed_plugin_version claude "$p")"
				if plugin_cmd update "$p"; then
					ok_updated "Claude" "$p" "$old" "$(installed_plugin_version claude "$p")"
				else
					fail "Claude: $p failed to update. Run manually: claude plugin update $p@$MARKETPLACE_NAME"
					INSTALL_FAILED=1
				fi
			else
				state=$?
				if [ "$state" -eq 2 ]; then
					fail "Claude: could not query installed plugins"
					INSTALL_FAILED=1
					break
				elif plugin_cmd install "$p"; then
					ok "Claude: $p installed"
				else
					fail "Claude: $p failed to install. Run manually: claude plugin install $p@$MARKETPLACE_NAME"
					INSTALL_FAILED=1
				fi
			fi
		done
	fi

	if [ "$CODEX_MARKETPLACE_READY" -eq 1 ]; then
		for p in $SELECTED; do
			if codex_plugin_installed "$p"; then
				old="$(installed_plugin_version codex "$p")"
				if codex plugin add "$p@$MARKETPLACE_NAME" >/dev/null 2>&1; then
					ok_updated "Codex" "$p" "$old" "$(installed_plugin_version codex "$p")"
				else
					fail "Codex: $p failed to update. Run manually: codex plugin add $p@$MARKETPLACE_NAME"
					INSTALL_FAILED=1
				fi
			else
				state=$?
				if [ "$state" -eq 2 ]; then
					fail "Codex: could not query installed plugins"
					INSTALL_FAILED=1
					break
				elif codex plugin add "$p@$MARKETPLACE_NAME" >/dev/null 2>&1; then
					ok "Codex: $p installed"
				else
					fail "Codex: $p failed to install. Run manually: codex plugin add $p@$MARKETPLACE_NAME"
					INSTALL_FAILED=1
				fi
			fi
		done
	fi
}

# Post-install steps must only run for plugins that actually got installed —
# the marketplace checkout contains the whole repo, so path lookups alone
# would succeed even for plugins the user skipped.
prune_selected_to_installed() {
	local kept="" p
	for p in $SELECTED; do
		if { [ "$HAS_CLAUDE" -eq 1 ] && plugin_installed "$p"; } ||
			{ [ "$HAS_CODEX" -eq 1 ] && codex_plugin_installed "$p"; }; then
			kept="$kept $p"
		fi
	done
	SELECTED="${kept# }"
}

# link_launcher <name> <target> — symlink into ~/.local/bin without clobbering
# a real file the user put there.
link_launcher() {
	local name="$1" target="$2" dest="$HOME/.local/bin/$1"
	if ! mkdir -p "$HOME/.local/bin"; then
		fail "could not create $HOME/.local/bin for $name"
		return 1
	fi
	if [ -e "$dest" ] && [ ! -L "$dest" ]; then
		warn "$dest exists and is not a symlink — left untouched"
		return 1
	fi
	if ! ln -sf "$target" "$dest"; then
		fail "could not link $name at $dest"
		return 1
	fi
	ok "linked $name → ~/.local/bin/$name"
}

step_cli_setup() {
	step "Setting up the cenci installer command"

	local cli
	if ! cli=$(find_plugin_path "cenci"); then
		fail "could not find the cenci installer command in the marketplace checkout — re-run the installer after refreshing the marketplace"
		INSTALL_FAILED=1
		return 0
	fi
	if ! link_launcher cenci-installer "$cli"; then
		INSTALL_FAILED=1
	fi
}

step_sandbox_setup() {
	selected cenci-sandbox || return 0
	step "Setting up the sandbox launcher"

	# The launcher is the cenci binary itself (cenci open / cenci sandbox).
	# `cn` is its one alias: a symlink chained through the bootstrap-maintained
	# ~/.local/bin/cenci link, so it keeps resolving across version bumps (and
	# becomes valid on the first agent session even if created dangling here).
	if ! link_launcher cn "$HOME/.local/bin/cenci"; then
		INSTALL_FAILED=1
	fi

	# The cenci-sand bash launcher is gone. Never create it — but a stale link
	# from a previous install is repointed at the cenci binary, whose argv[0]
	# tombstone fails loudly with a migration map instead of leaving old
	# scripts and docs with a dangling command.
	if [ -L "$HOME/.local/bin/cenci-sand" ]; then
		ln -sf "$HOME/.local/bin/cenci" "$HOME/.local/bin/cenci-sand"
		warn "cenci-sand is deprecated — repointed the stale ~/.local/bin/cenci-sand link at cenci (it now prints a migration map); use 'cn' / 'cenci open' instead"
	fi

	case ":$PATH:" in
	*":$HOME/.local/bin:"*) ;;
	*) warn "$HOME/.local/bin is not on your PATH — add it to your shell profile:
      export PATH=\"\$HOME/.local/bin:\$PATH\"" ;;
	esac

	local runtime
	if ! runtime=$(container_runtime); then
		if [ "$OS" = macos ]; then
			fail "no container runtime found — install Docker Desktop (https://docker.com/products/docker-desktop) or Podman, then run: cenci sandbox build"
		else
			fail "no container runtime found — install docker or podman, then run: cenci sandbox build"
		fi
		INSTALL_FAILED=1
		return 0
	fi

	if [ "$BUILD_IMAGE" = no ]; then
		say "  ${DIM}skipping image build — run 'cenci sandbox build' when ready${RESET}"
		return 0
	fi

	# The build runs through the cenci binary (it resolves the image assets
	# from the installed cenci-sandbox plugin). Provision it from the plugin
	# cache if the first agent session hasn't bootstrapped it yet. Resolved
	# here — before the BUILD_IMAGE=ask branch below — so that branch can
	# consult `sandbox build --check` and skip the prompt entirely when the
	# image is already current (#519). A resolution failure here is not
	# fatal: the ask branch below falls back to always asking, and the
	# post-prompt binary check further down still reports it clearly.
	local cenci_bin
	cenci_bin="$(current_cenci_binary || true)"

	if [ "$BUILD_IMAGE" = ask ]; then
		if [ -n "$cenci_bin" ] && "$cenci_bin" sandbox build --check >/dev/null 2>&1; then
			say "  ${DIM}sandbox image already up to date — skipping${RESET}"
			return 0
		fi
		if ! ask_yn "Build the sandbox container image now with $runtime? (takes a few minutes)" y; then
			say "  ${DIM}skipped — run 'cenci sandbox build' when ready${RESET}"
			return 0
		fi
	fi

	if [ -z "$cenci_bin" ]; then
		warn "cenci binary not available yet — build the image later with: cenci sandbox build"
		return 0
	fi
	say "  building cenci-sandbox:latest with $runtime (this can take a few minutes)…"
	if "$cenci_bin" sandbox build; then
		ok "sandbox image built"
	else
		fail "image build failed — fix the error above and re-run: cenci sandbox build"
		INSTALL_FAILED=1
	fi
}

# step_sandbox_refresh_plugins refreshes plugins in every currently running
# sandbox container after an update (#461), so `cenci update` doesn't leave
# already-running sandboxes on a stale plugin cache until a manual
# `cenci sandbox update-plugins`. Kept as its own function rather than folded
# into step_sandbox_setup, because that function early-returns before this
# point whenever there's no container runtime or BUILD_IMAGE=no — both cases
# that must still let this refresh attempt to run. Best-effort: a failure
# only warns (never sets INSTALL_FAILED) — RefreshRunningPlugins is itself
# best-effort per-container, and an older cached binary might not understand
# --all yet.
step_sandbox_refresh_plugins() {
	selected cenci-sandbox || return 0
	local runtime
	runtime="$(container_runtime || true)"
	[ -n "$runtime" ] || return 0

	local cenci_bin
	cenci_bin="$(current_cenci_binary || true)"
	if [ -z "$cenci_bin" ]; then
		warn "cenci binary not available — skipping running-sandbox plugin refresh; run manually with: cenci sandbox update-plugins --all"
		return 0
	fi

	step "Refreshing plugins in running sandbox containers"
	if "$cenci_bin" sandbox update-plugins --all; then
		ok "refreshed plugins in running sandbox containers"
	else
		warn "'$cenci_bin sandbox update-plugins --all' did not succeed — refresh running sandboxes manually with: cenci sandbox update-plugins --all"
	fi
}

# newest_cenci_root — path to the most recently installed version-pinned
# Claude plugin cache root. Plugin updates refresh the active manifest, making
# it a reliable selector even before that version's binary has bootstrapped.
newest_cenci_root() {
	local newest_manifest="" manifest
	for manifest in \
		"$HOME"/.claude/plugins/cache/*/cenci-watch/*/.claude-plugin/plugin.json \
		"$HOME"/.codex/plugins/cache/*/cenci-watch/*/.codex-plugin/plugin.json; do
		[ -f "$manifest" ] || continue
		if [ -z "$newest_manifest" ] || [ "$manifest" -nt "$newest_manifest" ]; then
			newest_manifest="$manifest"
		fi
	done
	[ -n "$newest_manifest" ] && dirname "$(dirname "$newest_manifest")"
}

# newest_provisioned_cenci_binary — path to the executable cenci binary from the
# most recently installed version-pinned cache root that has ALREADY bootstrapped
# one. Unlike current_cenci_binary it never triggers a download; it only reports
# binaries already present on disk. Used as an update-mode fallback when the
# newest plugin version's release isn't published yet (the manifest bump reaches
# the plugin cache before the release assets exist, so its bootstrap download
# 404s) — a routine manifest-ahead-of-release race must not turn `cenci update`
# into a hard failure while a previous version's binary, and the daemon running
# on it, remain perfectly usable. Mirrors newest_cenci_root's glob.
newest_provisioned_cenci_binary() {
	local newest_manifest="" newest_bin="" manifest root bin
	for manifest in \
		"$HOME"/.claude/plugins/cache/*/cenci-watch/*/.claude-plugin/plugin.json \
		"$HOME"/.codex/plugins/cache/*/cenci-watch/*/.codex-plugin/plugin.json; do
		[ -f "$manifest" ] || continue
		root="$(dirname "$(dirname "$manifest")")"
		bin="$root/bin/cenci"
		[ -x "$bin" ] || continue
		if [ -z "$newest_manifest" ] || [ "$manifest" -nt "$newest_manifest" ]; then
			newest_manifest="$manifest"
			newest_bin="$bin"
		fi
	done
	[ -n "$newest_bin" ] && printf '%s\n' "$newest_bin"
}

# cached_cenci_binary returns the binary belonging to the active plugin
# cache when SessionStart has already provisioned it.
cached_cenci_binary() {
	local root bin
	root="$(newest_cenci_root || true)"
	[ -n "$root" ] || return 1
	bin="$root/bin/cenci"
	[ -x "$bin" ] || return 1
	printf '%s\n' "$bin"
}

# current_cenci_binary provisions and returns the binary belonging to the
# active plugin cache. Updates cannot rely on a later SessionStart hook: an old
# daemon may continue owning the sockets indefinitely.
current_cenci_binary() {
	local root bootstrap root_var
	if cached_cenci_binary; then
		return 0
	fi
	root="$(newest_cenci_root || true)"
	[ -n "$root" ] || return 1
	if [ -f "$root/.codex-plugin/plugin.json" ]; then
		bootstrap="$root/codex/bootstrap.sh"
		root_var=PLUGIN_ROOT
	else
		bootstrap="$root/hooks/bootstrap.sh"
		root_var=CLAUDE_PLUGIN_ROOT
	fi
	if [ -f "$bootstrap" ]; then
		if [ "$root_var" = PLUGIN_ROOT ]; then
			PLUGIN_ROOT="$root" bash "$bootstrap" >/dev/null || return 1
		else
			CLAUDE_PLUGIN_ROOT="$root" bash "$bootstrap" >/dev/null || return 1
		fi
	else
		return 1
	fi
	cached_cenci_binary
}

# restart_cenci_daemon replaces the standard plugin-managed daemon after
# an explicit update. It delegates to the binary's own `daemon restart`
# lifecycle verb (stop the old daemon, spawn the new one, wait for it to
# become reachable — see runDaemonRestart in watch/main.go), so this
# installer path and a user running `cenci daemon restart` by hand share
# one implementation instead of install.sh reimplementing daemon process
# control. Falls back to the pre-daemon-lifecycle ad-hoc pkill/nohup restart
# when the binary can't do it itself (missing/not executable, older cached
# binary without the `daemon` verb group, or the invocation otherwise fails).
restart_cenci_daemon() {
	local bin="$1"
	if [ -x "$bin" ] && "$bin" daemon restart >/dev/null 2>&1; then
		ok "restarted cenci with the updated binary"
		return 0
	fi
	warn "'$bin daemon restart' did not succeed — falling back to a manual restart"
	restart_cenci_daemon_fallback "$bin"
}

# restart_cenci_daemon_fallback is the pre-daemon-lifecycle ad-hoc
# restart: SIGTERM the old daemon, wait for it to exit, then nohup-spawn the
# given binary directly. The pgrep/pkill pattern matches both the bare
# `cenci daemon` form (hand-started in a pane) and `cenci daemon
# start` (the form Spawn/`daemon restart` itself use), mirroring
# daemonProcessPattern in watch/internal/daemon/processctl.go.
restart_cenci_daemon_fallback() {
	local bin="$1" i pid
	if ! have pkill || ! have pgrep; then
		fail "pkill/pgrep unavailable — restart cenci manually to finish the update"
		return 1
	fi

	pkill -TERM -f '[/]cenci daemon( start)?$' >/dev/null 2>&1 || true
	i=0
	while pgrep -f '[/]cenci daemon( start)?$' >/dev/null 2>&1 && [ "$i" -lt 30 ]; do
		sleep 0.1
		i=$((i + 1))
	done
	if pgrep -f '[/]cenci daemon( start)?$' >/dev/null 2>&1; then
		fail "the previous cenci daemon did not stop; restart it manually"
		return 1
	fi

	if [ -z "$bin" ] || [ ! -x "$bin" ]; then
		warn "no usable cenci binary to restart — the next agent session will bootstrap it"
		return 1
	fi

	nohup "$bin" daemon >/dev/null 2>&1 &
	pid=$!
	sleep 0.2
	if kill -0 "$pid" 2>/dev/null; then
		ok "restarted cenci with the updated binary"
	else
		fail "the updated cenci daemon did not stay running"
		return 1
	fi
}

step_cenci_watch_setup() {
	selected cenci-watch || return 0
	step "Setting up cenci"
	local cache_bin=""
	cache_bin="$(current_cenci_binary || true)"
	if [ -z "$cache_bin" ]; then
		if [ "$MODE" = update ]; then
			# The newest plugin version's binary couldn't be provisioned — most
			# often because its release isn't published yet (the manifest bump
			# reaches the plugin cache before the release assets exist, so the
			# bootstrap download 404s). Fall back to the newest previously
			# provisioned binary if one exists: the daemon keeps running on it
			# and the update completes as a deferred no-op for the binary rather
			# than a hard failure.
			cache_bin="$(newest_provisioned_cenci_binary || true)"
			if [ -z "$cache_bin" ]; then
				fail "could not provision the cenci binary from the installed cenci-watch plugin"
				INSTALL_FAILED=1
				return 0
			fi
			warn "the newest cenci-watch release isn't available yet — keeping the currently provisioned binary; it updates once the release publishes"
			say "  ${DIM}log: \${TMPDIR:-/tmp}/cenci-bootstrap.log${RESET}"
		else
			# A fresh install stays useful without the binary: widgets come from
			# the checkout, and the first agent session bootstraps and links the
			# binary itself.
			warn "could not provision the cenci binary yet — the first agent session bootstraps it"
			say "  ${DIM}this can happen offline, or before release assets or the plugin cache are populated; log: \${TMPDIR:-/tmp}/cenci-bootstrap.log${RESET}"
		fi
	else
		ok "the cenci binary is provisioned — the daemon self-manages from here"
		say "  ${DIM}first session may take a moment before status appears; log: \${TMPDIR:-/tmp}/cenci-bootstrap.log${RESET}"
	fi

	if [ "$OS" != macos ]; then
		if [ -n "$cache_bin" ]; then
			setup_cenci_linux_path "$cache_bin"
		fi
		setup_cenci_linux_widgets
		if [ "$MODE" = update ] && ! restart_cenci_daemon "$cache_bin"; then
			INSTALL_FAILED=1
		fi
		return 0
	fi
	if [ "$MODE" = update ] && ! restart_cenci_daemon "$cache_bin"; then
		INSTALL_FAILED=1
	fi

	# macOS menu bar (SwiftBar) — optional, and the fiddliest manual step, so
	# offer to wire it up here. Delegate to the widget's self-contained
	# install.sh, which sets SwiftBar's Plugin Folder, symlinks the plugin, and
	# reloads SwiftBar. Re-runs on update so widget changes take effect.
	local script
	if ! script=$(find_plugin_path "watch/plugin/macos/install.sh"); then
		return 0
	fi
	if [ ! -d /Applications/SwiftBar.app ]; then
		say "  ${DIM}optional: menu bar status via SwiftBar — brew install swiftbar, then re-run this script${RESET}"
		return 0
	fi
	if ! ask_yn "SwiftBar detected — install the cenci menu bar widget and reload it?" y; then
		say "  ${DIM}skipped — see watch/plugin/macos/README.md to wire it manually${RESET}"
		return 0
	fi
	chmod +x "$script" 2>/dev/null || true
	if bash "$script"; then
		ok "menu bar widget installed and reloaded"
	else
		warn "SwiftBar widget setup failed — see watch/plugin/macos/README.md"
		INSTALL_FAILED=1
	fi
}

# step_opencode_setup is OpenCode's opt-in counterpart to the SwiftBar block
# above: an already-detected optional integration, offered here rather than
# gating install on it (OpenCode-only hosts stay unsupported — see
# have_supported_client). Guards on HAS_OPENCODE plus every asset it needs
# being resolvable from the marketplace checkout, mirroring the SwiftBar
# script's own silent-skip-if-unresolvable shape. Also gates on `selected
# cenci-watch` like every other opt-in step below (step_cenci_watch_setup,
# etc.): the plugin it registers belongs to cenci-watch, so a user who
# deselected it (SELECTED pruned by prune_selected_to_installed) must not
# have it silently registered anyway. Idempotent and safe to re-run:
# flow/opencode/install-skills.sh only links what's missing, and
# seed_opencode_config only seeds what's absent and dedups the plugin entry —
# never reimplemented here, just called.
step_opencode_setup() {
	[ "$HAS_OPENCODE" -eq 1 ] || return 0
	selected cenci-watch || return 0
	local skills_script watch_dir config_lib plugin_root opencode_json

	skills_script=$(find_plugin_path "flow/opencode/install-skills.sh") || return 0
	watch_dir=$(find_plugin_path "watch/plugin/opencode") || return 0
	config_lib=$(find_plugin_path "sandbox/lib/opencode-config.sh") || return 0

	step "Setting up OpenCode integration"
	if ! ask_yn "OpenCode detected — link cenci's skills and register its plugin?" y; then
		say "  ${DIM}skipped — re-run the installer to set it up${RESET}"
		return 0
	fi

	plugin_root="$(dirname "$(dirname "$skills_script")")"
	chmod +x "$skills_script" 2>/dev/null || true
	if ! PLUGIN_ROOT="$plugin_root" bash "$skills_script" install; then
		warn "OpenCode skill linking failed — see flow/opencode/install-skills.sh"
		INSTALL_FAILED=1
	fi

	# shellcheck source=/dev/null
	source "$config_lib"
	opencode_json="$(opencode_config_dir)/opencode.json"
	seed_opencode_config "$opencode_json" "file://$watch_dir" || true
	# seed_opencode_config can return 0 without actually registering anything
	# (e.g. an existing opencode.json that isn't valid JSON is left
	# untouched) — verify the entry actually landed rather than trusting its
	# exit code alone, so a silent no-op doesn't get reported as success.
	if opencode_plugin_registered; then
		ok "OpenCode plugin registered ($opencode_json)"
	else
		warn "OpenCode plugin registration may not have applied — check $opencode_json manually"
		INSTALL_FAILED=1
	fi
}

# setup_cenci_linux_path makes bar widgets (DMS, noctalia, waybar) able to
# resolve a bare `cenci`. The binary lives in the version-pinned plugin
# cache (~/.claude/plugins/cache/.../cenci-watch/<version>/bin), which is on no
# login PATH — so a widget spawned by the compositor can't find it and hides
# itself. We keep a stable link on the user's writable PATH and, for GUI bars
# that don't inherit ~/.local/bin, offer a one-time /usr/local/bin link.
setup_cenci_linux_path() {
	local cache_bin="$1" user_link="$HOME/.local/bin/cenci"

	# Ensure the bootstrap-maintained user link exists now. The plugin bootstrap
	# re-points it on version bumps, so pinning the current cache path is fine;
	# if the binary isn't cached yet, the first agent session creates the link.
	if [ -n "$cache_bin" ]; then
		if ! link_launcher cenci "$cache_bin"; then
			INSTALL_FAILED=1
		fi
	else
		fail "cenci binary not available after plugin bootstrap"
		INSTALL_FAILED=1
	fi

	case ":$PATH:" in
	*":$HOME/.local/bin:"*) ;;
	*) warn "$HOME/.local/bin is not on your PATH — add it to your shell profile:
      export PATH=\"\$HOME/.local/bin:\$PATH\"" ;;
	esac

	# GUI/compositor bars inherit the login PATH, which usually lacks
	# ~/.local/bin but always includes /usr/local/bin. A root link there, chained
	# through the bootstrap-maintained ~/.local/bin link, lets them resolve
	# cenci and survives version bumps with no re-run.
	if [ -L /usr/local/bin/cenci ] &&
		[ "$(readlink /usr/local/bin/cenci 2>/dev/null)" = "$user_link" ]; then
		ok "/usr/local/bin/cenci already links to ~/.local/bin/cenci (GUI bars can resolve it)"
		return 0
	fi
	if [ -e /usr/local/bin/cenci ] && [ ! -L /usr/local/bin/cenci ]; then
		warn "/usr/local/bin/cenci exists and is not a symlink — left untouched; remove it manually or configure the widget's cenci path"
		return 0
	fi

	local manual="sudo ln -sf \"$user_link\" /usr/local/bin/cenci"
	if [ "$INTERACTIVE" -eq 1 ] &&
		ask_yn "Link cenci into /usr/local/bin so GUI bar widgets (DMS, noctalia) can find it? (one-time sudo)" y; then
		if sudo ln -sf "$user_link" /usr/local/bin/cenci; then
			ok "linked /usr/local/bin/cenci → ~/.local/bin/cenci"
		else
			warn "could not create the /usr/local/bin link — run it yourself:
      $manual"
		fi
	else
		say "  ${DIM}skipped the GUI-bar PATH link. If a bar widget stays hidden, run:${RESET}"
		say "      $manual"
		say "  ${DIM}or point the widget at the binary directly (cenciPath for DMS/noctalia, CENCI_BIN for SwiftBar).${RESET}"
	fi
}

# de_detected <de> — true if the desktop bar for <de> is present. Mirrors the
# self-detection each widget's install.sh does, so we only prompt for bars that
# are actually installed (keeps CI, where none are present, non-interactive).
de_detected() {
	case "$1" in
	gnome) have gnome-shell || have gnome-extensions ;;
	plasma) have plasmashell ;;
	dms) have dms || [ -d "$HOME/.config/DankMaterialShell" ] ;;
	noctalia) have noctalia-shell || [ -d "$HOME/.config/noctalia" ] ;;
	*) return 1 ;;
	esac
}

de_label() {
	case "$1" in
	gnome) printf 'GNOME Shell' ;;
	plasma) printf 'KDE Plasma' ;;
	dms) printf 'DankMaterialShell' ;;
	noctalia) printf 'noctalia-shell' ;;
	esac
}

# setup_cenci_linux_widgets detects each present GUI bar and delegates to
# that widget's self-contained install.sh, which (re)installs and reloads it.
# Runs on both install and update — re-running is what refreshes the widget and
# reloads the bar, so widget changes become visible after `cenci update`.
# Restarting a running panel is disruptive, so each bar is gated behind its own
# prompt (default yes).
setup_cenci_linux_widgets() {
	local de label script
	for de in gnome plasma dms noctalia; do
		de_detected "$de" || continue
		label="$(de_label "$de")"
		if ! ask_yn "$label detected — install the Cenci Watch widget and reload it?" y; then
			say "  ${DIM}skipped the $label widget — see watch/plugin/$de/README.md to wire it manually${RESET}"
			continue
		fi
		if ! script=$(find_plugin_path "watch/plugin/$de/install.sh"); then
			warn "could not find watch/plugin/$de/install.sh in the marketplace checkout — re-run after refreshing the marketplace"
			INSTALL_FAILED=1
			continue
		fi
		chmod +x "$script" 2>/dev/null || true
		if bash "$script"; then
			ok "$label widget installed and reloaded"
		else
			warn "$label widget setup failed — see watch/plugin/$de/README.md"
			INSTALL_FAILED=1
		fi
	done

	# waybar has no bundled widget — its config is hand-managed. Point at the
	# docs and the live-reload signal; write nothing.
	if have waybar; then
		say "  ${DIM}waybar detected — add the Cenci Watch module from watch/README.md (Waybar section),${RESET}"
		say "  ${DIM}then reload waybar to apply: pkill -SIGUSR2 waybar${RESET}"
	fi
}

# teardown_cenci_linux_widgets is setup_cenci_linux_widgets' uninstall-side
# sibling: detects each present GUI bar and delegates to that widget's
# self-contained uninstall.sh, which removes the widget and reloads the bar.
# Runs under run_uninstall's single confirmation gate (#457), so — unlike
# install — there is no per-widget prompt (confirmed Q1). A missing/undetected
# bar is a silent no-op; only a genuine removal failure warns, so one bar's
# failure never aborts the rest of the purge.
teardown_cenci_linux_widgets() {
	local de label script
	for de in gnome plasma dms noctalia; do
		de_detected "$de" || continue
		label="$(de_label "$de")"
		if ! script=$(find_plugin_path "watch/plugin/$de/uninstall.sh"); then
			warn "could not find watch/plugin/$de/uninstall.sh in the marketplace checkout — remove the $label widget manually"
			continue
		fi
		chmod +x "$script" 2>/dev/null || true
		if bash "$script"; then
			ok "$label widget removed and bar reloaded"
		elif [ "$?" -eq 2 ]; then
			warn "$label widget left untouched — a non-cenci file/dir is present at its plugin path"
		else
			warn "$label widget teardown failed — see watch/plugin/$de/README.md"
		fi
	done

	# waybar has no bundled widget, so there is nothing to remove — just point
	# at manual removal and the live-reload signal.
	if have waybar; then
		say "  ${DIM}waybar detected — remove the Cenci Watch module from your waybar config manually,${RESET}"
		say "  ${DIM}then reload waybar to apply: pkill -SIGUSR2 waybar${RESET}"
	fi
}

# ------------------------------------------------------------- lazyboards ----

# lazyboards is the optional board-orchestration layer
# (https://github.com/matteobortolazzo/lazyboards) — a separate project
# co-developed with cenci that dispatches the workflow from a kanban board via
# `cenci run`, labels, and the watch daemon (see docs/orchestration.md).
# The installer can install and update it, but never by default: install is
# opt-in (prompt or --lazyboards), update only refreshes an existing install.

lazyboards_managed_binary() {
	[ -x "$HOME/.local/bin/lazyboards" ] && [ ! -L "$HOME/.local/bin/lazyboards" ] || return 1
	printf '%s\n' "$HOME/.local/bin/lazyboards"
}

lazyboards_path_binary() {
	have lazyboards || return 1
	command -v lazyboards
}

lazyboards_binary() {
	lazyboards_managed_binary || lazyboards_path_binary
}

# lazyboards_latest_version resolves the newest release by following the
# GitHub releases/latest redirect (no API token, no jq) and prints the bare
# semver. The value is validated strictly before use — it flows into a
# download URL and an archive filename, so a malformed redirect must never be
# trusted.
lazyboards_latest_version() {
	local url ver
	url="$(curl -fsSLI -o /dev/null -w '%{url_effective}' "https://github.com/${LAZYBOARDS_REPO}/releases/latest" 2>/dev/null)" || return 1
	ver="${url##*/}"
	ver="${ver#v}"
	printf '%s' "$ver" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+$' || return 1
	printf '%s\n' "$ver"
}

# cenci_latest_ref resolves the newest release tag across all three cenci
# plugins by following the same GitHub releases/latest redirect (no API
# token, no jq) as lazyboards_latest_version above. Cenci tags are
# two-segment and per-plugin (`<layer>/vX.Y.Z`, e.g. `watch/v0.19.0`) rather
# than lazyboards' single-segment semver, so the tail is stripped up to
# `/releases/tag/` instead of lazyboards' `${url##*/}`. The result is
# strictly whole-string validated before use with bash `[[ =~ ]]` (not
# line-oriented grep) — it flows straight into a GitHub release asset
# download URL (releases/download/<tag>/...), so a malformed or injected
# redirect must never be trusted. This is the supply-chain trust boundary
# the immutable-tag default exists to enforce.
cenci_latest_ref() {
	local url tag
	url="$(curl -fsSLI -o /dev/null -w '%{url_effective}' "https://github.com/${MARKETPLACE_REPO}/releases/latest" 2>/dev/null)" || return 1
	tag="${url#*/releases/tag/}"
	[[ "$tag" =~ ^(flow|watch|sandbox)/v[0-9]+\.[0-9]+\.[0-9]+$ ]] || return 1
	printf '%s\n' "$tag"
}

# verify_installer_asset <file> <bundle> — verifies <file> (a downloaded
# install.sh) against <bundle> (its cosign sign-blob bundle) using the
# canonical, tag-refs-only release identity (#626). Requires cosign on
# PATH — cosign is a new mandatory prerequisite for the default verified
# install path — and dies with a distinct message for each failure class:
# cosign missing, or cosign verify-blob rejecting the bundle/file pair
# (corrupt/truncated/tampered download, signature mismatch, or any other
# verify-blob rejection). Never returns on failure; the caller must not
# exec the file unless this function returns 0. Both failure paths remove
# <file>/<bundle> before calling die() — the caller's staged temp files —
# so this function's failures clean up exactly like the pin block's other
# checks (which all go through its pin_die wrapper).
verify_installer_asset() {
	local file="$1" bundle="$2"
	if ! command -v cosign >/dev/null 2>&1; then
		rm -f "$file" "$bundle"
		die "cosign not found — required to verify the installer before running it; install cosign (https://docs.sigstore.dev/system_config/installation/) and retry, or set CENCI_REF=main (or pass --ref main) to install from main instead (unverified)"
	fi
	cosign verify-blob \
		--bundle "$bundle" \
		--certificate-identity-regexp '^https://github\.com/matteobortolazzo/cenci/\.github/workflows/watch-release\.yml@refs/tags/watch/v' \
		--certificate-oidc-issuer 'https://token.actions.githubusercontent.com' \
		"$file" >/dev/null 2>&1 || {
		rm -f "$file" "$bundle"
		die "installer verification failed for the downloaded install.sh — cosign could not verify it against the pinned release identity (tampered, truncated, or mismatched signature); retry, or set CENCI_REF=main (or pass --ref main) to install from main instead (unverified)"
	}
}

# resolve_install_ref prints the ref that must govern this run's marketplace
# registration (and, through it, every installed plugin's content): the
# caller-selected CENCI_REF when set (env, --ref, or a piped re-exec's own
# CENCI_REF="$pin_tag" — see the immutable-release-pin block below), else the
# newest release tag via cenci_latest_ref. Applies uniformly to every
# dispatch path (install, `cenci-installer update`, repair, dev-from-clone),
# not just the piped re-exec (#625). Capture-only: prints exactly the ref and
# nothing else — no warn/echo leakage — so `ref="$(resolve_install_ref)"`
# callers never see a diagnostic swallowed into the captured value.
resolve_install_ref() {
	if [ -n "$CENCI_REF" ]; then
		printf '%s\n' "$CENCI_REF"
		return 0
	fi
	cenci_latest_ref
}

# validate_ref_format <ref> — whole-string whitelist ([A-Za-z0-9._/-]) via
# bash `[[ =~ ]]` (never line-oriented grep — a multi-line value with a
# matching first line must not slip through). The ref flows straight into a
# marketplace-registration URL/argv for both clients, so anything outside
# this whitelist (spaces, quotes, shell metacharacters) is rejected before
# it's ever interpolated. Also rejects a `..` path segment (which could let
# the ref_available manifest probe resolve a different ref's content than
# the one actually registered with the client CLI) and a leading `-` (which
# risks Codex's `--ref <value>` invocation being misparsed as another flag
# by some arg parsers) — both still empty-string-safe, and neither weakens
# the whitelist's acceptance of slash-containing tags like `watch/v1.2.3`.
validate_ref_format() {
	case "$1" in
	'' | -* | *..*) return 1 ;;
	esac
	[[ "$1" =~ ^[A-Za-z0-9._/-]+$ ]]
}

# ref_available <ref> — probes whether <ref> actually has published
# marketplace content by requesting the repo-root manifest
# (`.claude-plugin/marketplace.json`) both clients' registration ultimately
# reads from. A syntactically valid but unpublished ref (typo'd tag, deleted
# branch, or a release published before its plugin content) must fail here
# rather than silently registering a marketplace with nothing to install.
ref_available() {
	curl -fsSL -o /dev/null "https://raw.githubusercontent.com/${MARKETPLACE_REPO}/$1/.claude-plugin/marketplace.json" 2>/dev/null
}

# lazyboards_installed_version prints the bare semver of the installed binary
# (`lazyboards --version` prints "lazyboards <version>", where a go-install
# build may carry a leading v).
lazyboards_installed_version() {
	local bin
	bin="$(lazyboards_managed_binary)" || return 1
	run_bounded 5 "$bin" --version 2>/dev/null | sed -n '1s/^lazyboards v\{0,1\}//p'
}

# install_lazyboards_binary <version> — download the GoReleaser archive for
# this platform, verify its sha256 against the release's checksums.txt, and
# install the binary at ~/.local/bin/lazyboards.
install_lazyboards_binary() {
	local ver="$1" goos sumtool archive url_base tmp sum dest_dir dest staged staged_ver
	case "$OS" in
	macos) goos=darwin ;;
	*) goos=linux ;;
	esac
	if have sha256sum; then
		sumtool="sha256sum"
	elif have shasum; then
		sumtool="shasum -a 256"
	else
		fail "lazyboards: neither sha256sum nor shasum is available to verify the download"
		return 1
	fi
	archive="lazyboards_${ver}_${goos}_${ARCH}.tar.gz"
	url_base="https://github.com/${LAZYBOARDS_REPO}/releases/download/v${ver}"
	# Verify temp-dir creation before use — an unchecked failure would let the
	# paths below collapse to the current directory.
	tmp="$(mktemp -d)" || tmp=""
	if [ -z "$tmp" ] || [ ! -d "$tmp" ]; then
		fail "lazyboards: could not create a temporary download directory"
		return 1
	fi
	if ! curl -fsSL -o "$tmp/$archive" "$url_base/$archive" ||
		! curl -fsSL -o "$tmp/checksums.txt" "$url_base/checksums.txt"; then
		fail "lazyboards: download failed ($url_base/$archive)"
		rm -rf "$tmp"
		return 1
	fi
	sum="$($sumtool "$tmp/$archive" | cut -d' ' -f1)" || sum=""
	if [ -z "$sum" ] || ! grep -q "^${sum}  ${archive}\$" "$tmp/checksums.txt"; then
		fail "lazyboards: checksum verification failed for $archive"
		rm -rf "$tmp"
		return 1
	fi
	if ! tar -xzf "$tmp/$archive" -C "$tmp" lazyboards; then
		fail "lazyboards: could not extract $archive"
		rm -rf "$tmp"
		return 1
	fi
	dest_dir="$HOME/.local/bin"
	dest="$dest_dir/lazyboards"
	if [ -L "$dest" ] || { [ -e "$dest" ] && [ ! -f "$dest" ]; }; then
		fail "lazyboards: $dest exists with an unsupported file type — left untouched"
		rm -rf "$tmp"
		return 1
	fi
	if ! mkdir -p "$dest_dir"; then
		fail "lazyboards: could not create $dest_dir"
		rm -rf "$tmp"
		return 1
	fi
	staged="$(mktemp "$dest_dir/.lazyboards.XXXXXX")" || staged=""
	if [ -z "$staged" ] || [ ! -f "$staged" ]; then
		fail "lazyboards: could not create a staged binary in $dest_dir"
		rm -rf "$tmp"
		return 1
	fi
	if ! cp "$tmp/lazyboards" "$staged" || ! chmod 755 "$staged"; then
		fail "lazyboards: could not stage the verified binary"
		rm -f "$staged"
		rm -rf "$tmp"
		return 1
	fi
	staged_ver="$(run_bounded 5 "$staged" --version 2>/dev/null | sed -n '1s/^lazyboards v\{0,1\}//p')"
	if [ "$staged_ver" != "$ver" ]; then
		fail "lazyboards: staged binary reports '${staged_ver:-no version}', expected $ver"
		rm -f "$staged"
		rm -rf "$tmp"
		return 1
	fi
	if ! mv -f "$staged" "$dest"; then
		fail "lazyboards: could not atomically install to $dest"
		rm -f "$staged"
		rm -rf "$tmp"
		return 1
	fi
	rm -rf "$tmp"
	ok "lazyboards v${ver} installed → ~/.local/bin/lazyboards"
}

verify_lazyboards_resolution() {
	local managed resolved
	managed="$(lazyboards_managed_binary)" || return 1
	resolved="$(lazyboards_path_binary || true)"
	if [ -z "$resolved" ]; then
		fail "lazyboards installed at $managed, but ~/.local/bin is not on PATH"
		return 1
	fi
	if [ "$resolved" != "$managed" ]; then
		fail "$resolved shadows the managed lazyboards at $managed — remove the older copy or put ~/.local/bin first on PATH"
		return 1
	fi
	ok "lazyboards command resolves to the managed binary"
}

# disable_cleanup_line comments out the board config's `cleanup:` action in
# place, so the seeded file keeps the setting visible (and trivially
# re-enabled) instead of silently dropping it. It fails loud rather than
# silently leaving auto-close on: it verifies the substitution actually fired,
# guarding against a future template whose cleanup line no longer matches.
disable_cleanup_line() {
	local cfg="$1" tmp
	tmp="$(mktemp)" || return 1
	if ! sed 's|^cleanup:.*|# &  # auto-close disabled at install; uncomment to enable|' "$cfg" >"$tmp"; then
		rm -f "$tmp"
		return 1
	fi
	if ! grep -q '^# cleanup:' "$tmp"; then
		rm -f "$tmp"
		return 1
	fi
	mv "$tmp" "$cfg"
}

# seed_lazyboards_config copies the packaged default board config (columns
# wired to the cenci workflow — see docs/orchestration.md) into
# ~/.config/lazyboards/config.yml, only when no config exists yet. An existing
# config is never merged into or overwritten: lazyboards has its own config
# UI, and YAML has no managed-marker regeneration story.
#
# On a fresh seed it resolves the optional auto-close action: an interactive
# run asks (default on); --no-cleanup / --cleanup force it; and a
# non-interactive or --yes run keeps the template default (on) so the seeded
# file matches flow/templates/lazyboards-config.yml byte-for-byte.
seed_lazyboards_config() {
	local cfg_dir cfg template cleanup="$CLEANUP"
	cfg_dir="${XDG_CONFIG_HOME:-$HOME/.config}/lazyboards"
	cfg="$cfg_dir/config.yml"
	if [ -e "$cfg" ]; then
		ok "board config already exists — left untouched ($cfg)"
		return 0
	fi
	if ! template="$(find_plugin_path "flow/templates/lazyboards-config.yml")"; then
		warn "board config template not found in the marketplace checkout — copy it from docs/orchestration.md"
		return 0
	fi
	if [ "$cleanup" = ask ]; then
		# ask_yn returns the default (yes) untouched under --yes / no tty.
		if ask_yn "Auto-close a card's tmux window when its ticket closes (lazyboards runs 'cenci close')?" y; then
			cleanup=yes
		else
			cleanup=no
		fi
	fi
	if ! { mkdir -p "$cfg_dir" && cp "$template" "$cfg"; }; then
		warn "could not write $cfg — copy flow/templates/lazyboards-config.yml there manually"
		return 0
	fi
	if [ "$cleanup" = no ]; then
		if disable_cleanup_line "$cfg"; then
			ok "seeded default board config → $cfg (auto-close disabled — uncomment cleanup: to enable)"
		else
			warn "seeded board config but could not disable the cleanup action — edit $cfg by hand"
		fi
		return 0
	fi
	ok "seeded default board config → $cfg (columns wired to cenci run refine/design/implement)"
}

step_lazyboards_setup() {
	local installed_ver latest_ver path_bin="" managed_present=0
	# An explicit skip applies in every mode, including updates of an existing
	# binary. Flags must override auto-detection rather than merely suppressing
	# a first-time install.
	if [ "$LAZYBOARDS" = no ] && [ "$LAZYBOARDS_EXPLICIT" -eq 1 ]; then
		return 0
	fi
	installed_ver="$(lazyboards_installed_version || true)"
	path_bin="$(lazyboards_path_binary || true)"
	if lazyboards_managed_binary >/dev/null 2>&1; then
		managed_present=1
	fi

	# An unmanaged lazyboards found only on PATH (never installed by this
	# installer) must never be silently adopted or shadowed by a fresh managed
	# install — that's exactly what run_doctor already tells the user to
	# reconcile by hand. Only an explicit --lazyboards proceeds past this
	# point; every other mode (ask, or auto no from --yes) just surfaces the
	# same reconcile hint doctor gives.
	if [ "$managed_present" -eq 0 ] && [ -n "$path_bin" ] && [ "$LAZYBOARDS" != yes ]; then
		warn "unmanaged lazyboards found at $path_bin — reconcile it with: cenci update --lazyboards"
		return 0
	fi

	if [ "$managed_present" -eq 0 ] && [ -z "$path_bin" ] && [ "$LAZYBOARDS" = no ]; then
		return 0
	elif [ "$managed_present" -eq 0 ] && [ -z "$path_bin" ] && [ "$LAZYBOARDS" = ask ]; then
		if ! ask_yn "Install lazyboards (optional separate project — kanban board that dispatches the cenci workflow)?" y; then
			say "  ${DIM}skipped — install later with: cenci-installer --lazyboards${RESET}"
			return 0
		fi
	fi

	step "Setting up lazyboards (board orchestration)"
	if ! latest_ver="$(lazyboards_latest_version)"; then
		fail "could not resolve the latest lazyboards release — see https://github.com/${LAZYBOARDS_REPO}/releases"
		INSTALL_FAILED=1
		return 0
	fi
	if [ -n "$installed_ver" ] && [ "$installed_ver" = "$latest_ver" ]; then
		ok "lazyboards v${installed_ver} is already up to date"
	elif ! install_lazyboards_binary "$latest_ver"; then
		INSTALL_FAILED=1
		return 0
	fi
	seed_lazyboards_config
	if ! verify_lazyboards_resolution; then
		INSTALL_FAILED=1
	fi
}

step_cenci_notes() {
	selected cenci || return 0
	[ "$HAS_CLAUDE" -eq 1 ] || return 0
	step "cenci next steps"
	if have gh && gh auth status >/dev/null 2>&1; then
		ok "GitHub CLI is authenticated"
	else
		warn "cenci drives GitHub issues and PRs through the gh CLI — run: gh auth login"
	fi
	say "  then, in each project you want to use it in, run ${BOLD}/cenci:configure${RESET} once inside Claude Code"
}

final_summary() {
	step "Done"
	if [ "$INSTALL_FAILED" -eq 1 ]; then
		say "  ${YELLOW}Some steps failed — see the ✗ lines above.${RESET}"
	fi
	if [ -z "$SELECTED" ]; then
		say "  ${YELLOW}No plugins ended up installed.${RESET}"
		return
	fi
	say "  Reconciled: ${BOLD}${SELECTED}${RESET}"
	say ""
	say "  Try it out:"
	if selected cenci-sandbox; then
		if [ "$HAS_CLAUDE" -eq 1 ] || [ "$HAS_CODEX" -eq 1 ]; then
			say "    cn                # Claude Code inside the container (alias for: cenci open)"
		fi
		if [ "$HAS_CLAUDE" -eq 1 ]; then
			say "    cn ch|cs|co|cf    # Claude in the container: haiku/sonnet/opus/fable"
		fi
		if [ "$HAS_CODEX" -eq 1 ]; then
			say "    cn xl|xt|xs       # Codex in the container: luna/terra/sol"
		fi
	fi
	if selected cenci && [ "$HAS_CLAUDE" -eq 1 ]; then
		say "    claude → /cenci:configure # one-time project setup"
	fi
	if selected cenci && [ "$HAS_CODEX" -eq 1 ]; then
		say "    codex                       # portable cenci conventions are available"
	fi
	if selected cenci-watch; then
		say "    (start a supported agent session — status appears in configured surfaces)"
	fi
	if lazyboards_binary >/dev/null 2>&1; then
		if have tmux; then
			say "    tmux              # lazyboards dispatches into tmux windows — start it first"
		fi
		say "    lazyboards        # kanban board wired to the workflow — see docs/orchestration.md"
	fi
	say ""
	say "  Check installation health: ${BOLD}cenci doctor${RESET}"
	say "  Update everything later:  ${BOLD}cenci update${RESET}"
	say "  Docs: https://github.com/$MARKETPLACE_REPO/blob/main/docs/getting-started.md"
}

# ---------------------------------------------------------------- uninstall ----

# collect_uninstall_targets prints what run_uninstall is about to remove,
# with no side effects — it only inspects state so the removal list can be
# shown before the single confirmation gate.
collect_uninstall_targets() {
	say "This will remove:"
	if [ "$HAS_CLAUDE" -eq 1 ]; then
		say "  • Claude Code plugins ($ALL_PLUGINS) and the cenci marketplace registration"
	fi
	if [ "$HAS_CODEX" -eq 1 ]; then
		say "  • Codex plugins ($ALL_PLUGINS) and the cenci marketplace registration"
	fi
	say "  • ~/.local/bin/{cenci,cn,cenci-installer} (only if they're cenci-managed symlinks)"
	say "  • the cenci daemon (stopped) and its managed state"
	collect_sandbox_cleanup_targets
	say "  • ${XDG_CONFIG_HOME:-$HOME/.config}/cenci/config.json"
	say "  • desktop bar widgets for any detected bar (GNOME/Plasma/DMS/noctalia/SwiftBar)"
	if [ "$LAZYBOARDS" = yes ]; then
		say "  • lazyboards and ${XDG_CONFIG_HOME:-$HOME/.config}/lazyboards/config.yml (--lazyboards)"
	fi
}

# sandbox_cleanup_summary_line prints one "This will remove:" bullet for a
# single sandbox object kind (containers/images/volumes) — count plus a
# comma-separated name list, styled like the sibling say "  • …" lines
# above — or nothing at all when there's nothing of that kind to remove.
sandbox_cleanup_summary_line() {
	local kind="$1" names="$2" count name_arr
	[ -n "$names" ] || return 0
	read -ra name_arr <<<"$names"
	count="${#name_arr[@]}"
	local note="" n
	if [ "$kind" = "volume(s)" ]; then
		for n in "${name_arr[@]}"; do
			case "$n" in
			claude-cenci-dind-* | codex-cenci-dind-* | opencode-cenci-dind-*)
				note=" (dind volumes may hold sensitive nested-Docker build/application state)"
				break
				;;
			esac
		done
	fi
	say "  • $count cenci sandbox $kind across every repo on this machine: ${names// /, }${note}"
}

# collect_sandbox_cleanup_targets is a read-only enumeration (#458) of every
# cenci-owned container/image/volume across every repo on this machine,
# across EVERY installed container runtime (#630: docker AND podman
# independently whenever each is installed — not just container_runtime()'s
# single podman-first preference for ordinary launches, since a dual-runtime
# host can have cenci-owned resources left behind on either one). Populates
# SANDBOX_RUNTIMES (space-separated list of installed runtimes actually
# enumerated) plus SANDBOX_CONTAINERS/SANDBOX_IMAGES/SANDBOX_VOLUMES
# (space-separated display names, combined across every runtime, for the
# confirmation-list summary) and their runtime-tagged sibling
# SANDBOX_CONTAINER_RECORDS/SANDBOX_IMAGE_RECORDS/SANDBOX_VOLUME_RECORDS
# (newline-separated "runtime<TAB>name" records — bash 3.2 has no
# associative arrays, so a delimited record is the portable substitute) for
# uninstall_sandbox_cleanup to reuse verbatim, so what's shown in the
# confirmation list is exactly what gets removed on exactly the runtime it
# was enumerated from — enumerate once, never re-enumerate, to avoid
# shown-vs-removed drift (TOCTOU) and cross-runtime contamination. Containers
# use the same claude-cenci-/codex-cenci-/opencode-cenci- prefix as
# IsSandboxContainerName (watch/internal/sandbox/sandbox.go) but, unlike
# `cenci sandbox prune` (which only targets exited/created containers),
# matches running containers too — every cenci-owned container must go.
# Volumes mirror prune.go's IsHomeVolumeName/IsAgentCLIVolumeName/
# IsDindVolumeName verbatim (the *-cenci-dind-* pattern added for #630).
# Images match the cenci-sandbox:latest monolith, every cenci-sandbox-base
# tag (including its :latest alias), and each per-repo
# cenci-sandbox-<slug>:latest image, with an optional podman localhost/
# prefix. A clean no-op (no output, empty globals) when no container runtime
# is installed — never errors.
#
# This bash logic is standalone (curl | bash, no repo checkout — it cannot
# import the Go registry) and must be kept in parity by hand with
# watch/internal/sandbox/sandbox.go's SupportedAgents, the Go source of
# truth for which agents cenci's sandbox owns resources for (#528).
collect_sandbox_cleanup_targets() {
	SANDBOX_RUNTIMES=""
	SANDBOX_CONTAINERS=""
	SANDBOX_IMAGES=""
	SANDBOX_VOLUMES=""
	SANDBOX_CONTAINER_RECORDS=""
	SANDBOX_IMAGE_RECORDS=""
	SANDBOX_VOLUME_RECORDS=""

	local runtime
	for runtime in docker podman; do
		have "$runtime" || continue
		SANDBOX_RUNTIMES="${SANDBOX_RUNTIMES:+$SANDBOX_RUNTIMES }$runtime"

		local out line repo tag bare_repo
		if ! out="$("$runtime" ps -a --format '{{.Names}}' 2>/dev/null)"; then
			warn "could not list $runtime containers — skipping sandbox container cleanup"
			out=""
		fi
		while IFS= read -r line; do
			[ -n "$line" ] || continue
			case "$line" in
			claude-cenci-* | codex-cenci-* | opencode-cenci-*)
				SANDBOX_CONTAINERS="${SANDBOX_CONTAINERS:+$SANDBOX_CONTAINERS }$line"
				SANDBOX_CONTAINER_RECORDS="${SANDBOX_CONTAINER_RECORDS}${runtime}	${line}
"
				;;
			esac
		done <<<"$out"

		if ! out="$("$runtime" images --format '{{.Repository}}:{{.Tag}}' 2>/dev/null)"; then
			warn "could not list $runtime images — skipping sandbox image cleanup"
			out=""
		fi
		while IFS= read -r line; do
			[ -n "$line" ] || continue
			repo="${line%%:*}"
			tag="${line#*:}"
			bare_repo="${repo#localhost/}"
			case "$bare_repo" in
			cenci-sandbox-base)
				SANDBOX_IMAGES="${SANDBOX_IMAGES:+$SANDBOX_IMAGES }$line"
				SANDBOX_IMAGE_RECORDS="${SANDBOX_IMAGE_RECORDS}${runtime}	${line}
"
				;;
			cenci-sandbox | cenci-sandbox-*)
				if [ "$tag" = latest ]; then
					SANDBOX_IMAGES="${SANDBOX_IMAGES:+$SANDBOX_IMAGES }$line"
					SANDBOX_IMAGE_RECORDS="${SANDBOX_IMAGE_RECORDS}${runtime}	${line}
"
				fi
				;;
			esac
		done <<<"$out"

		if ! out="$("$runtime" volume ls --format '{{.Name}}' 2>/dev/null)"; then
			warn "could not list $runtime volumes — skipping sandbox volume cleanup"
			out=""
		fi
		while IFS= read -r line; do
			[ -n "$line" ] || continue
			case "$line" in
			claude-cenci-home-* | codex-cenci-home-* | opencode-cenci-home-* | \
				cenci-agent-cli-claude | cenci-agent-cli-codex | cenci-agent-cli-opencode | \
				claude-cenci-dind-* | codex-cenci-dind-* | opencode-cenci-dind-*)
				SANDBOX_VOLUMES="${SANDBOX_VOLUMES:+$SANDBOX_VOLUMES }$line"
				SANDBOX_VOLUME_RECORDS="${SANDBOX_VOLUME_RECORDS}${runtime}	${line}
"
				;;
			esac
		done <<<"$out"
	done

	sandbox_cleanup_summary_line "container(s)" "$SANDBOX_CONTAINERS"
	sandbox_cleanup_summary_line "image(s)" "$SANDBOX_IMAGES"
	sandbox_cleanup_summary_line "volume(s)" "$SANDBOX_VOLUMES"
}

# resolve_uninstall_cenci_binary finds the cenci binary to use for
# `socket-dir` / `daemon stop`. Unlike cenci_binary_for_doctor (which checks
# PATH first), uninstall must work even when ~/.local/bin isn't on PATH yet —
# so it checks the bootstrap-maintained link directly before falling back to
# the version-pinned plugin cache.
resolve_uninstall_cenci_binary() {
	if [ -x "$HOME/.local/bin/cenci" ]; then
		printf '%s\n' "$HOME/.local/bin/cenci"
		return 0
	fi
	cached_cenci_binary
}

# uninstall_stop_daemon_fallback mirrors restart_cenci_daemon_fallback's
# pkill/pgrep pattern for when the primary `daemon stop` path isn't
# available or didn't succeed. Unlike the ad-hoc restart fallback it never
# spawns anything — it only needs to confirm the daemon is no longer
# running — so it returns 0 exactly when a re-check of `pgrep` after the
# wait loop finds nothing, and 1 (with a warn) otherwise, matching
# restart_cenci_daemon_fallback's own success/failure convention.
uninstall_stop_daemon_fallback() {
	if ! have pkill || ! have pgrep; then
		warn "pkill/pgrep unavailable — could not confirm the cenci daemon stopped"
		return 1
	fi
	pkill -TERM -f '[/]cenci daemon( start)?$' >/dev/null 2>&1 || true
	local i=0
	while pgrep -f '[/]cenci daemon( start)?$' >/dev/null 2>&1 && [ "$i" -lt 30 ]; do
		sleep 0.1
		i=$((i + 1))
	done
	if pgrep -f '[/]cenci daemon( start)?$' >/dev/null 2>&1; then
		warn "the cenci daemon did not stop — stop it manually"
		return 1
	fi
	return 0
}

# uninstall_default_socket_dir computes the daemon's managed-state dir the
# same way the daemon itself resolves it when no binary is available to ask
# via `socket-dir` (see SocketDir in watch/pkg/watch/socket.go): the daemon
# joins a "cenci" segment onto its base runtime dir, so this is
# $XDG_RUNTIME_DIR/cenci when set, else /tmp/cenci-<uid>/cenci. The uid lookup is checked
# explicitly per AGENTS.md's rule against unchecked command substitution for
# security-critical paths — an unchecked `id -u` failing silently would
# collapse this to /tmp/cenci-/cenci. Prints nothing but the resolved path
# (or nothing at all) on stdout and reports failure only via exit status —
# never `warn` here, since every call site captures this function's stdout
# via `$(...)`, and a `warn` call here would get swallowed into the
# captured string instead of reaching the terminal.
uninstall_default_socket_dir() {
	if [ -n "${XDG_RUNTIME_DIR:-}" ]; then
		printf '%s\n' "$XDG_RUNTIME_DIR/cenci"
		return 0
	fi
	local uid
	uid="$(id -u 2>/dev/null)" || return 1
	[ -n "$uid" ] || return 1
	printf '%s\n' "/tmp/cenci-$uid/cenci"
	return 0
}

# uninstall_socket_dir_is_safe rejects a small blocklist of paths a resolved
# socket_dir must never equal before it's handed to `rm -rf` — every other
# rm -rf target in this file is built from constants; this is the one path
# that can come from a subprocess (`cenci socket-dir`), so it gets its own
# bound.
uninstall_socket_dir_is_safe() {
	local dir="$1" cfg_home
	cfg_home="${XDG_CONFIG_HOME:-$HOME/.config}"
	case "$dir" in
	"/" | "$HOME" | "$cfg_home" | "$cfg_home/cenci")
		return 1
		;;
	esac
	return 0
}

# uninstall_stop_daemon resolves the binary while it still exists, captures
# its managed state dir via `socket-dir`, stops the daemon (`daemon stop` is
# idempotent — see runDaemonStop in watch/daemon_cmd.go), then removes the
# managed state dir — but only once the daemon is actually confirmed
# stopped (either `daemon stop` itself succeeded, or the pkill/pgrep
# fallback confirms no process remains); a daemon that couldn't be
# confirmed stopped keeps its managed state so a still-live process doesn't
# get its socket/pid files deleted out from under it.
uninstall_stop_daemon() {
	local bin socket_dir="" stopped=0
	bin="$(resolve_uninstall_cenci_binary || true)"
	if [ -n "$bin" ] && [ -x "$bin" ]; then
		socket_dir="$("$bin" socket-dir 2>/dev/null || true)"
		if [ -z "$socket_dir" ]; then
			if ! socket_dir="$(uninstall_default_socket_dir)"; then
				warn "could not determine the current uid — skipping the default managed-state cleanup"
				socket_dir=""
			fi
		fi
		if "$bin" daemon stop >/dev/null 2>&1; then
			ok "cenci daemon stopped"
			stopped=1
		else
			warn "'$bin daemon stop' did not succeed — falling back to a manual stop"
			if uninstall_stop_daemon_fallback; then
				stopped=1
			fi
		fi
	else
		if uninstall_stop_daemon_fallback; then
			stopped=1
		fi
		if ! socket_dir="$(uninstall_default_socket_dir)"; then
			warn "could not determine the current uid — skipping the default managed-state cleanup"
			socket_dir=""
		fi
	fi

	if [ "$stopped" -ne 1 ]; then
		warn "could not confirm the cenci daemon stopped — leaving its managed state in place"
		return 0
	fi

	if [ -n "$socket_dir" ] && [ -d "$socket_dir" ]; then
		if ! uninstall_socket_dir_is_safe "$socket_dir"; then
			warn "cenci's managed state dir ($socket_dir) looks unexpected — left untouched"
			return 0
		fi
		rm -rf "$socket_dir"
		ok "removed cenci's managed state ($socket_dir)"
	fi
}

# unlink_launcher <name> — reverse of link_launcher: removes
# ~/.local/bin/<name> only if it's a symlink, refusing to clobber a real file
# the user (or something else) put there.
unlink_launcher() {
	local name="$1" dest="$HOME/.local/bin/$1"
	if [ ! -e "$dest" ] && [ ! -L "$dest" ]; then
		return 0
	fi
	if [ ! -L "$dest" ]; then
		warn "$dest exists and is not a symlink — left untouched"
		return 0
	fi
	if rm -f "$dest"; then
		ok "removed ~/.local/bin/$name"
	else
		warn "could not remove ~/.local/bin/$name — remove it manually: rm ~/.local/bin/$name"
	fi
}

# uninstall_links removes the three link_launcher-managed symlinks, plus the
# optional /usr/local/bin/cenci link — mirroring setup_cenci_linux_path's
# guard in reverse: only touched when it's a symlink resolving to the user
# link, sudo only for that case, never forced otherwise.
uninstall_links() {
	local name
	for name in cenci cn cenci-installer; do
		unlink_launcher "$name"
	done

	local user_link="$HOME/.local/bin/cenci"
	if [ -L /usr/local/bin/cenci ] && [ "$(readlink /usr/local/bin/cenci 2>/dev/null)" = "$user_link" ]; then
		if sudo rm -f /usr/local/bin/cenci 2>/dev/null; then
			ok "removed /usr/local/bin/cenci"
		else
			warn "could not remove /usr/local/bin/cenci — remove it manually: sudo rm /usr/local/bin/cenci"
		fi
	elif [ -e /usr/local/bin/cenci ]; then
		warn "/usr/local/bin/cenci exists and was not created by cenci — left untouched"
	fi
}

# uninstall_plugin_cmd mirrors plugin_cmd's qualified-then-bare fallback
# (install.sh:153) for the removal direction, parameterized by client and
# verb since codex's removal verb ("remove") differs from claude's
# ("uninstall").
uninstall_plugin_cmd() {
	local client="$1" verb="$2" name="$3"
	"$client" plugin "$verb" "${name}@${MARKETPLACE_NAME}" >/dev/null 2>&1 ||
		"$client" plugin "$verb" "$name" >/dev/null 2>&1
}

# uninstall_plugins removes cenci/cenci-watch/cenci-sandbox for every
# detected client via the qualified-then-bare fallback. Failures (or an
# absent client CLI) are tracked in CLAUDE_UNINSTALL_FAILED /
# CODEX_UNINSTALL_FAILED so uninstall_marketplace's fallback rm -rf can catch
# anything the CLI left behind.
uninstall_plugins() {
	local p
	if [ "$HAS_CLAUDE" -eq 1 ]; then
		for p in $ALL_PLUGINS; do
			if uninstall_plugin_cmd claude uninstall "$p"; then
				ok "Claude: $p uninstalled"
			else
				warn "Claude: $p failed to uninstall via the CLI"
				CLAUDE_UNINSTALL_FAILED=1
			fi
		done
	else
		CLAUDE_UNINSTALL_FAILED=1
	fi

	if [ "$HAS_CODEX" -eq 1 ]; then
		for p in $ALL_PLUGINS; do
			if uninstall_plugin_cmd codex remove "$p"; then
				ok "Codex: $p removed"
			else
				warn "Codex: $p failed to remove via the CLI"
				CODEX_UNINSTALL_FAILED=1
			fi
		done
	else
		CODEX_UNINSTALL_FAILED=1
	fi
}

# uninstall_fallback_rm removes exactly <home>/plugins/marketplaces/cenci and
# <home>/plugins/cache/cenci for the given client — never a parent dir, never
# a sibling marketplace/cache — the safety net for when the CLI is absent or
# its removal verb failed.
uninstall_fallback_rm() {
	local client="$1" checkout cache_root
	checkout="$HOME/.$client/plugins/marketplaces/$MARKETPLACE_NAME"
	cache_root="$HOME/.$client/plugins/cache/$MARKETPLACE_NAME"
	if [ -d "$checkout" ]; then
		if rm -rf "$checkout"; then
			ok "removed $checkout"
		else
			warn "could not remove $checkout — remove it manually: rm -rf $checkout"
		fi
	fi
	if [ -d "$cache_root" ]; then
		if rm -rf "$cache_root"; then
			ok "removed $cache_root"
		else
			warn "could not remove $cache_root — remove it manually: rm -rf $cache_root"
		fi
	fi
}

# uninstall_marketplace removes the cenci marketplace registration for every
# detected client, then falls back to uninstall_fallback_rm for any client
# whose CLI was absent or whose removal (plugin or marketplace) failed —
# CLI-driven removal is primary, the rm -rf fallback only catches what the
# CLI left behind.
uninstall_marketplace() {
	if [ "$HAS_CLAUDE" -eq 1 ]; then
		if claude plugin marketplace remove "$MARKETPLACE_NAME" >/dev/null 2>&1; then
			ok "Claude: marketplace '$MARKETPLACE_NAME' removed"
		else
			warn "Claude: could not remove marketplace '$MARKETPLACE_NAME'"
			CLAUDE_UNINSTALL_FAILED=1
		fi
	fi
	if [ "$HAS_CODEX" -eq 1 ]; then
		if codex plugin marketplace remove "$MARKETPLACE_NAME" >/dev/null 2>&1; then
			ok "Codex: marketplace '$MARKETPLACE_NAME' removed"
		else
			warn "Codex: could not remove marketplace '$MARKETPLACE_NAME'"
			CODEX_UNINSTALL_FAILED=1
		fi
	fi

	if [ "$CLAUDE_UNINSTALL_FAILED" -eq 1 ]; then
		uninstall_fallback_rm claude
	fi
	if [ "$CODEX_UNINSTALL_FAILED" -eq 1 ]; then
		uninstall_fallback_rm codex
	fi
}

# uninstall_config removes ~/.config/cenci/config.json, then rmdir's the now
# (likely) empty cenci config dir — harmless no-op if anything else still
# lives there (e.g. files other children may own).
uninstall_config() {
	local cfg_dir cfg
	cfg_dir="${XDG_CONFIG_HOME:-$HOME/.config}/cenci"
	cfg="$cfg_dir/config.json"
	if [ -e "$cfg" ]; then
		if rm -f "$cfg"; then
			ok "removed $cfg"
		else
			warn "could not remove $cfg — remove it manually: rm $cfg"
		fi
	fi
	rmdir "$cfg_dir" >/dev/null 2>&1 || true
}

# uninstall_lazyboards is strictly opt-in: --lazyboards (LAZYBOARDS=yes), or
# an interactive prompt defaulting to no. Declined/absent leaves the managed
# binary and its config untouched.
uninstall_lazyboards() {
	local do_remove=0
	if [ "$LAZYBOARDS" = yes ]; then
		do_remove=1
	elif [ "$LAZYBOARDS" = ask ] && [ "$INTERACTIVE" -eq 1 ]; then
		if ask_yn "Also remove lazyboards and its config?" n; then
			do_remove=1
		fi
	fi
	[ "$do_remove" -eq 1 ] || return 0

	local bin cfg
	if bin="$(lazyboards_managed_binary 2>/dev/null)"; then
		if rm -f "$bin"; then
			ok "removed $bin"
		else
			warn "could not remove $bin — remove it manually: rm $bin"
		fi
	fi
	cfg="${XDG_CONFIG_HOME:-$HOME/.config}/lazyboards/config.yml"
	if [ -e "$cfg" ]; then
		if rm -f "$cfg"; then
			ok "removed $cfg"
		else
			warn "could not remove $cfg — remove it manually: rm $cfg"
		fi
	fi
}

# print_manual_path_note mirrors check_stale_tmux_vars/setup_cenci_linux_path's
# read-only philosophy: cenci never edits shell rc files, so if a PATH export
# was added during install, print the line to remove by hand instead.
print_manual_path_note() {
	say ""
	say "  cenci never edits shell rc files. If a PATH export for ~/.local/bin was"
	say "  added during install (e.g. in ~/.bashrc), remove it manually:"
	say "    export PATH=\"\$HOME/.local/bin:\$PATH\""
}

# uninstall_sandbox_cleanup (#458, multi-runtime per #630) removes every
# cenci-owned sandbox container/image/volume across every repo on this
# machine, on every runtime it was found on, consuming the
# SANDBOX_RUNTIMES/SANDBOX_CONTAINER_RECORDS/SANDBOX_IMAGE_RECORDS/
# SANDBOX_VOLUME_RECORDS globals collect_sandbox_cleanup_targets already
# populated during collect_uninstall_targets — never re-enumerates. Each
# *_RECORDS variable is a newline-separated set of "runtime<TAB>name"
# records (bash 3.2 has no associative arrays), so every removal call is
# issued against exactly the runtime that record was enumerated from,
# never cross-runtime. A no-op when no container runtime was found
# (SANDBOX_RUNTIMES empty). Order matters: containers before images before
# volumes, since an image `rmi` can fail while a not-yet-removed container
# still references it. Each removal is best-effort, reported per-object like
# unlink_launcher/uninstall_fallback_rm above.
uninstall_sandbox_cleanup() {
	[ -n "${SANDBOX_RUNTIMES:-}" ] || return 0

	local runtime name
	while IFS=$'\t' read -r runtime name; do
		[ -n "$name" ] || continue
		if "$runtime" rm -f "$name" >/dev/null 2>&1; then
			ok "removed sandbox container $name"
		else
			warn "could not remove sandbox container $name — remove it manually: $runtime rm -f $name"
		fi
	done <<<"${SANDBOX_CONTAINER_RECORDS:-}"

	while IFS=$'\t' read -r runtime name; do
		[ -n "$name" ] || continue
		if "$runtime" rmi "$name" >/dev/null 2>&1; then
			ok "removed sandbox image $name"
		else
			warn "could not remove sandbox image $name — remove it manually: $runtime rmi $name"
		fi
	done <<<"${SANDBOX_IMAGE_RECORDS:-}"

	while IFS=$'\t' read -r runtime name; do
		[ -n "$name" ] || continue
		if "$runtime" volume rm "$name" >/dev/null 2>&1; then
			ok "removed sandbox volume $name"
		else
			warn "could not remove sandbox volume $name — remove it manually: $runtime volume rm $name"
		fi
	done <<<"${SANDBOX_VOLUME_RECORDS:-}"
}

# uninstall_widget_cleanup removes each detected bar's widget as part of the
# full purge (#459). Mirrors step_cenci_watch_setup's platform branch in
# reverse: on Darwin, delegate to the SwiftBar uninstall.sh (gated on the app
# being present, like the install-side block); everywhere else, delegate to
# teardown_cenci_linux_widgets. Must run before uninstall_marketplace (see
# run_uninstall) so find_plugin_path can still resolve the widget scripts
# from the marketplace checkout.
uninstall_widget_cleanup() {
	if [ "$OS" = macos ]; then
		local script
		if ! script=$(find_plugin_path "watch/plugin/macos/uninstall.sh"); then
			warn "could not find watch/plugin/macos/uninstall.sh in the marketplace checkout — remove the SwiftBar menu bar widget manually"
			return 0
		fi
		if [ ! -d /Applications/SwiftBar.app ]; then
			return 0
		fi
		chmod +x "$script" 2>/dev/null || true
		if bash "$script"; then
			ok "menu bar widget removed and SwiftBar reloaded"
		elif [ "$?" -eq 2 ]; then
			warn "menu bar widget left untouched — a non-cenci file/dir is present at its plugin path"
		else
			warn "SwiftBar widget teardown failed — see watch/plugin/macos/README.md"
		fi
		return 0
	fi
	teardown_cenci_linux_widgets
}

# uninstall_opencode_cleanup removes only what step_opencode_setup itself
# added: the portable skill symlinks (via install-skills.sh remove, which
# only unlinks its own PORTABLE_SKILLS symlinks pointing at the plugin's own
# source — a user's own skills or a foreign symlink at the same name are left
# untouched) and the cenci-watch plugin entry from opencode.json's .plugin
# array — every other key (permission, autoupdate, other plugins) is left
# exactly as the user has it. Not gated on HAS_OPENCODE: leftover
# registration/symlinks should still be cleaned up even if the opencode
# binary itself is gone by the time uninstall runs. Must run before
# uninstall_marketplace (like uninstall_widget_cleanup above) so
# find_plugin_path can still resolve install-skills.sh from the checkout.
uninstall_opencode_cleanup() {
	local script plugin_root opencode_json

	if script=$(find_plugin_path "flow/opencode/install-skills.sh"); then
		plugin_root="$(dirname "$(dirname "$script")")"
		chmod +x "$script" 2>/dev/null || true
		if PLUGIN_ROOT="$plugin_root" bash "$script" remove >/dev/null 2>&1; then
			ok "removed cenci's OpenCode skill symlinks"
		else
			warn "could not remove OpenCode skill symlinks — run manually: PLUGIN_ROOT=$plugin_root bash $script remove"
		fi
	else
		warn "could not find flow/opencode/install-skills.sh in the marketplace checkout — remove OpenCode skill symlinks manually"
	fi

	opencode_json="$(opencode_config_dir)/opencode.json"
	if [ -f "$opencode_json" ] && have jq; then
		if jq --arg pat "$OPENCODE_PLUGIN_SPEC_PATTERN" \
			'if has("plugin") then .plugin |= map(select(test($pat) | not)) else . end' \
			"$opencode_json" >"$opencode_json.cenci-tmp" 2>/dev/null; then
			if mv "$opencode_json.cenci-tmp" "$opencode_json"; then
				ok "removed cenci's OpenCode plugin registration"
			else
				warn "failed to update $opencode_json — see $opencode_json.cenci-tmp"
			fi
		else
			rm -f "$opencode_json.cenci-tmp"
			warn "could not update $opencode_json — remove the cenci plugin entry manually"
		fi
	elif [ -f "$opencode_json" ]; then
		warn "jq not found — remove the cenci plugin entry from $opencode_json manually"
	fi
}

# have_controlling_tty probes whether /dev/tty is actually openable, not just
# whether its permission bits look readable/writable. `[ -r/-w /dev/tty ]`
# (INTERACTIVE, above) can misreport true when the device node exists with
# permissive bits but no controlling terminal is attached (e.g. a detached
# CI/agent sandbox) — opening it there fails with ENXIO regardless of those
# bits. The destructive uninstall confirmation gate needs that stronger
# signal so it doesn't silently fall through ask_yn's own failed-read path
# and print the wrong abort message.
have_controlling_tty() {
	exec 3<>/dev/tty 2>/dev/null || return 1
	exec 3<&-
	return 0
}

# run_uninstall is the uninstall MODE dispatcher: collect + print targets,
# gate on a single confirmation (special-cased for ASSUME_YES — routing
# --yes through ask_yn's destructive default of n would make --yes abort,
# see ask_yn above), then remove everything in dependency order (stop the
# daemon before touching the plugin cache its binary lives in).
run_uninstall() {
	step "Uninstalling cenci"
	collect_uninstall_targets

	if [ "$ASSUME_YES" -eq 1 ]; then
		:
	elif [ "$INTERACTIVE" -eq 1 ] && have_controlling_tty; then
		if ! ask_yn "Remove all of the above?" n; then
			say "Aborted — nothing was removed."
			return 1
		fi
	else
		die "Refusing to uninstall without confirmation in a non-interactive session — re-run with --yes to skip the confirmation prompt."
	fi

	CLAUDE_UNINSTALL_FAILED=0
	CODEX_UNINSTALL_FAILED=0

	uninstall_stop_daemon
	uninstall_sandbox_cleanup
	uninstall_widget_cleanup
	uninstall_opencode_cleanup
	uninstall_plugins
	uninstall_marketplace
	uninstall_links
	uninstall_config
	uninstall_lazyboards
	print_manual_path_note

	step "Done"
	say "  cenci has been uninstalled."
	return 0
}

# ------------------------------------------------------------------- main ----

MODE=install
BUILD_IMAGE=ask
LAZYBOARDS=ask
LAZYBOARDS_EXPLICIT=0
CLEANUP=ask
INSTALL_FAILED=0
CENCI_REF="${CENCI_REF:-}"
# Captured before the arg-parse loop below consumes "$@" via shift, so the
# piped-release-pin re-exec can forward the caller's original arguments
# (including --ref) to the resolved tag's install.sh.
CENCI_ORIG_ARGS=("$@")

usage() {
	cat <<'EOF'
cenci installer — one command for the whole package.

Usage:
  cenci-installer              interactive installer / repair
  cenci-installer update       update installed plugins (+ optional rebuild)
  cenci-installer doctor       check prerequisites, change nothing
  cenci-installer uninstall    remove plugins, PATH links, daemon, config,
                                and every cenci sandbox container/image/
                                volume on this machine

Initial install (canonical — download, verify, run):
  See docs/getting-started.md for the download-verify-run sequence: fetch
  install.sh + install.sh.bundle from the latest release, verify with
  `cosign verify-blob`, then run only the verified bytes. cosign is required.

  The legacy one-liner still works:
    curl -fsSL https://raw.githubusercontent.com/matteobortolazzo/cenci/main/install.sh | bash

  Resolves to the latest release tag by default (not live main) — a piped
  run with CENCI_REF unset re-execs itself, verified, from the newest
  release tag's install.sh. That resolved ref then pins the client
  marketplace registration and all three installed plugins' content too, on
  every path (install, update, repair, dev-from-clone) — not just which
  install.sh runs. Set CENCI_REF=main (or pass --ref main) to explicitly
  opt into bleeding-edge, unverified main instead (unsafe; development use
  only); it is the only path that intentionally tracks main.

From a source checkout, ./install.sh accepts the same arguments.

Flags:
  --yes                                 accept defaults, never prompt
  --build / --no-build                  force / skip the sandbox image build
  --lazyboards / --no-lazyboards        force / skip the optional lazyboards board install
                                         (uninstall mode: --lazyboards also removes it)
  --cleanup / --no-cleanup              seed the lazyboards board config with / without the
                                         auto-close action (`cleanup: "cenci close {number}"`)
                                         that closes a card's tmux window when its ticket does
  --ref <ref>                           marketplace + plugin content, and the installer
                                         itself, all come from this ref instead of the
                                         resolved latest release tag (e.g. --ref main,
                                         unverified — see above);
                                         same as CENCI_REF=<ref>
  --help                                this text
EOF
}

while [ $# -gt 0 ]; do
	case "$1" in
	update | --update) MODE=update ;;
	doctor | --doctor) MODE=doctor ;;
	uninstall | --uninstall) MODE=uninstall ;;
	--yes | -y) ASSUME_YES=1 ;;
	--build) BUILD_IMAGE=yes ;;
	--no-build) BUILD_IMAGE=no ;;
	--lazyboards) LAZYBOARDS=yes; LAZYBOARDS_EXPLICIT=1 ;;
	--no-lazyboards) LAZYBOARDS=no; LAZYBOARDS_EXPLICIT=1 ;;
	--cleanup) CLEANUP=yes ;;
	--no-cleanup) CLEANUP=no ;;
	--ref)
		[ $# -ge 2 ] || die "--ref requires a value (see --help)"
		CENCI_REF="$2"
		shift
		;;
	--ref=*) CENCI_REF="${1#--ref=}" ;;
	--help | -h)
		usage
		exit 0
		;;
	*) die "unknown option '$1' (see --help)" ;;
	esac
	shift
done

# ------------------------------------------------ immutable release pin ----

# Piped invocations (`curl -fsSL ... | bash`) have no on-disk script path:
# bash reports $0 (and BASH_SOURCE[0], when set) as the bare word "bash" —
# no path component, and not resolved against a real file — in that mode. A
# from-file run — a source checkout's ./install.sh, the cenci-installer
# wrapper, or `cenci-installer update` — always resolves to a real, readable
# file with a path component, so this guard is a no-op there (verified
# empirically; see the plan's Risks). Only the piped path, and only when
# CENCI_REF is still unset (env, --ref, or a prior re-exec's own
# CENCI_REF="$pin_tag" all skip this), resolves the latest release tag,
# downloads install.sh + install.sh.bundle from that release's assets,
# cryptographically verifies them with cosign, and only then re-execs — the
# AC1/AC2 default: a fresh piped install runs only cryptographically
# verified, release-pinned code, never live main, unless the caller
# explicitly opts out. Verification failure always calls die() with a
# distinct message per failure class and never falls back to running main
# or any other unverified ref (AC4).
pin_self="${BASH_SOURCE[0]:-$0}"
# A real `curl | bash` invocation reports $0 as the bare word "bash" — no
# path separator at all, since bash never resolves it against $PATH into a
# path for $0/BASH_SOURCE. A from-file run (source checkout's ./install.sh,
# the cenci-installer wrapper, `cenci-installer update`, or this script's own
# re-exec below) always has a path component. So the absence of a `/` in
# pin_self is itself sufficient evidence of a piped run, and — unlike -f/-r
# alone — can't be fooled by a coincidentally-named regular file called
# "bash" sitting in the caller's CWD (which would otherwise make -f/-r look
# like a from-file invocation). Blank out pin_self in that case so the -f/-r
# checks below correctly fall through to "piped". The `[ ! -t 0 ]` (stdin is
# not a terminal) check is a secondary guard: it stops an interactive
# from-file run with a bare-word BASH_SOURCE (unusual, but not impossible)
# from being misdetected as piped.
case "$pin_self" in
	*/*) : ;;
	*) pin_self="" ;;
esac
if [ -z "$CENCI_REF" ] && [ ! -t 0 ] && { [ -z "$pin_self" ] || [ ! -f "$pin_self" ] || [ ! -r "$pin_self" ]; }; then
	pin_tag="$(cenci_latest_ref)" || die "could not resolve the latest cenci release tag to install from (offline, or no release has been published yet) — retry, or set CENCI_REF=main (or pass --ref main) to install from main instead"
	pin_release_base="https://github.com/${MARKETPLACE_REPO}/releases/download/${pin_tag}"
	# Re-exec from a temp *file*, never a re-pipe: the child then sees a
	# real, readable BASH_SOURCE[0] and skips this block outright. Setting
	# CENCI_REF="$pin_tag" for the child below is a second, independent
	# guard against an infinite re-exec loop.
	# pin_die <message> — reports a failure of this download-verify block:
	# cleans up whichever staged temp files exist so far, then die()s. Used
	# instead of repeating "rm -f $pin_tmp $pin_bundle_tmp" at every one of
	# this block's several fail-closed checks below.
	pin_die() {
		rm -f "$pin_tmp" "$pin_bundle_tmp"
		die "$1"
	}
	pin_tmp="" pin_bundle_tmp=""
	pin_tmp="$(mktemp)" || pin_tmp=""
	if [ -z "$pin_tmp" ] || [ ! -f "$pin_tmp" ]; then
		pin_die "could not create a temporary file to stage the pinned install.sh"
	fi
	pin_bundle_tmp="$(mktemp)" || pin_bundle_tmp=""
	if [ -z "$pin_bundle_tmp" ] || [ ! -f "$pin_bundle_tmp" ]; then
		pin_die "could not create a temporary file to stage the pinned install.sh.bundle"
	fi
	if ! curl -fsSL -o "$pin_tmp" "${pin_release_base}/install.sh"; then
		pin_die "could not download install.sh from release tag '${pin_tag}' — retry, or set CENCI_REF=main (or pass --ref main) to install from main instead"
	fi
	if [ ! -s "$pin_tmp" ]; then
		pin_die "downloaded install.sh from release tag '${pin_tag}' was empty — retry, or set CENCI_REF=main (or pass --ref main) to install from main instead"
	fi
	if ! curl -fsSL -o "$pin_bundle_tmp" "${pin_release_base}/install.sh.bundle"; then
		pin_die "could not download install.sh.bundle from release tag '${pin_tag}' — retry, or set CENCI_REF=main (or pass --ref main) to install from main instead"
	fi
	if [ ! -s "$pin_bundle_tmp" ]; then
		pin_die "downloaded install.sh.bundle from release tag '${pin_tag}' was empty — retry, or set CENCI_REF=main (or pass --ref main) to install from main instead"
	fi
	verify_installer_asset "$pin_tmp" "$pin_bundle_tmp"
	rm -f "$pin_bundle_tmp"
	CENCI_REF="$pin_tag" exec bash "$pin_tmp" ${CENCI_ORIG_ARGS[@]+"${CENCI_ORIG_ARGS[@]}"}
fi

# Non-interactive runs never start a minutes-long image build unless asked.
if [ "$BUILD_IMAGE" = ask ] && { [ "$INTERACTIVE" -eq 0 ] || [ "$ASSUME_YES" -eq 1 ]; }; then
	BUILD_IMAGE=no
fi

# lazyboards is optional orchestration — non-interactive runs never install it
# unless explicitly asked with --lazyboards.
if [ "$LAZYBOARDS" = ask ] && { [ "$INTERACTIVE" -eq 0 ] || [ "$ASSUME_YES" -eq 1 ]; }; then
	LAZYBOARDS=no
fi

detect_platform
detect_clients

say ""
say "${BOLD}cenci installer${RESET} — $(platform_label)"

if [ "$MODE" = doctor ]; then
	run_doctor
	exit $?
fi

if [ "$MODE" = uninstall ]; then
	run_uninstall
	exit $?
fi

have_supported_client || die "no supported client found. Install Claude Code, Codex, or both, then re-run this script."

# ---------------------------------------------------- resolved install ref ----

# The ref that governs marketplace registration — and, through it, all three
# plugins' content — for the rest of this run. Applies to both dispatch
# branches below (install and update), matching the piped re-exec's own
# default: explicit CENCI_REF/--ref (including "main") wins, otherwise the
# latest release tag is resolved. Malformed, unavailable, or unresolvable
# refs are fatal here — never fall through to an unverified default-branch
# checkout (#625).
RESOLVED_REF="$(resolve_install_ref)" || die "could not resolve the latest cenci release tag to install from (offline, or no release has been published yet) — retry, or set CENCI_REF=main (or pass --ref main) to install from main instead"
validate_ref_format "$RESOLVED_REF" || die "invalid ref '${RESOLVED_REF}' — refs may only contain letters, digits, '.', '_', '/', and '-' — retry, or set CENCI_REF=main (or pass --ref main) to install from main instead"
ref_available "$RESOLVED_REF" || die "ref '${RESOLVED_REF}' is not available (no published marketplace manifest was found at that ref) — retry, or set CENCI_REF=main (or pass --ref main) to install from main instead"

if [ "$MODE" = update ]; then
	step_marketplace
	step_reconcile_plugins
	prune_selected_to_installed
	step_cli_setup
	step_sandbox_setup
	step_cenci_watch_setup
	step_opencode_setup
	step_sandbox_refresh_plugins
	step_lazyboards_setup
	final_summary
	exit $((INSTALL_FAILED))
fi

run_doctor || {
	if ! ask_yn "Continue anyway?" n; then
		exit 1
	fi
}
step_marketplace
step_reconcile_plugins
prune_selected_to_installed
step_cli_setup
step_sandbox_setup
step_cenci_watch_setup
step_opencode_setup
step_lazyboards_setup
step_cenci_notes
final_summary
exit $((INSTALL_FAILED))
