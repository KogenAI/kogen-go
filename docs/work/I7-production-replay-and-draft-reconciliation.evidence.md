# I7 — production replay and draft reconciliation evidence

## Gate, revisions, and inputs

**Gate: component only. Behavior acceptance is not established.** The assigned private xspec entrypoint is wired at source revision `5b59e294568a55bb9d3c01b541c68366ed888ce3` on `kgo/I7-production-replay-and-draft-reconciliation`. The implementation commit is `5b59e29` (`Add I7 replay gates and xspec slice routing`). It changes `cmd/kogen-xspec/main.go`, `protocol.go`, `slices.go`, `slices_test.go`, `tools/replay.py`, and `docs/work/I7.md`; the `main.go`/`protocol.go` changes close the private-entrypoint P1 assigned to I7 by `REVIEW-MIDBUILD.md`.

- Host: macOS 26.7.1, arm64. Pinned tools: Git 2.54.0, Go 1.27.1, Python 3.14.7, Node 24.21.0.
- The pre-acceptance CLI build used by the v1.2 command was based on Go revision `117a61dd986346da76f4cc09e65de76684dac01e`, `vcs.modified=true`, SHA-256 `d5d1322cd03735c8d005d83a681ee0efa2b22a43bb9f2098dd8310b308dc9b31`. The private xspec source in that build was also dirty; this binary was not used for a Quint replay.
- A post-implementation-commit `GOMAXPROCS=2 make build` passed. Both binaries have `vcs.revision=5b59e294568a55bb9d3c01b541c68366ed888ce3` and `vcs.modified=false`: CLI SHA-256 `06ca5ac033d58e460d0c43805a50d1a047e31b776ab9232acb71213ec7f5b78b`; private xspec SHA-256 `2edcb207bf29d45a8011393556f368ce2a9f28823429151fe150bb30f59be878`.
- Frozen suite path: `$HOME/cx/kgo/inputs/conformance-v1.2`. Runner SHA-256 `0b53a3b60956cbe8dde7af54e274c46b3e8d430c45662d7dff67445f440b39f9`; runner metadata reports `v1.2+unknown`. Literal case file SHA-256 `243caa45c79129bee577e7ac0b36032b0b8c4e1fcec9b9b4227db08e1b284cba`.
- The authoritative local spec checkout is `$HOME/Areas/Kogen/kogen-spec`, HEAD `e19dd1c21c19c5be1201c3b6a42c59c28b5c2887`, and dirty before this work. Its uncommitted changes include the xspec harness and intent/approve/queue/session/stream Quint schemas, scenarios, and hand goldens. The expected committed manifest `quint/shared-v1.3-cohort.json` is absent. This worker did not modify that checkout, its harness, models, or goldens.
- `REVIEW-MIDBUILD.md` records coordinator update `kogen-spec` `96cefde`: exhausted provider requests stop the Build, leave the Intent queued, and exit the drain with 4; there is no `failed_provider`; only `login` refreshes a token, while `usage_limit` pauses without refresh. The `96cefde` object is not present in the local spec checkout, and its `spec/03-build.md §3.11` still contains the older `failed_provider`-continues policy. The updated source clause therefore remains an authority/input gap for the next replay closure.

## Implemented component

`kogen-xspec` now routes production-backed `intent`, `approve`, `queue`, `status`, `stream`, and `session` factories. The required G registry still names all eight G slices. `recovery` and `rebase` return explicit adapter-unavailable errors and emit no observation; they are not replaced by replay policy copies. D slices are a separate diagnostic lane and are never registered as G.

`tools/replay.py` requires a clean Go and spec checkout, a tracked frozen manifest descended from the v1.3-draft commit, digests for each G schema/scenario/hand-golden tree and the shared harness, explicit D classifications with observational migration, and clean CLI/xspec build metadata at the same revision. It copies the prototype and all eight slices to a scratch directory, verifies generated hand goldens, runs `spec`, generates 500 × 25 traces for seeds 17, 23, and 41, then conforms all hand and generated observations without projection. It reports D separately with `counts_toward_g:false`; missing production observations remain “not run” rather than a pass.

