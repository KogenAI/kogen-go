# 67 — Landing and recovery replay evidence

## Revision, source and scope

- Worker branch: `kgo/67-landing-recovery-replay`; source base `351c657cdc1da3f10c3a3f2ed1cb4a1975bf2c01`.
- Implementation commit: `64677d6f82a7208175ddec5d98b86dd65587d4b2`. This package changes only fixtures and documentation; application source is unchanged from the listed base.
- Target: `kogen-spec` `e19dd1c21c19c5be1201c3b6a42c59c28b5c2887`, v1.3-draft. Reviewed `spec/03-build.md` §§3.9.2–3.10, `spec/02-formats.md` §§2.5.3/2.8, and `CHANGES-v1.3.md` §3.
- Rust reference: `kogen-rs` `a402540b39cedc7f788472297add7ae2f8a6631a`; read the landing engine/rebase/repository paths, recovery project/replay, and xspec rebase/recovery decoders.
- Host/toolchain: macOS 26.7.1 arm64; Go `1.27.1`, Git `2.54.0`, Python `3.14.7`, Node `24.21.0`, Quint `0.33.0`. The required pinned PATH was used.
- CLI and private adapter were built together with `make build` from the application source in the implementation commit. SHA-256: `bin/kogen` `9482e354a9e961350d0044ce8c4401aa29045f6432fc3863ea663b882849ccd7`; `bin/kogen-xspec` `d1790fe83217a1dd91b347da31bbad98daeeb8d37dc9c9ac0534144f9eabebd8`.
- Scope changed only `internal/xspec/landingrecovery/**` and this package's evidence/gate files. No shared contract, command registry, spec, suite, source oracle, golden, or external harness was changed.

## Implemented production effects

The owned package now has temporary-repository effect fixtures that call the existing production `publish.Publish`, `integrate.Land`, and `recovery.Controller.Recover` functions. The tests do not implement a second landing/recovery state machine.

- A real moved-base flow advances a temporary bare origin, gets a lost CAS from the actual ref update, rebases the workspace, runs the production gate through `acceptancecommand.Runner` and a shell test that writes a ledger row, creates a one-parent candidate on the refreshed base, and publishes it. It checks the final base, parent, tree bytes, retained candidate refs, journal events, and terminal status.
- A crash observer stops `publish.Publish` immediately after the real base CAS. Recovery then reconciles the reachable candidate as landed, stops writers before preservation, writes a base-relative recovery tree and create-only ref, records it as unverified, releases only the run-owned claim, deletes the matching incoming ref, and removes the workspace. The preserved Git tree is checked for latest bytes, deletion, executable mode, symlink target, and native ignored-file behavior.
- A separate origin race advances the target ref immediately before CAS and verifies the production 1/2/4-second retry sequence followed by `BaseMoved`, without replacing the competing tip. A dirty checked-out base returns the exact §3.9.3 warning while retaining the local bytes.

The preservation port in these fixtures is a test effect; it is not the separately owned D3 production preservation implementation. No live provider or account was used.

## Commands and results

| Command | Result |
|---|---|
| `GIT_CONFIG_GLOBAL=/dev/null GOMAXPROCS=2 GOFLAGS='-mod=vendor -p=2' go test -count=1 -parallel=2 ./internal/xspec/landingrecovery` | Final source passed all four effect fixtures (`ok`, 20.696s); the full check reran the package in 18.259s. Earlier attempts are retained below. |
| `GIT_CONFIG_GLOBAL=/dev/null GOMAXPROCS=2 make check` | Passed formatting, vendor fingerprints, `go vet ./...`, all tests, and both CLI builds on the final source state. It ran while holding this worker's `~/cx/kgo/gates.lock/port1455` and `~/cx/kgo/gates.lock/custody` locks; both were released by the owning shell. |
| `make build` | Passed for `bin/kogen` and `bin/kogen-xspec`; those binaries were used together for the acceptance/replay commands. |
| Exact frozen v1.2 acceptance command below | Runner exit 1: **12 selected cases / 13 instances, 0 pass, 12 fail, 0 error, 0 skip, 0 unimplemented**. Every case stopped at approval because the public CLI route is still the bootstrap. Result JSONL and workdirs are retained at `/Users/almirsarajcic/cx/kgo/evidence/67-landing-recovery-replay/results.jsonl` and `/Users/almirsarajcic/cx/kgo/evidence/67-landing-recovery-replay/work/`. |

