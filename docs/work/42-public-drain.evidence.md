# 42 — Public drain evidence

## Revisions and scope

- Worker branch: `kgo/42-public-drain`.
- Worker implementation commit: `939108b240d762fd2dd80fa087f48d35d5d7e879` (parent `de3cd35a63851ccfd5c5e1063ca6b4acd5df8fac`).
- CLI and private adapter were built from that source tree. `bin/kogen` SHA-256: `12b7bb38111d460dd99cc73c24ab646e7a71a8717f0b420a138ba5e643f1c33c`; `bin/kogen-xspec` SHA-256: `cf9388d7b30507a0690e0ec3d3be3fad41c7dfe6c0e9480af230e5b8fb516d08`.
- `kogen-xspec` is a build identity only for this package: `internal/xspec/queuestatus` remains a placeholder and does not import the drain.
- Changed package files: `internal/queue/drain/{doc.go,drain.go,drain_test.go}`, plus this evidence note and the gate receipt. No CLI/app routes, shared contracts, scheduler, lock, suite, goldens, or replay harness files changed.
- Host: macOS 26.7.1 arm64; Git 2.54.0; Go 1.27.1; Python 3.14.7. No Linux run was available.

## Implemented component

- `Controller.Start` prepares the state root through `safefs`, acquires the real queue owner lock, runs recovery, loads a status-derived queue snapshot, and drains one approval at a time through required recovery, snapshot, and Build ports. `SingleBuilder` adapts `single.Controller` to the Build port.
- The drain streams `building`, outcome, and final count lines. Failed and parked Builds continue. A stopped Build ends the drain, is counted, and stays queued even when the refreshed status snapshot omits it. Stop requests finish only after the current Build; a stop before the first Build reports zero Builds and retains the selected approval.
- Ordering and the per-drain attempted approval set remain in `queue/schedule`. The drain refreshes the status-derived candidate set after each completed Build before choosing the next approval. Branch mismatches are streamed as skipped and do not affect counts or exit status.
- `Controller.Stop` writes the real `queue.stop` request through `queue/lock`. Detached start delegates to the lock package's new-session relaunch, `queue.log`, and owner-acquisition protocol. A second live start reports the current owner.
- SIGINT and SIGTERM cancel the active Build context and return exits 130 and 143 without a drain final line. When a run identity is available, the drain records `interrupted` with `sigint` or `sigterm` using `journal.RunStore` and a rooted state descriptor; recovery can then close it as failed/interrupted.
- Focused tests cover subsequent Builds and counts, stopped-Intent retention, stop-after-current, stop-before-first-Build, live-owner stop, and SIGTERM journal/exit behavior.

## Inputs read

- `docs/work/WORKER-RULES.md`, `docs/work/PLAN.md`, `docs/work/QUEUE-source.md`, and the package task note.
- Target spec: `kogen-spec` commit `e19dd1c21c19c5be1201c3b6a42c59c28b5c2887`. Read `spec/01-cli.md` §§1.7.4 and 1.8, `spec/02-formats.md` §2.11, `spec/03-build.md` §3.11, and `spec/05-sandbox-custody.md` §5.5. `CHANGES-v1.3.md` has no queue/drain-specific delta.
- Rust structural reference: `kogen-rs` commit `a402540b39cedc7f788472297add7ae2f8a6631a`; read `crates/kogen-core/src/build/queue.rs`, `crates/kogen-core/src/queue/scheduler.rs` and tests, and `crates/kogen-core/src/queue/ownership.rs`.

## Commands and results

Pinned worker PATH was set as required by `WORKER-RULES.md` (Git 2.54.0, Go 1.27.1, Python 3.14.7, Node 24.21.0).

```sh
GOMAXPROCS=2 go test -p=2 -parallel=2 ./internal/queue/drain
```

Result: passed. The same package tests also passed in the full check below.

```sh
GOMAXPROCS=2 GIT_CONFIG_GLOBAL=/dev/null make check
```

