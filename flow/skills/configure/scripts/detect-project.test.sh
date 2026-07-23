#!/usr/bin/env bash
# detect-project.test.sh — runtime behavior of configure's deterministic
# detection script. Modelled on resolve-babysit-interval.test.sh (a
# `failures=` counter, small assert_* helpers, mktemp -d fixture repos,
# self-contained, auto-discovered by the flow gate's `*.test.sh` glob).
#
# Contract pinned here: one JSON object on stdout covering platform,
# container, package manager, MCP/LSP/dind/Playwright catalog triggers, and
# the plugin version; per-detector failures degrade to empty results plus a
# warnings entry (exit 0), while a missing jq or unknown flag fails closed.
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DETECT="${SCRIPT_DIR}/detect-project.sh"
failures=0

fail() { echo "FAIL: $1" >&2; failures=$((failures+1)); }
assert_eq() { [[ "$1" == "$2" ]] || fail "$3: expected [$2], got [$1]"; }

# run_detect [extra args…] — runs from $ROOT with container detection
# neutralized (no CENCI_SANDBOX, dockerenv probe pointed at a missing path)
# unless the caller overrides via env before calling.
run_detect() {
  OUT="$(cd "${ROOT}" && env -u CENCI_SANDBOX -u CLAUDE_PLUGIN_ROOT \
    CENCI_DETECT_DOCKERENV_PATH="${ROOT}/.no-dockerenv" bash "${DETECT}" "$@" 2>&1)"
  CODE=$?
}

jq_out() { printf '%s' "${OUT}" | jq -r "$1"; }

make_repo() { ROOT="$(mktemp -d)"; git -C "${ROOT}" init -q; }

# --- Case 1: SSH GitHub remote → platform parsed, .git stripped -----------
make_repo
git -C "${ROOT}" remote add origin "git@github.com:acme/widgets.git"
run_detect
assert_eq "${CODE}" "0" "case1 exit"
printf '%s' "${OUT}" | jq -e 'type == "object"' >/dev/null 2>&1 || fail "case1 output must be a JSON object (got: ${OUT})"
assert_eq "$(jq_out '.platform.name')" "github" "case1 platform name"
assert_eq "$(jq_out '.platform.owner')" "acme" "case1 platform owner"
assert_eq "$(jq_out '.platform.repo')" "widgets" "case1 platform repo (.git stripped)"
rm -rf "${ROOT}"

# --- Case 2: HTTPS remote (no .git suffix) ---------------------------------
make_repo
git -C "${ROOT}" remote add origin "https://github.com/acme/widgets"
run_detect
assert_eq "$(jq_out '.platform.owner')" "acme" "case2 platform owner"
assert_eq "$(jq_out '.platform.repo')" "widgets" "case2 platform repo"
rm -rf "${ROOT}"

# --- Case 3: no remote → platform null; empty repo defaults ----------------
make_repo
run_detect
assert_eq "$(jq_out '.platform')" "null" "case3 platform null"
assert_eq "$(jq_out '.packageManager')" "null" "case3 packageManager null"
assert_eq "$(jq_out '.mcpServers | length')" "0" "case3 no MCP triggers"
assert_eq "$(jq_out '.lspServers | length')" "0" "case3 no LSP triggers"
assert_eq "$(jq_out '.dindDetected')" "false" "case3 no dind"
assert_eq "$(jq_out '.playwrightTest')" "false" "case3 no playwright"
assert_eq "$(jq_out '.inContainer')" "false" "case3 not in container"
rm -rf "${ROOT}"

# --- Case 4: Angular + PrimeNG + npm lockfile + Playwright ------------------
make_repo
cat > "${ROOT}/package.json" <<'EOF'
{
  "dependencies": { "@angular/core": "^21.0.0", "primeng": "^21.0.0" },
  "devDependencies": { "@playwright/test": "^1.50.0", "typescript": "^5.6.0" }
}
EOF
touch "${ROOT}/package-lock.json"
run_detect
assert_eq "$(jq_out '.packageManager')" "npm" "case4 npm via package-lock.json"
assert_eq "$(jq_out '.mcpServers | index("angular") != null')" "true" "case4 angular MCP trigger"
assert_eq "$(jq_out '.mcpServers | index("primeng") != null')" "true" "case4 primeng MCP trigger"
assert_eq "$(jq_out '.lspServers | index("typescript") != null')" "true" "case4 typescript LSP trigger"
assert_eq "$(jq_out '.playwrightTest')" "true" "case4 playwright detected"
assert_eq "$(jq_out '.dindDetected')" "false" "case4 no dind trigger"
rm -rf "${ROOT}"

# --- Case 5: pnpm wins over npm lockfile; dockerode → dind ------------------
make_repo
printf '{"dependencies":{"dockerode":"^4.0.0"}}' > "${ROOT}/package.json"
touch "${ROOT}/pnpm-lock.yaml" "${ROOT}/package-lock.json"
run_detect
assert_eq "$(jq_out '.packageManager')" "pnpm" "case5 pnpm precedence"
assert_eq "$(jq_out '.dindDetected')" "true" "case5 dockerode dind trigger"
rm -rf "${ROOT}"

