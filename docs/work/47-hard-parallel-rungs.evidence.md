# 47 Hard Parallel Rungs Evidence

## Gate and revisions

**Gate: component. Behavior acceptance and I4 remain open.** The parallel controller and its focused tests are implemented in `internal/build/parallel/**`. The public Build/queue path does not call this component yet.

- Worker branch: `kgo/47-hard-parallel-rungs`.
- Component source commit: `4f20515a66494c4e0947ca707abee85ae866ef3f` (`Implement hard parallel rungs`).
- CLI source revision during the oracle run: `4f20515a66494c4e0947ca707abee85ae866ef3f`; `cmd/kogen` was unchanged by this package. Its queue handler reports `controller/internal_error: queue execution is wired in the Build integration round`.
- Frozen spec target: `kogen-spec` v1.3-draft `e19dd1c21c19c5be1201c3b6a42c59c28b5c2887`; read `spec/03-build.md` §§3.1, 3.4–3.6, 3.8.2–3.8.3, `CHANGES-v1.3.md` §1, and the parallel/ranking clauses.
- Rust reference: `kogen-rs` `a402540b39cedc7f788472297add7ae2f8a6631a`; read `crates/kogen-core/src/build/single_rung/execute/parallel.rs`, `execute.rs`, `run/orchestration/recipe.rs`, and `run/orchestration/machine/transitions/rung.rs`.
- Frozen suite: `~/cx/kgo/inputs/conformance-v1.2`, suite revision recorded in `PLAN.md` as `0f93bad988fb8d7a8eff4e94954d1db0a046c89d`. This input is a directory snapshot without a `.git` directory. The run recorded runner version `v1.2+unknown`, runner SHA-256 `0b53a3b60956cbe8dde7af54e274c46b3e8d430c45662d7dff67445f440b39f9`, Go `go1.27.1 darwin/arm64`, Git `2.54.0`, and platform `macOS-26.7.1-arm64-arm-64bit-Mach-O`.
- Built `bin/kogen` SHA-256: `0d6f7a7a1a6a2352d31e2143867167dece1443ae2696b4e32a812d656722975b`.

## Implemented component

`parallel.Controller` consults the resolved recipe entry schedule and starts parallel work only for a hard plan when both R1 and R2 fit under `max_rungs`. A non-applicable result has no side effects, leaving easy plans and a one-rung cap to the sequential controller.

The controller creates distinct workspaces and `session.Conversation` objects for the two rungs. Both conversations use the Build cache key as `session-id`; their stable thread IDs differ by rung/attempt. The rung executor receives the persistent conversation object for all turns in that rung. The observer records `parallel_started`, then `rung_started` for R1 and R2 before either rung runs.

The two rung executions run concurrently. A landable result cancels the peer; both executions are joined before snapshotting. Every result is durably preserved before cleanup. A green winner's workspace stays available for landing and the losing workspace is removed. If both rungs are red, the existing selector chooses the better candidate, both diff-free failure summaries are returned in R1/R2 order, and both workspaces are removed after preservation. Dual-green selection uses the existing deterministic selector, including its earliest-rung tie break. Results, preservation calls, cleanup calls, and initial journal events are ordered by rung rather than completion time.

Focused tests cover hard/max-rung eligibility, isolated workspace and conversation identities, concurrent execution, deterministic start/snapshot/result ordering, both-red continuation, green-winner cancellation and cleanup, and dual-green tie selection. They use local fake ports and a temporary Git fixture; no provider/account access occurs.

## Commands and results

Pinned worker tools were first on `PATH` for each command:

```sh
export PATH="$HOME/.local/share/mise/installs/git/2.54.0/bin:$HOME/.local/share/mise/installs/go/1.27.1/bin:$HOME/.local/share/mise/installs/python/3.14.7/bin:$HOME/.local/share/mise/installs/node/24.21.0/bin:$PATH"
```

