# Core protocol vector checks

Run from this directory with Go 1.26.10, Python 3 and a C compiler:

```sh
python3 -m unittest -v test_check_vectors.py
python3 check_vectors.py
```

The runner defaults to `-race`. `--no-race` is available for local hosts without
cgo and prints that limitation. CI uses the default on Linux amd64 and arm64.
These checks are separate from `go test ./...`: Go does not automatically run
the tests of imported modules.

The runner executes these upstream tests against their original implementation
code, without copying or reimplementing the cryptographic primitives:

| Module | Reviewed revision | Checks |
| --- | --- | --- |
| `github.com/the-sarge/cpace v0.1.3` | `b4174c6bf4bae78f4081c3d6d6baebff5f1cbbf1` | Generator, both shares, shared point, intermediate session key, transcript identifiers, core exchange/confirmation, invalid inputs and fixture integrity |
| `github.com/mendsec/cpace v0.1.0` | `836a13c444ce916f037f280765c6b0e721cc5092` | Length encoding, generator, both shares, shared point, intermediate session key and invalid inputs |

Both implementations compare their computed core outputs with the same
ristretto255/SHA-512 examples in
[draft-irtf-cfrg-cpace-21, Appendix B.3](https://www.ietf.org/archive/id/draft-irtf-cfrg-cpace-21.html#appendix-B.3).
The candidate also tests its confirmation tags against **package-local goldens**;
those tags are not claimed to be official draft vectors. No cross-library wire
exchange is attempted: the public APIs and message framing differ.

The script pins module versions and Go module `h1` checksums, rejects a different
or replaced candidate in the application's selected module graph, and runs `go mod
verify` before executing the downloaded source. Downloads happen in a temporary
module; mendsec is not added to the application's dependencies.
Each required test name must emit its own JSON `pass` event and the package must
pass. A renamed, absent, skipped or failing test fails the gate even if Go exits
zero. Tests are run with `-count=1` and a two-minute timeout.

## Fixture provenance

On 2026-10-27, Codex independently decoded the BASE64 JSON blocks from the
[IETF text archive](https://www.ietf.org/archive/id/draft-irtf-cfrg-cpace-21.txt)
and matched these SHA-256 values against the candidate's embedded JSON. Hashing
excludes trailing CR/LF only. CI verifies those same fixture hashes without
depending on the archive being online.

| Draft section | Candidate fixture | SHA-256 |
| --- | --- | --- |
| B.3.1.1 | `draft21-ristretto255-generator.json` | `05c8a34bd623fbdefd7fbffcd261d2420bd34363efa301d0b0dd9817f7f47c94` |
| B.3.9 | `draft21-ristretto255-sha512.json` | `dc74177668cc2374beaf57fcb6e4c08a908238bab6b74d8edf8c86e04bc663ae` |
| B.3.11.1 | `draft21-ristretto255-invalid.json` | `6288f7ff96dfb8c2d6c4d743927c5fe6ac4aecbc56da2d1f00f27104000b6dfd` |

The pinned mendsec `vectors_test.go` was also checked against the draft's same
generator, share, shared-point, ISK and invalid-input values. Its source is
protected by the module checksum pin; it uses Go constants rather than JSON.

This establishes conformance of the covered core examples. Both libraries use
the same ristretto255 dependency, so agreement is not an independent check of
that primitive. Passing vectors and race tests do not establish cryptographic
security, wire interoperability, production readiness, or the invitation
persistence and policy properties of peer pairing.
