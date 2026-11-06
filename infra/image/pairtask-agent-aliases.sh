# Interactive shells only; services and scripted CLI calls use native binaries.
case $- in
  *i*)
    alias dashboard='/usr/local/bin/pairtask dashboard'
    if [ "$(id -u)" = 1001 ]; then
      alias codex='/usr/local/lib/pairtask/pairtask-codex --codex /usr/local/bin/codex --agent-id codex-1 --socket /tmp/collab-ai.sock --terminal --'
      # Sessions get the pairtask MCP server, its tool permissions and the
      # channel; CLI subcommands (claude --help, 2.1.283) run as typed, so a
      # subcommand's own arguments are never read as channel entries. A private
      # POSIX name and an alias, so no older claude alias can rewrite the
      # definition and /bin/sh still parses this profile script.
      pairtask_claude() {
        case "${1-}" in
          agents|attach|auth|auto-mode|doctor|gateway|import|install|logs|mcp|plugin|plugins|project|respawn|rm|setup-token|stop|kill|ultrareview|update|upgrade)
            /usr/local/bin/claude "$@" ;;
          *)
            /usr/local/bin/claude --strict-mcp-config --mcp-config /etc/pairtask/claude-mcp.json \
              --settings /etc/pairtask/claude-settings.json \
              --dangerously-load-development-channels server:pairtask "$@" ;;
        esac
      }
      alias claude=pairtask_claude
    fi
    ;;
esac