The acceptance command was run exactly with the assigned literal IDs and full profiles:

```sh
make build
SUITE="$HOME/cx/kgo/inputs/conformance-v1.2"
EVIDENCE="$HOME/cx/kgo/evidence/67-landing-recovery-replay"
mkdir -p "$EVIDENCE"
PYTHONDONTWRITEBYTECODE=1 PATH="$HOME/.local/share/mise/installs/python/3.14.7/bin:$PATH" \
  "$SUITE/bin/kogen-conformance" run --kogen "$PWD/bin/kogen" \
  --profile cli,state,approval,shape,build,ladder,provider,custody,format,v1.2 --case 'build-36,build-37,build-38,v1.2-06-crash-after-base-cas,v1.2-56-build-22,v1.2-57-build-23,v1.2-58-build-24,v1.2-59-build-25,v1.2-60-build-26,v1.2-61-build-27,v1.2-62-build-28,v1.2-63-build-29' \
  --jobs 2 --time-scale 0.02 --workdir "$EVIDENCE/work" \
  --out "$EVIDENCE/results.jsonl"
```

For Quint runs, the working directory was `/Users/almirsarajcic/cx/kgo/evidence/67-landing-recovery-replay/replay/quint/prototype`; `XSPEC_GOLDEN` isolated each run under `replay/outputs`, leaving source goldens untouched. `KGO_BIN=/Users/almirsarajcic/Areas/Kogen/kogen-go-wt/67-landing-recovery-replay/bin` was set for conform. The slice/seed commands were:

```sh
XSPEC_SLICE=../slices/rebase XSPEC_GOLDEN=../../outputs/rebase/seed-17/golden python3 harness/xspec.py spec
XSPEC_SLICE=../slices/rebase XSPEC_GOLDEN=../../outputs/rebase/seed-17/golden python3 harness/xspec.py gen --traces 500 --steps 25 --seed 17
XSPEC_SLICE=../slices/rebase XSPEC_GOLDEN=../../outputs/rebase/seed-17/golden python3 harness/xspec.py conform -- "$KGO_BIN/kogen-xspec" rebase
XSPEC_SLICE=../slices/rebase XSPEC_GOLDEN=../../outputs/rebase/seed-23/golden python3 harness/xspec.py gen --traces 500 --steps 25 --seed 23
XSPEC_SLICE=../slices/rebase XSPEC_GOLDEN=../../outputs/rebase/seed-23/golden python3 harness/xspec.py conform -- "$KGO_BIN/kogen-xspec" rebase
XSPEC_SLICE=../slices/rebase XSPEC_GOLDEN=../../outputs/rebase/seed-41/golden python3 harness/xspec.py gen --traces 500 --steps 25 --seed 41
XSPEC_SLICE=../slices/rebase XSPEC_GOLDEN=../../outputs/rebase/seed-41/golden python3 harness/xspec.py conform -- "$KGO_BIN/kogen-xspec" rebase
XSPEC_SLICE=../slices/recovery XSPEC_GOLDEN=../../outputs/recovery/seed-17/golden python3 harness/xspec.py spec
XSPEC_SLICE=../slices/recovery XSPEC_GOLDEN=../../outputs/recovery/seed-17/golden python3 harness/xspec.py gen --traces 500 --steps 25 --seed 17
XSPEC_SLICE=../slices/recovery XSPEC_GOLDEN=../../outputs/recovery/seed-17/golden python3 harness/xspec.py conform -- "$KGO_BIN/kogen-xspec" recovery
XSPEC_SLICE=../slices/recovery XSPEC_GOLDEN=../../outputs/recovery/seed-23/golden python3 harness/xspec.py gen --traces 500 --steps 25 --seed 23
XSPEC_SLICE=../slices/recovery XSPEC_GOLDEN=../../outputs/recovery/seed-23/golden python3 harness/xspec.py conform -- "$KGO_BIN/kogen-xspec" recovery
XSPEC_SLICE=../slices/recovery XSPEC_GOLDEN=../../outputs/recovery/seed-41/golden python3 harness/xspec.py gen --traces 500 --steps 25 --seed 41
XSPEC_SLICE=../slices/recovery XSPEC_GOLDEN=../../outputs/recovery/seed-41/golden python3 harness/xspec.py conform -- "$KGO_BIN/kogen-xspec" recovery
```

