# 70 adversarial effect matrix evidence

## Revisions and environment

- Worker implementation commit: `13a9afe7f9abbf3ac82d712ecc12ec84020f695a` on `kgo/70-adversarial-effect-matrix`; source base at the start of this task: `e0abcc14aa12d168db79ba5775323108274f11f7`. The package adds tests and safety documentation only; the CLI production source and private adapter were not changed.
- CLI/adapter used by the oracle: `kogen` built from the same Go checkout at source base `e0abcc14aa12d168db79ba5775323108274f11f7`. No separate production adapter is wired. Its current bootstrap command route returned `kogen: implementation bootstrap; command routes are not wired`.
- Target spec: `kogen-spec` v1.3-draft, `e19dd1c21c19c5be1201c3b6a42c59c28b5c2887`. Read `CHANGES-v1.3.md` §3; `spec/02-formats.md` §§2.5.3 and 2.8; `spec/03-build.md` §3.10; and `spec/05-sandbox-custody.md` §§5.1, 5.4 and 5.5.
- Rust reference: `kogen-rs` `a402540b39cedc7f788472297add7ae2f8a6631a`. Read `crates/kogen-core/src/safe_fs.rs`, `crates/kogen-core/src/recovery/project/tests.rs`, and `crates/kogen-core/src/git/landing/tests/effects/crash_recovery.rs` for rooted writes and real-I/O recovery boundaries.
- Frozen oracle: `~/cx/kgo/inputs/conformance-v1.2`, sourced from `kogen-conformance` `0f93bad988fb8d7a8eff4e94954d1db0a046c89d`. The run metadata reported `suite_version: v1.2+unknown` and `macOS-26.7.1-arm64-arm-64bit-Mach-O`.
- Tools: pinned Git 2.54.0, Go 1.27.1, Python 3.14.7 and Node 24.21.0. No global Git configuration, source oracle, suite, or golden was changed.

## Local effects exercised

`internal/testkit/adversarial_test.go` adds cross-package tests using temporary real Git repositories and the production ports:

- `TestAdversarialPublicationMatrix` checks replacement of a symlink leaf without touching its target, hardlink refusal without changing either link, and prompt FIFO refusal for both read and publication.
- `TestAdversarialGitIgnoreAndWorkspacePoisoning` checks tracked-path retention after a new ignore rule, native ignore negation, and resistance to `.git/info/exclude`, a configured global exclude, filter, fsmonitor, and hooks. The filter and fsmonitor helpers write markers if invoked.
- `TestAdversarialSigningHelperHangIsSupervised` uses a temporary origin Git configuration and hanging `gpg.program`; `CommitTree` reports a supervised timeout promptly after the signer starts.
- `TestAdversarialRecoveryAdoptsUnrecordedSnapshotAndKeepsLaterWork` publishes the latest pre-snapshot workspace as an unverified create-only ref, repeats preservation without writing the run record to exercise crash adoption, then changes the workspace and verifies the later bytes have a separate durable archive identity while the old ref and workspace remain.
- `TestAdversarialPostCASRecoveryPreservesLaterEditsAsUnverified` advances a temporary base ref with an actual candidate commit, changes the workspace after that CAS, and runs the production recovery controller and preserver. Recovery keeps the run landed, publishes the later bytes as a distinct unverified tree, retains the landed base, and only then removes the workspace.

`tools/safety-matrix.sh` also runs named owner tests for descriptor-rooted parent replacement races, native nested Git ignores and exact modes, D3's tracked edit/deletion/untracked/mode/symlink snapshot, ref and archive failures, record-crash adoption, terminal cleanup retry, post-CAS reconciliation and live-owner no-op, plus the landing crash-after-CAS boundary. The script serializes custody and race fixtures with `$HOME/cx/kgo/gates.lock/custody`; it refuses an existing lock and removes only the lock it created.

## Commands and results

All commands used the pinned PATH. Safety tests and the full check were serialized with the custody mkdir lock.

