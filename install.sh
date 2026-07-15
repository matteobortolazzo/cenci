#!/usr/bin/env bash
# cenci installer — one command for the whole package.
#
# Installs or updates the three cenci plugins (cenci, cenci-watch,
# cenci-sandbox) as a single system: registers the marketplace, installs the
# plugins, and runs the post-install setup that used to be manual (cn launcher
# link + sandbox image build, macOS menu bar and Linux desktop bar-widget
# wiring).
#
# Usage:
#   ./install.sh                interactive wizard (install)
#   cenci-installer update      update installed plugins (+ optional rebuild)
#   cenci-installer doctor      check prerequisites, change nothing
#
#   curl -fsSL https://raw.githubusercontent.com/matteobortolazzo/cenci/main/install.sh | bash
#
# Flags:
#   --yes                                 accept defaults, never prompt
#   --build / --no-build                  force / skip the sandbox image build
#   --help                                this text

set -u

MARKETPLACE_REPO="matteobortolazzo/cenci"
MARKETPLACE_NAME="cenci"
ALL_PLUGINS="cenci cenci-watch cenci-sandbox"
CODEX_MARKETPLACE_READY=0
CLAUDE_MARKETPLACE_READY=0
HAS_CLAUDE=0
HAS_CODEX=0

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

detect_clients() {
	have claude && HAS_CLAUDE=1
	have codex && HAS_CODEX=1
}

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
	claude plugin list 2>/dev/null | grep -q "$1"
}

marketplace_registered() {
	claude plugin marketplace list 2>/dev/null | grep -q "$MARKETPLACE_NAME"
}

codex_plugin_installed() {
	codex plugin list 2>/dev/null |
		grep -Eq "^$1@${MARKETPLACE_NAME}[[:space:]]+installed"
}

codex_marketplace_registered() {
	codex plugin marketplace list 2>/dev/null |
		grep -Eq "^${MARKETPLACE_NAME}[[:space:]]"
}

# installed_plugin_version <claude|codex> <plugin> — version of the newest
# version-pinned cache entry for <plugin> (the cache directory is named after
# the version). Prints nothing when no cache entry exists, e.g. before the
# plugin's first install or on older CLIs without a version-pinned cache.
installed_plugin_version() {
	local client="$1" plugin="$2" newest="" manifest root
	for manifest in "$HOME/.$client/plugins/cache"/*/"$plugin"/*/".$client-plugin"/plugin.json; do
		[ -f "$manifest" ] || continue
		if [ -z "$newest" ] || [ "$manifest" -nt "$newest" ]; then
			newest="$manifest"
		fi
	done
	[ -n "$newest" ] || return 0
	root="${newest%/*/*}"
	printf '%s\n' "${root##*/}"
}

# ok_updated <label> <plugin> <old-version> <new-version> — success line for a
# plugin update, showing the version transition when the version-pinned cache
# reveals it ("1.2.3 → 1.2.4", or "(already up to date)" when nothing new was
# pulled). Falls back to the plain wording when no version is known.
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
		"$HOME"/.claude/plugins/marketplaces/*/"$rel" \
		"$HOME"/.codex/plugins/marketplaces/*/"$rel"; do
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

