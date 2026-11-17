# Build caches (#400) go to their own volume when this deployment has one,
# so a few builds can't fill the small agent home. Values already set win.
# Only a mounted volume counts: an older checkout that removed it can leave
# the bare directory on the root disk.
if mountpoint -q /var/cache/collab-ai-agent 2>/dev/null; then
  : "${GOCACHE:=/var/cache/collab-ai-agent/go-build}"
  : "${GOMODCACHE:=/var/cache/collab-ai-agent/go-mod}"
  : "${npm_config_cache:=/var/cache/collab-ai-agent/npm}"
  : "${PIP_CACHE_DIR:=/var/cache/collab-ai-agent/pip}"
  : "${UV_CACHE_DIR:=/var/cache/collab-ai-agent/uv}"
  export GOCACHE GOMODCACHE npm_config_cache PIP_CACHE_DIR UV_CACHE_DIR
fi
