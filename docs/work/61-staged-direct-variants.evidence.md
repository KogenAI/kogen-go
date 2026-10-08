# 61-staged-direct-variants evidence

## Revisions and scope

- Worker branch: `kgo/61-staged-direct-variants`.
- Package commit: `97c23b2335dfa9880cc6817611e92049b534f2c0`; parent/base: `002a719ca70e4b6ff997dd8f3cd57e5388daad0a`.
- Target spec: `kogen-spec` `e19dd1c21c19c5be1201c3b6a42c59c28b5c2887`, including `CHANGES-v1.3.md`.
- Authority read: `spec/02-formats.md` §2.3, `spec/03-build.md` §3.1, `spec/04-provider.md` §§4.5, 4.7, 4.10.1, and `CHANGES-v1.3.md` §§5–7.
- Rust reference read: `kogen-rs` `a402540b39cedc7f788472297add7ae2f8a6631a`; frozen v1.2 suite: `kogen-conformance` `0f93bad988fb8d7a8eff4e94954d1db0a046c89d`.
- CLI: `bin/kogen` built from the package source in commit `97c23b2`. No public route or provider adapter was changed in this package; `StageRunner` and `BuildController` still need the integration adapter.
- Owned code: `internal/optional/staged/**`. The existing literal case list is `docs/work/61-staged-direct-variants.cases`.

## Implementation

`Resolve` composes context → plan → builder rungs → review for `staged`. It gets context, planner, reviewer, and builder tuples from the effective `project.Resolution.Roles` manifest. Context and reviewer overload targets use `provider/retry.ResolveOverloadFallback`; there is no new retry loop. Each model stage gets a stable session bound to the Build run and cache key, with a distinct stage thread.

The review runs only after the common Build controller returns a ready candidate, and before the candidate is returned to its caller for commit. It receives a copied diff and value-only gate summary, cannot mutate the gate report, and is advisory. Context, planner, and reviewer stages expose no callable tools. Builder stages copy their input and tool set from the resolved recipe. `direct` and `direct-escalate` retain direct tools; `direct-shell` and `escalate-shell` retain shell tools. All variants go through the same `BuildController` boundary; gate, repair, retry, snapshot, and landing policy remain outside this package.

The staged runner is not yet connected to the public queue/provider path. These are component fixtures, not end-to-end acceptance.

## Commands and results

Commands used the pinned worker PATH from `WORKER-RULES.md`.

| Command | Result |
|---|---|
| `GOMAXPROCS=2 go test -p=2 -parallel=2 ./internal/optional/staged` | Pass. Includes `TestComposeEveryRecipeUsesRecipeBuilderToolAllowlist`, role/fallback pin fixtures, staged order/session fixtures, direct no-plan/escalation fixture, and stage-failure stop fixture. |
| `GOMAXPROCS=2 GIT_CONFIG_GLOBAL=/dev/null make check` | Pass, run twice; final output: `check: format, vendor fingerprints, vet, tests and both builds passed`. |
| `make build` | Pass; built `bin/kogen` and `bin/kogen-xspec`. |
| Frozen v1.2 command below | 3 cases / 3 instances, all failed; 0 pass, 0 skip, 0 error. The result file is retained at [`results.jsonl`](/Users/almirsarajcic/cx/kgo/evidence/61-staged-direct-variants/results.jsonl). |

Exact frozen command:

```sh
make build
SUITE="$HOME/cx/kgo/inputs/conformance-v1.2"
EVIDENCE="$HOME/cx/kgo/evidence/61-staged-direct-variants"
mkdir -p "$EVIDENCE"
PYTHONDONTWRITEBYTECODE=1 PATH="$HOME/.local/share/mise/installs/python/3.14.7/bin:$PATH" \
  "$SUITE/bin/kogen-conformance" run --kogen "$PWD/bin/kogen" \
  --profile cli,state,approval,shape,build,ladder,provider,custody,format,v1.2 --case 'state-03,v1.2-118-provider-25,v1.2-126-state-15' \
  --jobs 2 --time-scale 0.02 --workdir "$EVIDENCE/work" \
  --out "$EVIDENCE/results.jsonl"
```

Resolved IDs and outcomes:

| Requested anchor | Effective frozen ID | Instances | Result |
|---|---|---:|---|
| S03 | `state-03` | 1 | Fail: historical config keys are rejected under the target schema; exact conflict below. |
| P25 / V118 | `v1.2-118-provider-25` | 1 | Fail before provider behavior: `controller/internal_error: queue execution is wired in the Build integration round`. |
| S15 / V126 | `v1.2-126-state-15` | 1 | Fail before role precedence is exercised: `controller/internal_error: queue execution is wired in the Build integration round`. |

The frozen runner summary reported `state: 1 case, 0 pass, 1 fail`; `v1.2: 2 cases, 0 pass, 2 fail`; total `3 cases, 0 pass, 3 fail`. The v1.2 titles mention “A tool outside the allowed set” and “project roles win over machine config,” but neither reached its intended provider/build assertion because the public queue route is unwired. V118 and V126 are preserved as failures and are not counted as passes.

## Exact historical conflict

`state-03` stopped at status with these diagnostics for the fixture project:

```text
environment/project_config_invalid: /Users/almirsarajcic/cx/kgo/evidence/61-staged-direct-variants/work/state-03/checkout/.kogen/project.yaml
  build.roles has unknown role "fallback_shaper"
  build.roles has unknown role "rung2"
  build.roles has unknown role "rung3"
```

This is the v1.2 fixture expecting those legacy role keys to be accepted. The target v1.3-draft makes `fallback_shaper` an internal alias and allows only the six configured roles (`builder`, `planner`, `shaper`, `auditor`, `reviewer`, `context`); it does not accept `rung2` or `rung3`. This exact historical conflict is reported without loosening the schema.

## Replay, effects, and closure

- Local every-recipe/tool-allowlist and staged ordering fixtures passed. They prove composition and recipe policy selection only; they do not prove a live provider call, public queue run, verification, landing, or OS sandbox effect.
- No real provider/account call was made. The selected frozen provider case stopped before its fake provider behavior.
- R(slice) was not run: no coherent shared migrated v1.3 Quint cohort/private adapter was supplied for this closure. Seeds `17`, `23`, and `41` (500 traces × 25 steps each) were not launched; no replay divergence is claimed.
- Planned D-* fixtures have no frozen v1.3 IDs yet. Wait for shared frozen IDs; do not treat local fixtures as those cases.
- Remaining closure: I6 must wire the component into the integrated Build/optional path and rerun the selected cases on that changed revision. That round must close V118/V126 and every-recipe tool behavior through the production tool controller. Linux, optional runtime, coherent v1.3 replay, and live-comparison evidence remain external gaps.

## Gate

Accepted gate: **component**. Code, role/tool composition fixtures, full `make check`, and `make build` pass. No behavior gate is claimed because the public handler and provider adapter are not wired and all selected frozen cases failed as recorded above.
