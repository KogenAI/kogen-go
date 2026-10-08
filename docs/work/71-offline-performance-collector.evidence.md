# 71-offline-performance-collector evidence

## Revisions and scope

- Worker branch: `kgo/71-offline-performance-collector`; source base before this package: `117a61dd986346da76f4cc09e65de76684dac01e`. `tools/perf-offline.py` SHA-256 at the final run: `c386e5f1aa5593471bee72185dbc266acc5e528b6e58d819dad773291c077479`.
- Target: `kogen-spec` v1.3-draft `e19dd1c21c19c5be1201c3b6a42c59c28b5c2887`, including `CHANGES-v1.3.md`; reviewed status/report clauses §1.7.5 and §§2.10–2.11. The spec worktree was dirty; the final manifest records its paths and content digest. This is not a frozen shared Quint cohort.
- Read `WORKER-RULES.md`, `PLAN.md`, `QUEUE-source.md`, `docs/work/REVIEW-MIDBUILD.md`, the package brief/cases, and the relevant Rust status model/handler/render/detail and xspec status modules. No review finding was assigned directly to package 71. The public pipeline/status wiring findings assigned to integration packages remain blocking there.
- The final pinned manifest identifies the Go, Rust and Bun source revisions as `117a61dd986346da76f4cc09e65de76684dac01e`, `a402540b39cedc7f788472297add7ae2f8a6631a` and `3145004a305d0db3c71ce326ab1071f4b526ae88`. Host/tool identities and full source, spec and suite digests are in `/Users/almirsarajcic/cx/kgo/evidence/71-offline-performance-collector/manifest-03-final.json` (SHA-256 `471ac755bbae45acd137fa7c139c4a02e6f8557f0af2ff36851cbec2e844e0a1`).
- Final measurement JSONL: `/Users/almirsarajcic/cx/kgo/evidence/71-offline-performance-collector/measurements-03-final.jsonl` (SHA-256 `3f8555ca798e90d41738b7e85208521ff2efb78b7f90078340881fe0a2b9c3c9`); retained workdirs/logs: `/Users/almirsarajcic/cx/kgo/evidence/71-offline-performance-collector/work-03-final`. Earlier attempts remain at `manifest-01-initial.json`, `measurements-01-initial.jsonl`, `manifest-02-corrected.json`, and `measurements-02-corrected.jsonl`, with their respective `work` and `work-02-corrected` directories. They are not used to replace or erase final-run failures.

The CLI shim captures each public command's stdout/stderr bytes and forwards those bytes unchanged after the process exits. Timing/resource records go only to the separate collector JSONL. The frozen `cli-20` and `state-30` assertions passed through this shim; instrumentation did not alter their asserted public output.

## Commands and results

Pinned worker PATH was exported as required:

```sh
export PATH="$HOME/.local/share/mise/installs/git/2.54.0/bin:$HOME/.local/share/mise/installs/go/1.27.1/bin:$HOME/.local/share/mise/installs/python/3.14.7/bin:$HOME/.local/share/mise/installs/node/24.21.0/bin:$PATH"
```

- `make build` — passed. Built `bin/kogen` SHA-256: `d5d1322cd03735c8d005d83a681ee0efa2b22a43bb9f2098dd8310b308dc9b31`.
- `GIT_CONFIG_GLOBAL=/dev/null GOMAXPROCS=2 make check` — failed (exit 2). `TestConcurrentAcquireCreatesOneOwner` in `internal/queue/lock` got `queue.pid is not a safe regular owner file`; temporary-directory cleanup then reported `directory not empty`. Other packages printed as passing, but the check is not accepted as green. This failed run is retained in the command transcript; it was not retried.
- Frozen acceptance command — exit 1. The exact run used literal effective IDs `cli-20,state-30,v1.2-01-fixed-cli-help-and-grok`, the required full standard profiles plus v1.2 overlay, `--jobs 2`, `--time-scale 0.02`, and wrote `/Users/almirsarajcic/cx/kgo/evidence/71-offline-performance-collector/results.jsonl` (SHA-256 `30841588b7a11564921a4f801f6b762935259bfae2b7e0c6a011ba7a396d0a48`).

```sh
make build
SUITE="$HOME/cx/kgo/inputs/conformance-v1.2"
EVIDENCE="$HOME/cx/kgo/evidence/71-offline-performance-collector"
mkdir -p "$EVIDENCE"
PYTHONDONTWRITEBYTECODE=1 PATH="$HOME/.local/share/mise/installs/python/3.14.7/bin:$PATH" \
  "$SUITE/bin/kogen-conformance" run --kogen "$PWD/bin/kogen" \
  --profile cli,state,approval,shape,build,ladder,provider,custody,format,v1.2 --case 'cli-20,state-30,v1.2-01-fixed-cli-help-and-grok' \
  --jobs 2 --time-scale 0.02 --workdir "$EVIDENCE/work" \
  --out "$EVIDENCE/results.jsonl"
```

Resolved results: `cli-20` passed 1/1; `state-30` passed 1/1; `v1.2-01-fixed-cli-help-and-grok` failed 3 of 21 (18 passed, 3 failed). Its failing instances are `19:provider list includes Grok`, `20:logout accepts Grok`, and `21:use accepts Grok`. Each returned `controller/internal_error: provider commands are wired in the provider integration round`; expected public provider list/logout/use results were absent. The three failures are retained, not counted as passes.

