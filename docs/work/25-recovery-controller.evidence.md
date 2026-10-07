# 25-recovery-controller evidence

## Revision and inputs

- Worker branch: `kgo/25-recovery-controller`; component commit: `718a40b9f1b3a430cff8cf9c372c3d5fbcd7812b`. Base at start: `cbbe79aba2d0edc77a0ccfcde7cc5b8af9202975`.
- Target spec: `kogen-spec` `e19dd1c21c19c5be1201c3b6a42c59c28b5c2887`; reviewed recovery clauses in `spec/02-formats.md`, `spec/03-build.md`, `spec/05-sandbox-custody.md`, plus `CHANGES-v1.3.md`. Rust reference: `kogen-rs` `a402540b39cedc7f788472297add7ae2f8a6631a`, recovery project and replay modules.
- Host/toolchain: macOS 26.7.1 arm64, Go 1.27.1, Git 2.54.0. Pinned Go/Git/Python/Node tools were selected from the worker-rule PATH.
- Final local build hashes: `bin/kogen` SHA-256 `fdae62babdf17812fa8342e4ca85fcb6d9d4bbfc3b4bc1451f84e1d14da199f1`; `bin/kogen-xspec` SHA-256 `975bf68890ce684c1de8badf85d56d0f05a12d628efdd093b146356d35f996ad`. Both commands still enter `internal/app.Bootstrap`; the CLI and xspec adapter are not wired to recovery.
- Frozen v1.2 input: `$HOME/cx/kgo/inputs/conformance-v1.2`; runner reports `suite_version: v1.2+unknown` because the local suite copy has no Git metadata. The oracle JSONL is retained at `/Users/almirsarajcic/cx/kgo/evidence/25-recovery-controller/results.jsonl`; the preceding run is copied to `results-before-final-source.jsonl`.

## Component behavior

`internal/recovery` now identifies owners by PID and process start instant, leaves live or uncertain owners untouched, and reconciles a recorded post-CAS candidate as landed only when it is reachable from its recorded target branch. Recovery persists terminal outcome and `cleanup_pending` before cleanup, stops run writers, and calls the required preservation port for each real workspace before deleting it. It validates preserved-record identity and ref-backed tree identity, keeps snapshots marked `unverified`, records preservation/cleanup failures, and retries terminal cleanup without retrying Build or changing the terminal outcome. Claim cleanup compares the claim blob to this run ID and uses ref CAS; incoming cleanup is limited to the matching landed candidate. Candidate and recovery refs are retained.

Workspace discovery and cleanup use retained directory descriptors. Cleanup rejects symlink workspace leaves, traverses without following symlinks, unlinks hardlink aliases rather than writing through them, and syncs directories. The D3 implementation remains a separate injected port and is not wired into the public command path.

Component tests in `internal/recovery/recovery_test.go` cover post-CAS reconciliation and effect order, latest-byte visibility before cleanup, preservation failure and retry, no Build retry or terminal rewrite, live-owner no-op, owned-claim release, writer-stop failure, symlink/hardlink cleanup, and PID-reuse detection.

## Commands and results

| Command | Result |
|---|---|
| `GOMAXPROCS=2 GOFLAGS=-p=2 GIT_CONFIG_GLOBAL=/dev/null make check` | Final run passed: formatting, vendor fingerprints, vet, all Go tests, and both builds. An earlier run failed once in unowned `internal/queue/lock` (`TestConcurrentAcquireCreatesOneOwner`: `queue.pid is not a safe regular owner file`); three subsequent runs passed, including the final-source run. The initial failure is retained here and was not fixed in this package. |
| `make build` | Passed; the final `make check` also rebuilt both binaries after the last source change. |
| `GOOS=linux CGO_ENABLED=0 GOMAXPROCS=2 GOFLAGS=-p=2 go test -c -o /tmp/kogen-recovery-linux.test ./internal/recovery` | Linux test binary compiled. Linux runtime behavior was not run. |
| Exact frozen v1.2 acceptance command below | 0/9 passed; all 9 failed before behavior on the unwired CLI bootstrap. No recovery behavior or historical product conflict was observed. |

The exact acceptance invocation was:

