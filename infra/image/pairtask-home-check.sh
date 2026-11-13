# Once per interactive agent shell: say when the agent home is nearly full
# (#371), before a session or the prompt fails on it. It never fails the
# shell; `pairtask start` refuses on its own when there is too little left.
case $- in
  *i*)
    if [ "$(id -u)" = 1001 ] && [ -z "${_PAIRTASK_HOME_CHECKED:-}" ]; then
      _PAIRTASK_HOME_CHECKED=1
      /usr/local/bin/pairtask home-check || true
    fi
    ;;
esac
