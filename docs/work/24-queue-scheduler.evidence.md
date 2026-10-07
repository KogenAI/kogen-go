# 24 — Queue scheduler evidence

## Revisions and scope

- Worker branch: `kgo/24-queue-scheduler`.
- Scheduler source commit: `53d17d84e103e559993935d1d0136e76de110529` (parent `5b87ad33f54e5cebf49262f7b47910da1819e0a0`).
- CLI and private adapter binaries were built from the same Go source tree: `bin/kogen` SHA-256 `b975a197ca922a2c772185ecb709d2a5591bfd591d7324ec6c35827ec8f9583c`; `bin/kogen-xspec` SHA-256 `36d149cd20c1b9856246fc6dc751a14c2a07977484ce24cff5f1b55756619a2c`.
- The queue xspec adapter remains unwired (`internal/xspec/queuestatus` is a placeholder); the binary digest is build identity only, not adapter evidence.
- Changed files: `internal/queue/schedule/doc.go`, `scheduler.go`, and `scheduler_test.go`, plus this evidence note and the gate receipt. No shared contracts, CLI routes, suite files, goldens, or replay harness files changed.

## Implemented component

- The scheduler orders eligible approvals by descending priority, approval time, then slug. Blocked candidates stay out of selection and can be updated by a refreshed status snapshot.
- The per-drain attempted set uses `(slug, ApprovalKey)`, where `ApprovalKey` is the stable digest of the exact approved Intent and acceptance bytes. Republishing the same content does not repeat a Build; a changed key remains eligible. The approval commit is not used as the attempt identity.
- A target-branch mismatch enters the `skipping` phase and is completed as `skipped`, without increasing Build or landed counts. A stopped Build is counted and remains queued. Landed, failed, parked, and recoverable per-Intent provider failures continue to the next approval; drain-stopping environment/provider/controller outcomes use exits 3/4/70.
- A queue-stop request ends after the current Build. Its final exit follows §1.7.4's normal count rule: prior failed Builds yield exit 1; all landed or no Builds yields 0.
- Effects are in-memory transition state only. The component does not read Git/status, run Builds, publish output, or acquire/release the process lock.

## Inputs read

- `docs/work/WORKER-RULES.md`, `PLAN.md`, `QUEUE-source.md`, `INTERFACES.md`, and `INPUTS.md`.
- Authoritative target: `kogen-spec` commit `e19dd1c21c19c5be1201c3b6a42c59c28b5c2887`. Read `spec/01-cli.md` §1.7.4, `spec/02-formats.md` §2.11, `spec/03-build.md` B0 and §3.11, and `CHANGES-v1.3.md` (no queue-specific change).
- Structural Rust reference: `kogen-rs` commit `a402540b39cedc7f788472297add7ae2f8a6631a`; read `crates/kogen-core/src/queue/scheduler.rs`, its tests, `crates/kogen-core/src/build/queue.rs`, `crates/kogen-core/src/queue.rs`, and `crates/kogen-xspec/src/xspec/queue.rs`.

## Commands and results

Pinned worker PATH was set as required by `WORKER-RULES.md` (Git 2.54.0, Go 1.27.1, Python 3.14.7, Node 24.21.0).

```sh
GIT_CONFIG_GLOBAL=/dev/null make check
```

Result: **passed**. Formatting, vendor fingerprints, `go vet ./...`, all Go tests (including `internal/queue/schedule`), and both binaries passed. This is component evidence, not behavior acceptance.

The exact assigned frozen v1.2 command was run once:

```sh
make build
SUITE="$HOME/cx/kgo/inputs/conformance-v1.2"
EVIDENCE="$HOME/cx/kgo/evidence/24-queue-scheduler"
mkdir -p "$EVIDENCE"
PYTHONDONTWRITEBYTECODE=1 PATH="$HOME/.local/share/mise/installs/python/3.14.7/bin:$PATH" \
  "$SUITE/bin/kogen-conformance" run --kogen "$PWD/bin/kogen" \
  --profile cli,state,approval,shape,build,ladder,provider,custody,format,v1.2 --case 'build-01,build-41,build-42,v1.2-112-provider-11,v1.2-124-build-31' \
  --jobs 2 --time-scale 0.02 --workdir "$EVIDENCE/work" \
  --out "$EVIDENCE/results.jsonl"
```

`make build` passed. The runner resolved these five literal IDs to one instance each: `build-01`, `build-41`, `build-42`, `v1.2-112-provider-11` (replacement for `provider-11`), and `v1.2-124-build-31` (replacement for `build-31`). Result: **0 passed, 5 failed, 0 errors, 0 skipped, 0 unimplemented; 5 instances total**. Results and workdirs are retained at `/Users/almirsarajcic/cx/kgo/evidence/24-queue-scheduler/`.

- `build-01` reached `kogen queue start` and failed at step 1: exit 2, empty stdout instead of `queue: nothing to build`, stderr `kogen: implementation bootstrap; command routes are not wired`.
- `build-41`, `build-42`, `v1.2-112-provider-11`, and `v1.2-124-build-31` stopped at their approval step (step 2, except step 3 for `v1.2-124-build-31`) with the same bootstrap error.
- None exercised scheduler behavior. No compatible v1.2 behavior pass or historical case conflict is established by these results. No retry was made.

## Replay, conflicts, and closure

- `R(queue)` was not run. This worktree has no same-revision queue xspec adapter, and the frozen committed Quint queue model identifies itself as v1.2; it is not the required shared coherent migrated v1.3 cohort. Seeds 17, 23, and 41 (500 traces ×25 steps each), full-observation conformance, and first-divergence artifacts are therefore **not run / unavailable**, not passes. The source oracle and goldens were not changed.
- Planned D-* fixtures have no shared frozen v1.3 IDs yet. No draft fixture or v1.3 behavior gate is claimed; wait for the coordinator's shared frozen IDs and the integration replay round.
- Exact reference/model discrepancy to reconcile before replay: committed `quint/slices/queue/spec/queue.qnt` line 78 finishes `stopped_on_request` with exit `0`; target `spec/01-cli.md` §1.7.4 line 175 requires the normal count-based exit unless a Build stopped the drain. The Go transition follows the target spec. No selected v1.2 CLI case reached this assertion, so this is a model/spec reconciliation item, not an observed conformance-case failure.
- Linux behavior and the production queue lifecycle remain unverified. I3 must wire the queue route and prove the approval → queue → provider → gate → CAS → status path; package 66 must add the production-backed queue/status adapter. Those closures must rerun the applicable behavior cases and the coherent shared `R(queue)` cohort.