The runner resolved the cases as follows. Every failure was before landing/recovery behavior; the fake provider was not reached.

| Effective ID | Instances | Result |
|---|---:|---|
| `build-36` | 1 | Step 2 approval: expected exit 0, got 2; stderr `kogen: implementation bootstrap; command routes are not wired`. |
| `build-37` | 1 | Step 2 approval: same exit 2/bootstrap failure. |
| `build-38` | 1 | Step 2 approval: same exit 2/bootstrap failure. |
| `v1.2-06-crash-after-base-cas` | 1 | Step 2 approval: expected exit 5 and SHA-256 output, got exit 2 and bootstrap stderr. |
| `v1.2-56-build-22` | 1 | Step 2 approval: same exit 2/bootstrap failure. |
| `v1.2-57-build-23` | 1 | Step 2 approval: same exit 2/bootstrap failure. |
| `v1.2-58-build-24` | 1 | Step 2 approval: same exit 2/bootstrap failure. |
| `v1.2-59-build-25` | 1 | Step 2 approval: same exit 2/bootstrap failure. |
| `v1.2-60-build-26` | 1 | Step 3 approval: same exit 2/bootstrap failure. |
| `v1.2-61-build-27` | 1 | Step 2 approval: same exit 2/bootstrap failure. |
| `v1.2-62-build-28` | 2 (`non-bare origin`, `checkout is the origin`) | Both instances failed at step 2 approval with exit 2/bootstrap stderr. |
| `v1.2-63-build-29` | 1 | Step 2 approval: same exit 2/bootstrap failure. |

No compatible v1.2 behavior pass is counted. The frozen runner reports `v1.2+unknown` because the installed suite is a snapshot without Git metadata.

### Focused test iterations

All attempts were retained in this note; the final pass did not replace earlier failures.

1. The first focused command failed to compile because `contract.ObjectID` values were passed to fixture helpers requiring `string`; explicit conversions were added.
2. The next focused run passed the crash/recovery and CAS-race tests but failed the dirty-checkout assertion: Git reported macOS's canonical `/private/var/...` worktree path while the test expected the original `/var/...` spelling. The assertion now resolves the checkout with `filepath.EvalSymlinks`.
3. After adding the moved-base flow, a focused run failed because the assertion read `refs/heads/main` from the local checkout while `integrate.Land` was publishing to the temporary bare origin. The assertion now reads the configured publication repository.
4. After replacing the scripted all-green acceptance result with `acceptancecommand.Runner` executing the fixture shell test and parsing its ledger, the focused run passed all four tests (`20.696s`). The final `make check` passed on this source state and reran the package successfully.

## Quint replays

Scratch source: `/Users/almirsarajcic/cx/kgo/evidence/67-landing-recovery-replay/replay/quint`, archived from `kogen-spec` commit `e19dd1c`; the source oracle was not edited. The source worktree had unrelated uncommitted changes in the prototype harness and other slices, so replay artifacts were generated only in the scratch copy. Each seed has its own `outputs/<slice>/seed-<n>/golden` directory. Full observations were requested; no `--project` projection was used.

- `rebase` spec phase: **7/7** hand scenarios agree with the Quint model.
- `recovery` spec phase: **0/5**. Typechecking fails at `recovery/build/scenarios_test.qnt:4`: `Fact` now requires `work`, `preserved`, and `preserveOk`, while the checked-in hand scenarios omit those fields (`QNT000: Couldn't unify row and empty`). This is a source-cohort schema conflict, not a production divergence.
- Generation completed for all six slice/seed combinations, 500 traces × 25 steps each; model invariants held:

