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
