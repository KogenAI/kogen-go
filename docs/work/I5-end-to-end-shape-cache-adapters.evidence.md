# I5 evidence: end-to-end Shape/cache/adapters

## Worker and source revision

- Worktree: `/Users/almirsarajcic/Areas/Kogen/kogen-go-wt/I5-end-to-end-shape-cache-adapters`
- Branch: `kgo/I5-end-to-end-shape-cache-adapters`
- Base HEAD before the worker commit: `f571b583ea2d5cd42d4cc1e51bfaa75dbfdb5a79`
- Worker commit tested by the exact suite: `cdbe0d8be94338ffda2eb7e160104137d7b4ac52` (`Wire Shape controller adapters`).
- CLI/adapter binary for the exact suite: `make build` from the committed worker revision above.
- Component evidence only. The public `intent shape` dispatcher remains the `foundation.go` bootstrap stub, so no Shape behavior case is counted as accepted.

## Commands and results

- `export PATH="$HOME/.local/share/mise/installs/git/2.54.0/bin:$HOME/.local/share/mise/installs/go/1.27.1/bin:$HOME/.local/share/mise/installs/python/3.14.7/bin:$HOME/.local/share/mise/installs/node/24.21.0/bin:$PATH"; GOMAXPROCS=2 go test -p=2 -parallel=2 ./internal/app` — passed.
- `export PATH="$HOME/.local/share/mise/installs/git/2.54.0/bin:$HOME/.local/share/mise/installs/go/1.27.1/bin:$HOME/.local/share/mise/installs/python/3.14.7/bin:$HOME/.local/share/mise/installs/node/24.21.0/bin:$PATH"; GIT_CONFIG_GLOBAL=/dev/null GOMAXPROCS=2 make check` — passed: format, vendor fingerprints, vet, all tests and both builds.
- `make build` — passed as the first command in the initial conformance invocation.
- The exact command was extracted from `docs/work/I5-end-to-end-shape-cache-adapters.md`; a preflight compared all 240 literal IDs, in order, against `docs/work/I5-end-to-end-shape-cache-adapters.cases`. The command exited 1. Full results and instance observations are retained at `/Users/almirsarajcic/cx/kgo/evidence/I5-end-to-end-shape-cache-adapters/results.jsonl`.
- The initial v1.2 invocation used the requested profiles and nearly the requested case list, but a transcription error omitted exactly `v1.2-11-cli-07-unexpected-argument`, `v1.2-12-cli-08-option-needs-value`, `v1.2-13-cli-09-boolean-takes-no-value`, `v1.2-78-ladder-10` and `v1.2-79-ladder-11`. It is not the acceptance command and is retained at `/Users/almirsarajcic/cx/kgo/evidence/I5-end-to-end-shape-cache-adapters/results-initial-miscopied.jsonl`.
- Initial invocation resolved 235 case IDs and 556 instances: 58 pass, 177 fail, 0 errors, 0 skipped. Per-profile case totals: CLI 10, state 19, approval 23, Shape 20, Build 10, provider 5, custody 5, format 7, ExUnit 6 and v1.2 overlay 130.
- Initial observed compatible passes (recorded only, not an I5 behavior gate): `cli-10,cli-15,cli-18,cli-19,cli-20,cli-22,cli-23,state-01,state-04,state-05,state-07,state-08,state-09,state-10,state-13,state-16,state-24,state-25,state-29,state-30,approval-02,approval-03,approval-04,approval-05,approval-06,approval-07,approval-09,approval-11,approval-12,approval-15,approval-16,approval-19,approval-20,approval-21,approval-22,approval-23,build-01,format-02,format-03,format-04,format-06,v1.2-07,v1.2-08,v1.2-09,v1.2-10,v1.2-14,v1.2-15,v1.2-16,v1.2-17,v1.2-18,v1.2-19,v1.2-20,v1.2-21,v1.2-25,v1.2-26,v1.2-33,v1.2-35,v1.2-36` (overlay suffixes omitted here where the full IDs are shown in the JSONL).
- All 20 initial `shape-*` cases failed because the CLI still reaches the dispatcher stub. All 6 ExUnit cases failed through the public CLI. The initial format-02/03/04/06 corpora passed; format-07/08/09 failed. Full failures and instance detail are in the retained JSONL.
- Exact-run totals: 240 cases, 574 instances, 61 pass, 179 fail, 0 errors, 0 skipped and 0 unimplemented. Per-profile case totals: CLI 10 (7 pass/3 fail), state 19 (13/6), approval 23 (16/7), Shape 20 (0/20), Build 10 (1/9), provider 5 (0/5), custody 5 (0/5), format 7 (4/3), ExUnit 6 (0/6), v1.2 overlay 135 (20/115).
- Exact observed compatible passes, recorded as oracle results but not an I5 behavior gate: `cli-10,cli-15,cli-18,cli-19,cli-20,cli-22,cli-23,state-01,state-04,state-05,state-07,state-08,state-09,state-10,state-13,state-16,state-24,state-25,state-29,state-30,approval-02,approval-03,approval-04,approval-05,approval-06,approval-07,approval-09,approval-11,approval-12,approval-15,approval-16,approval-19,approval-20,approval-21,approval-22,approval-23,build-01,format-02,format-03,format-04,format-06,v1.2-07-cli-03-unknown-command,v1.2-08-cli-04-unknown-subcommand,v1.2-09-cli-05-unknown-option,v1.2-10-cli-06-missing-positionals,v1.2-11-cli-07-unexpected-argument,v1.2-12-cli-08-option-needs-value,v1.2-13-cli-09-boolean-takes-no-value,v1.2-14-cli-11-unknown-provider,v1.2-15-cli-12-watch-with-json,v1.2-16-cli-13-double-dash,v1.2-17-cli-14-options-before-command,v1.2-18-cli-16-short-option,v1.2-19-cli-17-help-bad-topic,v1.2-20-cli-21-invalid-slug,v1.2-21-cli-24-help-after-positionals,v1.2-25-state-20-status-next,v1.2-26-state-22-status-next,v1.2-33-shape-json-is-unsupported,v1.2-35-state-02-schema-errors,v1.2-36-state-06-lint-card-warnings`.
- Exact run: all 20 `shape-*` cases failed through the dispatcher stub; all 6 ExUnit cases failed through the public CLI. The format-02/03/04/06 corpora passed; format-07/08/09 failed. All other outcomes and instance details are in the exact results JSONL.

