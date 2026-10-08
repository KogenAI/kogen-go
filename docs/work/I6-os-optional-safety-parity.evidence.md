# I6 OS and optional wiring evidence

## Revision and scope

- Worktree: `/Users/almirsarajcic/Areas/Kogen/kogen-go-wt/I6-os-optional-safety-parity`.
- Branch: `kgo/I6-os-optional-safety-parity`; source base before this worker: `03d369ea10af59274f7939a0be360b70f2ef0d7b`.
- The acceptance binary reported `kogen 03d369ea (2026-10-08, uncommitted changes)`. It was built by the prescribed `make build` from the current worktree before the worker commit.
- Draft target: `kogen-spec` revision `e19dd1c21c19c5be1201c3b6a42c59c28b5c2887`, including `CHANGES-v1.3.md`. Relevant Rust safety references were read at `a402540`.
- Frozen runner: `$HOME/cx/kgo/inputs/conformance-v1.2/bin/kogen-conformance`, `suite_version: v1.2+unknown`, runner SHA-256 `0b53a3b60956cbe8dde7af54e274c46b3e8d430c45662d7dff67445f440b39f9`.
- The app adapter resolves recipe stages; requires configured edge evidence when enabled; delegates witness/checkpoint behavior; and provides macOS/Linux sandbox selection with integrity-checked fallback when confinement is unavailable. It is not connected to the public Build route in this owned-file set.

## Commands and results

- `GOMAXPROCS=2 go test -p=2 -parallel=2 -run 'TestOptionalWiring|TestBuildSandbox' -v ./internal/app` — passed. The host test logged `host=darwin sandbox_available=true warning=""` and supervised a harmless write beneath its declared workspace. Unit coverage resolves all 13 recipe fixtures: `ladder`, `ladder-diverse`, `ladder-luna`, `ladder-sol-low`, `ladder-sol-medium`, `ladder-sol-high`, `plan-shell`, `staged`, `direct`, `direct-escalate`, `direct-shell`, `escalate-shell`, and `ladder+edge`.
- `GOMAXPROCS=2 tools/safety-matrix.sh` — passed on macOS. All six groups passed: testkit adversarial checks; safefs publication races/FIFO/hardlink/symlink checks; gitio ignore/tree poisoning; recovery preservation; recovery/post-CAS/live-owner; and landing crash-boundary checks. This is macOS-only evidence.
- `GIT_CONFIG_GLOBAL=/dev/null make check` — failed once, exit 2, at the unowned `internal/queue/lock.TestConcurrentAcquireCreatesOneOwner`: `queue.pid is not a safe regular owner file`. Do not count this as a passing full check; the run was not repeated. The remaining packages continued through the make check.
- `git diff --check` and `gofmt -d internal/app/optional_wiring.go internal/app/optional_wiring_test.go` — no output.
- The exact `sh` block in `docs/work/I6-os-optional-safety-parity.md` was extracted verbatim to `/tmp/kogen-I6-optional-safety-parity-acceptance.sh` and run with the pinned tools. It ran `make build`, the frozen runner with the block's literal case list (also listed in `I6-os-optional-safety-parity.cases`), profiles `cli,state,approval,shape,build,ladder,provider,custody,format,v1.2,exunit`, `--jobs 2`, and `--time-scale 0.02`. Runner exit: 1. Full JSONL and workdirs are retained at `/Users/almirsarajcic/cx/kgo/evidence/I6-os-optional-safety-parity/`; JSONL SHA-256: `0ba825b81a52c9a9f91f89f6c56a962c143094a36ed6f5b6df1a1ea6a0ef8a1a`.

The runner resolved 242 case IDs and 576 instances: 61 cases passed, 181 failed, 370 instances passed, 206 failed, 0 errors and 0 skipped. Results by profile:

| Profile | Cases pass/fail | Instances passed/total |
| --- | ---: | ---: |
| cli | 7/3 | 36/39 |
| state | 13/6 | 62/68 |
| approval | 16/7 | 21/28 |
| shape | 0/20 | 0/23 |
| build | 1/9 | 1/10 |
| provider | 0/5 | 0/15 |
| custody | 0/5 | 0/6 |
| format | 4/3 | 126/134 |
| exunit | 0/6 | 0/6 |
| v1.2 overlay | 20/117 | 124/247 |

Observed passing IDs (oracle results only; unwired cases are not behavior acceptance):

