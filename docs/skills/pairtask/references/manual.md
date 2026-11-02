# Manual delivery and delegated fallback

Use this path when the runtime does not deliver notifications, or for recovery
through the existing owner's fallback tools. Default MCP initialization is passive:
call `receive` once to register its configured identity. Do not register a manual
adapter if a managed runtime already owns that ID.

Check `receive` between meaningful work steps; use `wait_reply` for a particular
request and `wait` when waiting for any message. Avoid competing consuming readers:
a general receive can take the reply a selective wait expects. Use waits of at
most 30 seconds and a finite overall deadline. After timeout, continue independent
work or report the blocked dependency, without resending an accepted request.
Manual polling does not guarantee notification while the model is idle.

Keep `message_id`, sender/session, `in_reply_to`, replay flags, and connection/error
state intact. A reply can be buffered alongside a disconnect: handle the reply
before stopping. Consider the error code and acceptance evidence; a post-acceptance
disconnect is not proof the broker lost a durable message.

## Optional delegated listener

Only use delegation when the manual adapter exposes `delegate_listener` and the
user/host permits a background child with parent notification. Managed sessions
already have their own consumer. If those facilities are unavailable, use parent
polling and say that background notification is unavailable.

1. The parent calls `delegate_listener`. Give the returned `socket_path` and secret
   `token` only to the chosen child through private local host context. The grant
   lasts 15 minutes; a new grant revokes its predecessor.
2. The child calls **only** `wait_delegated`, using `after_cursor: 0`, a bounded
   timeout (default 25 seconds), and a finite assignment deadline. It does not
   send, acknowledge, edit files, or call ordinary `receive`/`wait`/`wait_reply`.
3. Relay every frame verbatim, including receipts/errors, plus connection state
   and `next_cursor`. Advance a cursor only after relaying the whole batch. On
   a nonempty batch, error, disconnect, or deadline, hand back and stop; do not
   silently renew or loop indefinitely. Relay buffered messages even on disconnect.
4. The parent considers messages, drains its retained queue with `receive`, and
   explicitly acknowledges requested messages. Deduplicate relay, parent-copy,
   and replay IDs before acting. The child never acknowledges for the parent.
5. Revoke the grant when done. If another bounded assignment is warranted, start
   its cursor at zero to recover still-retained frames; duplicates are expected.
   One wait per grant is allowed. Report a competing-wait error instead of racing.

Delegated reads do not drain parent memory. The manual queue is bounded to 256
frames / 4 MiB, so the parent must still consume it between work steps. Grant
expiry, revocation, owner shutdown, or overflow ends the listener. Reconnect the
same logical owner to recover accepted unacknowledged durable messages; replace
the grant after owner restart. Never solve a listener fault by taking over the
parent's inbox. A child with a different ID has its own inbox and cannot listen
for messages addressed to the parent.

The [delegation protocol guide](https://github.com/pairtask/pairtask/blob/main/docs/delegated-listeners.md)
contains API limits and capability details. A successful child read alone does
not prove that its parent was notified or resumed.
