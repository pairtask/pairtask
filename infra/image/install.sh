#!/bin/bash
# Runs only inside the disposable, unprivileged Incus build container.
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive

# Prevent package post-install scripts from starting a network SSH listener.
printf '#!/bin/sh\nexit 101\n' >/usr/sbin/policy-rc.d
chmod 755 /usr/sbin/policy-rc.d
apt-get -o APT::Update::Error-Mode=any -o Acquire::Retries=3 update
apt-get install -y --no-install-recommends ca-certificates curl python3 git gh \
    openssh-server sshfs ripgrep tmux less locales apparmor
# The rootful daemon never runs here, even once the Docker deb set is installed.
systemctl mask docker.service docker.socket containerd.service
# Go only compiles collab here; the toolchain is removed again below.
python3 /root/build/infra/image/install-build-go.py
export PATH="/usr/local/go/bin:$PATH" GOTOOLCHAIN=local CGO_ENABLED=0
cd /root/build
go mod download
go mod verify
go test ./... -timeout=120s
# Internal programs live off PATH in /usr/local/lib/pairtask; the CLI is `pairtask` (and `pk`).
install -d -m 0755 /usr/local/lib/pairtask
for spec in broker:/usr/local/lib/pairtask/pairtask-broker codex:/usr/local/lib/pairtask/pairtask-codex \
        mcp:/usr/local/lib/pairtask/pairtask-mcp collab:/usr/local/bin/pairtask; do
    go build -trimpath -buildvcs=false -o "${spec#*:}" "./cmd/${spec%%:*}"
done
ln -s pairtask /usr/local/bin/pk
go clean -cache -modcache -testcache
rm -rf /usr/local/go
gh --version
addgroup --gid 1001 agent
adduser --uid 1001 --gid 1001 --disabled-password --gecos '' agent
addgroup --gid 1002 broker
addgroup --gid 1003 collab-clients
adduser --uid 1002 --gid 1002 --disabled-password --gecos '' --no-create-home --home /nonexistent --shell /usr/sbin/nologin broker
usermod -aG collab-clients agent
# Keep subordinate IDs inside the workspace's 131072-ID Incus mapping.
sed -i '/^agent:/d' /etc/subuid /etc/subgid
printf 'agent:65536:65536\n' >>/etc/subuid
printf 'agent:65536:65536\n' >>/etc/subgid
install -d -o agent -g agent /workspace /var/lib/collab-ai
install -d /etc/pairtask /etc/codex /etc/ssh/authorized_keys /usr/local/share/pairtask
install -m 0644 infra/image/codex-config.toml /etc/codex/config.toml
cp infra/image/tools.lock.json /usr/local/share/pairtask/
cp build-source.json /usr/local/share/pairtask/
# The tool store (docs/sandbox-tools.md): this image carries no third-party
# tools; the rollout installs the selected ones into the store, and the tool
# names below launch from it.
printf 'store\n' >/usr/local/share/pairtask/tools-mode
install -d -m 0755 /var/lib/collab-ai-tools
install -m 0644 infra/image/tool_lock.py infra/image/tool_store.py /usr/local/lib/pairtask/
for name in tools-activate tools-ready tool; do
    install -m 0755 "infra/image/pairtask-$name" "/usr/local/lib/pairtask/pairtask-$name"
done
for name in go gofmt codex claude node npm npx cs-mcp rtk starship; do
    ln -s ../lib/pairtask/pairtask-tool "/usr/local/bin/$name"
done
cp infra/image/pairtask-tools-setup.service infra/image/pairtask-tools-ready.service /etc/systemd/system/
bash infra/image/install-skill.sh docs/skills/collab-ai /usr/local/share/pairtask \
    /etc/codex/skills /etc/claude-code/.claude/skills
cp infra/image/claude-mcp.json /etc/pairtask/claude-mcp.json
# The activator writes the agents' MCP configuration and the tool markers from
# the tool selection at rollout; these are its base files.
cp infra/image/claude-mcp.json /usr/local/share/pairtask/claude-mcp.base.json
cp infra/image/codex-config.toml /usr/local/share/pairtask/codex-config.base.toml
cp infra/image/mcp/servers.json /usr/local/share/pairtask/mcp-servers.json
install -d /etc/pairtask/tools
install -m 0755 infra/image/pairtask-playwright /usr/local/lib/pairtask/pairtask-playwright
install -m 0755 infra/image/pairtask-codescene-login /usr/local/bin/pairtask-codescene-login
install -m 0644 infra/image/pairtask-chromium.apparmor /etc/apparmor.d/pairtask-chromium
cp infra/image/sshd_config /etc/ssh/sshd_config.collab-ai
cp infra/image/pairtask-broker.service infra/image/pairtask-secured-setup.service /etc/systemd/system/
cp infra/image/pairtask-secured-broker.service infra/image/pairtask-dev-setup.service \
    infra/image/pairtask-dev-executor.socket infra/image/pairtask-dev-executor@.service \
    infra/image/pairtask-client-setup.service infra/image/pairtask-workspace-setup.service /etc/systemd/system/