`cli-10,cli-15,cli-18,cli-19,cli-20,cli-22,cli-23,state-01,state-04,state-05,state-07,state-08,state-09,state-10,state-13,state-16,state-24,state-25,state-29,state-30,approval-02,approval-03,approval-04,approval-05,approval-06,approval-07,approval-09,approval-11,approval-12,approval-15,approval-16,approval-19,approval-20,approval-21,approval-22,approval-23,build-01,format-02,format-03,format-04,format-06,v1.2-07-cli-03-unknown-command,v1.2-08-cli-04-unknown-subcommand,v1.2-09-cli-05-unknown-option,v1.2-10-cli-06-missing-positionals,v1.2-11-cli-07-unexpected-argument,v1.2-12-cli-08-option-needs-value,v1.2-13-cli-09-boolean-takes-no-value,v1.2-14-cli-11-unknown-provider,v1.2-15-cli-12-watch-with-json,v1.2-16-cli-13-double-dash,v1.2-17-cli-14-options-before-command,v1.2-18-cli-16-short-option,v1.2-19-cli-17-help-bad-topic,v1.2-20-cli-21-invalid-slug,v1.2-21-cli-24-help-after-positionals,v1.2-25-state-20-status-next,v1.2-26-state-22-status-next,v1.2-33-shape-json-is-unsupported,v1.2-35-state-02-schema-errors,v1.2-36-state-06-lint-card-warnings`.

All 20 selected Shape cases failed at the public handler with `intent shaping is wired in the Shape integration round`; `v1.2-135-shape-26` failed both `green` and `red undecided` witness instances at that same handler. Thus this run supplies no V100–101 or V135 public witness behavior evidence. The component tests delegate to the witness controller, and `make check` exercises the witness/checkpoint package tests, but those do not substitute for the missing CLI behavior.

Other representative failures: `cli-28` reports `status watch is closed with queue lifecycle in the Build integration round`; `custody-01` exceeded its 9,000 ms limit at 11,801 ms; both `custody-05` TERM/INT instances stayed at one fake request; `v1.2-119-custody-06` through `v1.2-123-custody-10` stopped in the public queue with `controller/approval_invalid`. All six selected ExUnit cases failed: `exunit-01` failed its base-check baseline with `environment/approval_check_failed`, and the queue cases did not reach passing fixture behavior. The selected command contains no Rails profile/case; Rails source fixtures were exercised only by Go adapter tests, so no real Rails execution is claimed. Ruby/bundle and Elixir/mix binaries were present, but binary presence is not optional-runtime evidence.

## Historical conflicts and scope limits

Known v1.2 expectations superseded by the v1.3 draft are:

- `v1.2-68-build-44` expects a tool-using auditor and the historical `acceptance_upheld` result; the draft auditor is tool-less and observational.
- `v1.2-73-ladder-05`, `v1.2-74-ladder-06`, `v1.2-75-ladder-07`, `v1.2-78-ladder-10`, and `v1.2-79-ladder-11` expect auditor-driven acceptance demotion. CHANGES-v1.3 §1 makes the auditor advisory and forbids it from changing acceptance or landing.

These are exact historical assertion conflicts, not measured behavior in this run: the selected queue cases stopped at `controller/approval_invalid` before reaching their ladder assertions. Other failed IDs are recorded in the retained JSONL and are not classified as draft conflicts without evidence.

- The host was macOS 26.7.1 arm64. No Linux host was available in this worker session; no Linux safety/custody matrix ran. The Linux helper path is not a production confinement claim, and a cross-compile would not close this gate.
- `build_routes.go` does not call `ConfigureSingleBuild`/`ProbeBuildSandbox`; `foundation.go` still returns the Shape integration stub. These are outside the owned files and require the coordinator's public integration handoff before behavior reruns.
- REVIEW-MIDBUILD P1 #1/#7 public Build confinement wiring and both-platform enforcement remain open. P2 #12's observed-lock-wait change targets `internal/auth/refresh/refresh_test.go`, outside this package's owned files, and remains assigned for its owner/handoff. The full-check `internal/queue/lock` race failure above is a separate unowned finding.
- No real Rails acceptance case was selected; selected ExUnit fixtures all failed before a passing runtime result. Do not report optional runtime evidence as passed.
- Planned D-* fixtures and shared frozen v1.3 IDs are unavailable. R(slice) was not run: it needs the scratch copy of the shared coherent migrated Quint cohort, 500 traces × 25 steps per seed 17, 23, and 41, and full-observation conformance against the same-revision private binary.
- No live provider/account access was used. No external oracle, suite, golden, or global config was changed.

The delivered gate is `component` only. The frozen results document historical conflicts and compatible parser/format passes; they do not confer behavior acceptance.
