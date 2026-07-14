#!/usr/bin/env bash
# agent-stack installer — one command for the whole package.
#
# Installs or updates the three agent-stack plugins (agentflow, agentwatch,
# agent-sandbox) as a single system: registers the marketplace, installs the
# plugins, and runs the post-install setup that used to be manual (agent-sandbox
# launcher symlink + image build, macOS menu bar and Linux desktop bar-widget
# wiring).
#
# Usage:
#   ./install.sh                interactive wizard (install)
#   agent-stack update          update installed plugins (+ optional rebuild)
#   agent-stack doctor          check prerequisites, change nothing
#
#   curl -fsSL https://raw.githubusercontent.com/matteobortolazzo/agent-stack/main/install.sh | bash
#
# Flags:
#   --yes                                 accept defaults, never prompt
#   --build / --no-build                  force / skip the sandbox image build
#   --help                                this text

set -u

MARKETPLACE_REPO="matteobortolazzo/agent-stack"
MARKETPLACE_NAME="agent-stack"
ALL_PLUGINS="agentflow agentwatch agent-sandbox"
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
	*) die "unsupported OS '$(uname -s)' — agent-stack supports Linux, macOS, and WSL2" ;;
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
	dev-sandbox/*)
		rel=${rel#dev-sandbox/}
		for f in \
			"$HOME"/.claude/plugins/cache/*/agent-sandbox/*/"$rel" \
			"$HOME"/.codex/plugins/cache/*/agent-sandbox/*/"$rel"; do
			[ -e "$f" ] && { printf '%s\n' "$f"; return 0; }
		done
		;;
	agentwatch/plugin/*)
		rel=${rel#agentwatch/plugin/}
		for f in \
			"$HOME"/.claude/plugins/cache/*/agentwatch/*/"$rel" \
			"$HOME"/.codex/plugins/cache/*/agentwatch/*/"$rel"; do
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
	say "  ${BOLD}For agentflow (workflow)${RESET}"
	check "gh (GitHub CLI)" optional \
		"needed for issues/PRs: https://cli.github.com" command -v gh
	if have gh; then
		check "gh authenticated" optional "run: gh auth login" gh auth status
	fi

	say ""
	say "  ${BOLD}For agentwatch (attention)${RESET}"
	check "tmux" optional \
		"agentwatch's main frontend is the tmux status bar; other surfaces (waybar, macOS menu bar) still work without it" \
		command -v tmux
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
			ok "waybar detected — add the AgentWatch module (see agentwatch/README.md)"
		fi
	fi

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
	check "agent-stack utility" optional "re-run the installer to create it" command -v agent-stack
	if [ "$HAS_CLAUDE" -eq 1 ]; then
		check "agent-sand launcher" optional "re-run the installer to create it" command -v agent-sand
	fi
	if [ "$HAS_CLAUDE" -eq 1 ] || [ "$HAS_CODEX" -eq 1 ]; then
		check "sb launcher" optional "re-run the installer to create it" command -v sb
	fi
	if runtime="$(container_runtime 2>/dev/null)"; then
		check "agent-sandbox:latest image" optional "build it with the installed sandbox launcher --build" \
			"$runtime" image inspect agent-sandbox:latest
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
	step "Registering the agent-stack marketplace"
	if [ "$HAS_CLAUDE" -eq 0 ]; then
		:
	elif marketplace_registered; then
		# Registration alone doesn't mean the checkout is current — refresh it so
		# find_plugin_path (agent-stack launcher, agent-sand, agentwatch macOS
		# script) sees files added since the last update.
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
				ok "Claude: $p already installed (run 'agent-stack update' to update)"
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
			ok "Codex: $p already installed (run 'agent-stack update' to update)"
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
	step "Updating plugins"
	if [ "$HAS_CLAUDE" -eq 1 ]; then
		claude plugin marketplace update "$MARKETPLACE_NAME" >/dev/null 2>&1 || true
		for p in $SELECTED; do
			if ! plugin_installed "$p"; then
				warn "Claude: $p is not installed — skipping (run './install.sh' to install it)"
				continue
			fi
			if plugin_cmd update "$p"; then
				ok "Claude: $p updated"
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
		if codex plugin add "$p@$MARKETPLACE_NAME" >/dev/null 2>&1; then
			ok "Codex: $p updated"
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
	step "Setting up the agent-stack command"

	local cli
	if ! cli=$(find_plugin_path "agent-stack"); then
		warn "could not find the agent-stack command in the marketplace checkout — re-run the installer after refreshing the marketplace"
		return 0
	fi
	link_launcher agent-stack "$cli" || true
}

step_sandbox_setup() {
	selected agent-sandbox || return 0
	step "Setting up the agent-sandbox launcher"

	local launcher launcher_name="sb"
	if ! launcher=$(find_plugin_path "dev-sandbox/agent-sand"); then
		warn "could not find the installed agent-sandbox plugin cache — re-run the installer after restarting your client"
		return 0
	fi

	if [ "$HAS_CLAUDE" -eq 1 ]; then link_launcher agent-sand "$launcher" || true; fi
	link_launcher sb "$launcher" || true

	case ":$PATH:" in
	*":$HOME/.local/bin:"*) ;;
	*) warn "$HOME/.local/bin is not on your PATH — add it to your shell profile:
      export PATH=\"\$HOME/.local/bin:\$PATH\"" ;;
	esac

	local runtime
	if ! runtime=$(container_runtime); then
		if [ "$OS" = macos ]; then
			fail "no container runtime found — install Docker Desktop (https://docker.com/products/docker-desktop) or Podman, then run: $launcher_name --build"
		else
			fail "no container runtime found — install docker or podman, then run: $launcher_name --build"
		fi
		INSTALL_FAILED=1
		return 0
	fi

	if [ "$BUILD_IMAGE" = no ]; then
		say "  ${DIM}skipping image build — run '$launcher_name --build' when ready${RESET}"
		return 0
	fi
	if [ "$BUILD_IMAGE" = ask ]; then
		if ! ask_yn "Build the sandbox container image now with $runtime? (takes a few minutes)" y; then
			say "  ${DIM}skipped — run '$launcher_name --build' when ready${RESET}"
			return 0
		fi
	fi
	say "  building agent-sandbox:latest with $runtime (this can take a few minutes)…"
	if "$HOME/.local/bin/$launcher_name" --build; then
		ok "sandbox image built"
	else
		fail "image build failed — fix the error above and re-run: $launcher_name --build"
		INSTALL_FAILED=1
	fi
}

# newest_agentwatch_root — path to the most recently installed version-pinned
# Claude plugin cache root. Plugin updates refresh the active manifest, making
# it a reliable selector even before that version's binary has bootstrapped.
newest_agentwatch_root() {
	local newest_manifest="" manifest
	for manifest in \
		"$HOME"/.claude/plugins/cache/*/agentwatch/*/.claude-plugin/plugin.json \
		"$HOME"/.codex/plugins/cache/*/agentwatch/*/.codex-plugin/plugin.json; do
		[ -f "$manifest" ] || continue
		if [ -z "$newest_manifest" ] || [ "$manifest" -nt "$newest_manifest" ]; then
			newest_manifest="$manifest"
		fi
	done
	[ -n "$newest_manifest" ] && dirname "$(dirname "$newest_manifest")"
}

# cached_agentwatch_binary returns the binary belonging to the active plugin
# cache when SessionStart has already provisioned it.
cached_agentwatch_binary() {
	local root bin
	root="$(newest_agentwatch_root || true)"
	[ -n "$root" ] || return 1
	bin="$root/bin/agentwatch"
	[ -x "$bin" ] || return 1
	printf '%s\n' "$bin"
}

# current_agentwatch_binary provisions and returns the binary belonging to the
# active plugin cache. Updates cannot rely on a later SessionStart hook: an old
# daemon may continue owning the sockets indefinitely.
current_agentwatch_binary() {
	local root bootstrap root_var
	if cached_agentwatch_binary; then
		return 0
	fi
	root="$(newest_agentwatch_root || true)"
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
	cached_agentwatch_binary
}

# restart_agentwatch_daemon replaces the standard plugin-managed daemon after
# an explicit update. SIGTERM lets it restore tmux state and remove its sockets
# before the updated binary starts.
restart_agentwatch_daemon() {
	local bin="$1" i pid
	if ! have pkill || ! have pgrep; then
		warn "pkill/pgrep unavailable — restart agentwatch manually to finish the update"
		return 0
	fi

	pkill -TERM -f '[/]agentwatch daemon$' >/dev/null 2>&1 || true
	i=0
	while pgrep -f '[/]agentwatch daemon$' >/dev/null 2>&1 && [ "$i" -lt 30 ]; do
		sleep 0.1
		i=$((i + 1))
	done
	if pgrep -f '[/]agentwatch daemon$' >/dev/null 2>&1; then
		fail "the previous agentwatch daemon did not stop; restart it manually"
		INSTALL_FAILED=1
		return 0
	fi

	nohup "$bin" daemon >/dev/null 2>&1 &
	pid=$!
	sleep 0.2
	if kill -0 "$pid" 2>/dev/null; then
		ok "restarted agentwatch with the updated binary"
	else
		fail "the updated agentwatch daemon did not stay running"
		INSTALL_FAILED=1
	fi
}

step_agentwatch_setup() {
	selected agentwatch || return 0
	step "Setting up agentwatch"
	local cache_bin=""
	if [ "$MODE" = update ]; then
		cache_bin="$(current_agentwatch_binary || true)"
	else
		cache_bin="$(cached_agentwatch_binary || true)"
	fi

	ok "the binary and daemon self-bootstrap on your first agent session"
	say "  ${DIM}first session may take a moment before status appears; log: \${TMPDIR:-/tmp}/agentwatch-bootstrap.log${RESET}"

	if [ "$OS" != macos ]; then
		setup_agentwatch_linux_path "$cache_bin"
		setup_agentwatch_linux_widgets
		if [ "$MODE" = update ] && [ -n "$cache_bin" ]; then
			restart_agentwatch_daemon "$cache_bin"
		elif [ "$MODE" = update ]; then
			warn "updated agentwatch binary is not available yet; the next agent session will bootstrap it"
		fi
		return 0
	fi
	if [ "$MODE" = update ] && [ -n "$cache_bin" ]; then
		restart_agentwatch_daemon "$cache_bin"
	elif [ "$MODE" = update ]; then
		warn "updated agentwatch binary is not available yet; the next agent session will bootstrap it"
	fi

	# macOS menu bar (SwiftBar) — optional, and the fiddliest manual step, so
	# offer to wire it up here. Delegate to the widget's self-contained
	# install.sh, which sets SwiftBar's Plugin Folder, symlinks the plugin, and
	# reloads SwiftBar. Re-runs on update so widget changes take effect.
	local script
	if ! script=$(find_plugin_path "agentwatch/plugin/macos/install.sh"); then
		return 0
	fi
	if [ ! -d /Applications/SwiftBar.app ]; then
		say "  ${DIM}optional: menu bar status via SwiftBar — brew install swiftbar, then re-run this script${RESET}"
		return 0
	fi
	if ! ask_yn "SwiftBar detected — install the agentwatch menu bar widget and reload it?" y; then
		say "  ${DIM}skipped — see agentwatch/plugin/macos/README.md to wire it manually${RESET}"
		return 0
	fi
	chmod +x "$script" 2>/dev/null || true
	if bash "$script"; then
		ok "menu bar widget installed and reloaded"
	else
		warn "SwiftBar widget setup failed — see agentwatch/plugin/macos/README.md"
		INSTALL_FAILED=1
	fi
}

# setup_agentwatch_linux_path makes bar widgets (DMS, noctalia, waybar) able to
# resolve a bare `agentwatch`. The binary lives in the version-pinned plugin
# cache (~/.claude/plugins/cache/.../agentwatch/<version>/bin), which is on no
# login PATH — so a widget spawned by the compositor can't find it and hides
# itself. We keep a stable link on the user's writable PATH and, for GUI bars
# that don't inherit ~/.local/bin, offer a one-time /usr/local/bin link.
setup_agentwatch_linux_path() {
	local cache_bin="$1" user_link="$HOME/.local/bin/agentwatch"

	# Ensure the bootstrap-maintained user link exists now. The plugin bootstrap
	# re-points it on version bumps, so pinning the current cache path is fine;
	# if the binary isn't cached yet, the first agent session creates the link.
	if [ -n "$cache_bin" ]; then
		link_launcher agentwatch "$cache_bin" || true
	else
		say "  ${DIM}agentwatch binary not in the plugin cache yet — the first agent session links it onto ~/.local/bin automatically${RESET}"
	fi

	case ":$PATH:" in
	*":$HOME/.local/bin:"*) ;;
	*) warn "$HOME/.local/bin is not on your PATH — add it to your shell profile:
      export PATH=\"\$HOME/.local/bin:\$PATH\"" ;;
	esac

	# GUI/compositor bars inherit the login PATH, which usually lacks
	# ~/.local/bin but always includes /usr/local/bin. A root link there, chained
	# through the bootstrap-maintained ~/.local/bin link, lets them resolve
	# agentwatch and survives version bumps with no re-run.
	if [ -L /usr/local/bin/agentwatch ] &&
		[ "$(readlink /usr/local/bin/agentwatch 2>/dev/null)" = "$user_link" ]; then
		ok "/usr/local/bin/agentwatch already links to ~/.local/bin/agentwatch (GUI bars can resolve it)"
		return 0
	fi
	if [ -e /usr/local/bin/agentwatch ] && [ ! -L /usr/local/bin/agentwatch ]; then
		warn "/usr/local/bin/agentwatch exists and is not a symlink — left untouched; remove it manually or configure the widget's agentwatch path"
		return 0
	fi

	local manual="sudo ln -sf \"$user_link\" /usr/local/bin/agentwatch"
	if [ "$INTERACTIVE" -eq 1 ] &&
		ask_yn "Link agentwatch into /usr/local/bin so GUI bar widgets (DMS, noctalia) can find it? (one-time sudo)" y; then
		if sudo ln -sf "$user_link" /usr/local/bin/agentwatch; then
			ok "linked /usr/local/bin/agentwatch → ~/.local/bin/agentwatch"
		else
			warn "could not create the /usr/local/bin link — run it yourself:
      $manual"
		fi
	else
		say "  ${DIM}skipped the GUI-bar PATH link. If a bar widget stays hidden, run:${RESET}"
		say "      $manual"
		say "  ${DIM}or point the widget at the binary directly (agentwatchPath for DMS/noctalia, AGENTWATCH_BIN for SwiftBar).${RESET}"
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

# setup_agentwatch_linux_widgets detects each present GUI bar and delegates to
# that widget's self-contained install.sh, which (re)installs and reloads it.
# Runs on both install and update — re-running is what refreshes the widget and
# reloads the bar, so widget changes become visible after `agent-stack update`.
# Restarting a running panel is disruptive, so each bar is gated behind its own
# prompt (default yes).
setup_agentwatch_linux_widgets() {
	local de label script
	for de in gnome plasma dms noctalia; do
		de_detected "$de" || continue
		label="$(de_label "$de")"
		if ! ask_yn "$label detected — install the AgentWatch widget and reload it?" y; then
			say "  ${DIM}skipped the $label widget — see agentwatch/plugin/$de/README.md to wire it manually${RESET}"
			continue
		fi
		if ! script=$(find_plugin_path "agentwatch/plugin/$de/install.sh"); then
			warn "could not find agentwatch/plugin/$de/install.sh in the marketplace checkout — re-run after refreshing the marketplace"
			INSTALL_FAILED=1
			continue
		fi
		chmod +x "$script" 2>/dev/null || true
		if bash "$script"; then
			ok "$label widget installed and reloaded"
		else
			warn "$label widget setup failed — see agentwatch/plugin/$de/README.md"
			INSTALL_FAILED=1
		fi
	done

	# waybar has no bundled widget — its config is hand-managed. Point at the
	# docs and the live-reload signal; write nothing.
	if have waybar; then
		say "  ${DIM}waybar detected — add the AgentWatch module from agentwatch/README.md (Waybar section),${RESET}"
		say "  ${DIM}then reload waybar to apply: pkill -SIGUSR2 waybar${RESET}"
	fi
}

step_agentflow_notes() {
	selected agentflow || return 0
	[ "$HAS_CLAUDE" -eq 1 ] || return 0
	step "agentflow next steps"
	if have gh && gh auth status >/dev/null 2>&1; then
		ok "GitHub CLI is authenticated"
	else
		warn "agentflow drives GitHub issues and PRs through the gh CLI — run: gh auth login"
	fi
	say "  then, in each project you want to use it in, run ${BOLD}/agentflow:configure${RESET} once inside Claude Code"
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
	if selected agent-sandbox; then
		if [ "$HAS_CLAUDE" -eq 1 ]; then
			say "    agent-sand                # Claude Code inside the container"
		fi
		if [ "$HAS_CLAUDE" -eq 1 ]; then
			say "    sb ch|cs|co|cf             # Claude in the container: haiku/sonnet/opus/fable"
		fi
		if [ "$HAS_CODEX" -eq 1 ]; then
			say "    sb xl|xt|xs                # Codex in the container: luna/terra/sol"
		fi
	fi
	if selected agentflow && [ "$HAS_CLAUDE" -eq 1 ]; then
		say "    claude → /agentflow:configure # one-time project setup"
	fi
	if selected agentflow && [ "$HAS_CODEX" -eq 1 ]; then
		say "    codex                       # portable agentflow conventions are available"
	fi
	if selected agentwatch; then
		say "    (start a supported agent session — status appears in configured surfaces)"
	fi
	say ""
	say "  Check installation health: ${BOLD}agent-stack doctor${RESET}"
	say "  Update everything later:  ${BOLD}agent-stack update${RESET}"
	say "  Docs: https://github.com/$MARKETPLACE_REPO/blob/main/docs/getting-started.md"
}

# ------------------------------------------------------------------- main ----

MODE=install
BUILD_IMAGE=ask
INSTALL_FAILED=0

usage() {
	cat <<'EOF'
agent-stack installer — one command for the whole package.

Usage:
  agent-stack                 interactive installer / repair
  agent-stack update          update installed plugins (+ optional rebuild)
  agent-stack doctor          check prerequisites, change nothing

Initial install:
  curl -fsSL https://raw.githubusercontent.com/matteobortolazzo/agent-stack/main/install.sh | bash

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
say "${BOLD}agent-stack installer${RESET} — $(platform_label)"

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
	step_agentwatch_setup
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
step_agentwatch_setup
step_agentflow_notes
final_summary
exit $((INSTALL_FAILED))
