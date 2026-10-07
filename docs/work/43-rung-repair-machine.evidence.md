# 43 — Rung repair machine evidence

**Gate state:** component-ready; Build/ladder behavior remains open for I4.

## Revisions and environment

- Worktree: `/Users/almirsarajcic/Areas/Kogen/kogen-go-wt/43-rung-repair-machine`, branch `kgo/43-rung-repair-machine`.
- Starting worker/CLI/adapter revision: `9c112bfe7b4236acc2c0e58022416bf046dbbd1b`. The command binary was built from this worktree with only the owned `internal/build/repair/**` source changes; `cmd/kogen`, `internal/app`, and provider adapters were not changed. The CLI had no public Build route at this revision.
- Target spec: `kogen-spec` `e19dd1c21c19c5be1201c3b6a42c59c28b5c2887` (v1.3-draft).
- Frozen oracle: `kogen-conformance` `0f93bad988fb8d7a8eff4e94954d1db0a046c89d` (`v1.2`). Its effective suite label in the result header is `v1.2+unknown`.
- Rust reference: `kogen-rs` `a402540b39cedc7f788472297add7ae2f8a6631a`.
- Host/toolchain: macOS 26.7.1 arm64; Git 2.54.0, Go 1.27.1 darwin/arm64, Python 3.14.7, Node 24.21.0. `GOMAXPROCS=2`; Makefile sets Go build parallelism `-p=2` and test parallelism `-parallel=2`.

## Implemented component

`internal/build/repair/**` now contains a pure per-rung transition function and append-only session feedback helpers. It enforces at most six repair requests, requires each available red count to decrease strictly, ends on a repeated/increased count or a second uncounted red verification, and records unchanged repair completion only after requesting verification. Turn and wall caps request final verification. The fourth protected restore appends its per-path note, stops development, and requests verification. Gate red counts use non-excused failing check identities (or one for an identity-free failed check), failed fixes, and failing approved items. Auditor advice does not affect the count. Repair feedback is appended with the exact §3.6 wrapper to the same `session.Conversation`; protected restore notes preserve restorer order.

Package source SHA-256 values at verification:

| File | SHA-256 |
|---|---|
| `internal/build/repair/doc.go` | `8bb2a67345d8a1d263bee3ce5d501568e5e979e7d9c6cfe0bdf24edb749657f2` |
| `internal/build/repair/feedback.go` | `3c6e9dedfe9b757dbc9e2953276d401f7cc1e5b8f68d791b6d09c973b56edaf5` |
| `internal/build/repair/feedback_test.go` | `fad21fc7b9fa4510c3c550638b35634c34f330b802fc14bc7169a2288ba1b7ab` |
| `internal/build/repair/machine.go` | `333cabd59f764039eb96de646683cb6f1a1ce8caef3a86201484a90a94b38b04` |
| `internal/build/repair/machine_test.go` | `48d20205985a84c74bfe8a0859bc5be992ac8042d7c9708962e25d2bdf704691` |

## Commands and results

1. `GOMAXPROCS=2 GIT_CONFIG_GLOBAL=/dev/null make check` — first run exited 2. The new equal-count test case incorrectly supplied `4` after `5` (which is progress); I corrected the assertion to use `5` and `6`. That same run also observed one unrelated `internal/queue/lock.TestConcurrentAcquireCreatesOneOwner` failure: `queue.pid is not a safe regular owner file`.
2. `GOMAXPROCS=2 GIT_CONFIG_GLOBAL=/dev/null make check` — rerun after the assertion correction exited 0: format, vendor fingerprints, vet, tests, and both builds passed. The queue lock test passed on this run without queue changes.
3. `make build` — passed as the first command in the required acceptance invocation.
4. Required frozen-oracle command, run once with `--jobs 2 --time-scale 0.02`, exited 1. Full stdout and per-case JSONL are retained at `/Users/almirsarajcic/cx/kgo/evidence/43-rung-repair-machine/results.jsonl`; instance workdirs are under `/Users/almirsarajcic/cx/kgo/evidence/43-rung-repair-machine/work/`.

