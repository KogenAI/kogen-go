# I8 release and comparison admission evidence

## Scope and revisions

- Worktree/branch: `/Users/almirsarajcic/Areas/Kogen/kogen-go-wt/I8-release-comparison-admission`, `kgo/I8-release-comparison-admission`.
- Worker base revision: `0cb8042d7e7705f40973e0683af123395f00ce1f`. The implementation is this base plus the I8 files in this receipt. Go CLI/adapter source revision: `0cb8042d7e7705f40973e0683af123395f00ce1f`; no Go implementation files were changed. Built `bin/kogen` SHA-256: `d6154172f1cce7169ebbe379fe2e054f15d945b5ce6e7187c2b426df21a10c37`.
- `tools/release-check.py` SHA-256: `1608af1b3eec0729af855c804fefe0005b3b6903f6740fd51a7f5f2697792ca5`.
- `kogen-spec` target: committed v1.3-draft `e19dd1c21c19c5be1201c3b6a42c59c28b5c2887`; the shared spec worktree contains unrelated uncommitted Quint/harness migration changes. They were not used as a frozen oracle or modified.
- Read `docs/work/WORKER-RULES.md`, `PLAN.md`, `QUEUE-source.md`, `REVIEW-MIDBUILD.md`, I8 and 72 package briefs/evidence, relevant authoritative v1.3 clauses and `CHANGES-v1.3.md`, plus Rust reference commit `a402540b39cedc7f788472297add7ae2f8a6631a` modules for provider sessions/wire, recipes, shaping journals, and Build provider journals.
- Review findings: no mid-build finding assigns a direct code fix to I8. The six P1 findings assigned to corrective integration remain blocking release gates: confinement and Build integrity; crash preservation; child environment; public Build/provider wiring; durable status/scheduling; and moved-base handling. They are listed in the component gate receipt.

## Commands and results

Pinned worker PATH was exported before commands:

```sh
export PATH="$HOME/.local/share/mise/installs/git/2.54.0/bin:$HOME/.local/share/mise/installs/go/1.27.1/bin:$HOME/.local/share/mise/installs/python/3.14.7/bin:$HOME/.local/share/mise/installs/node/24.21.0/bin:$PATH"
```

- `make build` — passed. The frozen run used the required full profile set and v1.2 overlay, `--jobs 2`, `--time-scale 0.02`, and this exact command (stdout was also retained in `conformance.log`):

  ```sh
  SUITE="$HOME/cx/kgo/inputs/conformance-v1.2"
  EVIDENCE="$HOME/cx/kgo/evidence/I8-release-comparison-admission"
  mkdir -p "$EVIDENCE"
  PYTHONDONTWRITEBYTECODE=1 PATH="$HOME/.local/share/mise/installs/python/3.14.7/bin:$PATH" \
    "$SUITE/bin/kogen-conformance" run --kogen "$PWD/bin/kogen" \
    --profile cli,state,approval,shape,build,ladder,provider,custody,format,v1.2 \
    --jobs 2 --time-scale 0.02 --workdir "$EVIDENCE/work" \
    --out "$EVIDENCE/results.jsonl"
  ```

  Run exit: **1**. It resolved the exact frozen suite tree (`527c68f52015b2f1628169c6c22dde992aa4c7642db4c974218b337b3db22b89`, suite revision `0f93bad988fb8d7a8eff4e94954d1db0a046c89d`) and all 236 literal I8 case IDs / 570 instances. Results JSONL SHA-256 is `271ac75fe3920f4a4cfcf1fb7334c0904aeacbb7c26501e0c272b88e35833edc`; the complete log is `conformance.log`, SHA-256 `c38e77e4e3cf5f154081a5d3b54db54f1a16d5d43706d1c36e0d05fe70ff17b7`. Every case row and every failure detail is retained in those files; no rerun replaced them.