```sh
make build
SUITE="$HOME/cx/kgo/inputs/conformance-v1.2"
EVIDENCE="$HOME/cx/kgo/evidence/25-recovery-controller"
mkdir -p "$EVIDENCE"
PYTHONDONTWRITEBYTECODE=1 PATH="$HOME/.local/share/mise/installs/python/3.14.7/bin:$PATH" \
  "$SUITE/bin/kogen-conformance" run --kogen "$PWD/bin/kogen" \
  --profile cli,state,approval,shape,build,ladder,provider,custody,format,v1.2 --case 'build-36,build-37,build-38,state-17,state-18,state-19,state-21,v1.2-06-crash-after-base-cas,v1.2-25-state-20-status-next' \
  --jobs 2 --time-scale 0.02 --workdir "$EVIDENCE/work" \
  --out "$EVIDENCE/results.jsonl"
```

The result JSONL resolved to the following literal cases; each had one instance:

| Effective case | Instances | Result | Observed failure |
|---|---:|---|---|
| `build-36` | 1 | Fail | Approval command exited 2 instead of 0; stderr: `kogen: implementation bootstrap; command routes are not wired`. |
| `build-37` | 1 | Fail | Same bootstrap failure during approval. |
| `build-38` | 1 | Fail | Same bootstrap failure during approval. |
| `state-17` | 1 | Fail | Same bootstrap failure during approval. |
| `state-18` | 1 | Fail | Same bootstrap failure during approval; no provider request reached the fake server. |
| `state-19` | 1 | Fail | Same bootstrap failure during approval. |
| `state-21` | 1 | Fail | Same bootstrap failure during approval. |
| `v1.2-06-crash-after-base-cas` | 1 | Fail | Approval exited 2 instead of expected 5; stdout was empty and stderr was the bootstrap diagnostic. |
| `v1.2-25-state-20-status-next` | 1 | Fail | Same bootstrap failure during approval. |

Totals: 9 cases, 9 instances, 0 pass, 9 fail, 0 error, 0 skip, 0 unimplemented. No compatible v1.2 behavior pass is counted. Exact historical semantic conflicts: none observed; these failures do not reach the recovery behavior.

## Quint recovery slice

The shared Quint source was copied to `/Users/almirsarajcic/cx/kgo/evidence/25-recovery-controller/replay/quint`; only that scratch copy was used. Source input digests are retained in `replay/source-inputs.sha256`. No source oracle or golden was changed.

- `XSPEC_SLICE=../slices/recovery python3 harness/xspec.py spec` failed 0/5 hand scenarios. `02-interrupted` has stale `Put` facts: the migrated `Fact` requires `work`, `preserved`, and `preserveOk`, while its scenario facts omit those fields. Quint reported `QNT000: Couldn't unify row and empty` at `slices/recovery/build/scenarios_test.qnt:4`. No hand goldens were published.
- Generation completed for each requested seed with 500 traces ×25 steps (12,500 events); generator invariants held: seed 17: 9,898 accepted/2,602 refused; seed 23: 9,886 accepted/2,614 refused; seed 41: 9,893 accepted/2,607 refused. Logs, including duplicate generation logs, are retained under `replay/`.
- Conformance used the private worker `bin/kogen-xspec` and recovery adapter. Each corrected run reported 0/505 traces (5 hand + 500 generated), 0 steps. All 505 diverged at reset because the adapter returned no answer and stderr was `kogen-xspec: implementation bootstrap; command routes are not wired`. Full observations were unavailable, so these are adapter wiring failures, not product divergences or passes.
- The first seed-41 conform invocation used a malformed executable path and is retained as `replay/conform-41.log`; the corrected absolute-path invocation is `replay/conform-41-corrected.log` and has the same reset/bootstrap result as seeds 17 and 23.

## Conflicts and remaining closure gates

- Exact v1.2 and tested draft semantic conflicts: none observed. The Quint hand-scenario schema failure is a shared-cohort coherence issue, not a recovery behavior conflict.
- `D-REC-01`–`D-REC-06` are planned fixtures, not available v1.2 cases. Wait for shared frozen v1.3 IDs before claiming those gates. The live D3 preservation implementation and its integration are also required; this package only defines and calls the preservation port.
- I3 remains open. Wire recovery and D3 through the real approval → queue → provider → gate → CAS → status path, then rerun the assigned v1.2 cases while retaining this failed result. The `kogen` and `kogen-xspec` command routes are currently bootstrap stubs; the recovery xspec adapter is not available for observation comparison.
- R(recovery) replay remains open until the shared migrated Quint cohort typechecks and the same-revision private xspec adapter emits full observations. The current scratch generation invariants do not close replay.
- Package 00 remains a foundation task. Linux runtime, optional-runtime, and live comparison gates require their external evidence; only Linux compile evidence is available here.

This worker is component-ready only. It does not confer behavior acceptance or I3 closure.
