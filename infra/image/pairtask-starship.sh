# Initialize once per interactive Bash shell, including SSH and tmux.
case $- in
  *i*)
    if [ -n "${BASH_VERSION:-}" ] && [ "$(id -u)" = 1001 ] &&
       [ -z "${_PAIRTASK_STARSHIP_INITIALIZED:-}" ]; then
      # Its session logs are disposable: keep them off the agent home, whose
      # volume a full disk would otherwise break the prompt on (#371).
      export STARSHIP_CACHE="${XDG_RUNTIME_DIR:-/tmp}/starship-$(id -u)"
      eval "$(/usr/local/bin/starship init bash)"
      _PAIRTASK_STARSHIP_INITIALIZED=1
    fi
    ;;
esac