The D comparator consumes full observations, reports changed/missing/additional JSON paths, and always emits class `D` with `counts_toward_g:false`. No boolean or synthetic D result is used as a conformance pass. Existing package 69 adapters map production gate/orchestration/accounts/setup-cache effects; those mappings remain diagnostic until shared frozen D fixtures/IDs are available.

## Commands and component results

All shell commands used the pinned worker PATH. Commands/results:

| Command | Result |
|---|---|
| `GIT_CONFIG_GLOBAL=/dev/null GOMAXPROCS=2 go test -count=1 -p=2 -parallel=2 ./cmd/kogen-xspec ./internal/xspec/...` | PASS. The production xspec, protocol, diagnostic, intent/approval, queue/status, stream/session, and real Git/filesystem landing/recovery effect packages passed. |
| `GIT_CONFIG_GLOBAL=/dev/null GOMAXPROCS=2 go test -count=1 -p=2 -parallel=2 ./cmd/kogen-xspec` | PASS after the final D-comparison regression was changed to assert a reported `/project/tree` difference with no G count. |
| `PYTHONDONTWRITEBYTECODE=1 python tools/replay.py --help`; Python `ast.parse` of `tools/replay.py` | PASS. |
| `git diff --check` | PASS before source commit. |
| `GOMAXPROCS=2 make build` at implementation commit | PASS; clean CLI and xspec build metadata and hashes are above. |
| `GIT_CONFIG_GLOBAL=/dev/null make check` | **FAIL**, retained below: all checked packages passed except `internal/queue/lock`'s concurrent-owner test. No retry was made. |
| Post-build private xspec route check | PASS for a full JSON object from `stream reset`; `recovery` exits 2 with the explicit unavailable-adapter diagnostic and no stdout observation. Focused tests also verify all six registered reset observations and both recovery/rebase refusals. |
| Replay preflight after the implementation commit | **REFUSED**, exit 2, because the authoritative spec tree is dirty. The log is `$HOME/cx/kgo/evidence/I7-production-replay-and-draft-reconciliation/replay-preflight.log` (SHA-256 `7d0c64c365527b45cf0d24b5acb23467417137c8c9deb0cf78bb0696b57a011f`). No scratch copy, Quint generation, or conformance trace was started. |

The full `make check` also ran the embedded draft regressions: D1's
`internal/build/select.TestAuditTimingAndParallelCompletionOrderCannotChangeRequiredA2OrWinner`,
D2's `internal/setupcache` baseline-v3 vectors, and D3's real preservation and
recovery tests (`internal/recovery/preserve` and `internal/recovery`). The
xspec-focused run separately passed its real Git/filesystem landing and
recovery effects. Those component results do not convert the overall
`make check` failure or the v1.2 runner failures into behavior acceptance.

Exact `make check` finding:

```text
--- FAIL: TestConcurrentAcquireCreatesOneOwner (0.01s)
internal/queue/lock/lock_test.go:127: concurrent Acquire() error: queue.pid is not a safe regular owner file
```

The command was run once. Other worker test/conformance processes were active on the host around this run; this is retained as a failed check, not a pass.

## Frozen v1.2 command and actual results

The requested command ran once with the provided profiles and the exact literal IDs from `docs/work/I7-production-replay-and-draft-reconciliation.cases`; stdout/stderr were captured through `tee` under `pipefail`, with no selection or timing flags changed. Runner metadata started at `2026-10-08T04:53:58Z` on macOS 26.7.1 arm64. All **242/242 IDs resolved** and all cases were implemented: **576 instances**, 61 case rows passed and 181 failed, 0 runner errors, 0 skipped, 0 unimplemented. The runner reports 370 passed instances and 206 failed instances; three failing case rows contain partial instance passes (`format-09` 9/15, `v1.2-01-fixed-cli-help-and-grok` 18/21, and `v1.2-128-format-05` 22/24).

