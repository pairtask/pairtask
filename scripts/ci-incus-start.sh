# Sourced by scripts/ci-incus.sh: start Incus on a disposable container runner
# whose PID 1 is not systemd (an ephemeral Namespace profile).

# start_namespace_incus WRAPPER LOG starts Zabbly's packaged incusd wrapper
# with fixed CI flags and waits until it serves. Incus writes LOG; the
# wrapper's own stdout and stderr go to LOG.out, so a failure before Incus
# opens its log is still visible. On failure both are printed and it returns 1.
start_namespace_incus() {
    local wrapper=$1 log=$2
    if test "${PAIRTASK_EPHEMERAL_RUNNER:-}" != namespace; then
        echo "starting Incus without systemd is only for an ephemeral Namespace runner" >&2
        return 1
    fi
    if ! test -x "$wrapper"; then
        echo "Incus daemon wrapper $wrapper is missing or not executable" >&2
        return 1
    fi
    mkdir -p "$(dirname "$log")"
    "$wrapper" --group incus-admin --logfile "$log" >>"$log.out" 2>&1 &
    if ! "$wrapper" waitready --timeout=120; then
        echo "Incus did not become ready; daemon output follows" >&2
        cat "$log.out" "$log" >&2 2>/dev/null || true
        return 1
    fi
}

# ensure_loop_devices DEV creates the loop control node and loop0..7 under DEV
# when they are missing, so a container runner without loop devices can back
# the CI btrfs pool. It returns 1 with a clear message when the runner forbids
# creating them; nothing that exists is changed.
ensure_loop_devices() {
    local dev=$1 i
    if ! test -e "$dev/loop-control" && ! mknod -m 0660 "$dev/loop-control" c 10 237; then
        echo "this runner forbids creating loop devices; it can't host the CI btrfs pool" >&2
        return 1
    fi
    for i in 0 1 2 3 4 5 6 7; do
        if ! test -e "$dev/loop$i" && ! mknod -m 0660 "$dev/loop$i" b 7 "$i"; then
            echo "this runner forbids creating loop devices; it can't host the CI btrfs pool" >&2
            return 1
        fi
    done
}
