# D2 baseline-independence-regression evidence

## Revision and scope

- Worktree/branch: `/Users/almirsarajcic/Areas/Kogen/kogen-go-wt/D2-baseline-independence-regression`, `kgo/D2-baseline-independence-regression`.
- Source revision before D2 changes: `ea5b8ea93d4515205ca2e3115c6bedcd12d934f0`.
- Prerequisite implementation revisions in that source tree: setup/baseline cache `c5b14d5b4415c38d3b7bbcffb3c41b4bc8879add`; approval preparation `3368458e81b79b9486d626e4081bcbd32ed1180b`.
- CLI and private adapter source revision: `fba340e5928b2a6c6644bb056ff76b3eb1276616` (`cmd/kogen/main.go` and `cmd/kogen-xspec/main.go`). They were compiled by `make check`, not executed for D2.
- Target spec: `kogen-spec` commit `e19dd1c21c19c5be1201c3b6a42c59c28b5c2887`; relevant clauses: `CHANGES-v1.3.md` §2, `spec/02-formats.md` §2.9, `spec/03-build.md` §3.3 and §3.7.2.
- Rust reference: `kogen-rs` commit `a402540b39cedc7f788472297add7ae2f8a6631a`; inspected `crates/kogen-core/src/approval/checks.rs`, `approval/checks/cache.rs`, and `run/setup_cache/key.rs`.
- Frozen v1.2 oracle: `kogen-conformance` commit `0f93bad988fb8d7a8eff4e94954d1db0a046c89d`, snapshot `~/cx/kgo/inputs/conformance-v1.2`.
- Host/toolchain: macOS 26.7.1, darwin/arm64; Go 1.27.1; Git 2.54.0. Commands used the pinned worker PATH from `WORKER-RULES.md`.

Only the reserved D2 test files and D2 documentation were changed. No provider, account, external application, global Git configuration, source oracle, suite, or golden was touched.

## Local acceptance evidence

`internal/setupcache/baseline_v3_test.go` adds:

- `TestBaselineV3SourceOnlyTreeChangesReuseSetupButRefreshBaseline`: three source-only trees retain the same lockfile/setup key; setup runs once, while each exact tree gets a fresh baseline. The sequence is red → green → reintroduced red, and a repeated identical tree reuses its red row and renders the same card/hash.
- `TestBaselineV3FullContextUnknownAndLegacyIdentitiesMiss`: changed checked tree, setup key, check definition/order/deadline, child environment, toolchain, platform, and adapter all miss; unknown and v2 legacy identities compute every time.
- `TestBaselineV3LocalSerializationVector`: pins the current Go v3 serialization and deterministic digest for review. It is not claimed as the shared cross-language fixture; see the exact draft conflict below.

`internal/approval/prepare/checked_base_test.go` adds `TestPrepareChecksDirtyCheckoutAgainstExactBaseAndTracesBaseTreeCacheKey`. It presents dirty checkout bytes that differ from every resolved base, then asserts baseline checks only see the corresponding exact-base scratch contents. The trace verifies `ScratchRequest.BaseTree`, setup request `BaseTree`, `BaselineKey.CheckedBaseTree`, and `Prepared.BaseTree`. A source-only base change replaces red with green, a later base reintroducing the defect computes red, and the same-tree card and hash calls reuse only that tree's baseline.

Commands and results:

```sh
gofmt -w internal/setupcache/baseline_v3_test.go internal/approval/prepare/checked_base_test.go
GIT_CONFIG_GLOBAL=/dev/null GOMAXPROCS=2 go test -p=2 -parallel=2 ./internal/setupcache ./internal/approval/prepare
# PASS: both packages.
GIT_CONFIG_GLOBAL=/dev/null make check
# Final-revision attempt 1: failed in unrelated internal/gitio test; see retained result below.
GIT_CONFIG_GLOBAL=/dev/null make check
# Final-revision attempt 2: PASS: format, vendor fingerprints, vet, all tests, and both builds.
python3 tools/package-gate.py D2-baseline-independence-regression
# PASS: accepted compiled component evidence; behavior remains with closure round.
```

The first full check, before a test-comment/name-only edit, also passed. On the final test revision, `GIT_CONFIG_GLOBAL=/dev/null make check` had one failed run: `internal/gitio` test `TestRunnerSeparatesStreamsAndTimesOutHangingGit` reported `captured stdout = "", want "stdout-before-hang\n"; stderr=""` at `gitio_test.go:149`. No implementation change followed that failure; the next exact full-check command passed. Both outcomes are retained here. The full check used Go 1.27.1 and Git 2.54.0. Its build outputs are created in a temporary directory and removed, so no persistent CLI binary or binary hash was available; no CLI behavior was exercised.

## Oracle resolution and compatibility

- `docs/work/D2-baseline-independence-regression.cases` is empty: **0 frozen v1.2 case IDs / 0 instances resolved**. The assigned acceptance stanza says no black-box cases are assigned. No conformance runner command was run and no case is counted as passing.
- Planned D-BASE-01–05 are not frozen v1.2 cases. There are **0 compatible oracle passes** and **0 historical v1.2 conflicts observed** for this component because there were no assigned oracle instances.
- The frozen v1.2 suite remains the only permitted oracle until the coordinator freezes shared v1.3 inputs. The Rust modules above are historical reference code, not a current v1.3 implementation or fixture source.

## Exact draft conflict and fixture gap

Frozen `spec/02-formats.md` §2.9 requires the v3 baseline key to be the SHA-256 of canonical JSON with sorted keys and no spaces; its `child_env` field is an object. At the tested source revision, `prepare.NewBaselineKey` marshals a struct in declaration order and serializes `baselineKeyDocument.ChildEnvironment` as a sorted string array. For the fixture values in `TestBaselineV3LocalSerializationVector`, the Go serialization hash is `8a9d09574395261b1633f2324b97c4a4faf8f164484aad4e5de1e2f352ce8fe6`; the canonical sorted-object serialization described by §2.9 hashes to `fe65716657b6320a5fcdf9972e16b08748c92c26ae54fa70c6987926fdb891d9`. This is an exact v1.3-draft representation mismatch in the package-54 implementation and means the local vector cannot establish cross-language canonical parity. It is outside D2's owned files and remains open for package 54/shared fixture resolution; it is not reported as a historical v1.2 case conflict.

The frozen Rust reference's `run_setup_and_baseline` computes its approval key from setup-cache material plus checks. With narrowed `setup_inputs`, that material does not bind the exact source tree; its checks also use the checkout path. This records the historical implementation limitation only and does not change the v1.3 target.

## Replay, effects, and deferred closure gates

- No Quint `R(approve)` replay was run. The required coherent shared migrated cohort is unavailable. No 500 traces × 25 steps are claimed for seeds 17, 23, or 41, and no divergence result is claimed.
- The frozen `approve.qnt` at `e19dd1c` models baseline identity as `(baseTree, cacheKey)` and includes `baseTreeCacheTest`; D2's Go approval test mirrors that trace locally. The shared migrated model/golden/adapter cohort and frozen replacement IDs remain required for behavior acceptance.
- I5 must wire setup v2, baseline v3, and production approval preparation; rerun integrated behavior cases on the same revision and preserve all results. D2's worker commit alone does not close I5.
- Package 00 remains a foundation task. Shared v1.3 conformance IDs, canonical cross-language v3 fixture, and coherent Quint cohort remain open. Linux runtime, optional-runtime, and live-comparison gates require their external evidence.
- No local effects escaped temporary directories. No live provider calls or account access occurred.
