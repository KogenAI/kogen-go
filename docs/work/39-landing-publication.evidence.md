# 39 Landing Publication Evidence

Recorded 8 October 2026 in the assigned worktree
`/Users/almirsarajcic/Areas/Kogen/kogen-go-wt/39-landing-publication`, branch
`kgo/39-landing-publication`.

## Revisions and scope

- Implementation commit: `7b11d41` (`Implement durable landing publication`).
  The branch began at `c5b14d5b4415c38d3b7bbcffb3c41b4bc8879add`.
- Target spec: `kogen-spec` `e19dd1c21c19c5be1201c3b6a42c59c28b5c2887`
  (`v1.3-draft`), `spec/03-build.md` §3.9.3 steps 1–6 and failure handling,
  §3.10; `CHANGES-v1.3.md` §3 and its open-finding note in §6.
- Rust reference: `kogen-rs` `a402540b39cedc7f788472297add7ae2f8a6631a`;
  inspected `git/landing/engine.rs`, `persist.rs`, `repository.rs`,
  `worktree.rs`, `refs.rs`, and landing effect fixtures.
- Frozen oracle: `$HOME/cx/kgo/inputs/conformance-v1.2`, source revision
  `0f93bad988fb8d7a8eff4e94954d1db0a046c89d`. The installed runner reports
  `v1.2+unknown`; runner SHA-256 is
  `0b53a3b60956cbe8dde7af54e274c46b3e8d430c45662d7dff67445f440b39f9`.
- CLI and private adapter binaries were built with Go `1.27.1`, Git
  `2.54.0`, Darwin arm64. `bin/kogen` SHA-256 is
  `87af5b3aa6e117b09551c00b6c609604cd7952d93e8e140f03ef1a59c6fa5d2d`;
  `bin/kogen-xspec` SHA-256 is
  `54ecdc148debe763f10f3512d1d67caac311cca6689164747a63d4a13026649c`.
  Both binaries report source revision `c5b14d5b4415c38d3b7bbcffb3c41b4bc8879add`
  with `vcs.modified=true`. `internal/xspec/landingrecovery` remains a stub;
  no adapter replay was run.
- Scope is `internal/landing/publish/**` and this package's evidence and gate
  receipt. No shared contract, command route, Makefile, spec, suite, golden, or
  external replay harness was changed.

## Implemented boundary and local effects

`publish.Publish` validates the complete candidate commit, sole expected
parent, and actual commit tree before publication. It durably writes
`landing_prepared` and the schema-2 landing record before checking the base
lock or creating `refs/kogen/incoming/<run_id>`. It advances
`refs/heads/<branch>` with expected-parent CAS, records transient retries at
1, 2, and 4 seconds, and returns `base_moved` after exhausted retries for the
moved-base integration owner.

Before base CAS it inspects every checked-out worktree attached to the target
branch. After a winning CAS it rechecks and updates clean worktrees with
supervised `update-ref --no-deref HEAD`, `reset --keep`, and `symbolic-ref` Git
calls. Dirty or newly changed checkouts are left alone and returned as the
normative `land: warning: landed …` string with a `landing_warning` event.
Incoming-ref deletion failures are recorded as `cleanup_failure`; they do not
undo the successful CAS or change the landed outcome. The caller owns rendering
returned warning strings to stderr, claim release, and workspace removal.

An observer can stop execution immediately after base CAS for crash fixtures.
That boundary leaves the durable landing record and incoming ref intact so the
recovery controller can reconcile the landed commit. Focused real-Git tests
passed for record/incoming/CAS order, base advancement, clean checkout update,
dirty checkout preservation, lock retry, nonfatal incoming cleanup failure,
and crash-after-CAS retention.

## Commands and results

