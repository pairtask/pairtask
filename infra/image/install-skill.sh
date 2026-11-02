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
# Check both harnesses before changing either: the skill's link may be
# absent or our own; anything else is refused, so a failed install leaves
# both harnesses as they were.
for skills_dir in "$codex_skills_dir" "$claude_skills_dir"; do
    link="$skills_dir/pairtask"
    if [ -L "$link" ]; then
        [ "$(readlink "$link")" = "$destination" ] || {
            printf 'refusing to replace existing skill link: %s\n' "$link" >&2
            exit 1
        }
    elif [ -e "$link" ]; then
        printf 'refusing to replace existing skill directory: %s\n' "$link" >&2
        exit 1
    fi
done
# The skill was named collab-ai: drop that name's link when it is ours, so
# agents don't discover the skill twice; anything else there isn't ours and
# stays. Then link the current name.
for skills_dir in "$codex_skills_dir" "$claude_skills_dir"; do
    old="$skills_dir/collab-ai"
    if [ -L "$old" ] && [ "$(readlink "$old")" = "$destination" ]; then
        rm "$old"
    fi
    [ -L "$skills_dir/pairtask" ] || ln -s "$destination" "$skills_dir/pairtask"
done
