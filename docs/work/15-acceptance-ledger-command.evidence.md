# 15 acceptance ledger and command evidence

## Revisions and environment

- Worker branch: `kgo/15-acceptance-ledger-command`.
- Implementation commit: `f183e7904442419184cd0f04e620e645974e2843`.
- Parent revision: `275641611dc0cbab2507958612e8320463e800de`.
- Acceptance package sources: `internal/acceptance/ledger.go` SHA-256
  `e43657435758f83626353799553cd710cdc1828b392d919cf34ce474dd00621c`,
  `internal/acceptance/adapter.go` SHA-256
  `71271b05f16ad33f67e3fe9232a356a6fa0ca0336441ccfc0bc2e4b7c7b72c4d`, and
  `internal/acceptance/command/command.go` SHA-256
  `7316acd1068e2f4aa79763bccdfa77a3a70e2aba5cd7d15ea11b0660d499a077`.
- Target spec: v1.3-draft commit
  `e19dd1c21c19c5be1201c3b6a42c59c28b5c2887`; `CHANGES-v1.3.md` was read.
- Rust reference: `a402540b39cedc7f788472297add7ae2f8a6631a`.
- Frozen v1.2 suite: `/Users/almirsarajcic/cx/kgo/inputs/conformance-v1.2`,
  sourced from conformance commit `0f93bad988fb8d7a8eff4e94954d1db0a046c89d`.
- Runner: `kogen-conformance` `v1.2+unknown`; `runner.py` SHA-256
  `0135fd9095294b3701fc51ebdcc23e596b75f91cd53c10f365dead6824ed8f0e`;
  `fake_server.py` SHA-256
  `0c23f7b9b6c5603a39ba6fe18d64930bd3d45e443efcacc8c310177f467ce08f`.
- Pinned tools: Go `go1.27.1 darwin/arm64`, Git `2.54.0`, Python `3.14.7`,
  Node `24.21.0`. Host: `macOS-26.7.1-arm64-arm-64bit-Mach-O`.
- A final `make build` succeeded after the implementation commit. Current
  `bin/kogen` SHA-256 is
  `316d3423fd34a3c3bd15afb3f940bdb3bcda188e0b04ef3e482a69ee27988383`;
  `bin/kogen-xspec` SHA-256 is
  `9ad356f05de64a659b06904d08adaaef2cad42591ff40bea054a2d922610593e`.
  The conformance invocation itself built `bin/kogen` immediately before the
  run, before the final acceptance-timeout default was corrected from the
  process default to the project acceptance default. That run stopped at the
  public bootstrap route and did not invoke the acceptance adapter.

## Implemented component

`internal/acceptance/ledger.go` strictly parses UTF-8 JSONL rows with exactly
`tag`, `test`, and `status`, aggregates each expected `A<n>` only when it has
at least one row and every matching row passed, detects unknown tags for the
current Intent, and adds `suite` when the runner exits nonzero despite every
expected item passing. Missing, empty, malformed, unavailable, timed-out and
tree-mutating outcomes remain distinct observations.

`internal/acceptance/adapter.go` implements the shared check adapter through
the supervised process port. It records actual exit, timeout, unavailable,
output-tail and before/after tree identities. Candidate tree capture delegates
to `gitio.SnapshotCandidateTree` with immutable base metadata. Private process
logs and command report directories use rooted filesystem operations.

`internal/acceptance/command/command.go` validates configured command paths,
resolves source and candidate paths, replaces `{path}` in argv, supplies a
complete deterministic child environment with `KOGEN_LEDGER_REPORT` and
`KOGEN_INTENT_SLUG`, clears stale reports, rejects report paths outside the
private run root, and interprets the report only after the supervised run and
the second tree snapshot.

## Commands and results

The worker shell used the pinned PATH required by `WORKER-RULES.md`.