The acceptance command exited **1** because of the 181 failed case rows.

| Profile | Cases | Pass | Fail | Instances |
|---|---:|---:|---:|---:|
| cli | 10 | 7 | 3 | 39 |
| state | 19 | 13 | 6 | 68 |
| approval | 23 | 16 | 7 | 28 |
| shape | 20 | 0 | 20 | 23 |
| build | 10 | 1 | 9 | 10 |
| provider | 5 | 0 | 5 | 15 |
| custody | 5 | 0 | 5 | 6 |
| format | 7 | 4 | 3 | 134 |
| exunit | 6 | 0 | 6 | 6 |
| v1.2 overlay | 137 | 20 | 117 | 247 |
| **Total** | **242** | **61** | **181** | **576** |

Case-level passes (v1.2 results, not R(slice) draft passes):

- cli: `cli-10, cli-15, cli-18, cli-19, cli-20, cli-22, cli-23`.
- state: `state-01, state-04, state-05, state-07, state-08, state-09, state-10, state-13, state-16, state-24, state-25, state-29, state-30`.
- approval: `approval-02, approval-03, approval-04, approval-05, approval-06, approval-07, approval-09, approval-11, approval-12, approval-15, approval-16, approval-19, approval-20, approval-21, approval-22, approval-23`.
- build: `build-01`.
- format: `format-02, format-03, format-04, format-06`.
- v1.2 overlay: `v1.2-07-cli-03-unknown-command, v1.2-08-cli-04-unknown-subcommand, v1.2-09-cli-05-unknown-option, v1.2-10-cli-06-missing-positionals, v1.2-11-cli-07-unexpected-argument, v1.2-12-cli-08-option-needs-value, v1.2-13-cli-09-boolean-takes-no-value, v1.2-14-cli-11-unknown-provider, v1.2-15-cli-12-watch-with-json, v1.2-16-cli-13-double-dash, v1.2-17-cli-14-options-before-command, v1.2-18-cli-16-short-option, v1.2-19-cli-17-help-bad-topic, v1.2-20-cli-21-invalid-slug, v1.2-21-cli-24-help-after-positionals, v1.2-25-state-20-status-next, v1.2-26-state-22-status-next, v1.2-33-shape-json-is-unsupported, v1.2-35-state-02-schema-errors, v1.2-36-state-06-lint-card-warnings`.

The complete per-ID/per-instance records and failure text are retained in `$HOME/cx/kgo/evidence/I7-production-replay-and-draft-reconciliation/results.jsonl` (SHA-256 `955f34003a9a950ae2ce74d599c11f83e3abd900938370818095a16b39e77ce1`); the captured command/summary is `$HOME/cx/kgo/evidence/I7-production-replay-and-draft-reconciliation/conformance.log` (SHA-256 `257287423d1adaeaa5ec5e4e23de766a517697dee670f900e6abe8e01319193b`). The most material integration stops are:

- All 20 standard `shape` cases exit 70 at `kogen intent shape`; the public Shape route is not available at this revision.
- The selected provider cases stop at provider command exit 70 or before a fake request; the queue/build ladder cases commonly stop with `controller/approval_invalid` or “no run found for greet.” They do not exercise the intended provider, rung, moved-base, or audit behavior.
- The selected `v1.2-73` through `v1.2-79` audit-history cases each fail at step 3, `kogen queue start`, with exit 70 and stdout `stopped greet: controller/approval_invalid; it stays queued`. The old audit/demotion assertion was not reached.
- The selected moved-base references `v1.2-56` through `v1.2-64` likewise fail before the moved-base assertions, generally because the run is absent after `controller/approval_invalid`. These failures do not reproduce the historical reference defects described below.
- The full command included `custody-01` through `custody-05` and `v1.2-119` through `v1.2-123`. **The required `$HOME/cx/kgo/gates.lock` custody/race lock was not acquired before launch**, and other workers had concurrent custody/conformance processes. All custody outcomes are preserved in JSONL but are non-isolated and are not counted as accepted evidence. The command was not rerun.

