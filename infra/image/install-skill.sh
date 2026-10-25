#!/bin/sh
# Install the complete, portable skill bundle into a fresh image or staging tree.
set -eu
source_dir=${1:?Usage: install-skill.sh SOURCE DESTINATION CODEX_SKILLS CLAUDE_SKILLS}
destination=${2:?Usage: install-skill.sh SOURCE DESTINATION CODEX_SKILLS CLAUDE_SKILLS}
codex_skills_dir=${3:?Usage: install-skill.sh SOURCE DESTINATION CODEX_SKILLS CLAUDE_SKILLS}
claude_skills_dir=${4:?Usage: install-skill.sh SOURCE DESTINATION CODEX_SKILLS CLAUDE_SKILLS}
install -d "$destination/references"
install -m 0644 "$source_dir/SKILL.md" "$destination/SKILL.md"
install -m 0644 "$source_dir"/references/*.md "$destination/references/"
install -d "$codex_skills_dir" "$claude_skills_dir"
for skills_dir in "$codex_skills_dir" "$claude_skills_dir"; do
    link="$skills_dir/collab-ai"
    if [ -L "$link" ]; then
        [ "$(readlink "$link")" = "$destination" ] || {
            printf 'refusing to replace existing skill link: %s\n' "$link" >&2
            exit 1
        }
    elif [ -e "$link" ]; then
        printf 'refusing to replace existing skill directory: %s\n' "$link" >&2
        exit 1
    else
        ln -s "$destination" "$link"
    fi
done