| Slice | Seed | Accepted events | Refused events | Generated traces |
|---|---:|---:|---:|---:|
| rebase | 17 | 2,343 | 10,157 | 500 |
| rebase | 23 | 2,306 | 10,194 | 500 |
| rebase | 41 | 2,436 | 10,064 | 500 |
| recovery | 17 | 9,898 | 2,602 | 500 |
| recovery | 23 | 9,886 | 2,614 | 500 |
| recovery | 41 | 9,893 | 2,607 | 500 |

- Corrected production conform results were **0/507 traces** for rebase seeds 17, 23, and 41, and **0/505 traces** for recovery seeds 17, 23, and 41; each had 0 replay steps. All traces diverged at reset because `bin/kogen-xspec` exits with `kogen-xspec: implementation bootstrap; command routes are not wired`. The seed-17 rebase log also preserves an earlier mistyped binary path invocation (`FileNotFoundError`); the corrected run is separately retained as `conform-corrected.log`.
- Generator and conform logs and all generated full traces remain under `/Users/almirsarajcic/cx/kgo/evidence/67-landing-recovery-replay/replay/`. These results do **not** close R(rebase) or R(recovery): the adapter produced no observations, and the recovery hand cohort does not typecheck.

## Historical drift, conflicts and remaining gates

The acceptance command reached no product assertion, so **no v1.2-versus-draft product conflict was observed in this run**. The frozen v1.2 reference metadata documents these exact historical divergences for selected anchors; they were not reproduced by this run:

- B22 / `v1.2-56-build-22`: reference lands on the moved tip but omits `base_moved_at_start` (`ref-bug`).
- B23 / `v1.2-57-build-23`: reference stops with `environment/base_acceptance_failed` when items are already green on a moved base, contrary to the moved-base landing path (`ref-bug`).
- B24 / `v1.2-58-build-24`: reference lands on the moved base but omits verification journal events (`ref-bug`).
- B25 / `v1.2-59-build-25`: reference has no integration repair and parks on a rebase conflict (`R-gap`).
- B26 / `v1.2-60-build-26`: reference has no integration repair and stops the drain with exit 70 after parking (`R-gap`).
- B27 / `v1.2-61-build-27`: reference retries a `.lock` once and then reports `environment/landing_failed`, omitting the 1/2/4-second `landing_retry` sequence (`ref-bug`).
- B29 / `v1.2-63-build-29`: reference omits the dirty checked-out-base warning line and `landing_warning` event (`ref-bug`). B28 has no recorded historical conflict in the v1.2 reference metadata.
- V06 / `v1.2-06-crash-after-base-cas`: the frozen suite README records the historical Kogen reference leaving one `refs/kogen/incoming/` ref after recovery; the assigned case expects incoming cleanup. This run stopped during approval and did not reproduce that behavior.

Closure gaps:

- The private xspec entrypoint remains `app.Bootstrap("kogen-xspec", ...)`; no landing/recovery slice factories are registered. I7 owns the registry and full production observation mapping. The production APIs currently expose publication as a single call with crash observers, while the Quint event stream has finer phase events; completing per-event R replay must continue calling these production effects rather than add an adapter policy copy.
- R(recovery) requires a coherent shared migrated model/scenario/golden/adapter cohort. The available draft hand cohort has the `Fact` schema mismatch above. Planned D-REC fixtures are not frozen v1.2 cases; wait for shared frozen v1.3 IDs and D3's production preservation effect.
- I3 remains open for public approve → queue → provider → gate → CAS → status behavior integration. The 12 selected v1.2 cases must be rerun after that integration changes the source revision, retaining this result.
- Package 00 remains a real foundation task. Linux runtime evidence, optional-runtime checks, and live comparison evidence were unavailable/not run on this macOS worker. No live provider/account access was used.

This worker is **component-ready only**. The production-effect fixtures pass locally; neither the historical CLI cases nor the two xspec behavior gates are accepted.
