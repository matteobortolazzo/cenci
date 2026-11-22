#!/usr/bin/env bash
# Exercise the same activation helper used for fresh and reused homes.
set -euo pipefail
here=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
helper="${here}/../lib/host-content.sh"
work=$(mktemp -d)
trap 'rm -rf -- "$work"' EXIT

for agent in claude codex; do
    home="${work}/${agent}/home"
    stage="${work}/${agent}/selected sources"
    if [[ $agent == claude ]]; then
        skills="$home/.claude/skills"
        instructions="$home/.claude/CLAUDE.md"
    else
        skills="$home/.agents/skills"
        instructions="$home/.codex/AGENTS.md"
    fi
    mkdir -p "$home" "$stage"
    run() { bash "$helper" "$agent" "$home" "$stage"; }
    run
    [[ ! -e $skills && ! -e $instructions ]]
    mkdir -p "$stage/skills/editor" "$stage/instructions"
    printf 'host skill\n' > "$stage/skills/editor/SKILL.md"
    printf 'host instructions\n' > "$stage/instructions/${instructions##*/}"
    run
    [[ $(cat "$skills/editor/SKILL.md") == 'host skill' ]]
    [[ $(cat "$instructions") == 'host instructions' ]]
    mkdir -p "$skills/other-directory"
    printf '%s\n' '---' 'name: "editor"' '---' 'local instructions' > "$skills/other-directory/SKILL.md"
    if run; then echo 'Expected a same-name skill collision via YAML frontmatter' >&2; exit 1; fi
    rm "$skills/other-directory/SKILL.md"
    printf 'new revision\n' > "$stage/skills/editor/SKILL.md"
    mkdir "$stage/skills/formatter" "$skills/local"
    touch "$stage/skills/formatter/SKILL.md"
    printf 'session\n' > "$home/history"
    run
    [[ $(cat "$skills/editor/SKILL.md") == 'new revision' && -f "$skills/formatter/SKILL.md" ]]
    mv "$stage/skills/editor" "$work/prior-$agent"
    run
    [[ ! -L "$skills/editor" && -d "$skills/local" ]]
    mkdir "$stage/skills/local"
    touch "$stage/skills/local/SKILL.md"
    if run; then echo 'Expected a sandbox-local skill collision' >&2; exit 1; fi
    [[ -d "$skills/local" && ! -L "$skills/local" ]]
    mv "$stage" "$stage.disabled"
    run
    [[ ! -L "$instructions" && ! -L "$skills/formatter" && -d "$skills/local" ]]
    [[ $(cat "$home/history") == session ]]
    [[ $(cat "$stage.disabled/instructions/${instructions##*/}") == 'host instructions' ]]
    mv "$stage.disabled" "$stage"
    rmdir "$skills/local"
    printf 'personal\n' > "$instructions"
    if run; then echo 'Expected an instruction collision' >&2; exit 1; fi
    [[ $(cat "$instructions") == personal && ! -L "$skills/formatter" ]]
    rm "$instructions"
    run
    [[ $(cat "$instructions") == 'host instructions' ]]
done
echo 'PASS: host content activation, refresh, collisions, disable and re-enable'
