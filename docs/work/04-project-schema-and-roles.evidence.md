# 04 — Project schema and roles evidence

## Gate

**Component-ready; behavior acceptance remains open.** This package provides validated project/machine configuration, canonical project/origin/base resolution through the supervised Git port, provider-aware role merging, and the v1.3 draft defaults/refusals. The public CLI still reports that command routes are not wired, so none of the selected black-box cases is a pass.

## Revisions and authority

- Worker branch: `kgo/04-project-schema-and-roles`
- Worker/package commit: `7e0a7427fabcff9e5af3d91d949b77407c9fcdc7`
- The final CLI and private adapter binaries were built from that same worktree revision with `make build`; the CLI route remains the bootstrap handler, and there is no project slice wired into `kogen-xspec`.
- Go starting revision: `363f15f43829edd9716fe6fdc71a5a4a88b0ea2c`
- Target spec: `kogen-spec` `e19dd1c21c19c5be1201c3b6a42c59c28b5c2887` (v1.3-draft)
- Rust reference inspected: `kogen-rs` `a402540b39cedc7f788472297add7ae2f8a6631a`, including `crates/kogen-core/src/project.rs`, `project/schema.rs`, `project/schema/build.rs`, and project tests.
- Frozen input: `$HOME/cx/kgo/inputs/conformance-v1.2`, `VERSION=v1.2`; frozen source revision recorded in PLAN.md is `0f93bad988fb8d7a8eff4e94954d1db0a046c89d`. The supplied input directory is a snapshot without a Git directory.

## Implemented component

- `internal/project/project.go`: strict project and machine config entry points, grouped schema errors, canonical checkout and state key, origin/base precedence, and Git calls through injected `contract.GitPort`/policy.
- `internal/project/schema.go`: project and build schema diagnostics; rejects the independently configurable `fallback_shaper` role; accepts `green` and the legacy spelling; defaults land policy through the resolver to `green`; refuses `auditor_demotion: true` with the required diagnostic.
- `internal/project/roles.go`: field-wise project → machine → default role merge; selected-provider checks; fallback shaper aliases the effective shaper provider/model/effort.
- Package-local fixtures cover grouped schema diagnostics, the D-AUD-01 config refusal, D-SHAPE-04/05 config behavior, role precedence, fallback alias values, and canonical project/base resolution. These are Go unit fixtures, not frozen v1.3 cases.

## Commands and results

The pinned repo-local Git/Go/Python/Node PATH from WORKER-RULES.md was exported in the worker shell before the commands below.

1. `gofmt -w internal/project/*.go && GOMAXPROCS=2 go test -p=2 -parallel=2 ./internal/project` — **passed** (`ok kogen-go/internal/project`).
2. `GIT_CONFIG_GLOBAL=/dev/null GOMAXPROCS=2 make check` on revision `e1f3b57` — **failed** in unowned `internal/gitio` tests:
   - `TestRunnerSeparatesStreamsAndTimesOutHangingGit`: captured stdout was `""`, expected `"stdout-before-hang\n"`; stderr was empty.
   - `TestOriginPolicyUsesGlobalIdentityAndSigningHelperNotLocalHelper`: expected global signing-helper marker was absent.
   This first failed run is recorded here; it was not rerun at that revision.