cp infra/image/secured-codex.toml infra/image/secured-environments.toml /etc/pairtask/
install -m 0755 infra/image/pairtask-executor-connect /usr/local/lib/pairtask/pairtask-executor-connect
install -m 0755 infra/image/pairtask-supervised-codex /usr/local/bin/pairtask-supervised-codex
install -m 0755 infra/image/pairtask-broker-setup /usr/local/lib/pairtask/pairtask-broker-setup
install -m 0755 infra/image/pairtask-workspace-setup /usr/local/lib/pairtask/pairtask-workspace-setup
install -m 0755 infra/image/pairtask-rtk-setup /usr/local/lib/pairtask/pairtask-rtk-setup
install -m 0644 infra/image/pairtask-rtk-setup.service /etc/systemd/system/
install -m 0755 infra/image/pairtask-docker-setup /usr/local/lib/pairtask/pairtask-docker-setup
install -m 0644 infra/image/pairtask-docker-{session,setup}.service /etc/systemd/system/
install -d /etc/systemd/system/user@1001.service.d
install -m 0644 infra/image/pairtask-user-workspace.conf /etc/systemd/system/user@1001.service.d/
printf 'd /run/sshd 0755 root root -\n' >/etc/tmpfiles.d/pairtask-ssh.conf
printf 'DISABLE_UPDATES=1\nDISABLE_AUTOUPDATER=1\n' >>/etc/environment
printf 'export DISABLE_UPDATES=1 DISABLE_AUTOUPDATER=1\ncd /workspace\n' >/etc/profile.d/pairtask.sh
install -m 0644 infra/image/pairtask-agent-aliases.sh /etc/profile.d/pairtask-agent-aliases.sh
# Login shells read profile.d; non-login Bash shells (including tmux) read bash.bashrc.
printf '\n. /etc/profile.d/pairtask-agent-aliases.sh\n' >>/etc/bash.bashrc
install -m 0644 infra/image/pairtask-starship.sh /etc/profile.d/pairtask-starship.sh
printf '\n. /etc/profile.d/pairtask-starship.sh\n' >>/etc/bash.bashrc
# Legacy names an older host checkout drives (docs/sandbox-tools.md#image-layout).
python3 infra/image/install-layout.py apply --root / --layout infra/image/layout-v2.json
systemctl disable ssh.service ssh.socket || true
systemctl mask ssh.service ssh.socket
systemctl enable pairtask-broker.service pairtask-secured-setup.service
systemctl enable pairtask-secured-broker.service pairtask-dev-executor.socket
systemctl enable pairtask-client-setup.service
systemctl enable pairtask-workspace-setup.service
systemctl enable pairtask-tools-setup.service pairtask-tools-ready.service
systemctl enable pairtask-rtk-setup.service
systemctl enable pairtask-docker-session.service pairtask-docker-setup.service
dpkg-query -W >/usr/local/share/pairtask/os-packages.txt
# Last: certify the layout only once everything above has succeeded.
python3 infra/image/install-layout.py finalize --root / --layout infra/image/layout-v2.json

# Images must not share SSH host keys, machine identities, credentials or state.
rm -f /etc/ssh/ssh_host_* /var/lib/dbus/machine-id
truncate -s 0 /etc/machine-id
apt-get clean
rm -rf /var/lib/apt/lists/* /root/.cache /root/.npm /root/.codex /root/.claude /root/.ssh
# Documentation and foreign locales have no reader in a sandbox image.
rm -rf /usr/share/doc/* /usr/share/man/* /usr/share/info/*
find /usr/share/locale -mindepth 1 -maxdepth 1 ! -name 'en*' ! -name 'locale.alias' -exec rm -rf {} +
rm -f /root/.claude.json /root/.bash_history /usr/sbin/policy-rc.d
find /var/log -type f -exec truncate -s 0 {} +
rm -rf /root/build /root/source.tar /tmp/* /var/tmp/*