```sh
export PATH="$HOME/.local/share/mise/installs/git/2.54.0/bin:$HOME/.local/share/mise/installs/go/1.27.1/bin:$HOME/.local/share/mise/installs/python/3.14.7/bin:$HOME/.local/share/mise/installs/node/24.21.0/bin:$PATH"
make build
SUITE="$HOME/cx/kgo/inputs/conformance-v1.2"
EVIDENCE="$HOME/cx/kgo/evidence/43-rung-repair-machine"
mkdir -p "$EVIDENCE"
PYTHONDONTWRITEBYTECODE=1 PATH="$HOME/.local/share/mise/installs/python/3.14.7/bin:$PATH" \
  "$SUITE/bin/kogen-conformance" run --kogen "$PWD/bin/kogen" \
  --profile cli,state,approval,shape,build,ladder,provider,custody,format,v1.2 --case 'v1.2-127-build-10,v1.2-39-build-04,v1.2-40-build-05,v1.2-41-build-06,v1.2-42-build-07,v1.2-43-build-08,v1.2-44-build-09,v1.2-68-build-44,v1.2-88-ladder-20,v1.2-89-ladder-21,v1.2-95-ladder-27,v1.2-97-ladder-29,v1.2-98-ladder-30' \
  --jobs 2 --time-scale 0.02 --workdir "$EVIDENCE/work" \
  --out "$EVIDENCE/results.jsonl"
```

Resolved IDs and results (`docs/work/43-rung-repair-machine.cases`):

| Effective case ID | Instances | Result |
|---|---:|---|
| `v1.2-127-build-10` | 1 | fail before target behavior |
| `v1.2-39-build-04` | 1 | fail before target behavior |
| `v1.2-40-build-05` | 1 | fail before target behavior |
| `v1.2-41-build-06` | 1 | fail before target behavior |
| `v1.2-42-build-07` | 1 | fail before target behavior |
| `v1.2-43-build-08` | 1 | fail before target behavior |
| `v1.2-44-build-09` | 1 | fail before target behavior |
| `v1.2-68-build-44` | 1 | fail before target behavior |
| `v1.2-88-ladder-20` | 1 | fail before target behavior |
| `v1.2-89-ladder-21` | 1 | fail before target behavior |
| `v1.2-95-ladder-27` | 1 | fail before target behavior |
| `v1.2-97-ladder-29` | 1 | fail before target behavior |
| `v1.2-98-ladder-30` | 1 | fail before target behavior |

Totals: 13 cases, 13 instances, 0 passed, 13 failed, 0 errors, 0 skipped, 0 unimplemented. Every instance failed at `step 2 (approve)`: `kogen intent approve greet {hash8:greet}` exited 2 instead of 0, stdout was empty, and stderr was `kogen: implementation bootstrap; command routes are not wired`. The runner also reported that no provider request reached the fake server. Therefore none of the requested repair/ladder assertions ran and none is counted as a compatible pass.

## Conflicts and deferred closure

- No repair-machine behavior conflict with historical v1.2 was adjudicated: all selected cases stopped at the same unwired approval route before reaching their assertions. The observed CLI route failure is an integration gap, not an inferred historical behavior conflict.
- The selected cases have retained failures at the frozen result path above. Do not rerun them on this unchanged revision to replace those results.
- `internal/build/single/**` does not yet call this machine; the public approval → queue → provider → gate → CAS → status path is also unwired. I4 must wire the component and rerun the selected cases on a changed revision, retaining these results.
- The shared frozen/migrated v1.3 suite and planned D-* fixture IDs are unavailable. No v1.3 behavior claim is made. No Quint production replay, Linux run, optional-runtime check, or live provider comparison was performed; those remain their owning gates.
- No live provider or account access occurred. The acceptance fake provider was never reached.

## Replay and side effects

No R(slice) replay was run by this component task, so there are no replay seeds or divergence results. Component tests exercised the pure state transitions, gate-count arithmetic, and append-only session history locally. The acceptance command created only its designated isolated case workdirs and result JSONL.
