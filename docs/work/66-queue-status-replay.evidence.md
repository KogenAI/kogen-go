# 66 — Queue/status replay evidence

## Revisions and scope

- Branch: `kgo/66-queue-status-replay`.
- Worker/package implementation commit: `b26ceb06f5fe4534199ee886e3a7eb074b7aa6f7`.
- Go worktree base before the worker commit: `0e94d83c8d6c42e04c40fb5aecf70a7cea6732c1`.
- `kogen` and `kogen-xspec` source at the time of the assigned conformance run: the unchanged base worktree. `cmd/kogen-xspec` delegates to `internal/app.Bootstrap`; that bootstrap still reports that command routes are not wired. The built binaries were `bin/kogen` SHA-256 `137d24807dfdccf5476a82d8855eaa7876fc71b1860c1d576e12bb9a97fcf307` and `bin/kogen-xspec` SHA-256 `27757cb775b2ac00a00facffaa796059fa617e9ff0d0e558427af81887b77ed4`.
- Spec target: `kogen-spec` commit `e19dd1c21c19c5be1201c3b6a42c59c28b5c2887`, v1.3-draft. The sibling spec worktree was dirty. The copied queue model includes its current `queue.qnt` bytes (SHA-256 `d6dfa3a1aafd57097fdb361d22b9ff576c03592638f7b0d6e3c82a2a993e8bd1`); the copied status model hash is `8be1a3a8825e8ae354787affd4adca56b2010b6cda043b7f5d370d88e9dcf167`; the copied harness hash is `1c3e70ac65ec74de56498b00cb956069c516d4fe85691e7b9fb837b4159b07b2`. This was an isolated diagnostic copy, not a frozen v1.3 manifest.
- Rust reference read at `kogen-rs` commit `a402540b39cedc7f788472297add7ae2f8a6631a`: `kogen-core/src/queue/scheduler.rs`, `kogen-core/src/status/replay.rs`, `kogen-core/src/status/replay/observe.rs`, and `kogen-xspec/src/xspec/{queue,status}.rs`.
- Host: Darwin 25.6.0 arm64. No Linux behavior evidence was available in this worktree.

The adapter factories call the production `schedule.QueueScheduler` and `derive.Derive`. Status inputs are assembled from injected `contract.StateObservations` ref, run, and owner facts; the run owner's PID is probed through the OS before deriving. Queue replay acquires/releases the production queue lock and observes a real stop marker. The complete queue observation is serialized from the production scheduler result; the status observation contains all 22 fields from the Quint schema. The unit checks exercise actual lock creation, a stale-owner takeover, derivation precedence/dependency blocking, watch state, and complete observation keys.

## Commands and local results

Commands were run with the pinned Git 2.54.0, Go 1.27.1, Python 3.14.7, and Node 24.21.0 paths. The shared mkdir gate under `~/cx/kgo/gates.lock` was held for `make check` and the assigned conformance run, then released.

| Command | Result |
|---|---|
| `GOMAXPROCS=2 go test -p=2 -parallel=2 ./internal/xspec/queuestatus` | Pass. |
| `GIT_CONFIG_GLOBAL=/dev/null make check` | Pass: formatting, vendor fingerprints, `go vet ./...`, all tests, and both builds. |
| `make build` | Pass; built both public and private binaries. |

The package tests and `make check` were run against the same source bytes committed as worker commit `b26ceb0`. `make check` did not run the race detector.

## Frozen v1.2 command

The assigned command was run once against `~/cx/kgo/inputs/conformance-v1.2`, with the full standard profiles and overlay. The exact selected IDs are also listed in [66-queue-status-replay.cases](66-queue-status-replay.cases):

```text
build-41
cli-28
state-17
state-18
state-19
state-21
state-24
v1.2-124-build-31
v1.2-129-cli-27
v1.2-132-state-23
v1.2-22-cli-25-status-overview
v1.2-23-cli-26-status-slug
v1.2-25-state-20-status-next
v1.2-26-state-22-status-next
```

The runner resolved 14 cases / 14 instances. Result: **0 passed, 14 failed, 0 errors, 0 skipped, 0 unimplemented**. The first required public command in each case hit the unwired bootstrap: exit 2, empty stdout, stderr `kogen: implementation bootstrap; command routes are not wired`. This is an integration/bootstrap failure, not evidence of an old-versus-draft semantic conflict. No selected case is counted as compatible or behavior-passing.

Full results are retained at [`results.jsonl`](/Users/almirsarajcic/cx/kgo/evidence/66-queue-status-replay/results.jsonl), SHA-256 `878114383c724ae8bfef8a9e0ff9153e72c60aaafabfc5a2be2b75f1507620b5`. There is no identified v1.2/v1.3 queue/status semantic conflict in this run; `conflicts` is empty in the gate receipt.

## Quint replay

The scratch copy is `/Users/almirsarajcic/cx/kgo/evidence/66-queue-status-replay/replay-scratch.tOJOgV`. It contains the prototype, the queue/status slices copied from the shared spec worktree, generated hand goldens, six retained 500-trace corpora, and a separate log for each command. Quint was `0.33.0`. The harness used full observations (no projection option).

| Slice | `spec` | Seed 17 | Seed 23 | Seed 41 |
|---|---:|---:|---:|---:|
| queue | 9/9 hand scenarios pass | 500 × 25 generated; conform 0/509 | 500 × 25 generated; conform 0/509 | 500 × 25 generated; conform 0/509 |
| status | 8/8 hand scenarios pass | 500 × 25 generated; conform 0/508 | 500 × 25 generated; conform 0/508 | 500 × 25 generated; conform 0/508 |

All six generations exited 0 and preserved 12,500 events each. All six conform commands exited 1 at step 0: the binary answered neither reset request and stderr said `kogen-xspec: implementation bootstrap; command routes are not wired`. Thus the hand/generation checks validate only the copied Quint model; they do not validate the Go adapter. The per-command receipts are under `replay-scratch.tOJOgV/receipts/`; generated corpora are under `replay-scratch.tOJOgV/corpora/`.

The current shared v1.3 suite/model cohort is not frozen. Planned D-* fixtures are not v1.2 cases and are not claimed here. The external spec worktree was not changed.

## Deferred closure gates

- **I7 behavior:** integration must compose `queuestatus.Factories()` into the private xspec command registry, then rerun queue and status conform against a binary from that same revision. Current conform attempts made no adapter calls.
- **Public behavior:** command routing is still owned by integration. Rerun the 14 retained v1.2 IDs after route wiring/rebase; preserve this failed JSONL and the new result.
- **Shared v1.3:** wait for the frozen migrated suite and model manifest before making a draft behavior claim. No D-* case was counted.
- **External host gates:** Linux behavior and any live comparison/replay evidence remain outstanding. No live provider or account access was used.

## Draft conflicts

None identified for queue/status. The open broader status-schema review item remains outside this package's evidence and does not convert the unwired cases into semantic conflicts.