- Final pinned manifest command:

  ```sh
  python3 tools/perf-offline.py snapshot --go-root "$PWD" --rust-root "$HOME/Areas/Kogen/kogen-rs" --bun-root "$HOME/Areas/Kogen/kogen-ts" --suite "$HOME/cx/kgo/inputs/conformance-v1.2" --spec "$HOME/Areas/Kogen/kogen-spec" --output "$HOME/cx/kgo/evidence/71-offline-performance-collector/manifest-03-final.json"
  ```

- Final collection command; it returned 1 because some measured phases/cases were failed or unavailable, while writing all rows and logs:

  ```sh
  python3 tools/perf-offline.py collect --manifest "$HOME/cx/kgo/evidence/71-offline-performance-collector/manifest-03-final.json" --output "$HOME/cx/kgo/evidence/71-offline-performance-collector/measurements-03-final.jsonl" --work-root "$HOME/cx/kgo/evidence/71-offline-performance-collector/work-03-final" --pipeline-case v1.2-37-build-02 --repetitions 3 --jobs 2 --command-timeout 900
  ```

- `git diff --check` — passed before commit.

## Measurement observations

The host was Darwin 26.7.1 / Darwin 25.6.0, arm64, 10 logical CPUs and 32 GiB RAM. Pins: Git 2.54.0, Go 1.27.1, Rust 1.97.1, Bun 1.4.2, Node 24.21.0 and Python 3.14.7. The v1.2 export is version 1.2, suite revision `0f93bad988fb8d7a8eff4e94954d1db0a046c89d`, input digest `527c68f52015b2f1628169c6c22dde992aa4c7642db4c974218b337b3db22b89`, runner digest `0b53a3b60956cbe8dde7af54e274c46b3e8d430c45662d7dff67445f440b39f9`. The workload fixture contains 50 Intents and 200 run records.

| Language | Cold/warm compile wall ms | Cold/warm check wall ms | Binary bytes / SHA-256 | Result |
| --- | --- | --- | --- | --- |
| Go | 11,342 / 454 | 416,700 / 322,016 | 9,958,626 / `18069e1873da6ec5195044bbfd1e503864cec19c3caac84ba65e96281eb89fbc` | compile passed; cold check passed; warm check failed |
| Rust | 287 / 58 | 573 / 507 | unavailable | offline resolution failed: no cached `jsonwebtoken` package |
| Bun | 683 / 503 | 76,536 / 72,932 | 62,623,218 / `b22718cfbbf8b5e8a516df27e685d8dbe2f15c94492eca961a7d506685c379d8` | compile/check passed; one Linux-only test skipped |

Go's warm check failed `TestQueueInvalidApprovalDoesNotCallTheAgent` and `TestRealApprovalCardPublishStatusAndDraftRemoval`, both with `could not construct the approval child environment`. The separate required worktree `make check` failed the queue-lock test above. Do not describe either Go check as green.

For the three `state-30` repetitions, public status invocation means were Go 838/484/480 ms and Bun 2,920/2,372/2,413 ms. Each runner case passed. The Go status route currently joins Intent and approval data; its source comment says run records and live queue ownership are deferred to the Build integration round. Its timing therefore does not establish processing of the 200 run records and is excluded from any 50/200 cross-language performance cell. The raw rows retain the case pass and measurements.

The fake-provider full-pipeline measurement used effective case `v1.2-37-build-02`. Go: incompatible, 13,564 ms wall, 2,496 ms user CPU, 4,215 ms system CPU, 20,791,296 B peak CLI RSS, 9,958,626 B binary. Bun: incompatible, 36,874 ms wall, 1,465 ms user CPU, 1,686 ms system CPU, 37,355,520 B peak CLI RSS, 62,623,218 B binary. Both made two public CLI calls and recorded zero fake-provider requests; Rust was unavailable with no binary. Go's exact failure was queue start “no run found for greet” (stdout: `building greet`, then `stopped greet: controller/approval_invalid; it stays queued`, then the queue summary). Bun's exact failure was `environment/command_unavailable: queue start`. These rows are not successful pipeline evidence.

Another repository check/conformance job was active on this host during collection. Per-process CPU/RSS are recorded, but wall times are subject to shared-host contention and are not suitable for precise cross-language ranking. No live provider calls, account access, or provider performance cells were used.

## Historical failures, scope, and deferred closure

- Exact observed v1.2 incompatibility: `v1.2-01-fixed-cli-help-and-grok`, instances 19–21, provider list/logout/use return an unwired `controller/internal_error` instead of their expected results. This is an integration wiring gap, not a claimed v1.3-draft conflict.
- The diagnostic fake Build case `v1.2-37-build-02` failed for Go and Bun as described above; no request reached the local fake provider. This does not count as a provider or pipeline pass.
- No v1.3-draft behavior conflict was asserted from these failures. Planned D-* fixtures are not frozen v1.2 cases; wait for shared frozen v1.3 IDs before claiming those gates.
- R(slice) was not run. Do not change the source oracle or goldens. It requires a scratch copy of the shared coherent migrated Quint cohort, then 500 traces × 25 steps for each seed 17, 23 and 41, conformed against a same-revision private binary. The current dirty shared spec worktree is not evidence of a frozen coherent cohort; retain only full observations when that gate is available.
- This is a component receipt only. I8 remains open and requires `shared_v13_manifest` and `production_replay_manifest`; neither is present. Package 00 remains a real foundation task. Linux, optional-runtime and live-comparison gates require their stated external evidence. Worker commit does not confer behavior acceptance.
