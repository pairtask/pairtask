#!/bin/bash
# Only for a disposable CI runner, never a developer machine: a GitHub-hosted
# runner, or a self-hosted runner that the workflow explicitly marks as an
# ephemeral Namespace profile (PAIRTASK_EPHEMERAL_RUNNER=namespace).
set -euo pipefail
test "${GITHUB_ACTIONS:-}" = true
case "${RUNNER_ENVIRONMENT:-}" in
    github-hosted) ;;
    self-hosted) test "${PAIRTASK_EPHEMERAL_RUNNER:-}" = namespace ;;
    *) exit 1 ;;
esac
test "$EUID" = 0
if test -e /var/lib/incus/database; then
    echo 'Refusing to initialize an existing Incus server' >&2
    exit 1
fi
apt-get update
apt-get install -y ca-certificates curl gnupg
# Ubuntu 24.04's original 6.0.0 package misdetects idmapped mounts on newer
# kernels (https://github.com/lxc/incus/issues/882). Use maintained 6.0 LTS.
# Repository and signing key: https://github.com/zabbly/incus
install -d -m 0755 /etc/apt/keyrings
curl -fsSL https://pkgs.zabbly.com/key.asc -o /etc/apt/keyrings/zabbly.asc
key_fingerprint=$(gpg --batch --show-keys --with-colons /etc/apt/keyrings/zabbly.asc | awk -F: '$1 == "fpr" {print $10; exit}')
test "$key_fingerprint" = 4EFC590696CB15B87C73A3AD82CC8797C838DCFD
chmod 0644 /etc/apt/keyrings/zabbly.asc
. /etc/os-release
cat > /etc/apt/sources.list.d/zabbly-incus-lts-6.0.sources <<EOF
Types: deb
URIs: https://pkgs.zabbly.com/incus/lts-6.0
Suites: $VERSION_CODENAME
Components: main
Architectures: $(dpkg --print-architecture)
Signed-By: /etc/apt/keyrings/zabbly.asc
EOF
apt-get update
apt-get install -y incus btrfs-progs
# The mount smoke test maps just its fixture owner into the container. Ubuntu's
# newuidmap/newgidmap also require this host-level allowance, independently of
# the restricted Incus project's UID/GID allowlist.
usermod --add-subuids 60000-60000 --add-subgids 60000-60000 root
# btrfs may be built into the runner's kernel (some providers ship no module
# tree); load the module only when the filesystem isn't already available.
uname -r
if ! grep -qw btrfs /proc/filesystems; then
    modprobe btrfs || { echo "btrfs is neither built in nor loadable on $(uname -r); this runner can't host the CI Incus pool" >&2; exit 1; }
fi
if test "$(cat /proc/1/comm)" = systemd; then
    systemctl start incus.socket
else
    # A container runner without systemd as PID 1, accepted above only as an
    # ephemeral Namespace profile: start the daemon the way its unit would,
    # with the unit's own environment and command, and wait until it serves.
    test "${PAIRTASK_EPHEMERAL_RUNNER:-}" = namespace
    unit=/usr/lib/systemd/system/incus.service
    mapfile -t unit_env < <(sed -n 's/^Environment=//p' "$unit" | tr -d '"')
    read -r -a unit_exec < <(sed -n 's/^ExecStart=//p' "$unit" | head -n 1)
    mkdir -p /var/log/incus
    env "${unit_env[@]}" "${unit_exec[@]}" >/var/log/incus/incusd.log 2>&1 &
    if ! incus admin waitready --timeout=120; then
        cat /var/log/incus/incusd.log >&2
        exit 1
    fi
fi
incus admin init --preseed <infra/image/ci-host.json
# GitHub runners also run Docker, whose forwarding policy can block Incus.
# Scope the exception to the disposable builder bridge; do not change the
# default policy or any developer host's firewall.
# https://linuxcontainers.org/incus/docs/main/howto/network_bridge_firewalld/
forward_chain=FORWARD
if iptables -S DOCKER-USER >/dev/null 2>&1; then
    forward_chain=DOCKER-USER
fi
iptables -I "$forward_chain" -i incusbr0 -j ACCEPT
iptables -I "$forward_chain" -o incusbr0 -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT
incus version
uname -r
incus query /1.0 | python3 -c 'import json,sys; e=json.load(sys.stdin)["environment"]; print("Kernel features:", e["kernel_features"]); print("LXC features:", e["lxc_features"])'