run_doctor() {
	DOCTOR_FAILED=0
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
			if plugin_installed "$p"; then ok "Claude: $p"; else warn "Claude: $p not installed"; fi
		done
	fi
	if [ "$HAS_CODEX" -eq 1 ]; then
		for p in $ALL_PLUGINS; do
			if codex_plugin_installed "$p"; then ok "Codex: $p"; else warn "Codex: $p not installed"; fi
		done
	fi

	say ""
	say "  ${BOLD}Launchers and container image${RESET}"
	check "cenci-installer utility" optional "re-run the installer to create it" command -v cenci-installer
	if [ "$HAS_CLAUDE" -eq 1 ] || [ "$HAS_CODEX" -eq 1 ]; then
		check "cn launcher (cenci open)" optional "re-run the installer to create it" command -v cn
	fi
	if runtime="$(container_runtime 2>/dev/null)"; then
		check "cenci-sandbox:latest image" optional "build it with: cenci sandbox build" \
			"$runtime" image inspect cenci-sandbox:latest
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
	step "Registering the cenci marketplace"
	if [ "$HAS_CLAUDE" -eq 0 ]; then
		:
	elif marketplace_registered; then
		# Registration alone doesn't mean the checkout is current — refresh it so
		# find_plugin_path (cenci-installer launcher, macOS/Linux widget
		# scripts) sees files added since the last update.
		if claude plugin marketplace update "$MARKETPLACE_NAME" >/dev/null 2>&1; then
			ok "Claude: marketplace '$MARKETPLACE_NAME' refreshed"
		else
			warn "Claude: could not refresh marketplace '$MARKETPLACE_NAME' — it may be stale. Run manually: claude plugin marketplace update $MARKETPLACE_NAME"
		fi
		CLAUDE_MARKETPLACE_READY=1
	elif claude plugin marketplace add "$MARKETPLACE_REPO" >/dev/null 2>&1; then
		ok "Claude: registered $MARKETPLACE_REPO"
		CLAUDE_MARKETPLACE_READY=1
	else
		die "could not register the marketplace. Run manually to see the error:
  claude plugin marketplace add $MARKETPLACE_REPO"
	fi

	[ "$HAS_CODEX" -eq 1 ] || return 0
	if codex_marketplace_registered; then
		if codex plugin marketplace upgrade "$MARKETPLACE_NAME" >/dev/null 2>&1; then
			ok "Codex: marketplace '$MARKETPLACE_NAME' refreshed"
		else
			warn "Codex: could not refresh marketplace '$MARKETPLACE_NAME' — it may be stale. Run manually: codex plugin marketplace upgrade $MARKETPLACE_NAME"
		fi
		CODEX_MARKETPLACE_READY=1
	elif codex plugin marketplace add "$MARKETPLACE_REPO" >/dev/null 2>&1; then
		ok "Codex: registered $MARKETPLACE_REPO"
		CODEX_MARKETPLACE_READY=1
	else
		fail "Codex marketplace registration failed. Run manually: codex plugin marketplace add $MARKETPLACE_REPO"
		INSTALL_FAILED=1
	fi
}

step_install_plugins() {
	step "Installing plugins"
	if [ "$CLAUDE_MARKETPLACE_READY" -eq 1 ]; then
		for p in $SELECTED; do
			if plugin_installed "$p"; then
				ok "Claude: $p already installed (run 'cenci update' to update)"
				continue
			fi
			if plugin_cmd install "$p"; then
				ok "Claude: $p installed"
			else
				fail "Claude: $p failed to install. Run manually: claude plugin install $p@$MARKETPLACE_NAME"
				INSTALL_FAILED=1
			fi
		done
	fi

	[ "$CODEX_MARKETPLACE_READY" -eq 1 ] || return 0
	for p in $SELECTED; do
		if codex_plugin_installed "$p"; then
			ok "Codex: $p already installed (run 'cenci update' to update)"
			continue
		fi
		if codex plugin add "$p@$MARKETPLACE_NAME" >/dev/null 2>&1; then
			ok "Codex: $p installed"
		else
			fail "Codex: $p failed to install. Run manually: codex plugin add $p@$MARKETPLACE_NAME"
			INSTALL_FAILED=1
		fi
	done
}

