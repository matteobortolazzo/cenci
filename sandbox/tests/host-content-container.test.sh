#!/usr/bin/env bash
# Verify native discovery with real clients and a real read-only bind. The
# client pins here stabilize the offline diagnostic commands used by this test.
set -euo pipefail
if command -v podman >/dev/null; then runtime=podman
elif command -v docker >/dev/null; then runtime=docker
else echo 'SKIP: host-content-container needs Docker or Podman'; exit 0; fi
here=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
work=$(mktemp -d)
trap 'rm -rf -- "$work"' EXIT
mkdir -p "$work/skill"
printf '%s\n' '---' 'name: sandbox-check' 'description: CenciSelectedSkillMarker' '---' 'Test fixture.' > "$work/skill/SKILL.md"
printf '%s\n' 'CenciInstructionMarker' > "$work/personal.md"
# A write must fail specifically because of the mount, even with writable DAC bits.
chmod 0777 "$work/skill"
chmod 0666 "$work/skill/SKILL.md" "$work/personal.md"
before=$(sha256sum "$work/skill/SKILL.md" "$work/personal.md")
for agent in claude codex; do
    case $agent in
        claude) instruction=CLAUDE.md; package='@anthropic-ai/claude-code@2.1.330' ;;
        codex) instruction=AGENTS.md; package='@openai/codex@0.171.0' ;;
    esac
    "$runtime" run --rm --user node -w /tmp -e HOME=/home/node \
        -v "$here/../lib/host-content.sh:/helper.sh:ro" \
        -v "$work/skill:/opt/cenci-host-content/skills/sandbox-check:ro" \
        -v "$work/personal.md:/opt/cenci-host-content/instructions/$instruction:ro" \
        node:24-bookworm bash -euo pipefail -c '
            agent=$1
            npm install --no-fund --no-audit --prefix /tmp/test-clients "$2"
            mkdir /tmp/project
            cd /tmp/project
            bash /helper.sh "$agent" "$HOME"
            case $agent in
                claude)
                    instructions="$HOME/.claude/CLAUDE.md"
                    skill="$HOME/.claude/skills/sandbox-check"
                    output=$(ANTHROPIC_API_KEY=unused ANTHROPIC_BASE_URL=http://127.0.0.1:9 CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1 timeout 35 /tmp/test-clients/node_modules/.bin/claude -p /context)
                    [[ $output == *"| sandbox-check | User |"* && $output == *"$instructions"* ]]
                    ;;
                codex)
                    instructions="$HOME/.codex/AGENTS.md"
                    skill="$HOME/.agents/skills/sandbox-check"
                    output=$(timeout 35 /tmp/test-clients/node_modules/.bin/codex debug prompt-input probe)
                    [[ $output == *CenciSelectedSkillMarker* && $output == *CenciInstructionMarker* ]]
                    ;;
            esac
            [[ $(cat "$instructions") == CenciInstructionMarker ]]
            if echo changed >> "$instructions" || echo changed >> "$skill/SKILL.md" || touch "$skill/added"; then
                echo "Workload wrote to a selected host source" >&2
                exit 1
            fi
        ' _ "$agent" "$package"
done
after=$(sha256sum "$work/skill/SKILL.md" "$work/personal.md")
[[ $before == "$after" && ! -e $work/skill/added ]]
echo 'PASS: native Claude Code and Codex discovery and read-only enforcement'