| Command / attempt | Result |
| --- | --- |
| `GOMAXPROCS=2 go test -p=2 -parallel=2 ./internal/acceptance/...` — initial local run | Failed at compile time because the new ledger matrix declared an unused test fixture. The fixture was removed; no production code change was needed for this compiler error. |
| `GOMAXPROCS=2 go test -p=2 -parallel=2 ./internal/acceptance/...` — after the fixture correction | Passed for `internal/acceptance` and `internal/acceptance/command`; ExUnit/Rails packages compiled and have no tests in this package. |
| `GIT_CONFIG_GLOBAL=/dev/null make check` | Failed in the unrelated `internal/queue/lock` test `TestConcurrentAcquireCreatesOneOwner` at `lock_test.go:127`: `concurrent Acquire() error: queue.pid is not a safe regular owner file`. The check reached the full test suite after format, vendor fingerprint and vet stages; it stopped before the check's own temporary builds and source-mutation guard. Other packages shown in that run passed. No files outside package ownership were changed. |
| `GOMAXPROCS=2 go test -p=2 -parallel=2 -count=1 ./internal/acceptance/...` — final source | Passed for `internal/acceptance` and `internal/acceptance/command`; ExUnit/Rails packages compiled and have no tests in this package. |
| `git diff --cached --check` | Passed before the implementation commit. |
| `make build` — immediately before the assigned oracle run | Passed; built both binaries. |
| `make build` — final implementation source | Passed; final binary hashes are listed above. |
| Assigned frozen v1.2 command below | Runner completed 4 selected cases / 5 expanded instances: 0 passed, 4 cases failed, 0 errors, 0 skipped. Every case stopped before the package behavior because the public command routes are still bootstrap stubs. Results and work directories are retained. |

The assigned command was run once, without modifying the oracle or goldens:

```sh
make build
SUITE="$HOME/cx/kgo/inputs/conformance-v1.2"
EVIDENCE="$HOME/cx/kgo/evidence/15-acceptance-ledger-command"
mkdir -p "$EVIDENCE"
PYTHONDONTWRITEBYTECODE=1 PATH="$HOME/.local/share/mise/installs/python/3.14.7/bin:$PATH" \
  "$SUITE/bin/kogen-conformance" run --kogen "$PWD/bin/kogen" \
  --profile cli,state,approval,shape,build,ladder,provider,custody,format,v1.2 --case 'approval-10,approval-11,build-40,shape-16' \
  --jobs 2 --time-scale 0.02 --workdir "$EVIDENCE/work" \
  --out "$EVIDENCE/results.jsonl"
```

The immutable result file is
`/Users/almirsarajcic/cx/kgo/evidence/15-acceptance-ledger-command/results.jsonl`.
The runner metadata records `2026-10-07T19:23:58Z`, host/platform, profile,
selector, workdir and result path. The run resolved these IDs and instances:

| Effective case ID | Instances | Result and exact blocker |
| --- | ---: | --- |
| `approval-10` | 1 | Failed. `kogen intent approve greet` exited 2 rather than 1; stdout was empty and stderr was `kogen: implementation bootstrap; command routes are not wired`. |
| `approval-11` | 1 | Failed. `kogen intent approve greet` exited 2 rather than 3; stdout was empty and stderr was `kogen: implementation bootstrap; command routes are not wired`. |
| `shape-16` | 2 | Both `1:red` and `2:127` failed before shaping behavior. The command exited 2 rather than 0 and 3 respectively; stdout was empty and stderr identified the unwired bootstrap routes. The runner noted that no fake provider request was reached. |
| `build-40` | 1 | Failed at approval before Build start. `kogen intent approve greet {hash8:greet}` exited 2 rather than 0; stdout was empty and stderr identified the unwired bootstrap routes. No fake provider request was reached. |

These are not behavior passes. The adapter's local tests exercise the ledger
matrix, including the required nonzero-exit/all-green `suite` case, but do not
replace the public CLI or a production-backed Git/process integration run.

## Local effects, conflicts, and closure gates

- Local unit tests used temporary roots and scripted process/tree observations;
  no live provider, account, OAuth port, or user project was accessed. `make
  build` wrote only ignored binaries under `bin/`. The selected oracle's work
  directories and failed JSONL were retained under the absolute evidence path.
- No exact v1.2/v1.3-draft assertion conflict applies to the selected IDs.
  `conflicts` is empty in the gate receipt. These cases failed because the CLI
  and approval/shape/Build routes are unwired; no historical behavior was
  reinterpreted to make them green.
- I1 owns the public foundation and approval routes. I3 owns the executable
  approval → queue → provider → gate → CAS → status path; these selected
  behavior cases must be rerun on the integrated revision and the new result
  retained. The current `cmd/kogen` bootstrap refusal blocks those cases.
- The complete required `make check` remains failed at the queue-lock fixture
  above. This component's final package test passed, but repository-wide check
  acceptance is still open.
- The shared coherent migrated v1.3 Quint cohort and frozen v1.3 IDs are not
  available. R(slice) was not run; it still requires a scratch cohort copy,
  seeds 17/23/41, 500 traces × 25 steps per seed, and conformance against the
  same-revision private binary. Planned D-* fixtures are not v1.2 cases and are
  not claimed as passing.
- Linux, optional runtimes, live comparison, race/custody evidence, and the full
  236-case / 570-instance historical denominator were not verified here.

This receipt is **component** evidence only. It does not confer behavior
acceptance.
