# I3 first executable approval to landing: evidence

## Revisions and scope

- Branch: `kgo/I3-first-executable-approval-landing`.
- Base revision before this change: `092945ee57eb452214146d28cf54be5f3938eeef`.
- Target spec checkout: `e19dd1c21c19c5be1201c3b6a42c59c28b5c2887`; its shared Quint tree is dirty, so no replay was run or counted.
- Frozen oracle: `$HOME/cx/kgo/inputs/conformance-v1.2`, `VERSION` 1.2. The assigned literal effective IDs are in [I3 cases](I3-first-executable-approval-landing.cases).
- Files changed: `internal/app/build_routes.go`, `internal/app/build_routes_test.go`, `internal/app/foundation.go` for the required public queue dispatch, `internal/process/entry.go`, and the guardian init move in `internal/process/guardian.go`, plus this evidence and [I3 implementation note](I3.md).
- Rust references reviewed: `kogen-rs/crates/kogen-core/src/build/queue.rs`, `single_rung.rs`, and `provider.rs`; Go queue, single-rung, gate, recovery, preservation, workspace, and landing ports were reviewed before wiring.

## Local route evidence

The following command passed after the route fixes:

```sh
GOMAXPROCS=2 go test -p=2 -parallel=2 ./internal/app -run 'TestPublicApprovalQueueGateLandingAndStatus|TestQueueInvalidApprovalDoesNotCallTheAgent' -count=1
```

The first test invokes public `CLI.Run` for approve, queue start, and status. It observed one fake planner call, one fake builder call, a green gate receipt, a landing commit with one parent, and status `Landed (1): greet`. The invalid-approval test observed zero planner and builder calls and a B0 `approval_invalid` stop. The test uses the real gate, Git candidate creation, landing CAS, and status derivation.

Development attempts before the passing route test exposed and fixed: empty approval scratch run directories reported by recovery; a missing tracked candidate directory in the fixture; metadata loaded through the origin-Git port instead of workspace Git; and successful workspace cleanup crossing safefs' deliberate `.git` path rejection. The last case is handled in the app route with a private-rooted `os.Root.RemoveAll` after the candidate is published. No source-oracle result was deleted or retried.

## Required check

```sh
GIT_CONFIG_GLOBAL=/dev/null GOMAXPROCS=2 make check
```

Passed: formatting, vendor fingerprints, vet, all Go tests, and both builds. `make build` also passed before the oracle invocation.

## Frozen v1.2 invocation and result

The runner command was launched with the assigned profiles and case list, but I mistyped the literal `v1.2-49-build-15` as `v1.2-45-build-15`. I also failed to acquire `~/cx/kgo/gates.lock` before launch; the shared directory was held by a different worker running custody work. I stopped only this run after it reached `custody-04`; it exited 143. The other worker's lock and processes were not touched. The partial result is preserved at:

- JSONL: `/Users/almirsarajcic/cx/kgo/evidence/I3-first-executable-approval-landing/results.jsonl`
- Workdirs: `/Users/almirsarajcic/cx/kgo/evidence/I3-first-executable-approval-landing/work/`
- Runner metadata: suite v1.2, macOS 26.7.1 arm64, started `2026-10-08T01:08:35Z`.
- Resolved results: 73 case IDs / 166 instances; 37 cases and 120 instances passed, 36 cases and 46 instances failed, 0 skipped, 0 errors. This is a partial, unserialized run with a malformed case literal; it is not a behavior receipt and no case outside its JSONL is claimed.

Passed case IDs: `cli-10, cli-15, cli-18, cli-19, cli-20, cli-22, cli-23, state-01, state-04, state-05, state-07, state-08, state-09, state-10, state-13, state-16, state-24, state-25, state-29, state-30, approval-02, approval-03, approval-04, approval-05, approval-06, approval-07, approval-09, approval-11, approval-12, approval-15, approval-16, approval-19, approval-20, approval-21, approval-22, approval-23, build-01`.

Failed case IDs: `cli-28, cli-29, cli-30, state-03, state-17, state-18, state-19, state-21, state-28, approval-01, approval-08, approval-10, approval-13, approval-14, approval-17, approval-24, shape-09, shape-16, shape-24, shape-25, build-32, build-35, build-36, build-37, build-38, build-39, build-40, build-41, build-42, provider-19, provider-24, provider-26, custody-01, custody-02, custody-03, custody-04`.

The queue/shape/provider Build cases that wait for the fake provider failed before the fake received a request: the default production `single.Agent` intentionally returns `provider/build_agent_unavailable`. Other observed failures include `state-03` (`status` exit 3 vs expected 0), `state-17`/`state-19` (`queued` vs historical failed-run status), `build-37` (snapshot remained `running` because the fake request was not reached), and custody approval timing/fixture failures. These are failures or closure gaps, not passes and not automatically draft conflicts. The exact full acceptance command remains open; the partial invocation must not be reused as if it were the assigned command.

## Historical v1.2 conflicts and draft gaps

- Frozen `build-36-kill9-crashed.json` asserts after SIGKILL that `{state_root}/{run_id:greet}-*` has `glob_count: 0`. Target spec §3.10.5 and CHANGES-v1.3 §3 require preserving the latest recoverable workspace/candidate as unverified and recording the recovery identity before cleanup. The zero-workspace assertion is incompatible with that preservation requirement. The selected case failed at its `fake_requests: 1` wait, so its cleanup assertion was not observed in this run.
- Frozen `v1.2-130-state-11-run-json-events-schema.json` uses `$exact: true` for a run snapshot and omits the `recovery` field. The local landed run snapshot includes `"recovery": []`; draft §3.10 and CHANGES-v1.3 §3 add recovery records and cleanup-pending state. The v1.2 exact key set conflicts with the draft schema. This overlay case was not reached by the interrupted run.
- `v1.2-06-crash-after-base-cas` expects the post-CAS status to remain landed and to reconcile the incoming ref. That terminal landed status remains compatible; draft §3.10 additionally requires preserving later post-CAS edits as unverified recovery. The partial run did not reach this overlay case, so no recovery result is claimed.
- D-REC-01–06 have no frozen v1.2 cases. Wait for shared frozen v1.3 IDs before claiming those gates. The dirty shared Quint cohort was not copied, modified, or replayed.

## Gate and remaining closure

Gate: **component**. The public fake-agent route test is a real local behavior check, but the production provider adapter is absent and the assigned frozen v1.2 run is incomplete, so no behavior acceptance is claimed.

Remaining gates: run the exact assigned v1.2 command on a changed source revision after the shared lock is available; configure the production ChatGPT planner/builder adapter; finish the v1.3 recovery and schema receipts when frozen IDs exist; verify queue signal/lock behavior and fixture timing; complete later audit/ladder integration and moved-base repair; obtain Linux and optional/live runtime evidence. No R(slice), D-REC, provider live call, or draft conformance result is claimed.