## Draft conflicts and replay gaps

No full Quint `R(slice)` replay was run. The post-commit preflight refused the dirty spec checkout; the expected shared manifest is also absent. `recovery` and `rebase` adapters are unavailable, so the tool correctly will not claim eight-slice replay. Seeds 17, 23, and 41 × 500 traces × 25 steps were **not run**; no full-observation divergence or R(slice) pass is claimed. No source oracle, spec, hand golden, or replay harness was edited.

Known historical/draft differences are recorded without presenting an unwired case as a pass or as a newly observed behavior conflict:

- D1's v1.2 audit history `v1.2-73-ladder-05`, `-74-ladder-06`, `-75-ladder-07`, `-76-ladder-08`, `-78-ladder-10`, and `-79-ladder-11` encodes audit demotion/eligibility/landing effects. Draft `spec/03-build.md §3.8.2` makes audit advice observational; it cannot demote approved items, alter counts/selection, or land without actual passing verification. `v1.2-77-ladder-09`'s malformed-JSON/no-demotion eligibility assertion is compatible at that level, but the draft also requires an unknown-verdict warning. In this I7 command all seven stopped at `controller/approval_invalid` before an auditor result, so this remains a documented model conflict, not a measured Go divergence.
- D2 documents an exact v1.3-draft §2.9 baseline serialization conflict: Go fixture digest `8a9d09574395261b1633f2324b97c4a4faf8f164484aad4e5de1e2f352ce8fe6` vs the draft-canonical sorted-object digest `fe65716657b6320a5fcdf9972e16b08748c92c26ae54fa70c6987926fdb891d9`. The current Go encoding uses declaration-order fields and serializes `child_env` as a sorted string array; the draft requires canonical sorted JSON with `child_env` as an object. This belongs to package 54/shared fixture resolution.
- D3's available recovery hand cohort is not schema-coherent with the migrated model: `Fact` requires `work`, `preserved`, and `preserveOk`, while the hand scenarios omit those fields (prior package 67 observed 0/5). D-REC-01–06 are planned, not frozen v1.2 cases or shared v1.3 IDs.
- Package 68 recorded a production-vs-local-draft stream mismatch in `11-auth-capability-and-provider-fallback`: with `refreshable=false`, Go retry emits Build `provider_wait`/pause with `waited=300000`, `exit=0`, `refreshed=false`; that draft scenario expects stopped/provider-login with `exit=4`. This is not a v1.2 runner conflict and still needs the stream/model owner to reconcile.
- Package 67 records historical v1.2 reference defects for the moved-base set: `v1.2-56-build-22` omits `base_moved_at_start`; `v1.2-57-build-23` rejects an already-green moved tip instead of Intent-only landing; `v1.2-58-build-24` omits verification journal events; `v1.2-59-build-25` has no integration repair; `v1.2-60-build-26` omits the repair-exhaustion history and exits 70 after parking; `v1.2-61-build-27` retries a lock once rather than journaling 1/2/4-second retries; `v1.2-63-build-29` omits the dirty checked-out-base warning/event. They were not reached by this run because queue start stopped earlier.

## Deferred closure gates

1. Coordinator/spec owner must make the authoritative `96cefde` provider-exhaustion update available in the shared source and freeze the coherent migrated v1.3 schema/scenario/hand-golden/harness cohort plus literal G and D IDs. Do not use the dirty local cohort as release evidence.
2. Wire full production event adapters for recovery and rebase, then run the isolated scratch replay for all eight G slices with exact hand cohorts plus 500 × 25 for seeds 17/23/41, full observations, and same-revision clean CLI/xspec binaries. Reconcile any actual divergence and retain the complete replay manifest.
3. I3/I4 integration must close public approval → queue → provider → gate → CAS → status, durable status/scheduling, crash-writer preservation barriers, and the required `failed_provider` retirement/exit-4 policy. The 181 runner failures remain open; rerun only after changed source revision and preserve both old and new result sets.
4. Wait for shared frozen D fixtures/IDs and compare actual production observations through the D adapter lane; keep every D result separate from G counts.
5. Linux enforcement, optional runtime, and live comparison evidence remain external. I8 `shared_v13_manifest` and `production_replay_manifest` are unavailable. Package 00 remains a foundation task.

