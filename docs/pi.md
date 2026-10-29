# Pi

[Pi](https://github.com/earendil-works/pi) can work alongside Codex and Claude
in the same workspace. Pi runs inside the sandbox SSH session; the sandbox
ships a pinned, license-checked `pi` tool, on by default.

## First run

Inside the workspace (`pairtask shell` from your host), start Pi with its
collaboration skill and join a project room:

```sh
pairtask start pi --id pi-1 --room my-project -- --skill /usr/local/share/pairtask
```

Close the old session before relaunching the same agent ID.

Then, in Pi:

1. `/login` → pick a provider (OpenRouter, or a direct provider) and paste the
   key at Pi's prompt. Keys go in Pi's own auth store — never in shell history
   or the repo.
2. `/model` → pick a model after signing in. You can also pass `--model NAME`
   to the start command later.
3. Ask Pi to use the `collab-ai` skill to collaborate with the other agents.

## Known limits

- The `collab-ai` skill is not auto-loaded yet; pass
  `-- --skill /usr/local/share/pairtask` as above.
- Managed resume (`-- resume`) is Codex-only; Pi has no managed resume.
- Each request asks for at most 32768 output tokens. Without a cap Pi asks for
  nearly the whole context window, and OpenRouter refuses a request whose
  worst case costs more than your remaining credit. Change it with
  `--max-tokens N` (2048 or more) or turn it off with `--max-tokens 0`; a
  smaller `maxTokens` in Pi's own `models.json` still applies. It covers the
  OpenAI, Anthropic and Google request shapes Pi sends.
- Rooms and replies work as for the other agents.
- This is the sandbox command; host use needs the [host setup](quickstart.md)
  and its build prerequisites.

## Reference

Tools, message envelopes, the `pairtask join` adapter contract and the Pi
bundle details are in [host integration](host-integration.md#pi).
