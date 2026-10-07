# 18-approval-preparation evidence

## Revision and scope

- Package commit: `d4d23291b06ef33e70b72c1824e44ea6214ce092` on `kgo/18-approval-preparation`.
- Starting repository revision: `ff08b7108221fe398acfe1fd57a4c3c03d6b0ca4`.
- Public CLI and private adapter sources were unchanged from the starting revision. The public CLI still calls `app.Bootstrap`; the command routes are not wired.
- Pinned worker tools: Go `go1.27.1 darwin/arm64`; Git `2.54.0`; Python `3.14.7`; Node `24.21.0`.
- Conformance runner metadata: suite `v1.2+unknown`, Git `2.54.0`, macOS `26.7.1 arm64`.
- Binaries from the assigned acceptance command: `bin/kogen` SHA-256 `6cac85b4b008d0a7d6e04d1cfc34eec3019e66ed4291273de7b57f052ab3b012`; `bin/kogen-xspec` SHA-256 `a3d199673d125a0c51ecd6915bcba68b2fb7a28126eed8f1d26bc94b5748749c`.

## Implementation

`internal/approval/prepare` now computes and checks the exact Intent-plus-NUL-plus-acceptance hash before identity, base resolution, manifest, setup, baseline or acceptance work. It renders the approval card and warnings, resolves the author identity, checks the protected manifest, opens an exact-base scratch workspace, and sequences setup → independent v3 baseline → staged acceptance checks → restore.

The baseline identity is a canonical v3 SHA-256 over the exact checked base tree, setup key, ordered check definitions and deadlines, filtered child environment, toolchain, platform, and adapter version. Unknown cache identities disable reuse. Each baseline check starts from the base tree; a mutating check resets the scratch tree and restores setup products before the next check. The staged acceptance file is written with rooted atomic publication and removed on every return path.

Setup and baseline cache implementations are effect ports. The `internal/setupcache` package in this revision is still only a package stub, so the concrete v2 setup cache and the v3 baseline adapter are not wired here. Package tests use fake ports for those boundaries. This commit is component-ready, not behavior-accepted.

## Commands and local results

Commands run with the pinned worker `PATH` from `WORKER-RULES.md`:

```sh
GOMAXPROCS=2 go test -p=2 -parallel=2 ./internal/approval/prepare
GIT_CONFIG_GLOBAL=/dev/null make check
```

Both passed. `make check` reported `check: format, vendor fingerprints, vet, tests and both builds passed`.

The required frozen v1.2 command was run once and retained without retry:

```sh
make build
SUITE="$HOME/cx/kgo/inputs/conformance-v1.2"
EVIDENCE="$HOME/cx/kgo/evidence/18-approval-preparation"
mkdir -p "$EVIDENCE"
PYTHONDONTWRITEBYTECODE=1 PATH="$HOME/.local/share/mise/installs/python/3.14.7/bin:$PATH" \
  "$SUITE/bin/kogen-conformance" run --kogen "$PWD/bin/kogen" \
  --profile cli,state,approval,shape,build,ladder,provider,custody,format,v1.2 --case 'approval-01,approval-03,approval-05,approval-06,approval-07,approval-08,approval-09,approval-10,approval-11,approval-12,approval-13,approval-14,approval-15,approval-16,approval-17,approval-19,approval-20,approval-21,state-09,state-25,state-28,v1.2-27-approval-18-status-next' \
  --jobs 2 --time-scale 0.02 --workdir "$EVIDENCE/work" \
  --out "$EVIDENCE/results.jsonl"
```

The runner resolved **22 case IDs / 22 instances**: `approval-01,approval-03,approval-05,approval-06,approval-07,approval-08,approval-09,approval-10,approval-11,approval-12,approval-13,approval-14,approval-15,approval-16,approval-17,approval-19,approval-20,approval-21,state-09,state-25,state-28,v1.2-27-approval-18-status-next`. Result: **0 passed, 22 failed, 0 errors**. All 22 records contain the actual CLI diagnostic `kogen: implementation bootstrap; command routes are not wired`; the approval assertions did not run. The failures are retained at `/Users/almirsarajcic/cx/kgo/evidence/18-approval-preparation/results.jsonl`, with workdirs under `/Users/almirsarajcic/cx/kgo/evidence/18-approval-preparation/work`. None of these IDs is counted as passing or as a spec conflict.

## Component evidence

The package tests pass for hash-first refusal, exact-tree/check-deadline v3 key changes, unknown-identity cache bypass, card Verify semantics and raw Request omission, staged-file cleanup after a red check, and a fake-port setup → baseline → acceptance flow bound to the resolved base tree. No real Git check commands, setup cache, baseline cache, public CLI route, account, or provider call ran in those fakes.

No Quint `R(slice)` replay was run. The required shared coherent migrated cohort is not available in this checkout; therefore no 500 × 25 traces are claimed for seeds 17, 23, or 41, and no replay divergence result is claimed.

## Conflicts and closure gates

- Exact historical v1.2 conflicts observed: none. The public handler stopped every case before its assertion, so these results cannot identify behavioral compatibility or incompatibility.
- Draft `D-BASE-01–05` fixtures, including `D-BASE-04`, are not frozen v1.2 cases. Wait for shared frozen v1.3 IDs and their migrated models/goldens before claiming those gates. `S28` also remains open until the concrete v3 port from package 54 is wired and the migrated cache cases run.
- I1 uncached-base correctness and the assigned approval cases need the executable public approval integration; rerun them on a changed integrated revision and retain both results.
- Linux native checks, optional runtimes, and live comparison evidence are unavailable here. No live provider or account access was used.
- Component evidence does not close I5 or behavior acceptance. The setupcache v2 and independent v3 implementation/wiring, shared v1.3 suite, and exact-base regression remain closure work.