# --- Case 6: go.mod → gopls; testcontainers-go → dind -----------------------
make_repo
mkdir -p "${ROOT}/svc"
cat > "${ROOT}/svc/go.mod" <<'EOF'
module example.com/svc

go 1.25

require github.com/testcontainers/testcontainers-go v0.35.0
EOF
run_detect
assert_eq "$(jq_out '.lspServers | index("gopls") != null')" "true" "case6 gopls via nested go.mod"
assert_eq "$(jq_out '.dindDetected')" "true" "case6 testcontainers-go dind trigger"
rm -rf "${ROOT}"

# --- Case 7: .csproj → csharp-ls; Docker.DotNet PackageReference → dind -----
make_repo
mkdir -p "${ROOT}/src/Api"
cat > "${ROOT}/src/Api/Api.csproj" <<'EOF'
<Project Sdk="Microsoft.NET.Sdk.Web">
  <ItemGroup>
    <PackageReference Include="Docker.DotNet" Version="3.125.15" />
  </ItemGroup>
</Project>
EOF
run_detect
assert_eq "$(jq_out '.lspServers | index("csharp-ls") != null')" "true" "case7 csharp-ls via .csproj"
assert_eq "$(jq_out '.dindDetected')" "true" "case7 Docker.DotNet dind trigger"
rm -rf "${ROOT}"

# --- Case 8: Python + Rust triggers ------------------------------------------
make_repo
printf 'requests==2.32.0\ntestcontainers>=4.0\n' > "${ROOT}/requirements.txt"
printf '[package]\nname = "demo"\n' > "${ROOT}/Cargo.toml"
run_detect
assert_eq "$(jq_out '.lspServers | index("pyright") != null')" "true" "case8 pyright via requirements.txt"
assert_eq "$(jq_out '.lspServers | index("rust-analyzer") != null')" "true" "case8 rust-analyzer via Cargo.toml"
assert_eq "$(jq_out '.dindDetected')" "true" "case8 python testcontainers dind trigger"
rm -rf "${ROOT}"

# --- Case 9: malformed package.json → degrade with warning, exit 0 ----------
make_repo
printf '{ not json' > "${ROOT}/package.json"
run_detect
assert_eq "${CODE}" "0" "case9 exit 0 despite malformed package.json"
assert_eq "$(jq_out '.mcpServers | length')" "0" "case9 no MCP triggers from unreadable deps"
assert_eq "$(jq_out '[.warnings[] | select(contains("package.json"))] | length >= 1')" "true" "case9 warning recorded"
rm -rf "${ROOT}"

# --- Case 10: pluginVersion from --plugin-root; warning when unresolvable ---
make_repo
PLUGIN="$(mktemp -d)"
mkdir -p "${PLUGIN}/.claude-plugin"
printf '{"name":"cenci","version":"0.22.0"}' > "${PLUGIN}/.claude-plugin/plugin.json"
run_detect --plugin-root "${PLUGIN}"
assert_eq "$(jq_out '.pluginVersion')" "0.22.0" "case10 pluginVersion resolved"
run_detect
assert_eq "$(jq_out '.pluginVersion')" "null" "case10 pluginVersion null without plugin root"
assert_eq "$(jq_out '[.warnings[] | select(contains("plugin version"))] | length >= 1')" "true" "case10 warning recorded"
rm -rf "${ROOT}" "${PLUGIN}"

# --- Case 11: container detection via CENCI_SANDBOX and dockerenv probe -----
make_repo
OUT="$(cd "${ROOT}" && env -u CLAUDE_PLUGIN_ROOT CENCI_SANDBOX=1 \
  CENCI_DETECT_DOCKERENV_PATH="${ROOT}/.no-dockerenv" bash "${DETECT}" 2>&1)"
assert_eq "$(jq_out '.inContainer')" "true" "case11 CENCI_SANDBOX=1 detected"
touch "${ROOT}/.fake-dockerenv"
OUT="$(cd "${ROOT}" && env -u CENCI_SANDBOX -u CLAUDE_PLUGIN_ROOT \
  CENCI_DETECT_DOCKERENV_PATH="${ROOT}/.fake-dockerenv" bash "${DETECT}" 2>&1)"
assert_eq "$(jq_out '.inContainer')" "true" "case11 dockerenv sentinel detected"
rm -rf "${ROOT}"

# --- Case 12: vendor dirs are pruned (node_modules go.mod is invisible) -----
make_repo
mkdir -p "${ROOT}/node_modules/some-dep"
printf 'module vendored\n' > "${ROOT}/node_modules/some-dep/go.mod"
run_detect
assert_eq "$(jq_out '.lspServers | index("gopls")')" "null" "case12 pruned node_modules go.mod ignored"
rm -rf "${ROOT}"

# --- Case 13: unknown flag fails closed --------------------------------------
make_repo
run_detect --bogus-flag
assert_eq "${CODE}" "2" "case13 unknown flag exits 2"
rm -rf "${ROOT}"

echo "detect-project.test.sh: failures=${failures}"
[[ "${failures}" -eq 0 ]]
