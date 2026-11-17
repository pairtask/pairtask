# Build caches (#400) go to /workspace/.cache, on the projects volume, so a
# few builds can't fill the small agent home. Values already set win.
if mountpoint -q /workspace 2>/dev/null; then
  : "${GOCACHE:=/workspace/.cache/go-build}"
  : "${GOMODCACHE:=/workspace/.cache/go-mod}"
  : "${npm_config_cache:=/workspace/.cache/npm}"
  : "${PIP_CACHE_DIR:=/workspace/.cache/pip}"
  : "${UV_CACHE_DIR:=/workspace/.cache/uv}"
  export GOCACHE GOMODCACHE npm_config_cache PIP_CACHE_DIR UV_CACHE_DIR
fi
