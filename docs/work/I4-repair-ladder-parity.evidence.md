# I4 repair and ladder parity evidence

## Revisions and scope

- Worker branch: `kgo/I4-repair-ladder-parity`; source base: `8ddf36efcf4c5e742dc1b6bec8748579335ca10a`.
- The CLI used by the acceptance run reported `kogen 8ddf36ef (2026-10-08, uncommitted changes)`. Its executable was built by the acceptance block before the final small change that preserves the parallel failure reason when no suffix rung remains. The public CLI does not call this I4 coordinator, so that change does not affect the recorded route results.
- Draft target: spec revision `e19dd1c21c19c5be1201c3b6a42c59c28b5c2887`, including `CHANGES-v1.3.md`. The adapter was compared with the relevant format/build clauses and Rust auditor, selector, recipe, and report modules.
- Frozen runner: `$HOME/cx/kgo/inputs/conformance-v1.2/bin/kogen-conformance`; result metadata says `suite_version: v1.2+unknown`. Runner SHA-256: `0b53a3b60956cbe8dde7af54e274c46b3e8d430c45662d7dff67445f440b39f9`.
- Adapter SHA-256: `e527a46d452c0402eabe313b88b0c10fc4a46ac0e2d4c441bfa5ee0f8df1a453`; test SHA-256: `e8897178c2a580d51918740ddbf836b32f15b55a27c5378a90f2e15a18881924`; `docs/work/I4.md` SHA-256: `8e04e7e70c93066a708d16d6880a213a28aa2ff3bc4172e766ddfa801266ad9c`.

The adapter loads the resolved recipe and builder role, composes sequential and hard-plan parallel execution, gives rung attempts separate sessions sharing the Build cache/session affinity, offsets continuation workspaces/snapshots/attempt numbers after a dual-red R1/R2, carries prior failure summaries forward, and sends candidates through the observational selector. It does not land candidates; the caller owns the distinct landing allowance. Tests cover resolved builder versus recipe escalation models, session/thread separation and Build affinity, dual-red continuation and global snapshot identities, parallel cancellation/loser cleanup, and observational advice preserving a real green gate.

## Commands and results

The frozen acceptance command was run verbatim from the `sh` fenced block in `docs/work/I4-repair-ladder-parity.md`, including its literal effective IDs, overlay, `--jobs 2`, and `--time-scale 0.02`. Its output is `/Users/almirsarajcic/cx/kgo/evidence/I4-repair-ladder-parity/results.jsonl` (SHA-256 `35f067291f2614b8efe221a46aefd2a446abc3ae39b96e8f92fa8efb68b3d93f`). The runner started at `2026-10-08T01:37:23Z` on macOS 26.7.1 arm64.

The command exited 1: 215/215 selected cases produced results, 61 passed, 154 failed, 0 errored, and 0 skipped; 370/546 instances passed. Per-profile case totals were CLI 7/10, state 13/19, approval 16/23, shape 0/4, build 1/10, provider 0/5, custody 0/5, format 4/6, and v1.2 overlay 20/133. Passing IDs were `cli-10,cli-15,cli-18,cli-19,cli-20,cli-22,cli-23,state-01,state-04,state-05,state-07,state-08,state-09,state-10,state-13,state-16,state-24,state-25,state-29,state-30,approval-02,approval-03,approval-04,approval-05,approval-06,approval-07,approval-09,approval-11,approval-12,approval-15,approval-16,approval-19,approval-20,approval-21,approval-22,approval-23,build-01,format-02,format-03,format-04,format-06,v1.2-07-cli-03-unknown-command,v1.2-08-cli-04-unknown-subcommand,v1.2-09-cli-05-unknown-option,v1.2-10-cli-06-missing-positionals,v1.2-11-cli-07-unexpected-argument,v1.2-12-cli-08-option-needs-value,v1.2-13-cli-09-boolean-takes-no-value,v1.2-14-cli-11-unknown-provider,v1.2-15-cli-12-watch-with-json,v1.2-16-cli-13-double-dash,v1.2-17-cli-14-options-before-command,v1.2-18-cli-16-short-option,v1.2-19-cli-17-help-bad-topic,v1.2-20-cli-21-invalid-slug,v1.2-21-cli-24-help-after-positionals,v1.2-25-state-20-status-next,v1.2-26-state-22-status-next,v1.2-33-shape-json-is-unsupported,v1.2-35-state-02-schema-errors,v1.2-36-state-06-lint-card-warnings`.

All 44 requested acceptance cases in V39–44, V68–99, V102–103, and V124–127 resolved to one instance and failed 0/1: `v1.2-39-build-04` through `v1.2-44-build-09`; `v1.2-68-build-44`; `v1.2-69-ladder-01` through `v1.2-99-ladder-31`; `v1.2-102-ladder-34`, `v1.2-103-ladder-35`; `v1.2-124-build-31`, `v1.2-125-ladder-36`, `v1.2-126-state-15`, and `v1.2-127-build-10`. Every selected ladder route case, including demotion, rank, and report scenarios, stopped before its ladder assertion with `stopped greet: controller/approval_invalid; it stays queued` and exit 70. Those are observed public-route failures, not measured demotion/rank/report behavior; none is counted as passing or as a behavioral conflict.

