# 46 Sequential Ladder Evidence

## Gate and revisions

**Gate: component-ready. Behavior acceptance remains open.** The sequential controller is implemented under `internal/build/ladder/**`, but the public Build command does not call it.

- Branch: `kgo/46-sequential-ladder`.
- Starting revision: `49c3a327aeacad924417021f0fae3a8530893e6e`.
- Component source commits: `f02778f850f2086869b1db4f54c33a85f5f71e39` (`Implement sequential build ladder`) and `157a5d9bf47d304fccbcdd5fd68a723beebd97bd` (`Cover raw ladder rung policy`).
- Target: `kogen-spec` v1.3-draft `e19dd1c21c19c5be1201c3b6a42c59c28b5c2887`; read `spec/03-build.md` §§3.1, 3.4–3.6, 3.8.2–3.8.3 and `CHANGES-v1.3.md` §1. Also read `docs/work/WORKER-RULES.md`, `PLAN.md`, `QUEUE-source.md`, and `INTERFACES.md`.
- Rust reference: `kogen-rs` `a402540b39cedc7f788472297add7ae2f8a6631a`; read `crates/kogen-core/src/run/orchestration/recipe.rs`, `.../machine/transitions/rung.rs`, and `crates/kogen-core/src/build/single_rung/execute.rs` with its `start.rs` and `helpers.rs`.
- Go source revision for the CLI/entrypoint at the acceptance run: `49c3a327...`; `cmd/kogen` was unchanged and still returns `kogen: implementation bootstrap; command routes are not wired`. No ladder adapter is wired. The component source was not imported by that CLI.
- Frozen suite: `$HOME/cx/kgo/inputs/conformance-v1.2`, suite commit `0f93bad988fb8d7a8eff4e94954d1db0a046c89d`, runner SHA-256 `0b53a3b60956cbe8dde7af54e274c46b3e8d430c45662d7dff67445f440b39f9`, reported version `v1.2+unknown`.
- Toolchain/host: Go `go1.27.1 darwin/arm64`, Git `2.54.0`, oracle platform `macOS-26.7.1-arm64-arm-64bit-Mach-O`. Final built `bin/kogen` SHA-256: `d8914905089e6b6f7bae3672904e06d5d3943493216d1cf2ab5ca2cc2fda395e`.

## Implemented component

`ladder.Controller` consumes the resolved `recipe.Build` and uses `Build.NextAttempt` for `max_rungs`, repeat cycles, and raw `experimental_r4` handling. For every attempt, including repeats, it requests a new detached workspace from the workspace factory using the immutable base commit and a unique `R<n>` workspace identity.

Each rung request receives its recipe attempt, the plan only when that rung's input is `plan`, the immediately prior escalation reason, and prior failure summaries. Summaries are capped at five lines and 180 Unicode characters per line. The candidate diff is not part of the request; patch headers, patch lines, and exact candidate diff content are excluded from summaries. The raw resolved `recipe.Build` is also carried through requests and outcomes so experiment and repeat settings remain available to the integration owner.

Every executed attempt is passed to a required snapshot port before escalation or return. Candidate refs use unique attempt IDs (`R1`, `R2`, …), including repeats. A landable result requires a real landable `gate.GateReport` and returns `ready` for the later selection/landing integration. Exhausting attempts returns `failed`; provider/login/controller stops return `stopped`. Existing snapshots remain in the outcome on later stops or recipe-policy errors. Cancellation detaches the snapshot publication context so the current workspace can still be preserved.

Package tests use fake workspace, rung, and snapshot ports to cover fresh workspace identities and common base, escalation reasons, plan propagation/omission, diff-free summaries and 180-character clipping, repeat names, `max_rungs`, explicit raw-rung enable/disable/unresolved policy, stopped-versus-failed outcomes, and preservation of previous snapshots. These tests exercise the component contract; they do not stand in for public effects or production snapshot evidence.

## Commands and results

Pinned worker tools were first on `PATH` for every command:

```sh
export PATH="$HOME/.local/share/mise/installs/git/2.54.0/bin:$HOME/.local/share/mise/installs/go/1.27.1/bin:$HOME/.local/share/mise/installs/python/3.14.7/bin:$HOME/.local/share/mise/installs/node/24.21.0/bin:$PATH"
```