- Historical summary command exited 0:

  ```sh
  PYTHONDONTWRITEBYTECODE=1 python3 tools/compare-results.py historical \
    --results "$HOME/cx/kgo/evidence/I8-release-comparison-admission/results.jsonl" \
    --expected-cases 236 --expected-instances 570 --exclude-profile exunit \
    --output "$HOME/cx/kgo/evidence/I8-release-comparison-admission/historical-236.json"
  ```

  `historical-236.json` SHA-256: `ea1a6f81a57af554ada734d4004d821df0b1c7b7515d90173bcb837a0fc7e7c4`. Results were **61/236 cases passing, 175 failing; 370/570 instances passing, 200 failing; zero errors or skips**. The runner also reported zero unimplemented cases. No unmatched request diagnostics were present in these historical rows. The full table by profile was: cli 7/10 cases, state 13/19, approval 16/23, shape 0/20, build 1/10, provider 0/5, custody 0/5, format 4/7, and v1.2 overlay 20/137. The denominators are historical results, not a 236/236 claim and not v1.3 behavior evidence.

- Passing v1.2 assertion IDs (all 61 are preserved in the full results file):

  `cli-10`, `cli-15`, `cli-18`, `cli-19`, `cli-20`, `cli-22`, `cli-23`, `state-01`, `state-04`, `state-05`, `state-07`, `state-08`, `state-09`, `state-10`, `state-13`, `state-16`, `state-24`, `state-25`, `state-29`, `state-30`, `approval-02`, `approval-03`, `approval-04`, `approval-05`, `approval-06`, `approval-07`, `approval-09`, `approval-11`, `approval-12`, `approval-15`, `approval-16`, `approval-19`, `approval-20`, `approval-21`, `approval-22`, `approval-23`, `build-01`, `format-02`, `format-03`, `format-04`, `format-06`, `v1.2-07-cli-03-unknown-command`, `v1.2-08-cli-04-unknown-subcommand`, `v1.2-09-cli-05-unknown-option`, `v1.2-10-cli-06-missing-positionals`, `v1.2-11-cli-07-unexpected-argument`, `v1.2-12-cli-08-option-needs-value`, `v1.2-13-cli-09-boolean-takes-no-value`, `v1.2-14-cli-11-unknown-provider`, `v1.2-15-cli-12-watch-with-json`, `v1.2-16-cli-13-double-dash`, `v1.2-17-cli-14-options-before-command`, `v1.2-18-cli-16-short-option`, `v1.2-19-cli-17-help-bad-topic`, `v1.2-20-cli-21-invalid-slug`, `v1.2-21-cli-24-help-after-positionals`, `v1.2-25-state-20-status-next`, `v1.2-26-state-22-status-next`, `v1.2-33-shape-json-is-unsupported`, `v1.2-35-state-02-schema-errors`, `v1.2-36-state-06-lint-card-warnings`.

- Exact documented historical v1.2 audit assertion conflicts with v1.3-draft §3.8.2 are `v1.2-73-ladder-05`, `v1.2-74-ladder-06`, `v1.2-75-ladder-07`, `v1.2-76-ladder-08`, `v1.2-78-ladder-10`, and `v1.2-79-ladder-11`. Their old assertions require auditor demotion/eligibility effects or the old citation schema; the draft makes audit advice observational and still requires actual approved-item verification. In this run all six rows failed at step 3 (`queue start`, exit 70, `controller/approval_invalid`); the log hints that no provider request reached the fake server. The auditor assertion was not reached, so these are the exact documented *spec-level conflicts*, not measured Go-vs-draft divergences or accepted exceptions. `v1.2-77-ladder-09` is not a documented semantic conflict, but its row also stopped at `approval_invalid` before the assertion. The coordinator-owned `gate-policy.json` is absent; all 175 nonpassing historical rows remain unresolved, including the six conflicts and 169 other nonpass rows.

- `GIT_CONFIG_GLOBAL=/dev/null make check` — passed after the implementation edits. Log: `/Users/almirsarajcic/cx/kgo/evidence/I8-release-comparison-admission/make-check-final.log`, SHA-256 `d44c64623cb20446fdc65a914097c728fb2eb3cabcf16c76f4627c3448880101`.
- `GIT_CONFIG_GLOBAL=/dev/null make race` — passed. Both checks ran with `GOMAXPROCS=2`; the Makefile sets `GOFLAGS=-p=2` and `-parallel=2`. This worker acquired its own `$HOME/cx/kgo/gates.lock/port1455` and `/custody` directories before the run and released only those directories on completion. Race log SHA-256: `91aff81399222f71a463a8b9d9d64e398e4270fd89067a4d2d1bef2e1b0fa881`.
- Python AST parse, `tools/release-check.py --help`, JSON parse of the gate receipt, and `git diff --check` passed. The audit below also executed the final checker implementation.
- Final report command exited **2 as expected** because required release evidence is absent; it wrote the create-only report at `/Users/almirsarajcic/cx/kgo/evidence/I8-release-comparison-admission/release-report-final.json` (SHA-256 `02abe7e668701019e7956a89ad8a3051a06443eb61aae847c9ef524d596f3943`). It confirms the pinned historical inventory, records all 236 statuses and exact nonpass rows, and reports release admission blocked. The local macOS checks do not substitute for same-revision Linux check/race receipts.