1. `GOMAXPROCS=2 go test -count=1 -p=2 -parallel=2 ./internal/build/parallel` — passed.
2. `GIT_CONFIG_GLOBAL=/dev/null make check` — **exit 2**. `internal/build/parallel` passed in the full test run. The unrelated `internal/queue/lock` test `TestConcurrentAcquireCreatesOneOwner` failed with `queue.pid is not a safe regular owner file`. Vet completed before tests; `make check` stopped at the failed test command, so its temporary dual-build step did not run. This failed run is retained in this evidence and was not rerun.
3. Required frozen v1.2 command, run once at the component source revision:

   ```sh
   make build
   SUITE="$HOME/cx/kgo/inputs/conformance-v1.2"
   EVIDENCE="$HOME/cx/kgo/evidence/47-hard-parallel-rungs"
   mkdir -p "$EVIDENCE"
   PYTHONDONTWRITEBYTECODE=1 PATH="$HOME/.local/share/mise/installs/python/3.14.7/bin:$PATH" \
     "$SUITE/bin/kogen-conformance" run --kogen "$PWD/bin/kogen" \
     --profile cli,state,approval,shape,build,ladder,provider,custody,format,v1.2 --case 'v1.2-102-ladder-34,v1.2-71-ladder-03,v1.2-72-ladder-04' \
     --jobs 2 --time-scale 0.02 --workdir "$EVIDENCE/work" \
     --out "$EVIDENCE/results.jsonl"
   ```

   `make build` passed. The runner resolved **3 cases / 3 instances**, all failed before ladder assertions because `kogen queue start` is not wired. No fake provider request reached the server. Result JSONL: `/Users/almirsarajcic/cx/kgo/evidence/47-hard-parallel-rungs/results.jsonl`, SHA-256 `a3bc731663f4369cbfd9036fb49cbdaf544ec0e487ef9530850c005e0e48b06d`.

   | Effective ID | Instances | Result and exact boundary |
   | --- | ---: | --- |
   | `v1.2-102-ladder-34` | 1 | Fail; step 3 `kogen queue start` exited 70, expected 1. Stdout: `controller/internal_error: queue execution is wired in the Build integration round`. |
   | `v1.2-71-ladder-03` | 1 | Fail; step 3 `kogen queue start` exited 70, expected 0. Stdout had one line instead of the three expected lines: `controller/internal_error: queue execution is wired in the Build integration round`. |
   | `v1.2-72-ladder-04` | 1 | Fail; step 3 `kogen queue start` exited 70, expected 0. Stdout had one line instead of the three expected lines: `controller/internal_error: queue execution is wired in the Build integration round`. |

## Conflicts, effects, and deferred closure

- Compatible public behavior passes: **none**. The three cases did not reach their ladder assertions. Do not count these as hard-parallel behavior passes.
- Measured historical v1.2 behavior conflicts: **none**. The observed failures are the unwired queue/Build integration boundary, not a measured conflict with the ladder assertions.
- Measured v1.3-draft conflicts: **none**. The component does not implement auditor demotion; selector advice is not an input to winner choice.
- `I4` remains open. It must wire the public Build path to the single-rung and parallel components, journal rung events/results, select and land the green candidate, continue after both-red, and rerun the assigned behavior cases after the integration revision. `I3` Build execution is not closed on this worktree.
- Package 00 remains a real foundation task. No shared frozen v1.3 suite/IDs or coherent migrated Quint cohort was available; planned `D-*` fixtures have no v1.2 IDs. No scratch-copy spec replay or `500 × 25` traces for seeds `17`, `23`, and `41` were run. Wait for the coordinator's frozen v1.3 IDs/cohort before claiming those gates.
- Linux behavior, optional runtime, live comparison, and release admission gates remain open for their stated external evidence. No live provider or account access occurred.
- Local effects: `make build` produced the ignored `bin/kogen` and `bin/kogen-xspec`; the oracle created the retained results and work directories above. The suite, spec, goldens, replay harness, gate policy, global configuration, and other worktrees were not modified. No behavior claim follows from the worker commit.
