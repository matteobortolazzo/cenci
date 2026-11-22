#!/usr/bin/env bash
# Runs as the workload user. Only links whose entire target matches the reserved
# mount path for that name belong to this helper; the home has no trusted manifest.
set -euo pipefail
shopt -s nullglob

agent=${1:?missing agent}
home=${2:?missing sandbox home}
stage=${3:-/opt/cenci-host-content}
case $agent in
    claude) skills="$home/.claude/skills"; instructions="$home/.claude" ;;
    codex) skills="$home/.agents/skills"; instructions="$home/.codex" ;;
    *) printf 'Unknown host-content client %s\n' "$agent" >&2; exit 1 ;;
esac

die() { printf 'Host content: %s\n' "$*" >&2; exit 1; }
managed() { [[ -L $1 && $(readlink -- "$1") == "$2" ]]; }

# Serialize fresh startup and overlapping attaches on this home, without
# creating a lock file in a disabled or already populated home.
exec 9<"$home"
flock -x 9
declare -a sources=("$stage"/skills/* "$stage"/instructions/*)
for dir in "${skills%/*}" "$skills" "$instructions"; do
    if [[ -L $dir || ( -e $dir && ! -d $dir ) ]]; then
        if (( ${#sources[@]} == 0 )); then continue; fi
        die "discovery directory $dir must be a local directory; move it before enabling inheritance."
    fi
done

# Check every collision before creating or removing any discovery entry.
declare -a destinations=()
for source in "${sources[@]}"; do
    name=${source##*/}
    case $source in
        "$stage"/skills/*)
            [[ -d $source && -f $source/SKILL.md ]] || die "selected skill $source is missing SKILL.md."
            dest="$skills/$name"
            if [[ $agent == codex && -f $home/.codex/skills/$name/SKILL.md ]]; then
                die "local skill $home/.codex/skills/$name already exists; rename it or deselect $source."
            fi
            ;;
        *)
            case "$agent/$name" in
                claude/CLAUDE.md|codex/AGENTS.md|codex/AGENTS.override.md) ;;
                *) die "unsupported instruction file $name for $agent." ;;
            esac
            [[ -f $source ]] || die "selected instruction $source is missing."
            if [[ $agent == codex ]]; then
                other=AGENTS.override.md
                [[ $name != "$other" ]] || other=AGENTS.md
                if [[ -e $stage/instructions/$other || -e $instructions/$other || -L $instructions/$other ]]; then
                    managed "$instructions/$other" "$stage/instructions/$other" && [[ ! -e $stage/instructions/$other ]] ||
                        die "conflicting Codex instruction $other; keep a single global instruction selection."
                fi
            fi
            dest="$instructions/$name"
            ;;
    esac
    if [[ -e $dest || -L $dest ]]; then
        managed "$dest" "$source" || die "$dest already exists; rename the local entry or deselect $source."
    fi
    destinations+=("$dest")
done

if [[ -d $stage/skills ]]; then
    helper_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
    skill_roots=("$skills")
    if [[ $agent == codex ]]; then skill_roots+=("$home/.codex/skills"); fi
    perl "$helper_dir/host-skill-names.pl" "$stage" "${skill_roots[@]}"
fi

# Never traverse a user-replaced directory symlink when cleaning an old home.
if [[ -d $skills && ! -L $skills && ! -L ${skills%/*} ]]; then
    for dest in "$skills"/*; do
        source="$stage/skills/${dest##*/}"
        if managed "$dest" "$source" && [[ ! -d $source ]]; then rm -- "$dest"; fi
    done
fi
if [[ -d $instructions && ! -L $instructions ]]; then
    for name in CLAUDE.md AGENTS.md AGENTS.override.md; do
        source="$stage/instructions/$name"
        if managed "$instructions/$name" "$source" && [[ ! -f $source ]]; then rm -- "$instructions/$name"; fi
    done
fi

for index in "${!sources[@]}"; do
    dest=${destinations[index]}
    if [[ ! -L $dest ]]; then
        mkdir -p -- "${dest%/*}"
        ln -sT -- "${sources[index]}" "$dest"
    fi
done