Result: **passed** on the implementation commit. Formatting, vendor fingerprints, `go vet ./...`, all Go tests (including `internal/queue/drain`), and both binary builds passed.

The assigned frozen v1.2 command was run once under the shared `~/cx/kgo/gates.lock/custody` mkdir lock. The lock created by this worker was released. `make build` passed. The runner metadata reports `v1.2+unknown`, macOS 26.7.1 arm64, Git 2.54.0, and 0.02 time scale. The literal selected IDs resolved to **19 cases / 20 instances**: `build-01`, `build-32`, `build-35`–`build-42`, `cli-28`–`cli-30`, `custody-04`, `custody-05`, `v1.2-124-build-31`, `v1.2-129-cli-27`, `v1.2-65-build-33`, and `v1.2-66-build-34`. All IDs resolved; `custody-05` had two instances and every other ID had one.

Result: **0 passed, 19 failed, 0 errors, 0 skipped, 0 unimplemented; 20 instances**. The retained JSONL is `/Users/almirsarajcic/cx/kgo/evidence/42-public-drain/results.jsonl`; workdirs are under `/Users/almirsarajcic/cx/kgo/evidence/42-public-drain/work`.

- `build-01` failed at step 1 (`kogen queue start`): exit 2, empty stdout instead of `queue: nothing to build`; stderr was `kogen: implementation bootstrap; command routes are not wired`.
- `build-35` failed at its queue start step with the same exit 2, empty stdout, and bootstrap stderr.
- The other 17 selected cases stopped at `intent approve` before reaching their queue/drain assertions. Each failed with exit 2 instead of 0 and the same bootstrap stderr. This includes both instances of `custody-05`.
- None of these runner results exercises the new drain component. No unwired case is counted as a behavior pass. No historical v1.2-v1.3 behavior conflict was established by this run.

The exact acceptance invocation was:

```sh
make build
SUITE="$HOME/cx/kgo/inputs/conformance-v1.2"
EVIDENCE="$HOME/cx/kgo/evidence/42-public-drain"
mkdir -p "$EVIDENCE"
PYTHONDONTWRITEBYTECODE=1 PATH="$HOME/.local/share/mise/installs/python/3.14.7/bin:$PATH" \
  "$SUITE/bin/kogen-conformance" run --kogen "$PWD/bin/kogen" \
  --profile cli,state,approval,shape,build,ladder,provider,custody,format,v1.2 --case 'build-01,build-32,build-35,build-36,build-37,build-38,build-39,build-40,build-41,build-42,cli-28,cli-29,cli-30,custody-04,custody-05,v1.2-124-build-31,v1.2-129-cli-27,v1.2-65-build-33,v1.2-66-build-34' \
  --jobs 2 --time-scale 0.02 --workdir "$EVIDENCE/work" \
  --out "$EVIDENCE/results.jsonl"
```

## Replay, conflicts, and closure

- `R(queue)` / `R(slice)` was not run. The scratch copy of the shared coherent migrated v1.3 Quint cohort and a same-revision production-backed queue/status adapter are unavailable. The required spec-first runs for seeds 17, 23, and 41 at 500 traces × 25 steps each, same-revision private binary, full-observation conformance, and divergence artifacts are therefore **not run / unavailable**, not passes. The source oracle and goldens were not changed.
- Planned D-* fixtures are not frozen v1.2 cases. Wait for shared frozen v1.3 IDs before claiming those gates.
- I3 must wire the app route and real recovery/status/Build effects, then rerun this exact selected command on the changed integration revision while retaining this failed run. The black-box runner currently reaches `internal/app.Bootstrap`, so behavior acceptance remains open.
- Linux process behavior and optional runtime/live comparison gates were not run. No live provider or account access was used.
- Exact historical conflicts: **none established**. All 19 cases failed before the behavior assertions because the public routes remain unwired; this is not evidence of a semantic v1.2-v1.3 conflict.