Other selected regression evidence: `cli-28` failed with `controller/internal_error: status watch is closed with queue lifecycle in the Build integration round` (exit 70); `v1.2-92-ladder-24` and `v1.2-131-state-12` stopped at `controller/approval_invalid`. Thus the report/watch integration gate remains open. `v1.2-129-cli-27` and `v1.2-137-format-12` timed out waiting for their fake request condition. No D-AUD-01–05 or shared observational replacement IDs exist in this frozen v1.2 suite.

Verification commands and outcomes on the final source:

```sh
GOMAXPROCS=2 go test -p=2 -parallel=2 ./internal/app -run '^Test(LadderWiring|RemainingLadderRungs)' -count=1 -timeout=60s
```

Passed (`ok kogen-go/internal/app 3.248s`). The final `GOMAXPROCS=2 make build` passed and built `bin/kogen` and `bin/kogen-xspec`.

The first `GIT_CONFIG_GLOBAL=/dev/null GOMAXPROCS=2 make check` was run before the final failure-reason-preservation edit and passed (log: `/Users/almirsarajcic/cx/kgo/evidence/I4-repair-ladder-parity/make-check.log`). Two final-source attempts both exited 2 at the same unowned test, `internal/queue/lock.TestConcurrentAcquireCreatesOneOwner`, with `queue.pid is not a safe regular owner file`; all other packages in those runs passed. Logs are preserved as `make-check-final.log` and `make-check-confirmation.log` in that evidence directory. A one-test diagnostic command, `GOMAXPROCS=2 go test -p=2 -parallel=2 ./internal/queue/lock -run '^TestConcurrentAcquireCreatesOneOwner$' -count=1 -timeout=30s`, passed once. Do not treat this as a passing full check. The failing implementation and test are outside this worker's owned files.

## Historical v1.2 divergences and measurement limits

- `v1.2-68-build-44` expects the historical auditor to use tools and produce the old `acceptance_upheld` outcome. The v1.3 draft auditor is tool-less and observational; its audit is advice and cannot uphold or demote acceptance.
- Demotion expectations conflict with the draft's no-demotion rule: `v1.2-73-ladder-05` demotes `over_strict` before repair; `v1.2-74-ladder-06` lands a demoted candidate without repair; `v1.2-75-ladder-07` demotes recognized `over_strict` without citation; `v1.2-78-ladder-10` demotes every change item; and `v1.2-79-ladder-11` re-scores after demotion and lands. `v1.2-76-ladder-08` treats `infeasible` as a recognized upheld verdict, while the draft recognizes `valid`, `over_strict`, and `contradicts`, with unknown verdicts warning. The cases were selected but their assertions were unreachable behind the route error above.
- Rank cases `v1.2-81-ladder-13` through `v1.2-87-ladder-19` were selected. The v1.2 order of more passed items, fewer blocking findings, smaller diff, then earlier rung appears compatible with the draft selector; no rank conflict is identified. Their assertions were unreachable, so this run provides no rank behavior evidence.
- Report case `v1.2-92-ladder-24` expects the v1.2 audit citation shape (`audit[].citation`). The draft report only carries each audit's `{rung,id,verdict,reason}` fields. Its report assertion was unreachable in this run.

## Deferred closure gates and local effects

- `internal/app/build_routes.go` is owned by I3 and still constructs `single.Controller`; it supplies no rung executor to this adapter. The I4 worker is not authorized to edit that route. The coordinator integration handoff must connect approval, run state, journal, snapshots, and landing before behavior can be accepted.
- `internal/app/status_routes.go` remains outside I4 ownership and explicitly closes `status --watch` pending Build integration. Report/watch regression must be rerun after that integration.
- The separate landing allowance is preserved at the caller boundary but was not exercised end to end without the route integration.
- D-AUD/shared observational replacement fixtures and the migrated coherent Quint cohort are unavailable in v1.2. R(slice) was not run; it requires the scratch cohort, 500 traces × 25 steps for each seed 17, 23, and 41, and full observations against the same-revision private binary.
- Linux, optional-runtime, and live-comparison gates lack their required external evidence. No live provider or account access was used.
- During the acceptance run, another KGO-CONFCHECK run and a KTS runner were active in the shared environment. This acceptance was not protected by the required shared gate lock. The result was preserved without retry; no causal attribution is made for custody/provider failures.
- Source changes, test fixtures, and build outputs were confined to this worktree; conformance workdirs and JSONL were under the package evidence directory. No spec, suite, golden, other worktree, or global configuration was changed.

The delivered gate is component-only. The frozen suite results do not constitute behavior acceptance.