step_update_plugins() {
	local p old
	step "Updating plugins"
	if [ "$HAS_CLAUDE" -eq 1 ]; then
		claude plugin marketplace update "$MARKETPLACE_NAME" >/dev/null 2>&1 || true
		for p in $SELECTED; do
			if ! plugin_installed "$p"; then
				warn "Claude: $p is not installed — skipping (run './install.sh' to install it)"
				continue
			fi
			old="$(installed_plugin_version claude "$p")"
			if plugin_cmd update "$p"; then
				ok_updated "Claude" "$p" "$old" "$(installed_plugin_version claude "$p")"
			else
				fail "Claude: $p failed to update. Run manually: claude plugin update $p@$MARKETPLACE_NAME"
				INSTALL_FAILED=1
			fi
		done
	fi

	[ "$HAS_CODEX" -eq 1 ] || return 0
	if ! codex_marketplace_registered; then
		warn "Codex marketplace is not registered — skipping Codex updates (run './install.sh' to install it)"
		return 0
	fi
	if ! codex plugin marketplace upgrade "$MARKETPLACE_NAME" >/dev/null 2>&1; then
		fail "Codex marketplace update failed. Run manually: codex plugin marketplace upgrade $MARKETPLACE_NAME"
		INSTALL_FAILED=1
		return 0
	fi
	for p in $SELECTED; do
		if ! codex_plugin_installed "$p"; then
			warn "Codex: $p is not installed — skipping"
			continue
		fi
		# `plugin add` is idempotent and refreshes the installed cache from the
		# newly upgraded marketplace snapshot.
		old="$(installed_plugin_version codex "$p")"
		if codex plugin add "$p@$MARKETPLACE_NAME" >/dev/null 2>&1; then
			ok_updated "Codex" "$p" "$old" "$(installed_plugin_version codex "$p")"
		else
			fail "Codex: $p failed to update. Run manually: codex plugin add $p@$MARKETPLACE_NAME"
			INSTALL_FAILED=1
		fi
	done
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
	mkdir -p "$HOME/.local/bin"
	if [ -e "$dest" ] && [ ! -L "$dest" ]; then
		warn "$dest exists and is not a symlink — left untouched"
		return 1
	fi
	ln -sf "$target" "$dest"
	ok "linked $name → ~/.local/bin/$name"
}

step_cli_setup() {
	step "Setting up the cenci installer command"

	local cli
	if ! cli=$(find_plugin_path "cenci"); then
		warn "could not find the cenci installer command in the marketplace checkout — re-run the installer after refreshing the marketplace"
		return 0
	fi
	link_launcher cenci-installer "$cli" || true
}

step_sandbox_setup() {
	selected cenci-sandbox || return 0
	step "Setting up the sandbox launcher"

	# The launcher is the cenci binary itself (cenci open / cenci sandbox).
	# `cn` is its one alias: a symlink chained through the bootstrap-maintained
	# ~/.local/bin/cenci link, so it keeps resolving across version bumps (and
	# becomes valid on the first agent session even if created dangling here).
	link_launcher cn "$HOME/.local/bin/cenci" || true

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
	if [ "$BUILD_IMAGE" = ask ]; then
		if ! ask_yn "Build the sandbox container image now with $runtime? (takes a few minutes)" y; then
			say "  ${DIM}skipped — run 'cenci sandbox build' when ready${RESET}"
			return 0
		fi
	fi

	# The build runs through the cenci binary (it resolves the image assets
	# from the installed cenci-sandbox plugin). Provision it from the plugin
	# cache if the first agent session hasn't bootstrapped it yet.
	local cenci_bin
	if ! cenci_bin="$(current_cenci_binary)"; then
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
			PLUGIN_ROOT="$root" sh "$bootstrap" >/dev/null 2>&1 || true
		else
			CLAUDE_PLUGIN_ROOT="$root" sh "$bootstrap" >/dev/null 2>&1 || true
		fi
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
		warn "pkill/pgrep unavailable — restart cenci manually to finish the update"
		return 0
	fi

	pkill -TERM -f '[/]cenci daemon( start)?$' >/dev/null 2>&1 || true
	i=0
	while pgrep -f '[/]cenci daemon( start)?$' >/dev/null 2>&1 && [ "$i" -lt 30 ]; do
		sleep 0.1
		i=$((i + 1))
	done
	if pgrep -f '[/]cenci daemon( start)?$' >/dev/null 2>&1; then
		fail "the previous cenci daemon did not stop; restart it manually"
		INSTALL_FAILED=1
		return 0
	fi

	if [ -z "$bin" ] || [ ! -x "$bin" ]; then
		warn "no usable cenci binary to restart — the next agent session will bootstrap it"
		return 0
	fi

	nohup "$bin" daemon >/dev/null 2>&1 &
	pid=$!
	sleep 0.2
	if kill -0 "$pid" 2>/dev/null; then
		ok "restarted cenci with the updated binary"
	else
		fail "the updated cenci daemon did not stay running"
		INSTALL_FAILED=1
	fi
}

