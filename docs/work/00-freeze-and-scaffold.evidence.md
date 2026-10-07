# 00-freeze-and-scaffold evidence

Recorded 7 October 2026 for the assigned worktree
`/Users/almirsarajcic/Areas/Kogen/kogen-go-wt/00-freeze-and-scaffold` on
`kgo/00-freeze-and-scaffold`. The starting HEAD was
`fba340e5928b2a6c6644bb056ff76b3eb1276616`; the assigned worktree was clean.

## Revisions and frozen inputs

- Spec target: `kogen-spec` commit
  `e19dd1c21c19c5be1201c3b6a42c59c28b5c2887`; committed content manifest SHA-256
  `52388b2be73aade4b4fb2535cef475baefae1d3f662c1f2221afed737154468f`.
- Frozen diagnostic oracle: `~/cx/kgo/inputs/conformance-v1.2`, sourced from
  `kogen-conformance` commit `0f93bad988fb8d7a8eff4e94954d1db0a046c89d`; its
  snapshot content manifest is recorded in [INPUTS.md](INPUTS.md).
- Rust reference read-only revision: `kogen-rs`
  `a402540b39cedc7f788472297add7ae2f8a6631a`.
- Go foundation / CLI source revision: `93f7a74e15ff13b0e42b64bd66f9a564fd452be3`.
  The package receipt in this note is committed separately and does
  not change that tested source revision.
- Production adapter revision: none. `cmd/kogen-xspec` compiles, but
  `internal/app.Bootstrap` still explicitly refuses unwired routes with exit 2;
  it is not an adapter or a passing case. `cmd/kogen` is equally unavailable.

The oracle input's standard profiles plus v1.2 overlay resolve to 236 cases /
570 instances; the six ExUnit cases are outside that denominator. The assigned
`docs/work/00-freeze-and-scaffold.cases` is empty, so this package resolves 0
black-box IDs / 0 instances. No black-box conformance command was assigned or
run. The 236/570 count is oracle inventory evidence, not a Go pass.

The complete D1–D3 and should-fix ledger, hashes for all 35 embedded v1.2
corpus files, the unrelated upstream dirty patches, exact historical ladder
conflict IDs, and open shared decisions are in [INPUTS.md](INPUTS.md). The
ports and their invariants are in [INTERFACES.md](INTERFACES.md).

## Commands and results

The worker shell used the required pinned PATH: Git 2.54.0, Go 1.27.1, Python
3.14.7 and Node 24.21.0. Host: `darwin/arm64`.

| Command / attempt | Result |
|---|---|
| `make fmt` | Passed on the final source state. It formatted Go files; status review showed no unowned source changes. |
| `GIT_CONFIG_GLOBAL=/dev/null make check` — initial attempt | Failed before vet/tests/build: `docs/work/VENDOR.sha256` had the old `go.mod` checksum after a temporary redundant `toolchain go1.27.1` line was tried. The mismatch was `go.mod`; vendored files were unchanged. |
| `GIT_CONFIG_GLOBAL=/dev/null make check` — second attempt | Failed before vet/tests/build: Go reported `updates to go.mod needed, disabled by -mod=vendor`. `go mod tidy -diff` showed Go 1.27.1 removes `toolchain go1.27.1` when it duplicates `go 1.27.1`; that redundant line was removed and the original `go.mod` fingerprint restored. `GOTOOLCHAIN=local`, the `go 1.27.1` directive and mise pin keep toolchain downloads disabled and the version fixed. |
| `GIT_CONFIG_GLOBAL=/dev/null make check` — after module correction | Passed: format check, vendor fingerprints, `go vet ./...`, `go test -count=1 -parallel=2 ./...`, and both `CGO_ENABLED=0` builds. |
| `GIT_CONFIG_GLOBAL=/dev/null make check` — final source state | Passed after the session diagnostics and final shared-port review. `internal/cli/data`, `internal/contract`, and `internal/testkit` tests passed; all packages compiled and both CLI binaries built. The check's own source-mutation guard passed. |
| `PYTHONDONTWRITEBYTECODE=1 python -` corpus comparison | Passed after correcting the comparison filter: all 35 selected data/help files match the frozen v1.2 input byte-for-byte; digest `869c526d0f8e26ae482ddad1ecf47cd04ad038adbda590a5ae29481635e97d40`. One earlier comparison script included `data.go` and `data_test.go` in the input-file inventory and exited on those implementation-only paths; it found no corpus byte mismatch. |
| `git diff --check` | Passed. |

