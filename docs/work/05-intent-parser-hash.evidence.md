# 05-intent-parser-hash evidence

## Revisions and scope

- Worker branch: `kgo/05-intent-parser-hash`.
- Parser/hash implementation revision: `6a50190322a60491e9e2b3a372144ea25c364e26`.
- The CLI and private adapter were both built from that revision with Go `1.27.1` (`darwin/arm64`, `CGO_ENABLED=0`, `vcs.modified=false`). `go version -m` reports the same VCS revision for `bin/kogen` and `bin/kogen-xspec`.
- Git: `2.54.0`; host: macOS `26.7.1 arm64`.
- Authoritative spec / `CHANGES-v1.3.md`: `kogen-spec` revision `e19dd1c21c19c5be1201c3b6a42c59c28b5c2887` (`v1.3-draft`). Rust reference: `kogen-rs` revision `a402540b39cedc7f788472297add7ae2f8a6631a`.
- Owned files: `internal/intent/parser.go`, `frontmatter.go`, `hash.go`, and their tests. `Parse` validates frontmatter and structured sections, keeps original bytes, leaves Request bytes opaque after its marker, and parses Verify IDs/modifiers. `IntentSHA256` hashes exact Intent bytes; `ApprovalSHA256` hashes Intent + NUL + exact acceptance bytes.

## Commands and results

| Command | Result |
|---|---|
| `GIT_CONFIG_GLOBAL=/dev/null make check` | PASS after the final source edit: format, vendor fingerprints, vet, all Go tests, and both builds. |
| `make build` | PASS; produced `bin/kogen` and `bin/kogen-xspec` from implementation revision `6a50190322a60491e9e2b3a372144ea25c364e26`. |
| Frozen v1.2 command below | EXIT 1; retained results and workdirs. No retry was made. |

```sh
SUITE="$HOME/cx/kgo/inputs/conformance-v1.2"
EVIDENCE="$HOME/cx/kgo/evidence/05-intent-parser-hash"
mkdir -p "$EVIDENCE"
PYTHONDONTWRITEBYTECODE=1 PATH="$HOME/.local/share/mise/installs/python/3.14.7/bin:$PATH" \
  "$SUITE/bin/kogen-conformance" run --kogen "$PWD/bin/kogen" \
  --profile cli,state,approval,shape,build,ladder,provider,custody,format,v1.2 --case 'state-04,state-07,state-08,v1.2-02-approval-hash-intent-and-test-bytes' \
  --jobs 2 --time-scale 0.02 --workdir "$EVIDENCE/work" \
  --out "$EVIDENCE/results.jsonl"
```

The exact retained results file is `/Users/almirsarajcic/cx/kgo/evidence/05-intent-parser-hash/results.jsonl`; workdirs are under `/Users/almirsarajcic/cx/kgo/evidence/05-intent-parser-hash/work`. Runner metadata identifies suite `v1.2+unknown`, profile set `cli,state,approval,shape,build,ladder,provider,custody,format,v1.2`, and Git `2.54.0`.

## Resolved cases and actual observations

The command resolved to 4 cases / 19 instances:

- `state-04`: 16 instances — `1:no-frontmatter`, `2:no-closing`, `3:unknown-key`, `4:title-not-string`, `5:domains-not-list`, `6:domains-not-strings`, `7:missing-size`, `8:changes-gate`, `9:not-a-map`, `10:yaml-error`, `11:unknown-section`, `12:duplicate-section`, `13:acceptance-entry`, `14:verify-entry`, `15:duplicate-verify`, `16:verify-without-item`. Result: 0/16 passed.
- `state-07`: one Request CRLF/raw-hash instance. Result: 0/1 passed.
- `state-08`: one approval-hash-covers-test instance. Result: 0/1 passed.
- `v1.2-02-approval-hash-intent-and-test-bytes`: one exact Intent + NUL + acceptance-byte hash instance. Result: 0/1 passed.

All four cases failed at the same public command boundary. Each attempted `kogen intent approve greet` returned exit 2 and stderr `kogen: implementation bootstrap; command routes are not wired`; the expected parse diagnostic or approval card was not emitted. In particular, `state-04` expected exit 1 and parse diagnostics; `state-07`, `state-08`, and the overlay expected approval-card behavior (state-07 and overlay also assert exact digests). The CLI never reached the component. These failures are not evidence of a parser/hash semantic mismatch, and none of the 19 instances is counted as passing.

Local component coverage passed under `make check`, including CRLF normalization in structured sections, byte-exact raw Request with invalid UTF-8 after the marker, rejection of invalid UTF-8 before it, section/Verify grammar and line errors, frontmatter fields/defaults, and fixed digest fixtures.

## Draft conflicts, effects, and deferred gates

- No exact v1.2-versus-v1.3-draft semantic conflict was observed for this component. The draft change record does not change the §2.1.3 hash formula; the frozen suite failures above are the unwired CLI boundary.
- `make build` wrote `bin/kogen` and `bin/kogen-xspec`. The single oracle run wrote results and fixture workdirs only under the retained evidence directory. No source oracle, suite, or golden was changed; no live provider or account access was used.
- This is a **component** receipt. Public `intent approve`/shape integration must call the parser and hash functions, and package 00 remains a real foundation handoff. I1 closure requires a wired public command and a later integration rerun; preserve this failed result.
- R(intent) was not run. It requires a scratch copy of the shared coherent migrated Quint cohort, the spec phase, 500 traces × 25 steps for each seed `17`, `23`, and `41`, and conformance against the same-revision private binary with full observations. That cohort is not yet available here. No replay seed or divergence is claimed.
- Planned D-* fixtures are not frozen v1.2 cases; no shared frozen v1.3 replacement IDs were available, so those gates remain open. Linux evidence and optional-runtime/live-comparison gates also remain unavailable and are not claimed.
