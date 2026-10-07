# 03 YAML structure evidence

## Revisions and scope

- Worker branch: `kgo/03-yaml-structure`.
- Implementation commit: `1841aa79f6efd9bf2516cccf826ab6834e39c219`.
- Authoritative spec and `CHANGES-v1.3.md`: `kogen-spec` revision `e19dd1c21c19c5be1201c3b6a42c59c28b5c2887` (v1.3-draft). Rust reference: `kogen-rs` revision `a402540b39cedc7f788472297add7ae2f8a6631a`.
- CLI and private adapter binaries were built with Go `1.27.1`, `GOOS=darwin`, `GOARCH=arm64`; `go version -m` reports revision `1841aa79f6efd9bf2516cccf826ab6834e39c219` and `vcs.modified=false` for both. The private adapter was built but not exercised.
- The implementation owns only `internal/yamlmini/block.go`, `flow.go`, `parse.go`, and `parse_test.go`. `Parse` returns `Value` nodes (`Mapping`, `Sequence`, or strings); scalar coercion is deliberately absent.
- The suite is the frozen `$HOME/cx/kgo/inputs/conformance-v1.2` snapshot. Runner metadata reports `suite_version: v1.2+unknown`; host was macOS 26.7.1 arm64 and Git 2.54.0.

## Commands and results

| Command | Result |
|---|---|
| `GOMAXPROCS=2 go test -p=2 -parallel=2 ./internal/yamlmini` | PASS |
| `GIT_CONFIG_GLOBAL=/dev/null make check` | PASS: format, vendor fingerprints, vet, all tests, and both builds |
| `make build` | PASS |
| Frozen v1.2 command below | EXIT 1; retained result and work directories |

```sh
SUITE="$HOME/cx/kgo/inputs/conformance-v1.2"
EVIDENCE="$HOME/cx/kgo/evidence/03-yaml-structure"
mkdir -p "$EVIDENCE"
PYTHONDONTWRITEBYTECODE=1 PATH="$HOME/.local/share/mise/installs/python/3.14.7/bin:$PATH" \
  "$SUITE/bin/kogen-conformance" run --kogen "$PWD/bin/kogen" \
  --profile cli,state,approval,shape,build,ladder,provider,custody,format,v1.2 --case 'state-01,state-03,v1.2-128-format-05' \
  --jobs 2 --time-scale 0.02 --workdir "$EVIDENCE/work" \
  --out "$EVIDENCE/results.jsonl"
```

The retained JSONL is `/Users/almirsarajcic/cx/kgo/evidence/03-yaml-structure/results.jsonl`.

## Resolved cases and actual observations

The runner resolved the literal IDs to 3 cases and 45 instances:

- `state-01`: 20 instances (`1:bom`, `2:utf8`, `3:tab`, `4:directive`, `5:block-scalar`, `6:anchor`, `7:depth`, `8:merge`, `9:unicode-escape`, `10:bad-escape`, `11:unterminated-quote`, `12:after-quote`, `13:flow-unterminated`, `14:flow-trailing`, `15:colon-space`, `16:list-in-value`, `17:indentation`, `18:duplicate`, `19:key-no-value`, `20:item-no-value`). Result: 0/20 passed.
- `state-03`: 1 valid-config instance. Result: 0/1 passed.
- `v1.2-128-format-05`: 24 instances (`1:size`, `2:bom`, `3:utf8`, `4:tab`, `5:directive`, `6:block-scalar`, `7:anchor`, `8:depth`, `9:merge`, `10:unicode-escape`, `11:bad-escape`, `12:unterminated-quote`, `13:after-quote`, `14:flow-brackets`, `15:flow-malformed`, `16:flow-unterminated`, `17:flow-trailing`, `18:colon-space`, `19:list-in-value`, `20:indentation`, `21:duplicate`, `22:key-no-value`, `23:item-no-value`, `24:empty`). Result: 0/24 passed.

All 45 instances failed before reaching project YAML parsing. `kogen status` returned exit 2 with stderr `kogen: implementation bootstrap; command routes are not wired`; invalid-config instances expected exit 3 and a YAML diagnostic on stdout, while `state-03` expected exit 0 and three output lines. No parser diagnostic was observed and no selected black-box case is counted as passing. This is an unwired CLI integration blocker, not an observed YAML behavior conflict.

The local component tests cover nested block maps and sequences, string-only scalars, multiline flow collections, comments and quote escapes, duplicate block/flow keys, missing map/list values, indentation errors, and forbidden syntax. The frozen corpus IDs and their per-instance labels are preserved above; local component passes do not replace the blocked CLI observations.

## Effects, draft status, and remaining gates

- `make build` produced `bin/kogen` and `bin/kogen-xspec`. The single oracle run created its workdirs and results under the evidence path above. No source oracle, fixture, or golden was modified; no provider account or live provider call was used.
- No YAML-specific v1.3-draft conflict was found in `CHANGES-v1.3.md`. There were no exact semantic conflicts observed in this run because the CLI never reached parsing. Planned D-* fixtures are not v1.2 cases; no shared frozen v1.3 IDs were available, so they are not claimed.
- R(slice) was not run. It remains gated on a scratch copy of the coherent migrated Quint cohort, the spec run, 500 traces × 25 steps for each seed 17, 23, and 41, and conformance against the same-revision private binary with full observations. No replay seeds or divergences are reported.
- Package 00 remains a real foundation task. I1 must wire project parsing into the public command path and rerun the selected cases; the current component receipt is not behavior acceptance.
- Shared frozen v1.3 replacements, Linux evidence, optional-runtime evidence, and live-comparison evidence remain unavailable here and must stay open for their named closure rounds.