`make check` ran with `GOMAXPROCS=2`, `GOFLAGS=-mod=vendor -p=2`, and network module resolution disabled. `go.sum`, vendored dependency bytes, `mise.toml`, and module version pins were already frozen at the assigned base and remain unchanged.

## Local effects and compatible passes

- Added typed shared records and ports for failures, process supervision, Git,
  rooted filesystem access, clock/jitter, acceptance checks, exact-tree
  verification receipts, state observations, landing effects, provider
  requests/evidence and role manifests.
- Added an append-only persistent conversation session that copies raw JSON
  items and carries opaque per-thread routing state. Ordinary diagnostics redact
  request/response values and prompt/history contents.
- Embedded the frozen static corpus and v1.2 help overlay. The component test
  verifies the exact canonical corpus hash and required help/data entries.
- Added hermetic Git fixtures using an isolated HOME, empty global/system
  configuration, local fixture identity, disabled test signing/hooks, a bare
  local origin and a bounded Git subprocess timeout. Tests verify the clean seed
  checkout and that inherited hostile Git configuration has no effect.
- `make check` creates only temporary build outputs and temporary Git fixtures;
  the source-mutation guard passed. No live provider, credential, OAuth port
  1455, real project origin or user checkout was accessed.
- One read-only oracle package import generated `__pycache__` bytecode in the
  v1.2 input directory; only that generated cache was removed. The final
  inspection found no bytecode and the frozen oracle files were otherwise
  unchanged.

These are component-ready passes only. No unavailable CLI stub, xspec adapter,
unwired case or behavior gate is counted as passing.

## Conflicts and deferred closure gates

- Historical v1.2 diagnostic overlay IDs
  `v1.2-73-ladder-05`, `v1.2-74-ladder-06`, `v1.2-75-ladder-07`, and
  `v1.2-79-ladder-11` expect auditor demotion behavior that conflicts with the
  v1.3 observational auditor. They were not run by package 00. The wider
  `v1.2-76-ladder-08`, `v1.2-77-ladder-09`, and `v1.2-78-ladder-10` historical
  diagnostic anchors are also listed in [INPUTS.md](INPUTS.md); no result is
  claimed for any of them.
- D1–D3 and `D-SHAPE-*` / `D-CACHE-*` are planned fixture labels only. Shared
  migrated models, scenarios, goldens, adapters and a frozen v1.3 suite/ID
  manifest are unavailable. Do not claim any draft fixture pass until oracle
  owners freeze that coherent cohort.
- R(slice) was not run. It requires a scratch copy of the shared migrated
  cohort, seeds 17/23/41, 500 traces × 25 steps per seed, and same-revision
  conformance against the private binary.
- The shared decisions in [INPUTS.md](INPUTS.md) remain open, including
  serializer/`tool_choice`, provider terminal policy, status schema,
  post-CAS/durable-record reconciliation, recipe text versus `experimental_r4`,
  and reachable landing trailers versus slug reuse. Affected gates must wait
  for shared decisions and fixtures.
- I1 owns public command routing and foundation integration; I2/I3/I4/I5/I6/I7/I8
  and D1/D2/D3 retain their stated behavior closure. Linux, optional runtimes,
  race/custody gates, production replay, live cache comparison and release
  manifests remain unverified. No results JSONL exists because no behavior
  cases ran.

The receipt is **component** only. Worker exit, compilation and this commit do
not confer behavior acceptance.
