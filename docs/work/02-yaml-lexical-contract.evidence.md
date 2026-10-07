# 02 YAML lexical contract evidence

## Revisions and scope

- Worker branch: `kgo/02-yaml-lexical-contract`.
- Worker implementation commit: `6f5fc6062b7405e5c56d89187260ee4b900ffb29`.
- CLI and private adapter binaries were built from the source tree committed by that revision. `go version -m` reports Go `1.27.1`, `vcs.revision=9f982d22f856a3f1d453194d9a1ac19281fc715b`, and `vcs.modified=true`, because the binaries were built immediately before the implementation commit. `bin/kogen-xspec` was built but not exercised.
- Authoritative spec and `CHANGES-v1.3.md`: `kogen-spec` revision `e19dd1c21c19c5be1201c3b6a42c59c28b5c2887` (v1.3-draft). Rust reference: `kogen-rs` revision `a402540b39cedc7f788472297add7ae2f8a6631a`.
- Frozen oracle: `$HOME/cx/kgo/inputs/conformance-v1.2`; runner metadata reports `suite_version: v1.2+unknown`. The input directory is a frozen suite snapshot without Git metadata.
- Owned implementation is limited to `internal/yamlmini/lex.go`, `preflight.go`, and `lex_test.go`. The existing literal case list is `state-01,v1.2-128-format-05`.

## Commands and results

| Command | Result |
|---|---|
| `GOMAXPROCS=2 go test -p=2 -parallel=2 ./internal/yamlmini` | PASS |
| `GIT_CONFIG_GLOBAL=/dev/null make check` | PASS: format, vendor fingerprints, vet, tests, and both builds |
| `make build` | PASS |
| Frozen v1.2 command below | EXIT 1; retained output, no retry |

```sh
SUITE="$HOME/cx/kgo/inputs/conformance-v1.2"
EVIDENCE="$HOME/cx/kgo/evidence/02-yaml-lexical-contract"
mkdir -p "$EVIDENCE"
PYTHONDONTWRITEBYTECODE=1 PATH="$HOME/.local/share/mise/installs/python/3.14.7/bin:$PATH" \
  "$SUITE/bin/kogen-conformance" run --kogen "$PWD/bin/kogen" \
  --profile cli,state,approval,shape,build,ladder,provider,custody,format,v1.2 --case 'state-01,v1.2-128-format-05' \
  --jobs 2 --time-scale 0.02 --workdir "$EVIDENCE/work" \
  --out "$EVIDENCE/results.jsonl"
```

Result JSONL: `/Users/almirsarajcic/cx/kgo/evidence/02-yaml-lexical-contract/results.jsonl`.

## Resolved cases and actual observations

- `state-01`: 20 instances, 0 passed. Instance labels: `1:bom`, `2:utf8`, `3:tab`, `4:directive`, `5:block-scalar`, `6:anchor`, `7:depth`, `8:merge`, `9:unicode-escape`, `10:bad-escape`, `11:unterminated-quote`, `12:after-quote`, `13:flow-unterminated`, `14:flow-trailing`, `15:colon-space`, `16:list-in-value`, `17:indentation`, `18:duplicate`, `19:key-no-value`, `20:item-no-value`.
- `v1.2-128-format-05`: 24 instances, 0 passed. Instance labels: `1:size`, `2:bom`, `3:utf8`, `4:tab`, `5:directive`, `6:block-scalar`, `7:anchor`, `8:depth`, `9:merge`, `10:unicode-escape`, `11:bad-escape`, `12:unterminated-quote`, `13:after-quote`, `14:flow-brackets`, `15:flow-malformed`, `16:flow-unterminated`, `17:flow-trailing`, `18:colon-space`, `19:list-in-value`, `20:indentation`, `21:duplicate`, `22:key-no-value`, `23:item-no-value`, `24:empty`.
- All 44 failures are the same integration blocker: `kogen status` returned exit 2 with stderr `kogen: implementation bootstrap; command routes are not wired`, while each instance expects exit 3 and a project-config YAML diagnostic on stdout. No parser diagnostic was observed. These are not recorded as lexical passes or semantic v1.2 conflicts.
- Compatible component evidence: the local rejection corpus, byte-boundary checks, quote/escape/comment fixtures, and candidate precedence tests pass; `make check` also passes. No black-box case passed.

## Effects, draft status, and remaining gates

- `make build` produced `bin/kogen` and `bin/kogen-xspec`. The conformance runner's 44 work directories and JSONL are retained under the evidence path above. No provider account or live provider call was used.
- No v1.3 YAML-specific conflict was observed in `CHANGES-v1.3.md`. No shared frozen v1.3 replacement IDs were available; planned D-* fixtures are not v1.2 cases and are not claimed. Wait for the coordinator's shared frozen v1.3 IDs before making draft-suite claims.
- R(slice) was not run for this component. There are no replay seeds or divergences to report. I7 still requires a scratch copy of the coherent migrated cohort, the spec run, 500 traces × 25 steps for each seed 17, 23, and 41, and conformance against the same-revision private binary with full observations.
- Behaviour closure remains with package 00 and I1 after command routes are wired. I1 must rerun both selected IDs and retain the result; this component receipt does not accept their behavior. Package 03 still supplies block/flow structure and parser integration that consume the shared candidate ordering.
- This run was on macOS arm64 only. Linux parity, optional runtime evidence, and live-comparison gates remain open for their named integration rounds; none is inferred from `make check`.
