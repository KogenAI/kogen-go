# 69 — Diagnostic adapters evidence

## Gate and revisions

**Gate: component-ready; behavior acceptance and I7 remain open.** The owned
adapter package is committed at
`c8ce65366ea252ceffdb88cababe94f34dadba1c` on `kgo/69-diagnostic-adapters`.
The evidence and gate receipt are committed separately on the same assigned
branch. Starting Go base: `5eb96cbd6bce5bc34cd62baf49e73b87df85b55c`.

- Target spec: `kogen-spec` v1.3-draft
  `e19dd1c21c19c5be1201c3b6a42c59c28b5c2887`. Read `spec/01-cli.md`,
  `spec/02-formats.md`, `spec/03-build.md`, and relevant cache/accounts
  clauses; reviewed `CHANGES-v1.3.md` §§1–3.
- Read `docs/work/WORKER-RULES.md`, `PLAN.md`, `QUEUE-source.md`, and
  `69-diagnostic-adapters.md`.
- Rust reference: `kogen-rs`
  `a402540b39cedc7f788472297add7ae2f8a6631a`; inspected the xspec,
  gate, orchestration, accounts, and setup-cache modules, plus the production
  orchestration/replay, selector/replay, accounts, and setup-cache modules.
- Frozen oracle: `$HOME/cx/kgo/inputs/conformance-v1.2`, revision
  `0f93bad988fb8d7a8eff4e94954d1db0a046c89d`. The assigned case file
  `docs/work/69-diagnostic-adapters.cases` contains only its trailing newline
  (1 byte): there are no literal effective IDs or instances for this package.
  Planned D fixtures are not frozen v1.2 cases.
- CLI remains unwired: `cmd/kogen-xspec` still enters through
  `app.Bootstrap` (last CLI wiring revision `eccd2d0`). This package does not
  add a controller or public replay route.
- Host/toolchain: Darwin 25.6.0 arm64, Git 2.54.0, Go 1.27.1. The pinned
  worker PATH was exported before shell commands.

## Implemented component

`internal/xspec/diagnostic` maps actual production results for the four D
slices: gate reports; orchestration ladder outcomes and an optional already
computed selector report; rooted accounts store/list/resolver results; and
setup-cache plus independent baseline-cache results. The mappings preserve
available result fields and avoid recomputing production decisions. Accounts
observations include synthetic profile metadata but no credential or token
bytes. Setup-product reuse and baseline reuse remain separate facts.

`Compare` requires complete JSON objects, compares nested objects and arrays,
and returns deterministic changed, missing, and extra paths with both values
and presence flags. Its result is always class `D` with
`counts_toward_g:false`; it does not project away observed fields or infer a
G pass from matching fields.

Local tests invoke the production gate, selector, accounts store/resolver/list,
and setup/baseline cache APIs using temporary repositories and directories and
synthetic data. They check audit-advice/count independence, path-complete
comparison, and independent setup/baseline reuse. These tests establish the
adapter component boundary; they are not a D-slice replay or public behavior
acceptance.

## Commands and results

Commands used the pinned worker PATH.

1. Initial `GIT_CONFIG_GLOBAL=/dev/null make check` invocation — the tool call
   returned before its final exit status was captured. Its result is unknown
   and is not counted as a pass.
2. A fully captured `GIT_CONFIG_GLOBAL=/dev/null make check` on the initial,
   uncommitted adapter tree — failed in two new local tests. The comparison
   test had its expected/actual presence assertion reversed for missing
   `/gone`; the setup-cache test had not created its cache root and received
   `setupcache: resolve cache directory: .../cache: no such file or directory`.
   Both fixture assertions/setup were corrected; this failed run is retained
   here.
3. `git diff --cached --check` — passed before the adapter source commit.
4. `GIT_CONFIG_GLOBAL=/dev/null make check` at adapter source revision
   `c8ce65366ea252ceffdb88cababe94f34dadba1c` — passed. Formatting, vendor
   fingerprints, `go vet ./...`, `go test -count=1 -parallel=2 ./...`, and
   both CLI builds completed successfully; the diagnostic package tests
   passed.
5. No separate conformance runner command was run: this package has no
   assigned black-box IDs/instances. Resolved IDs/instances: **0/0**;
   compatible behavior passes: **0**. No case was counted as passing from
   `make check`.

No full D observations were compared against frozen or shared v1.3 model
fixtures, so measured D-slice divergences and replay seeds: **none**. No
R(slice) replay was run. No source oracle, goldens, spec, replay harness, or
other worktree was changed. Local runtime effects were confined to temporary
Git fixtures, scratch accounts metadata, setup/cache directories, and test
workspaces; no live provider call or user account access occurred.

## Conflicts, scope, and deferred closure

- Exact historical orchestration conflict: the v1.2 `orchestration.qnt`
  `Audit(nowLandable)` model can change landability. Draft v1.3
  `CHANGES-v1.3.md` §1 and `spec/03-build.md` §3.8.2 make audit advice
  observational; it cannot change approved items, counts, selector results,
  or landing. Historical fixtures
  `ladder-05-advisory-lands-by-default`,
  `ladder-06-advisory-with-land-green`,
  `ladder-10-all-change-items-demoted`, and
  `ladder-11-demotion-rescore-lands` assert the incompatible demotion/landing
  behavior. These are identified model conflicts, not observed runtime case
  failures; no assigned black-box case reached such an assertion.
- Setup-cache scope gap, not a measured conflict: the historical v1.2
  setup-cache model covers setup products but not independent baseline v3
  reuse. The adapter maps the actual production setup and baseline results
  independently; shared frozen D fixtures are needed to establish expected
  observations.
- The production result APIs do not expose every possible model event or
  private intermediate state. In particular, this package does not implement
  event stepping, reconstruct orchestration history, or run login/logout
  provider transitions. I7 must bind these adapters to the agreed shared D
  observations and classify full divergences. `cmd/kogen-xspec` routing and
  any shared contract/entrypoint changes belong to the coordinator's
  integration round.
- Deferred behavior closure: wait for shared frozen v1.3 D IDs and a coherent
  migrated Quint cohort; wire the four observations through I7; rerun the
  literal selected cases and retain every result. For R(slice), use a scratch
  copy of that cohort, run spec then 500 traces × 25 steps for each seed 17,
  23, and 41, and conform against the same-revision private binary using full
  observations. Package 00 remains a real foundation task.
- Linux, optional-runtime, live-comparison, and I8 manifest gates still need
  their stated external evidence. No such evidence is claimed here.