3. After the validation fix, the same check on revision `7e0a742` completed successfully. Its full output is retained at [make-check-7e0a742.log](/Users/almirsarajcic/cx/kgo/evidence/04-project-schema-and-roles/make-check-7e0a742.log) and ends `check: format, vendor fingerprints, vet, tests and both builds passed`. The surrounding zsh capture command then returned 1 because it tried to assign the read-only shell variable `status`; the check itself reached its success marker.
4. Exact assigned frozen-suite command:

   ```sh
   make build
   SUITE="$HOME/cx/kgo/inputs/conformance-v1.2"
   EVIDENCE="$HOME/cx/kgo/evidence/04-project-schema-and-roles"
   mkdir -p "$EVIDENCE"
   PYTHONDONTWRITEBYTECODE=1 PATH="$HOME/.local/share/mise/installs/python/3.14.7/bin:$PATH" \
     "$SUITE/bin/kogen-conformance" run --kogen "$PWD/bin/kogen" \
     --profile cli,state,approval,shape,build,ladder,provider,custody,format,v1.2 --case 'cli-23,state-03,state-13,state-16,v1.2-126-state-15,v1.2-35-state-02-schema-errors' \
     --jobs 2 --time-scale 0.02 --workdir "$EVIDENCE/work" \
     --out "$EVIDENCE/results.jsonl"
   ```

   `make build` **passed**. The runner on revision `7e0a742` exited 1 and wrote [results.jsonl](/Users/almirsarajcic/cx/kgo/evidence/04-project-schema-and-roles/results.jsonl). The earlier revision’s complete JSONL is preserved at [results-e1f3b57.jsonl](/Users/almirsarajcic/cx/kgo/evidence/04-project-schema-and-roles/results-e1f3b57.jsonl). Summary: 6 selected cases, 8 instances, 0 passed, 6 failed, 0 errors, 0 skipped, 0 unimplemented.

   | Resolved case | Instances | Result |
   |---|---:|---|
   | `cli-23` | 1 | fail |
   | `state-03` | 1 | fail |
   | `state-13` | 1 | fail |
   | `state-16` | 3 | fail |
   | `v1.2-126-state-15` | 1 | fail |
   | `v1.2-35-state-02-schema-errors` | 1 | fail |

   Each observed public-path failure reports `kogen: implementation bootstrap; command routes are not wired` on stderr; commands exit 2 with empty stdout. Thus `cli-23`, `state-13`, and `v1.2-126-state-15` never reach project resolution/role use. `state-03`, `state-16`, and `v1.2-35-state-02-schema-errors` never reach the package schema validator. No selected case is counted as a compatible pass.

## Local effects

`make build` created `bin/kogen` and `bin/kogen-xspec`. The conformance runner wrote the two retained JSONL files above and used `$HOME/cx/kgo/evidence/04-project-schema-and-roles/work`. That work directory was reused for the second revision; the first revision’s JSONL is retained separately, while the current work directory contains the latest run’s fixture state. No live provider calls/account access or source-suite/golden edits occurred.

## Historical conflict and draft fixture status

- Exact frozen v1.2 conflict: `state-03` “A valid config using every key” requires successful loading with `build.roles.shaper: {model: gpt-6-luna, effort: max}`, an independent `build.roles.fallback_shaper: {model: gpt-6.1-sol, effort: high}`, and `build.roles.rung2`/`rung3`. Under v1.3-draft §2.3, the only configurable role keys are `builder`, `planner`, `shaper`, `auditor`, `reviewer`, and `context`; `fallback_shaper` is an internal alias and `rung2`/`rung3` are unknown roles. The old fixture’s explicit Sol fallback also conflicts with the draft alias rule because the effective shaper is Luna/max. Go intentionally rejects those old role keys; it does not preserve this v1.2 success expectation. The captured public run failed earlier at the unwired CLI route, so this is a spec/fixture conflict, not an observed CLI schema diagnostic.
- The selected v1.2 cases do not contain a historical demotion assertion. Historical v1.2 ladder cases 5–11 and related demotion assertions remain incompatible with the draft’s observational auditor; this package implements no demotion.
- `D-AUD-01` and `D-SHAPE-04–05` are planned fixture names, not frozen v1.2 IDs. Local unit fixtures exercise their configuration requirements but do not close those gates. Wait for the shared frozen v1.3 IDs before claiming them.
- No live provider calls, account access, Quint replay, or source-oracle/golden edits were performed. No coherent migrated shared v1.3 Quint cohort was available: seeds 17, 23, and 41 (500 traces × 25 steps each) remain unrun.

## Deferred closure gates

- **I1 schema:** wire project resolution and grouped config diagnostics through the public CLI, then rerun its compatible anchors.
- **I3 live roles:** wire the resolved role manifest into real Build stages and prove project → machine → default settings in public behavior.
- **I5 fallback:** wire the provider-aware fresh Shape fallback and close D-SHAPE-04/05 against frozen shared v1.3 cases.
- **D-AUD-01:** retain the shared frozen fixture and close the public-load refusal for uncalibrated demotion.
- **Shared replay:** run the coherent migrated Quint cohort against the same-revision private binary once available. This package has no wired project slice.
- The first `make check` failure at `e1f3b57` is retained above; the final package revision `7e0a742` reached the repository check success marker recorded in `make-check-7e0a742.log`. The wrapper’s zsh exit 1 came from the read-only variable assignment after that success marker.