| Command | Result |
|---|---|
| `GOMAXPROCS=2 go test -p=2 -parallel=2 ./internal/landing/publish` | Passed after correcting two development iterations: one compile error in the retry outcome comparison and one test-fixture worktree setup/removal error. The corrected focused run passed all publication tests. |
| `GIT_CONFIG_GLOBAL=/dev/null make check` | Passed format, vendor fingerprints, `go vet ./...`, all repository tests, and both builds. Full log: `$HOME/cx/kgo/evidence/39-landing-publication/make-check-001.log`. |
| `make build` | Passed as the first command in the assigned oracle invocation. Both `kogen` and `kogen-xspec` were built before the oracle run. |
| Assigned frozen v1.2 command below | Runner resolved 4 requested case IDs to 5 instances. **0 passed, 4 failed, 0 errors, 0 skipped.** Each case stopped at step 2 approval with the public CLI bootstrap response; no selected assertion reached landing. Results and workdirs are retained at `$HOME/cx/kgo/evidence/39-landing-publication/results.jsonl` and `$HOME/cx/kgo/evidence/39-landing-publication/work/`. |

The exact assigned command was run once:

```sh
make build
SUITE="$HOME/cx/kgo/inputs/conformance-v1.2"
EVIDENCE="$HOME/cx/kgo/evidence/39-landing-publication"
mkdir -p "$EVIDENCE"
PYTHONDONTWRITEBYTECODE=1 PATH="$HOME/.local/share/mise/installs/python/3.14.7/bin:$PATH" \
  "$SUITE/bin/kogen-conformance" run --kogen "$PWD/bin/kogen" \
  --profile cli,state,approval,shape,build,ladder,provider,custody,format,v1.2 --case 'v1.2-06-crash-after-base-cas,v1.2-61-build-27,v1.2-62-build-28,v1.2-63-build-29' \
  --jobs 2 --time-scale 0.02 --workdir "$EVIDENCE/work" \
  --out "$EVIDENCE/results.jsonl"
```

| Resolved case | Instances | Result |
|---|---:|---|
| `v1.2-06-crash-after-base-cas` | 1 | Failed at first approval: expected exit 5 and a SHA-256 digest; got exit 2, empty stdout, and `kogen: implementation bootstrap; command routes are not wired`. |
| `v1.2-61-build-27` | 1 | Failed at approval: expected exit 0 and queued approval output; got exit 2 and the CLI bootstrap response. |
| `v1.2-62-build-28` | 2 (`1:non-bare origin`, `2:checkout is the origin`) | Both failed at approval: expected exit 0 and queued approval output; got exit 2 and the CLI bootstrap response. |
| `v1.2-63-build-29` | 1 | Failed at approval: expected exit 0 and queued approval output; got exit 2 and the CLI bootstrap response. |

The runner reported that no provider request reached its fake server. Compatible
oracle passes: **none**. No landing behavior pass is claimed. The frozen
`v1.2-06` case file itself notes historical Elixir reference drift for its
`acceptance` project key; this Go run stopped earlier at the no-hash approval
step and did not reproduce or reach that parser incompatibility.

## Conflicts and closure gates

- No v1.2-versus-draft landing assertion conflict was observed: every selected
  case failed before landing at the CLI bootstrap. The exact observed command
  failures are above; `conflicts` is empty in the component receipt.
- **I3 behavior integration is open.** Public approval → queue → provider →
  gate → CAS → status routes are not wired. Integration must render returned
  dirty-checkout warnings to stderr and rerun the selected cases after a source
  revision change, retaining this result.
- **D3 recovery preservation remains open** in its owning package and is an I3
  prerequisite. This publisher does not preserve crashed workspaces or release
  claims.
- `R(rebase)` was not run. Its shared coherent migrated v1.3 Quint cohort is
  unavailable; no scratch-copy `spec` phase or 500 × 25 traces for seeds `17`,
  `23`, or `41` were run or counted. Package 67 owns that replay integration.
- Planned D-* fixtures are not frozen v1.2 cases. Wait for shared frozen v1.3
  case IDs and migrated models before claiming those gates.
- Linux verification is unavailable in this macOS worker. No optional-runtime,
  live-provider, or live-comparison evidence is claimed. No race or custody
  stress command was run.
- `CHANGES-v1.3.md` §3 requires recovery to preserve the latest crashed tree
  before cleanup; that is D3, outside this package. Its §6 also leaves broader
  post-CAS synchronization and durable-record reconciliation open for the
  integration/recovery closure.

This is **component** evidence only. A passing component build does not confer
landing behavior acceptance.
