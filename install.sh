#!/bin/sh
# pairtask one-line installer:
#   curl -fsSL https://raw.githubusercontent.com/pairtask/pairtask/main/install.sh | sh -s -- [options]
# Options: --version TAG  --no-provision  --no-modify-path  --yes  --help
# Environment: PAIRTASK_HOME (default $HOME/.pairtask), PAIRTASK_REPO (default
# pairtask/pairtask), PAIRTASK_RELEASE_URL, PAIRTASK_GIT_URL,
# PAIRTASK_INSTALL_DRY_RUN=1 (print the plan; no network, no writes).
# Exit codes: 0 ok · 1 prerequisites or root · 2 bad arguments · 3 fetch or
# release check failed, existing install untouched · otherwise pairtask's code.
set -eu

REPO="${PAIRTASK_REPO:-pairtask/pairtask}"
HOME_DIR="${PAIRTASK_HOME:-$HOME/.pairtask}"
RELEASE_URL="${PAIRTASK_RELEASE_URL:-https://api.github.com/repos/$REPO/releases/latest}"
GIT_URL="${PAIRTASK_GIT_URL:-https://github.com/$REPO.git}"
DRY="${PAIRTASK_INSTALL_DRY_RUN:-}"
VERSION="" PROVISION=1 MODIFY_PATH=1 YES=0

say() { printf '%s\n' "$*"; }
die() { code=$1; shift; printf 'pairtask install: %s\n' "$*" >&2; exit "$code"; }
plan() { say "plan: $*"; }

while [ $# -gt 0 ]; do
  case "$1" in
    --version) [ $# -ge 2 ] || die 2 "--version needs a tag"; VERSION="$2"; shift 2 ;;
    --version=*) VERSION="${1#--version=}"; shift ;;
    --no-provision) PROVISION=0; shift ;;
    --no-modify-path) MODIFY_PATH=0; shift ;;
    --yes|-y) YES=1; shift ;;
    --help|-h) sed -n '2,9p' "$0" 2>/dev/null || say "see the header of install.sh"; exit 0 ;;
    *) die 2 "unknown option: $1" ;;
  esac
done

