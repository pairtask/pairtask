# Short-code pairing spike (#178)

An **evaluation**, not an implementation. It shows how the operator's chosen pairing UX could work: a short code, shared over another medium (phone, in person, a private message), typed in on the other workspace. It uses an existing PAKE library instead of our own cryptography.

It isn't wired into the broker, it decides nothing about adoption, and **none of its tests have been executed**. The harness running the build agent refused, twice, to execute third-party library code. The code and tests are compiled and `go vet`-clean under Go 1.26.5, nothing more.

## What it does

- **Library:** [`github.com/the-sarge/cpace`](https://github.com/the-sarge/cpace) **v0.1.3**, commit `b4174c6bf4bae78f4081c3d6d6baebff5f1cbbf1`. It implements CPace, `draft-irtf-cfrg-cpace-21`, on ristretto255 with explicit mutual key confirmation. Its README says it has had **no independent cryptographic review and is not production-ready**. Its security write-ups are project-side self-assessments. It needs Go 1.26, hence this nested module; the repository root stays on 1.25.
- **First-contact TLS 1.3** (`Client`/`Server` → `Endpoint`): at first contact, neither side can trust the other's certificate yet. The endpoint configures TLS itself and records the leaf it actually presented. It accepts exactly one parsable leaf from the peer, and `Binding` refuses before the handshake has completed.
- **Binding** (`Endpoint.Binding`): the CPace exchange is bound to that exact connection. Each side builds its inputs from its own view:

  | CPace input | Value |
  | --- | --- |
  | `SessionID` | the connection's [RFC 9266](https://www.rfc-editor.org/rfc/rfc9266) `EXPORTER-Channel-Binding` (32 bytes) |
  | `SelfID` / `PeerID` | `pairtask-inviter:` / `pairtask-joiner:` + SHA-256 of each presented leaf |
  | `Context` | the length-prefixed profile version `pairtask-pairing-v1` and the invitation ID |
  | `Password` | the code |

  A relaying man in the middle terminates two TLS sessions, which have different exporters and certificates, so neither side can confirm.
- **Exchange** (`Join`/`Invite`):
  1. The joiner (TLS client, CPace initiator) sends A.
  2. The inviter answers with B.
  3. The joiner verifies B and sends C.
  4. The inviter verifies C, then sends one final `confirmed` frame over the now-bound connection.

  `Result.InviterConfirmed` acknowledges the **pairing** only. Contact acceptance remains a separate, explicit operator step (#178 acceptance 3). If the final frame is lost, the joiner gets `ErrOutcomeUnknown`. `ErrUnconfirmed` means nothing may be trusted or recorded.
- **After pairing:** the pairing connection is meant to close. Later traffic uses a fresh connection, strictly pinned to the verified certificates, as #179's exact-certificate client does. That part isn't in the spike.

## Tests, all unrun

| File | Author | Covers |
| --- | --- | --- |
| `candidate_acceptance_test.go` | @codex-1 | the library's public API at the pinned tag: confirmed keys and identities, mismatched inputs, replay and concurrent use, malformed messages, input and output copying, associated-data semantics |
| `transport_acceptance_test.go` | @codex-1 | real TLS 1.3: both certificates bound, every binding field changed independently, a two-session relaying MITM, the lost final ack, frame bounds |
| `pairing_test.go` | @claude-1 | sanity checks: correct code, wrong code and other invitation, relaying MITM, the binding per connection, before the handshake, oversized frames |

To run them, with the harness's permission or by hand:

```sh
cd spike/pairing && GOTOOLCHAIN=go1.26.5 go test -race -v ./...
```

## Against the seven pairing criteria (#178)

| # | Criterion | Spike |
| --- | --- | --- |
| 1 | Only an explicit operator action opens an invitation; the code is visible to the operator only | **Not implemented.** Callers supply the code. |
| 2 | Mutual confirmation binds both identities to one session; contact acceptance stays separate | Confirmation and binding covered by unrun tests. Contact acceptance **not implemented**. |
| 3 | Wrong, expired, cancelled or consumed codes fail closed; one winner under concurrency | Wrong code covered by unrun tests. Expiry, cancel, consumption and concurrency on an invitation **not implemented**. |
| 4 | Replay, swapped roles, other sessions and substituted keys fail | Key and session changes, the TLS MITM and candidate replay covered by unrun tests. |
| 5 | Documented code generation, expiry and attempt policy; restarts can't reset the guessing budget | **Not implemented.** |
| 6 | Disconnects reconcile without duplicates; a consumed code can't enroll another peer; identities survive restart | The lost final ack (`ErrOutcomeUnknown`) covered by unrun tests. Persistence and idempotent recovery **not implemented**. |
| 7 | A new key for a known workspace needs an explicit re-pair | Key substitution failing is covered by unrun tests. A rotation or re-pair policy is **not implemented**. |

## Before anything here could be adopted

- Run all tests, and check them against `mendsec/cpace` (836a13c) on the draft's vectors only, since the two packages frame their messages differently.
- An independent cryptographic review of the library, or of whichever one we choose. CPace is still an IRTF draft.
- An invitation state layer for criteria 1, 3, 5 and 6, with persistence.
- Move the repository to Go 1.26 or later, as its own change.