## Frozen v1.2 and draft conflicts

The exact case list was run after the source revision was committed. The preflight confirmed 240 assigned IDs in order. The recorded pass/fail counts are actual suite outcomes; no unwired Shape case is treated as a behavior pass.

Known historical audit-policy conflicts, which must be migrated rather than made green by restoring demotion, are:

- `v1.2-73-ladder-05`: `over_strict` demotes A2 before repair and permits advisory landing.
- `v1.2-74-ladder-06`: `green-or-advisory` parks a demoted candidate without repair.
- `v1.2-75-ladder-07`: a recognized `over_strict` reply demotes without a citation.
- `v1.2-78-ladder-10`: all change items are demoted.
- `v1.2-79-ladder-11`: demotion before repair is re-scored and lands.

CHANGES-v1.3.md supersedes the old automatic acceptance-demotion default. The exact run observed failures for all five listed conflicts: `v1.2-73`, `v1.2-74`, `v1.2-75`, `v1.2-78` and `v1.2-79`. `v1.2-76-ladder-08` (unsupported `infeasible` stays upheld) and `v1.2-77-ladder-09` (invalid JSON demotes nothing) also failed in this implementation, but their expected audit behavior is not a v1.3 draft conflict. The initial mistyped run observed 73–75 only.

## Deferred closure gates

- Public dispatcher wiring and end-to-end Shape→approve→Build fake pipeline: requires the shared `foundation.go`/package-00 integration handoff.
- Shape accounting/counter transitions, both audits, raw Request/session continuity and Grok P10: current unit tests cover only the listed component boundaries; the CLI route is not wired. Grok attempt metering is post-call and callable-tool filtering is not supported by the current Grok adapter API.
- Rails ledger-producing runner and executable Rails fixture; public ExUnit route fixture.
- D-SHAPE-01–06, D-BASE-01–05 and D-CACHE: no frozen v1.2 cases; await frozen v1.3 IDs.
- REVIEW-MIDBUILD P1 #3: Build check/fixer child environment and fake-wire public Build assertion remain outside the owned files.
- REVIEW-MIDBUILD P2 #10: public approval baseline/setup cache reuse with known toolchain identity remains outside the owned files.
- R(slice): shared Quint cohort and Go xspec command are not wired to this same-revision binary; required 500 × 25 traces per seed (17, 23, 41) and full-observation conform remain unrun.
- Linux, optional runtime, live cache replay and cross-language evidence remain external closure requirements.