| Command / attempt | Result |
| --- | --- |
| `GOMAXPROCS=2 tools/safety-matrix.sh` — initial run | Failed in the new recovery fixture because it passed a symlinked temporary state-root path; `preserve.New` correctly requires the canonical path. The fixture now resolves the root before constructing workspace paths. |
| `GOMAXPROCS=2 tools/safety-matrix.sh` — after fixture correction | Passed all six groups: testkit adversarial tests, safefs publication/race tests, gitio ignore/tree tests, D3 preservation tests, recovery tests, and landing crash tests. |
| `GOMAXPROCS=2 tools/safety-matrix.sh` — final run after adding real post-CAS recovery | Passed all six groups, including the real CAS → later workspace edit → production recovery/preservation fixture. |
| `GOMAXPROCS=2 GIT_CONFIG_GLOBAL=/dev/null make check` — before the final post-CAS test was added | Passed formatting, vendor fingerprints, `go vet`, all tests and both CLI builds. |
| `GOMAXPROCS=2 GIT_CONFIG_GLOBAL=/dev/null make check` — final source | Passed formatting, vendor fingerprints, `go vet ./...`, `go test -count=1 -parallel=2 ./...`, and both `CGO_ENABLED=0` builds. |
| `make build` | Passed; built `bin/kogen` for the assigned oracle run. |
| `git diff --check` | Passed. |
| Assigned frozen v1.2 command below | Runner exit 1: 13 cases / 14 instances, 0 passed, 13 failed, 0 errors, 0 skipped, 0 unimplemented. Full JSONL retained at `/Users/almirsarajcic/cx/kgo/evidence/70-adversarial-effect-matrix/results.jsonl`; workdirs remain under `/Users/almirsarajcic/cx/kgo/evidence/70-adversarial-effect-matrix/work`. The run occurred once; it was not retried. |

The exact acceptance command was:

```sh
make build
SUITE="$HOME/cx/kgo/inputs/conformance-v1.2"
EVIDENCE="$HOME/cx/kgo/evidence/70-adversarial-effect-matrix"
mkdir -p "$EVIDENCE"
PYTHONDONTWRITEBYTECODE=1 PATH="$HOME/.local/share/mise/installs/python/3.14.7/bin:$PATH" \
  "$SUITE/bin/kogen-conformance" run --kogen "$PWD/bin/kogen" \
  --profile cli,state,approval,shape,build,ladder,provider,custody,format,v1.2 --case 'custody-01,custody-02,custody-03,custody-04,custody-05,provider-26,v1.2-06-crash-after-base-cas,v1.2-119-custody-06,v1.2-120-custody-07,v1.2-121-custody-08,v1.2-122-custody-09,v1.2-123-custody-10,v1.2-31-provider-23-tool-result-budget' \
  --jobs 2 --time-scale 0.02 --workdir "$EVIDENCE/work" \
  --out "$EVIDENCE/results.jsonl"
```

Resolved cases and results:

| Effective IDs | Instances | Result and exact blocker |
| --- | ---: | --- |
| `provider-26` | 1 | Failed at `kogen intent shape greet ...`: exit 2, expected 0; stdout empty; stderr `kogen: implementation bootstrap; command routes are not wired`. Runner hint: no provider request reached the fake server. |
| `custody-01`–`custody-03` | 3 | Each failed before its custody assertion: `kogen intent approve greet` exited 2, expected 5, with the same bootstrap stderr. |
| `custody-04`–`custody-05` | 3 | Approval stopped at the same bootstrap stderr before queue/signal assertions; both `custody-05` instances (`1:TERM`, `2:INT`) failed. |
| `v1.2-06-crash-after-base-cas`, `v1.2-119-custody-06`–`v1.2-123-custody-10`, `v1.2-31-provider-23-tool-result-budget` | 7 | Each failed before its assigned behavior: the CLI approval command exited 2 with `kogen: implementation bootstrap; command routes are not wired`. |

All 13 selected IDs were implemented by the frozen suite, with 14 total instances. Compatible behavior passes: **0**. The complete JSONL retains every per-step failure. These are integration-route failures, not evidence that the local components failed their corresponding assertions.

## Conflicts, replay, and closure gates

- No exact v1.2/v1.3-draft semantic conflict was established. The selected v1.2 fixtures did not reach the production behavior; no old destructive cleanup, demotion behavior, or private oracle switch was added.
- `D-REC-01`–`D-REC-06` are planned draft fixtures, not frozen v1.2 cases, and shared frozen v1.3 IDs are unavailable. The local real-I/O tests do not count as those cases passing.
- I3 must wire the public approval → queue → provider → gate → CAS → status path. The retained v1.2 run must be interpreted by that integration closure; this package's behavior receipt is not accepted.
- I6 still requires the full adversarial/custody matrix on a real Linux host as well as macOS. Only Darwin execution is evidenced here. Linux, optional Rails/ExUnit runtime evidence, and live-provider comparison gates remain open.
- No `R(slice)` replay was run. The shared coherent migrated v1.3 Quint cohort is not available; seeds 17, 23 and 41 still require 500 traces × 25 steps each with full observations against the same-revision private binary.
- Package 00 remains a required foundation. No claim is made for an unwired case, Linux parity, optional-runtime availability, or behavior acceptance from the green component checks.

This receipt is **component** only. It does not confer I6 or behavior acceptance.