step_cenci_watch_setup() {
	selected cenci-watch || return 0
	step "Setting up cenci"
	local cache_bin=""
	if [ "$MODE" = update ]; then
		cache_bin="$(current_cenci_binary || true)"
	else
		cache_bin="$(cached_cenci_binary || true)"
	fi

	ok "the binary and daemon self-bootstrap on your first agent session"
	say "  ${DIM}first session may take a moment before status appears; log: \${TMPDIR:-/tmp}/cenci-bootstrap.log${RESET}"

	if [ "$OS" != macos ]; then
		setup_cenci_linux_path "$cache_bin"
		setup_cenci_linux_widgets
		if [ "$MODE" = update ] && [ -n "$cache_bin" ]; then
			restart_cenci_daemon "$cache_bin"
		elif [ "$MODE" = update ]; then
			warn "updated cenci binary is not available yet; the next agent session will bootstrap it"
		fi
		return 0
	fi
	if [ "$MODE" = update ] && [ -n "$cache_bin" ]; then
		restart_cenci_daemon "$cache_bin"
	elif [ "$MODE" = update ]; then
		warn "updated cenci binary is not available yet; the next agent session will bootstrap it"
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
		link_launcher cenci "$cache_bin" || true
	else
		say "  ${DIM}cenci binary not in the plugin cache yet — the first agent session links it onto ~/.local/bin automatically${RESET}"
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
	if [ "$MODE" = update ]; then
		say "  Updated: ${BOLD}${SELECTED}${RESET}"
	else
		say "  Installed: ${BOLD}${SELECTED}${RESET}"
	fi
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
	say ""
	say "  Check installation health: ${BOLD}cenci doctor${RESET}"
	say "  Update everything later:  ${BOLD}cenci update${RESET}"
	say "  Docs: https://github.com/$MARKETPLACE_REPO/blob/main/docs/getting-started.md"
}

# ------------------------------------------------------------------- main ----

MODE=install
BUILD_IMAGE=ask
INSTALL_FAILED=0

usage() {
	cat <<'EOF'
cenci installer — one command for the whole package.

Usage:
  cenci-installer              interactive installer / repair
  cenci-installer update       update installed plugins (+ optional rebuild)
  cenci-installer doctor       check prerequisites, change nothing

Initial install:
  curl -fsSL https://raw.githubusercontent.com/matteobortolazzo/cenci/main/install.sh | bash

From a source checkout, ./install.sh accepts the same arguments.

Flags:
  --yes                                 accept defaults, never prompt
  --build / --no-build                  force / skip the sandbox image build
  --help                                this text
EOF
}

while [ $# -gt 0 ]; do
	case "$1" in
	update | --update) MODE=update ;;
	doctor | --doctor) MODE=doctor ;;
	--yes | -y) ASSUME_YES=1 ;;
	--build) BUILD_IMAGE=yes ;;
	--no-build) BUILD_IMAGE=no ;;
	--help | -h)
		usage
		exit 0
		;;
	*) die "unknown option '$1' (see --help)" ;;
	esac
	shift
done

# Non-interactive runs never start a minutes-long image build unless asked.
if [ "$BUILD_IMAGE" = ask ] && { [ "$INTERACTIVE" -eq 0 ] || [ "$ASSUME_YES" -eq 1 ]; }; then
	BUILD_IMAGE=no
fi

detect_platform
detect_clients

say ""
say "${BOLD}cenci installer${RESET} — $(platform_label)"

if [ "$MODE" = doctor ]; then
	run_doctor
	exit $?
fi

have_supported_client || die "no supported client found. Install Claude Code, Codex, or both, then re-run this script."

if [ "$MODE" = update ]; then
	step_update_plugins
	prune_selected_to_installed
	step_cli_setup
	step_sandbox_setup
	step_cenci_watch_setup
	final_summary
	exit $((INSTALL_FAILED))
fi

run_doctor || {
	if ! ask_yn "Continue anyway?" n; then
		exit 1
	fi
}
step_marketplace
step_install_plugins
prune_selected_to_installed
step_cli_setup
step_sandbox_setup
step_cenci_watch_setup
step_cenci_notes
final_summary
exit $((INSTALL_FAILED))
