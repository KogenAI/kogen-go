# 45-recipes-budgets evidence

## Revision and gate

- Branch: `kgo/45-recipes-budgets`.
- Worker revision: `9ed9a71c47199650a477fd88ddceb439df654c5d` (`Apply experiment-aware rung selection`), based on `afa6af4c6b29e9c0e981af9a3ce1d8c0afbb2d35`.
- CLI revision: the selected-case binary was built from the unchanged CLI source at base revision `afa6af4`; `internal/build/recipe` was not imported by the CLI. Binary SHA-256: `7a7541473023adf6f1215cc92182b2f525c066978e36b6c2c53922a66d3cae4b`.
- Adapter revision: no Build CLI route or production recipe adapter is wired. The command reports `implementation bootstrap; command routes are not wired`.
- Gate: **component**. Local recipe tests and repository checks pass. No behavior case passed.

## Implemented component

- Added exact parsing for the twelve accepted recipe names, supported `+edge` ladder forms, rung model/input/tool definitions, hard-plan entry scheduling, stage counts, and repeat naming.
- Recipe role settings use `project.ResolveRoles`' effective builder and planner values. Project settings override machine settings; planner requests carry the 900-second timeout, 160,000-character input cap, and no-tools policy.
- Added the 500-word default and 300–2,000 range, format validation for the difficulty line and ordered plan headings, and wrapper-inclusive whitespace word counting.
- Added one 60-minute Build clock with 30-minute stage cap, wait-paused elapsed/remaining accounting, and a separate 10-minute wait-paused landing allowance.
- `experimental_r4` is retained as a raw setting. Explicit false suppresses R4 and explicit true allows it. If the configured cap reaches R4 while the setting is absent, `Build.NextAttempt` reports the unresolved policy instead of choosing one interpretation.

## Commands and results

Pinned tool path used:

```sh
export PATH="$HOME/.local/share/mise/installs/git/2.54.0/bin:$HOME/.local/share/mise/installs/go/1.27.1/bin:$HOME/.local/share/mise/installs/python/3.14.7/bin:$HOME/.local/share/mise/installs/node/24.21.0/bin:$PATH"
```

Final component checks, run against the source committed at `9ed9a71`:

```sh
GOMAXPROCS=2 go test -p=2 -parallel=2 ./internal/build/recipe
GOMAXPROCS=2 GIT_CONFIG_GLOBAL=/dev/null make check
```

Both passed. `make check` reported format, vendor fingerprints, vet, tests, and both builds passed. The recipe package fixtures cover recipe names and variants, resolved roles, project/machine precedence, planner format/counting, repeat cycles, R4 setting behavior, and Build/landing clock pauses.

An earlier `GIT_CONFIG_GLOBAL=/dev/null make check` was interrupted after I found concurrent checks in other worktrees; it stopped while compiling `internal/landing/publish` (`signal: interrupt`, exit 1). I waited for those checks to finish and reran the full command successfully. The interrupted run is retained here and is not counted as a pass.

The exact frozen v1.2 command was run once. `make build` succeeded. The runner reported suite `v1.2+unknown`, platform `macOS-26.7.1-arm64-arm-64bit-Mach-O`, Git `2.54.0`, started `2026-10-07T22:48:02Z`, and resolved seven cases / seven instances:

```sh
SUITE="$HOME/cx/kgo/inputs/conformance-v1.2"
EVIDENCE="$HOME/cx/kgo/evidence/45-recipes-budgets"
mkdir -p "$EVIDENCE"
PYTHONDONTWRITEBYTECODE=1 PATH="$HOME/.local/share/mise/installs/python/3.14.7/bin:$PATH" \
  "$SUITE/bin/kogen-conformance" run --kogen "$PWD/bin/kogen" \
  --profile cli,state,approval,shape,build,ladder,provider,custody,format,v1.2 --case 'state-03,v1.2-103-ladder-35,v1.2-125-ladder-36,v1.2-126-state-15,v1.2-80-ladder-12,v1.2-94-ladder-26,v1.2-99-ladder-31' \
  --jobs 2 --time-scale 0.02 --workdir "$EVIDENCE/work" \
  --out "$EVIDENCE/results.jsonl"
```

Results are retained at `/Users/almirsarajcic/cx/kgo/evidence/45-recipes-budgets/results.jsonl`, with per-case workdirs under `/Users/almirsarajcic/cx/kgo/evidence/45-recipes-budgets/work`.

| Effective case | Instances | Result and observed failure |
| --- | ---: | --- |
| `state-03` | 1 | Failed at `kogen status`: exit 2 instead of 0; stdout empty; stderr `kogen: implementation bootstrap; command routes are not wired`. |
| `v1.2-103-ladder-35` | 1 | Failed during approve setup: exit 2 instead of 0; same bootstrap stderr; fake provider was not reached. |
| `v1.2-125-ladder-36` | 1 | Failed during approve setup: exit 2 instead of 0; same bootstrap stderr; fake provider was not reached. |
| `v1.2-126-state-15` | 1 | Failed during approve setup: exit 2 instead of 0; same bootstrap stderr; fake provider was not reached. |
| `v1.2-80-ladder-12` | 1 | Failed during approve setup: exit 2 instead of 0; same bootstrap stderr; fake provider was not reached. |
| `v1.2-94-ladder-26` | 1 | Failed during approve setup: exit 2 instead of 0; same bootstrap stderr; fake provider was not reached. |
| `v1.2-99-ladder-31` | 1 | Failed during approve setup: exit 2 instead of 0; same bootstrap stderr; fake provider was not reached. |

Totals: **0 passed, 7 failed, 0 errors, 0 skipped, 7 instances**. No compatible behavior pass is claimed; each target assertion was unreachable because the CLI route is not wired.

During local development, one budget fixture first had an incorrect expected remaining landing time (6 minutes instead of 5 after five active minutes); a later R4 fixture expected no attempt even though the default repeat cycle correctly selected `sol-high-2`. Both fixture expectations were corrected, and the final recipe tests and full check passed. These local test failures were not oracle runs.

## Conflicts, replay, and remaining closure

- Exact historical v1.2 behavior conflicts observed: **none**. The seven selected cases failed before reaching their assertions, so no recipe behavior conflict was measured.
- Known unresolved contract tension: historical `v1.2-99-ladder-31` is titled “R4 never runs unless experimental_r4 is set.” Draft §3.1 lists four default ladder rungs, labels `raw-request` experimental, and describes repeats after rung 4; draft §2.3 still lists `ladder.experimental_r4`. `PLAN.md` calls out the recipe-text / experimental-flag tension. This selected case did not reach the assertion, and this note does not classify it as an observed v1.2 conflict. A frozen shared v1.3 decision is needed before behavior closure.
- I4 remains open: wire the public Build flow to the recipe/budget package and rerun the selected cases on the integrated revision. Package 00 remains a real foundation task. Planned D-* fixtures do not have frozen shared v1.3 IDs and are not claimed.
- R(slice) was not run. A scratch copy of the shared coherent migrated Quint cohort was unavailable. Seeds `17`, `23`, and `41` were not run for 500 traces × 25 steps each against a same-revision private binary; divergence is unmeasured. No source oracle or golden was changed.
- Linux, optional-runtime, and live-comparison evidence remains unavailable in this worker run.
- Scope stayed within `internal/build/recipe/**` and the assigned evidence/gate receipt. No spec, suite, golden, replay harness, or gate policy was changed.