1. `gofmt -w internal/build/ladder/*.go` — passed.
2. `GOMAXPROCS=2 GIT_CONFIG_GLOBAL=/dev/null make check` — an initial run passed format, vendor fingerprints, vet, all tests, and both builds. A later run, after package refinements, failed once in unrelated `internal/queue/lock` test `TestConcurrentAcquireCreatesOneOwner` with `queue.pid is not a safe regular owner file`; the ladder package passed. Two subsequent full runs passed, with the latest including the raw-rung and clipping fixtures. Retained latest log: [`make-check-final.log`](/Users/almirsarajc/cx/kgo/evidence/46-sequential-ladder/make-check-final.log), SHA-256 `95c26f388dff061a13fface8a6cae5a9c948e5f3c03190f89fa71b9f8bfe2197`.
3. `git diff --cached --check` — passed before the component commit.
4. Required frozen v1.2 command (run once; the failure was retained and not retried):

   ```sh
   make build
   SUITE="$HOME/cx/kgo/inputs/conformance-v1.2"
   EVIDENCE="$HOME/cx/kgo/evidence/46-sequential-ladder"
   mkdir -p "$EVIDENCE"
   PYTHONDONTWRITEBYTECODE=1 PATH="$HOME/.local/share/mise/installs/python/3.14.7/bin:$PATH" \
     "$SUITE/bin/kogen-conformance" run --kogen "$PWD/bin/kogen" \
     --profile cli,state,approval,shape,build,ladder,provider,custody,format,v1.2 --case 'v1.2-68-build-44,v1.2-69-ladder-01,v1.2-70-ladder-02,v1.2-80-ladder-12,v1.2-81-ladder-13,v1.2-88-ladder-20,v1.2-89-ladder-21,v1.2-90-ladder-22,v1.2-91-ladder-23,v1.2-94-ladder-26,v1.2-95-ladder-27,v1.2-96-ladder-28,v1.2-97-ladder-29,v1.2-98-ladder-30,v1.2-99-ladder-31' \
     --jobs 2 --time-scale 0.02 --workdir "$EVIDENCE/work" \
     --out "$EVIDENCE/results.jsonl"
   ```

   `make build` passed. The suite resolved all 15 literal IDs below to one instance each: **15 cases / 15 instances, 0 passed, 15 failed, 0 errors, 0 skipped**. This command ran before the final ladder test-only commit; the public CLI source was unchanged and does not import the ladder package, so it records the same bootstrap integration boundary rather than exercising the component.

   | Effective ID | Instances | Result |
   | --- | ---: | --- |
   | `v1.2-68-build-44` | 1 | Fail before assertion |
   | `v1.2-69-ladder-01` | 1 | Fail before assertion |
   | `v1.2-70-ladder-02` | 1 | Fail before assertion |
   | `v1.2-80-ladder-12` | 1 | Fail before assertion |
   | `v1.2-81-ladder-13` | 1 | Fail before assertion |
   | `v1.2-88-ladder-20` | 1 | Fail before assertion |
   | `v1.2-89-ladder-21` | 1 | Fail before assertion |
   | `v1.2-90-ladder-22` | 1 | Fail before assertion |
   | `v1.2-91-ladder-23` | 1 | Fail before assertion |
   | `v1.2-94-ladder-26` | 1 | Fail before assertion |
   | `v1.2-95-ladder-27` | 1 | Fail before assertion |
   | `v1.2-96-ladder-28` | 1 | Fail before assertion |
   | `v1.2-97-ladder-29` | 1 | Fail before assertion |
   | `v1.2-98-ladder-30` | 1 | Fail before assertion |
   | `v1.2-99-ladder-31` | 1 | Fail before assertion |

   Every instance stopped at step 2 (`intent approve greet {hash8:greet}`): exit 2 instead of 0, empty stdout, and stderr `kogen: implementation bootstrap; command routes are not wired`. No provider request reached the fake server. The assertions were not reached; these are not ladder behavior passes or measured v1.2/draft behavior conflicts.

   Results JSONL: [`results.jsonl`](/Users/almirsarajc/cx/kgo/evidence/46-sequential-ladder/results.jsonl), SHA-256 `9c86b66a7922617beb4e76701309908b97330dc05d80e5c54f1592f7e407fa3e`. Per-case workdirs are under `/Users/almirsarajc/cx/kgo/evidence/46-sequential-ladder/work`.

## Conflicts, effects, and deferred closure

- Observed v1.2/draft behavior conflicts: **none measured**; all selected cases failed before their ladder assertions. No compatible public behavior pass is claimed.
- Unresolved historical/draft tension, not a measured conflict: `v1.2-99-ladder-31` says R4 never runs unless `experimental_r4` is set; draft §3.1 lists four default ladder rungs while marking `raw-request` experimental, and §2.3 retains the setting. The recipe dependency returns an error when the cap reaches R4 without an explicit value. Keep this boundary visible and wait for the shared frozen v1.3 policy/IDs; do not silently choose a behavior.
- `I4` remains open: wire the sequential controller with hard-rung scheduling, audit, candidate selection/reporting, Build journal, and landing; then rerun the assigned behavior cases on the integrated revision. Package 00 remains a real foundation task after the bootstrap.
- Planned `D-*` fixtures do not exist as frozen v1.2 cases. Wait for shared frozen v1.3 IDs before claiming their gates. No coherent shared migrated Quint cohort was available, so `R(slice)` was not run: no scratch-copy spec replay or 500 traces × 25 steps for each seed `17`, `23`, and `41`; no same-revision private-binary conformance or divergence claim.
- Linux, optional-runtime, and live-comparison gates remain open for their stated external evidence. No live provider/account access occurred.
- Local effects: full checks built `bin/kogen` and `bin/kogen-xspec`; the conformance runner created the retained result/work directories listed above. No source oracle, spec, suite, golden, replay harness, gate policy, other worktree, or global configuration was changed. Scope stayed within `internal/build/ladder/**` and this package's evidence/gate files.