## Roles, offline measurements, and build effort

- No comparison manifest was frozen, so no arm's effective role resolution or transport request can be claimed as measured/validated. The target role contract requires project → machine → default resolution per role; shaper/planner/auditor default to Sol/high and builder to Luna/max; `fallback_shaper` aliases the effective shaper provider/model/effort; context and reviewer also use the resolver. The release checker rejects incomplete/cross-provider roles and validates the fallback alias when a frozen manifest is supplied. No cells were scored.
- Package 71's retained offline measurement evidence is from Go source revision `117a61dd986346da76f4cc09e65de76684dac01e`, Rust `a402540b39cedc7f788472297add7ae2f8a6631a`, and Bun `3145004a305d0db3c71ce326ab1071f4b526ae88`, on one contended Darwin arm64 host. Its pinned manifest is `/Users/almirsarajcic/cx/kgo/evidence/71-offline-performance-collector/manifest-03-final.json` (SHA-256 `471ac755bbae45acd137fa7c139c4a02e6f8557f0af2ff36851cbec2e844e0a1`); rows are `measurements-03-final.jsonl` (SHA-256 `3f8555ca798e90d41738b7e85208521ff2efb78b7f90078340881fe0a2b9c3c9`). They are descriptive and are not bound to an I8 frozen comparison manifest.

  | Arm | Cold / warm compile wall ms | Cold / warm check wall ms | Binary bytes | Recorded outcome |
  | --- | ---: | ---: | ---: | --- |
  | Go | 11,342 / 454 | 416,700 / 322,016 | 9,958,626 | compile passed; cold check passed; warm check failed |
  | Rust | 287 / 58 | 573 / 507 | unavailable | offline Cargo resolution lacked cached `jsonwebtoken`; no usable binary |
  | Bun | 683 / 503 | 76,536 / 72,932 | 62,623,218 | compile/check passed; one Linux-only test skipped |

  The workload had 50 Intents and 200 run records. Go's measured status route did not process all run records, so status timings are not a valid cross-language cell. Fake pipeline attempts failed before any fake provider request. Host contention limits timing comparisons. No complete worker/coordinator effort, usage, queue-failure, rebase, rework, or parity-milestone records exist for all three arms; build effort remains unmeasured.

## Exact conflicts and remaining closure

- D2's exact baseline representation mismatch recorded by package 72 is Go digest `8a9d09574395261b1633f2324b97c4a4faf8f164484aad4e5de1e2f352ce8fe6` versus v1.3-draft canonical digest `fe65716657b6320a5fcdf9972e16b08748c92c26ae54fa70c6987926fdb891d9`; draft §2.9 requires sorted canonical JSON and `child_env` as an object. This is not a v1.2 oracle conflict.
- D3's migrated recovery facts `work`, `preserved`, and `preserveOk` are absent from the prior hand fixtures. No planned D-* fixture is treated as a frozen case or pass.
- R(slice) was not run. It requires a scratch copy of the shared frozen coherent Quint cohort, the spec run, then 500 traces × 25 steps for seeds 17, 23, and 41 for each G slice, conforming against the same-revision private binary with full observations. No source oracle or golden files were changed.
- Missing release closure: frozen shared v1.3 manifest/suite and literal IDs; complete G/P/D mapping and passing results on macOS and Linux with zero skips/unimplemented/unmatched; same-revision production replay; I7/71/D1/D2/D3 behavior receipts; Linux `make check` and race; frozen effective-role/recipe and artifact comparison authority; bound offline timing and complete all-arm build effort; and a separately authorized feasible live cache replay with complete telemetry and ≥95% observed reuse on each designated warm request.
- No provider or account calls were made. No scored cells were launched. `docs/work/I8-release-comparison-admission.gate.json` is component-only; this package does not claim behavior acceptance or release/comparison admission.