# 0. Inputs: a release tag and a repository that are safe to pass on.
case "$REPO" in
  */*) printf '%s' "$REPO" | grep -Eq '^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$' || die 2 "bad PAIRTASK_REPO: $REPO" ;;
  *) die 2 "bad PAIRTASK_REPO: $REPO (owner/name)" ;;
esac
if [ -n "$VERSION" ]; then
  printf '%s' "$VERSION" | grep -Eq '^[A-Za-z0-9][A-Za-z0-9_.-]*$' || die 2 "bad --version: $VERSION (a release tag such as workspace-v1.2.3)"
fi

# 1. Platform: macOS or Linux, never as root.
OS=$(uname -s); ARCH=$(uname -m)
case "$OS" in Darwin|Linux) ;; *) die 1 "unsupported OS: $OS (macOS or Linux)" ;; esac
case "$ARCH" in arm64|aarch64|x86_64|amd64) ;; *) die 1 "unsupported architecture: $ARCH" ;; esac
if [ "$(id -u)" -eq 0 ] || [ -n "${SUDO_USER:-}" ]; then
  die 1 "run as your normal user, not root or sudo; the installer keeps everything under $HOME_DIR"
fi
plan "platform $OS/$ARCH, home $HOME_DIR"

# 2. Prerequisites the installer cannot bundle.
have() { command -v "$1" >/dev/null 2>&1; }
python_ok() { have python3 && python3 -c 'import sys; sys.exit(0 if sys.version_info >= (3, 9) else 1)' 2>/dev/null; }
missing=""
python_ok || missing="$missing python"
have git || missing="$missing git"
have incus || missing="$missing incus"
[ "$OS" != Darwin ] || have colima || missing="$missing colima"
if [ -n "$missing" ]; then
  if [ "$OS" = Darwin ]; then
    have brew || die 1 "missing:$missing; install Homebrew, then: brew install colima incus python git"
    say "missing:$missing. Homebrew can install them: brew install colima incus python git"
    if [ -n "$DRY" ]; then plan "brew install colima incus python git"
    else
      if [ "$YES" -ne 1 ]; then
        printf 'Run it now? [y/N] '; read -r answer </dev/tty || answer=n
        case "$answer" in y|Y|yes|YES) ;; *) die 1 "install them, then run this installer again" ;; esac
      fi
      brew install colima incus python git
    fi
  else
    die 1 "missing:$missing. Install Incus (https://linuxcontainers.org/incus/docs/main/installing/), Python 3.9+, Git and OpenSSH, then run this installer again"
  fi
fi

# 3. The release to install.
if [ -z "$VERSION" ]; then
  if [ -n "$DRY" ]; then VERSION="<latest release>"; plan "resolve the latest release from $RELEASE_URL"
  else
    body=$(curl -fsSL "$RELEASE_URL" 2>/dev/null) || body=$(cat "${RELEASE_URL#file://}" 2>/dev/null) || die 3 "cannot read $RELEASE_URL"
    VERSION=$(printf '%s' "$body" | sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -n 1)
    [ -n "$VERSION" ] || die 3 "no tag_name in $RELEASE_URL"
    printf '%s' "$VERSION" | grep -Eq '^[A-Za-z0-9][A-Za-z0-9_.-]*$' || die 3 "unexpected tag_name in $RELEASE_URL: $VERSION"
  fi
fi
plan "release $VERSION from $GIT_URL"

# 4. The host tooling: a git checkout at $HOME_DIR/host, updated in place so
#    untracked state (SSH keys under infra/incus/ssh, images under dist/) stays.
HOST="$HOME_DIR/host"; BIN="$HOME_DIR/bin"
fresh=1; [ -d "$HOST/.git" ] && fresh=0
if [ -n "$DRY" ]; then
  if [ $fresh -eq 1 ]; then plan "git clone --depth 1 --branch $VERSION $GIT_URL $HOST"
  else plan "git -C $HOST fetch and check out $VERSION, refusing if tracked files are modified"; fi
else
  mkdir -p "$HOME_DIR" "$BIN"
  if [ $fresh -eq 1 ]; then
    git clone -q --depth 1 --branch "$VERSION" "$GIT_URL" "$HOST" || die 3 "clone of $VERSION failed; nothing installed"
    git -C "$HOST" cat-file -e "$VERSION:scripts/pairtask.py" 2>/dev/null \
      || { rm -rf "$HOST"; die 3 "release $VERSION predates the pairtask host command; pass --version with a newer tag"; }
  else
    [ -z "$(git -C "$HOST" status --porcelain --untracked-files=no)" ] \
      || die 3 "$HOST has modified tracked files; commit or revert them, then run again (untracked state is kept)"
    git -C "$HOST" fetch -q --depth 1 origin "refs/tags/$VERSION:refs/tags/$VERSION" 2>/dev/null \
      || git -C "$HOST" fetch -q --depth 1 origin "$VERSION" || die 3 "fetch of $VERSION failed; the current checkout is untouched"
    git -C "$HOST" cat-file -e "$VERSION:scripts/pairtask.py" 2>/dev/null \
      || die 3 "release $VERSION predates the pairtask host command; the current checkout is untouched"
    git -C "$HOST" checkout -q "$VERSION" || die 3 "checkout of $VERSION failed"
  fi
fi

# 5. The launcher and PATH.
shim="$BIN/pairtask"
if [ -n "$DRY" ]; then plan "write $shim"
else
  printf '#!/bin/sh\nexec python3 "%s/scripts/pairtask.py" "$@"\n' "$HOST" > "$shim"
  chmod 755 "$shim"
fi
if [ "$MODIFY_PATH" -eq 1 ]; then
  case "${SHELL:-}" in
    */fish) rc="${XDG_CONFIG_HOME:-$HOME/.config}/fish/config.fish"; line="fish_add_path \"$BIN\"" ;;
    */zsh) rc="$HOME/.zshrc"; line="export PATH=\"$BIN:\$PATH\"" ;;
    *) rc="$HOME/.bashrc"; line="export PATH=\"$BIN:\$PATH\"" ;;
  esac
  if [ -n "$DRY" ]; then plan "add to $rc: $line"
  elif [ -f "$rc" ] && grep -qF "$line" "$rc"; then :
  else mkdir -p "$(dirname "$rc")"; printf '\n# pairtask\n%s\n' "$line" >> "$rc"; say "added $BIN to PATH in $rc (open a new shell)"; fi
fi

# 6. Provision the same release the tooling came from.
if [ "$PROVISION" -eq 1 ]; then
  action=install; [ $fresh -eq 1 ] || action=upgrade
  if [ -n "$DRY" ]; then plan "pairtask $action --release $VERSION"
  else "$shim" "$action" --release "$VERSION" || exit $?; fi
fi
say "pairtask $VERSION installed in $HOME_DIR. next: pairtask shell"
