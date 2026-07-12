#!/usr/bin/env bash
# agent-stack installer — one command for the whole package.
#
# Installs or updates the three agent-stack plugins (agentflow, agentwatch,
# agent-sandbox) as a single system: registers the marketplace, installs the
# plugins, and runs the post-install setup that used to be manual (agent-sandbox
# launcher symlink + image build, macOS menu bar wiring).
#
# Usage:
#   ./install.sh                interactive wizard (install)
#   ./install.sh update         update installed plugins (+ optional rebuild)
#   ./install.sh doctor         check prerequisites, change nothing
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
	for f in "$HOME"/.claude/plugins/marketplaces/*/"$rel"; do
		[ -e "$f" ] && { printf '%s\n' "$f"; return 0; }
	done
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

	say "  ${BOLD}Required${RESET}"
	check "claude CLI" required \
		"install Claude Code first: https://code.claude.com/docs/en/overview" \
		command -v claude
	check "git" required "install git from your package manager" command -v git
	if [ "$OS" = macos ]; then
		check "Docker or Podman" required \
			"install Docker Desktop (https://docker.com/products/docker-desktop) or Podman" \
			container_runtime
	else
		check "Docker or Podman" required \
			"install docker or podman from your package manager" container_runtime
	fi
	check "codex CLI" optional \
		"install Codex to add the same plugins there; Claude setup still works without it" \
		command -v codex

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
	if marketplace_registered; then
		ok "Claude: marketplace '$MARKETPLACE_NAME' already registered"
	elif claude plugin marketplace add "$MARKETPLACE_REPO" >/dev/null 2>&1; then
		ok "Claude: registered $MARKETPLACE_REPO"
	else
		die "could not register the marketplace. Run manually to see the error:
  claude plugin marketplace add $MARKETPLACE_REPO"
	fi

	have codex || return 0
	if codex_marketplace_registered; then
		ok "Codex: marketplace '$MARKETPLACE_NAME' already registered"
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
	for p in $SELECTED; do
		if plugin_installed "$p"; then
			ok "Claude: $p already installed (run './install.sh update' to update)"
			continue
		fi
		if plugin_cmd install "$p"; then
			ok "Claude: $p installed"
		else
			fail "Claude: $p failed to install. Run manually: claude plugin install $p@$MARKETPLACE_NAME"
			INSTALL_FAILED=1
		fi
	done

	[ "$CODEX_MARKETPLACE_READY" -eq 1 ] || return 0
	for p in $SELECTED; do
		if codex_plugin_installed "$p"; then
			ok "Codex: $p already installed (run './install.sh update' to update)"
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
	claude plugin marketplace update "$MARKETPLACE_NAME" >/dev/null 2>&1 || true
	for p in $SELECTED; do
		if ! plugin_installed "$p"; then
			warn "$p is not installed — skipping (run './install.sh' to install it)"
			continue
		fi
		if plugin_cmd update "$p"; then
			ok "Claude: $p updated"
		else
			fail "Claude: $p failed to update. Run manually: claude plugin update $p@$MARKETPLACE_NAME"
			INSTALL_FAILED=1
		fi
	done

	have codex || return 0
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
		plugin_installed "$p" && kept="$kept $p"
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

step_sandbox_setup() {
	selected agent-sandbox || return 0
	step "Setting up the agent-sandbox launcher"

	local launcher
	if ! launcher=$(find_plugin_path "dev-sandbox/agent-sand"); then
		warn "could not find the installed agent-sandbox plugin — run /agent-sandbox:setup inside Claude Code instead"
		return 0
	fi

	link_launcher agent-sand "$launcher" || true
	link_launcher codex-sand "$launcher" || true

	case ":$PATH:" in
	*":$HOME/.local/bin:"*) ;;
	*) warn "$HOME/.local/bin is not on your PATH — add it to your shell profile:
      export PATH=\"\$HOME/.local/bin:\$PATH\"" ;;
	esac

	local runtime
	if ! runtime=$(container_runtime); then
		if [ "$OS" = macos ]; then
			fail "no container runtime found — install Docker Desktop (https://docker.com/products/docker-desktop) or Podman, then run: agent-sand --build"
		else
			fail "no container runtime found — install docker or podman, then run: agent-sand --build"
		fi
		INSTALL_FAILED=1
		return 0
	fi

	if [ "$BUILD_IMAGE" = no ]; then
		say "  ${DIM}skipping image build — run 'agent-sand --build' when ready${RESET}"
		return 0
	fi
	if [ "$BUILD_IMAGE" = ask ]; then
		if ! ask_yn "Build the sandbox container image now with $runtime? (takes a few minutes)" y; then
			say "  ${DIM}skipped — run 'agent-sand --build' when ready${RESET}"
			return 0
		fi
	fi
	say "  building agent-sandbox:latest with $runtime (this can take a few minutes)…"
	if "$HOME/.local/bin/agent-sand" --build; then
		ok "sandbox image built"
	else
		fail "image build failed — fix the error above and re-run: agent-sand --build"
		INSTALL_FAILED=1
	fi
}

# newest_cache_binary — path to the most recent agentwatch binary in the
# version-pinned plugin cache, or empty if none is present yet (the plugin
# bootstrap installs it on the first agent session). Mirrors the cache glob the
# macOS SwiftBar widget's resolve_bin uses.
newest_cache_binary() {
	local newest="" g
	for g in "$HOME"/.claude/plugins/cache/*/agentwatch/*/bin/agentwatch; do
		[ -x "$g" ] || continue
		if [ -z "$newest" ] || [ "$g" -nt "$newest" ]; then
			newest="$g"
		fi
	done
	[ -n "$newest" ] && printf '%s\n' "$newest"
}

step_agentwatch_setup() {
	selected agentwatch || return 0
	step "Setting up agentwatch"

	ok "the binary and daemon self-bootstrap on your first agent session"
	say "  ${DIM}first session may take a moment before status appears; log: \${TMPDIR:-/tmp}/agentwatch-bootstrap.log${RESET}"

	if [ "$OS" != macos ]; then
		setup_agentwatch_linux_path
		return 0
	fi

	# macOS menu bar (SwiftBar) — optional, and the fiddliest manual step, so
	# offer to wire it up here. SwiftBar's own resolve_bin already covers
	# /usr/local/bin and the plugin cache glob, so no PATH linking is needed.
	local script
	if ! script=$(find_plugin_path "agentwatch/plugin/macos/agentwatch.5s.sh"); then
		return 0
	fi
	if [ ! -d /Applications/SwiftBar.app ]; then
		say "  ${DIM}optional: menu bar status via SwiftBar — brew install swiftbar, then re-run this script${RESET}"
		return 0
	fi
	if ! ask_yn "SwiftBar detected — link the agentwatch menu bar widget into ~/SwiftBarPlugins?" y; then
		say "  ${DIM}skipped — see agentwatch/plugin/macos/README.md to wire it manually${RESET}"
		return 0
	fi
	mkdir -p "$HOME/SwiftBarPlugins"
	chmod +x "$script" 2>/dev/null || true
	ln -sf "$script" "$HOME/SwiftBarPlugins/agentwatch.5s.sh"
	ok "linked menu bar widget → ~/SwiftBarPlugins/agentwatch.5s.sh"
	say "  in SwiftBar: set the Plugin Folder to ~/SwiftBarPlugins (Preferences → Plugin Folder), then Refresh All"
}

# setup_agentwatch_linux_path makes bar widgets (DMS, noctalia, waybar) able to
# resolve a bare `agentwatch`. The binary lives in the version-pinned plugin
# cache (~/.claude/plugins/cache/.../agentwatch/<version>/bin), which is on no
# login PATH — so a widget spawned by the compositor can't find it and hides
# itself. We keep a stable link on the user's writable PATH and, for GUI bars
# that don't inherit ~/.local/bin, offer a one-time /usr/local/bin link.
setup_agentwatch_linux_path() {
	local user_link="$HOME/.local/bin/agentwatch" cache_bin
	cache_bin="$(newest_cache_binary || true)"

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

step_agentflow_notes() {
	selected agentflow || return 0
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
		say "    agent-sand                # Claude Code inside the container"
	fi
	if selected agentflow; then
		say "    claude → /agentflow:configure # one-time project setup"
		if have codex; then
			say "    codex                       # portable agentflow conventions are available"
		fi
	fi
	if selected agentwatch; then
		say "    (start any Claude Code session — status appears in the tmux bar)"
	fi
	say ""
	say "  Update everything later:  ${BOLD}./install.sh update${RESET}"
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
  ./install.sh                interactive wizard (install)
  ./install.sh update         update installed plugins (+ optional rebuild)
  ./install.sh doctor         check prerequisites, change nothing

  curl -fsSL https://raw.githubusercontent.com/matteobortolazzo/agent-stack/main/install.sh | bash

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

say ""
say "${BOLD}agent-stack installer${RESET} — $(platform_label)"

if [ "$MODE" = doctor ]; then
	run_doctor
	exit $?
fi

have claude || die "the 'claude' CLI is required. Install Claude Code first:
  https://code.claude.com/docs/en/overview
then re-run this script."

if [ "$MODE" = update ]; then
	step_update_plugins
	prune_selected_to_installed
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
step_sandbox_setup
step_agentwatch_setup
step_agentflow_notes
final_summary
exit $((INSTALL_FAILED))