## Exact acceptance command

The case argument below is the literal contents of `docs/work/I7-production-replay-and-draft-reconciliation.cases` (242 IDs; no wildcard). The command was wrapped in `set -o pipefail` and `tee` only to retain stdout/stderr; all runner arguments matched the requested command.

```sh
export PATH="$HOME/.local/share/mise/installs/git/2.54.0/bin:$HOME/.local/share/mise/installs/go/1.27.1/bin:$HOME/.local/share/mise/installs/python/3.14.7/bin:$HOME/.local/share/mise/installs/node/24.21.0/bin:$PATH"
set -o pipefail
make build
SUITE="$HOME/cx/kgo/inputs/conformance-v1.2"
EVIDENCE="$HOME/cx/kgo/evidence/I7-production-replay-and-draft-reconciliation"
mkdir -p "$EVIDENCE"
PYTHONDONTWRITEBYTECODE=1 PATH="$HOME/.local/share/mise/installs/python/3.14.7/bin:$PATH" \
  "$SUITE/bin/kogen-conformance" run --kogen "$PWD/bin/kogen" \
  --profile cli,state,approval,shape,build,ladder,provider,custody,format,v1.2,exunit --case 'approval-01,approval-02,approval-03,approval-04,approval-05,approval-06,approval-07,approval-08,approval-09,approval-10,approval-11,approval-12,approval-13,approval-14,approval-15,approval-16,approval-17,approval-19,approval-20,approval-21,approval-22,approval-23,approval-24,build-01,build-32,build-35,build-36,build-37,build-38,build-39,build-40,build-41,build-42,cli-10,cli-15,cli-18,cli-19,cli-20,cli-22,cli-23,cli-28,cli-29,cli-30,custody-01,custody-02,custody-03,custody-04,custody-05,exunit-01,exunit-02,exunit-03,exunit-04,exunit-05,exunit-06,format-02,format-03,format-04,format-06,format-07,format-08,format-09,provider-10,provider-19,provider-22,provider-24,provider-26,shape-01,shape-03,shape-04,shape-05,shape-06,shape-07,shape-08,shape-09,shape-10,shape-11,shape-12,shape-13,shape-15,shape-16,shape-17,shape-18,shape-21,shape-23,shape-24,shape-25,state-01,state-03,state-04,state-05,state-07,state-08,state-09,state-10,state-13,state-16,state-17,state-18,state-19,state-21,state-24,state-25,state-28,state-29,state-30,v1.2-01-fixed-cli-help-and-grok,v1.2-02-approval-hash-intent-and-test-bytes,v1.2-03-consecutive-request-byte-prefix,v1.2-04-cache-key-session-headers,v1.2-05-missing-usage,v1.2-06-crash-after-base-cas,v1.2-07-cli-03-unknown-command,v1.2-08-cli-04-unknown-subcommand,v1.2-09-cli-05-unknown-option,v1.2-10-cli-06-missing-positionals,v1.2-100-ladder-32,v1.2-101-ladder-33,v1.2-102-ladder-34,v1.2-103-ladder-35,v1.2-104-provider-01,v1.2-105-provider-02,v1.2-106-provider-03,v1.2-107-provider-04,v1.2-108-provider-06,v1.2-109-provider-07,v1.2-11-cli-07-unexpected-argument,v1.2-110-provider-08,v1.2-111-provider-09,v1.2-112-provider-11,v1.2-113-provider-12,v1.2-114-provider-14,v1.2-115-provider-17,v1.2-116-provider-18,v1.2-117-provider-20,v1.2-118-provider-25,v1.2-119-custody-06,v1.2-12-cli-08-option-needs-value,v1.2-120-custody-07,v1.2-121-custody-08,v1.2-122-custody-09,v1.2-123-custody-10,v1.2-124-build-31,v1.2-125-ladder-36,v1.2-126-state-15,v1.2-127-build-10,v1.2-128-format-05,v1.2-129-cli-27,v1.2-13-cli-09-boolean-takes-no-value,v1.2-130-state-11,v1.2-131-state-12,v1.2-132-state-23,v1.2-133-state-26,v1.2-134-state-27,v1.2-135-shape-26,v1.2-136-format-10,v1.2-137-format-12,v1.2-14-cli-11-unknown-provider,v1.2-15-cli-12-watch-with-json,v1.2-16-cli-13-double-dash,v1.2-17-cli-14-options-before-command,v1.2-18-cli-16-short-option,v1.2-19-cli-17-help-bad-topic,v1.2-20-cli-21-invalid-slug,v1.2-21-cli-24-help-after-positionals,v1.2-22-cli-25-status-overview,v1.2-23-cli-26-status-slug,v1.2-24-state-14-grok-account-row,v1.2-25-state-20-status-next,v1.2-26-state-22-status-next,v1.2-27-approval-18-status-next,v1.2-28-provider-13-planner-no-fallback,v1.2-29-provider-15-idle-stall,v1.2-30-provider-16-total-cap,v1.2-31-provider-23-tool-result-budget,v1.2-32-provider-21-login-flow,v1.2-33-shape-json-is-unsupported,v1.2-34-format-11-account-selection,v1.2-35-state-02-schema-errors,v1.2-36-state-06-lint-card-warnings,v1.2-37-build-02,v1.2-38-build-03,v1.2-39-build-04,v1.2-40-build-05,v1.2-41-build-06,v1.2-42-build-07,v1.2-43-build-08,v1.2-44-build-09,v1.2-45-build-11,v1.2-46-build-12,v1.2-47-build-13,v1.2-48-build-14,v1.2-49-build-15,v1.2-50-build-16,v1.2-51-build-17,v1.2-52-build-18,v1.2-53-build-19,v1.2-54-build-20,v1.2-55-build-21,v1.2-56-build-22,v1.2-57-build-23,v1.2-58-build-24,v1.2-59-build-25,v1.2-60-build-26,v1.2-61-build-27,v1.2-62-build-28,v1.2-63-build-29,v1.2-64-build-30,v1.2-65-build-33,v1.2-66-build-34,v1.2-67-build-43,v1.2-68-build-44,v1.2-69-ladder-01,v1.2-70-ladder-02,v1.2-71-ladder-03,v1.2-72-ladder-04,v1.2-73-ladder-05,v1.2-74-ladder-06,v1.2-75-ladder-07,v1.2-76-ladder-08,v1.2-77-ladder-09,v1.2-78-ladder-10,v1.2-79-ladder-11,v1.2-80-ladder-12,v1.2-81-ladder-13,v1.2-82-ladder-14,v1.2-83-ladder-15,v1.2-84-ladder-16,v1.2-85-ladder-17,v1.2-86-ladder-18,v1.2-87-ladder-19,v1.2-88-ladder-20,v1.2-89-ladder-21,v1.2-90-ladder-22,v1.2-91-ladder-23,v1.2-92-ladder-24,v1.2-93-ladder-25,v1.2-94-ladder-26,v1.2-95-ladder-27,v1.2-96-ladder-28,v1.2-97-ladder-29,v1.2-98-ladder-30,v1.2-99-ladder-31' \
  --jobs 2 --time-scale 0.02 --workdir "$EVIDENCE/work" \
  --out "$EVIDENCE/results.jsonl" 2>&1 | tee "$EVIDENCE/conformance.log"
```
