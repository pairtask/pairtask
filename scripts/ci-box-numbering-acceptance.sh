#!/usr/bin/env bash
set -euo pipefail
export PAIRTASK_ALLOW_ROOT=1
export PAIRTASK_OPERATOR_HOME="$RUNNER_TEMP/operator"
git -c safe.directory="$PWD" worktree add --detach "$RUNNER_TEMP/box-host" "$FIRST_RELEASE"
cd "$RUNNER_TEMP/box-host"
sshdir="$RUNNER_TEMP/box-ssh"
rollout() {
  tag="$1"
  python3 scripts/sandbox-download.py --remote local --from-dir "$RUNNER_TEMP/$tag" --output "$RUNNER_TEMP/image-$tag"
  python3 scripts/pairtask.py "$2" --remote local --project pairtask --image-dir "$RUNNER_TEMP/image-$tag" --ssh-state-dir "$sshdir" --tools
  python3 scripts/pairtask.py version --remote local --refresh --json > "$RUNNER_TEMP/observed.json"
  jq -e --arg tag "$tag" '.app.release == $tag and .host.release == $tag and (.app.instances | length) == 2' "$RUNNER_TEMP/observed.json"
}
rollout "$FIRST_RELEASE" install
ssh -F "$sshdir/config" workspace 'echo numbered-upgrade > /workspace/version-acceptance; echo before-upgrade > /home/agent/version-acceptance'
sha256sum "$sshdir/id_ed25519" > "$RUNNER_TEMP/login-key.sha256"
git checkout --detach "$SECOND_RELEASE"
rollout "$SECOND_RELEASE" upgrade
sha256sum -c "$RUNNER_TEMP/login-key.sha256"
ssh -F "$sshdir/config" workspace 'test "$(cat /workspace/version-acceptance)" = numbered-upgrade && test "$(cat /home/agent/version-acceptance)" = before-upgrade && pairtask status --json' | jq -e '.health == "ready"'
