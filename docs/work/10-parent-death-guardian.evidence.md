# 10-parent-death-guardian evidence

## Revisions and environment

- Worker source commit: `b0900abde88de0d0f776e78eabe092f152f7a3c3` (`Add parent-death process guardian`), branch `kgo/10-parent-death-guardian`. The evidence and gate receipt are committed separately and identify this implementation revision.
- CLI source revision used by the acceptance run: `540cb346f8bff8bca50f7c7f6af5cd2d69f6b0ff`; `cmd/kogen` is still the bootstrap stub. There is no production adapter revision: the process package is not imported by the command route, and `Supervisor.Run` is not wired to this guardian component in this package boundary.
- Target spec: `kogen-spec` `e19dd1c21c19c5be1201c3b6a42c59c28b5c2887` (v1.3-draft). Relevant clauses: `spec/05-sandbox-custody.md` §§5.1.3–5.1.4 and 5.5.3–5.5.4. `CHANGES-v1.3.md` §3 adds preserve-before-cleanup behavior for crashed work; that recovery policy is outside this component.
- Frozen suite: `kogen-conformance` `0f93bad988fb8d7a8eff4e94954d1db0a046c89d` (v1.2), runner reports `v1.2+unknown`.
- Rust reference: `kogen-rs` `a402540b39cedc7f788472297add7ae2f8a6631a`; inspected `crates/kogen-core/src/run/watchdog.rs` and `run/process_tests.rs`.
- Host: macOS 26.7.1, Darwin arm64; Go `go1.27.1 darwin/arm64`; Git 2.54.0. Linux was not available as a runtime host.

## Implementation and local effects

`internal/process/guardian.go` provides a private re-exec entry selected by an internal environment marker plus inherited control FDs. The parent waits for the guardian and process-group-anchor handshakes before sending the child launch request over the control pipe. Parent pipe EOF, including parent `SIGKILL`, makes the guardian stop the anchored worker group with TERM, a 200 ms grace period, and KILL. The anchor stays unreaped while signals are sent; group signals verify its PID, start token, and PGID before use. Linux enables child-subreaper mode and reaps adopted grandchildren. macOS reaps the direct worker and anchor; orphaned descendants are handed to launchd because Darwin has no Linux-style subreaper interface.

Tests exercise the internal guardian directly without changing `run.go` or a shared command entrypoint. On macOS, real child fixtures cover the launch handshake, normal cleanup of a stray grandchild, and parent `SIGKILL` cleanup of a worker plus grandchild within two seconds. Process identity tests reject a changed start token. The existing process-runner tests also pass on the final source. Fixture processes and temporary files are local to `testing.T` temporary directories. No provider/account access or origin operation occurred.

## Commands and results

All commands used the pinned worker PATH from `WORKER-RULES.md`.

| Command | Result |
|---|---|
| `GOMAXPROCS=2 GOFLAGS="-mod=vendor -p=2" GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off go test -count=1 -parallel=2 ./internal/process` | Passed on macOS; real child fixtures ran. |
| `GIT_CONFIG_GLOBAL=/dev/null make check` | Passed on final source: format, vendor fingerprints, vet, all tests and both builds. |
| `GOMAXPROCS=2 GOFLAGS="-mod=vendor -p=2" GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off GOOS=linux GOARCH=amd64 go test -c -o /tmp/kogen-process-linux-amd64.test ./internal/process` | Compiled successfully; this is not Linux runtime evidence. |
| `GOMAXPROCS=2 GOFLAGS="-mod=vendor -p=2" GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off GOOS=linux GOARCH=arm64 go test -c -o /tmp/kogen-process-linux-arm64.test ./internal/process` | Compiled successfully; this is not Linux runtime evidence. |
| Assigned frozen v1.2 command below | Runner exit 1: 6 selected case IDs / 7 instances; 0 passed, 6 failed, 0 errors, 0 skipped, 0 unimplemented. All stopped at the initial approval step because the CLI route is unwired. |
| `git diff --check` | Passed before the source commit. |

The exact assigned acceptance command was run once at `2026-10-07T18:47:53Z`. Its complete JSONL and workdirs remain at `/Users/almirsarajc/cx/kgo/evidence/10-parent-death-guardian/results.jsonl` and `/Users/almirsarajc/cx/kgo/evidence/10-parent-death-guardian/work`.

```sh
make build
SUITE="$HOME/cx/kgo/inputs/conformance-v1.2"
EVIDENCE="$HOME/cx/kgo/evidence/10-parent-death-guardian"
mkdir -p "$EVIDENCE"
PYTHONDONTWRITEBYTECODE=1 PATH="$HOME/.local/share/mise/installs/python/3.14.7/bin:$PATH" \
  "$SUITE/bin/kogen-conformance" run --kogen "$PWD/bin/kogen" \
  --profile cli,state,approval,shape,build,ladder,provider,custody,format,v1.2 --case 'build-36,build-38,cli-29,custody-03,custody-04,custody-05' \
  --jobs 2 --time-scale 0.02 --workdir "$EVIDENCE/work" \
  --out "$EVIDENCE/results.jsonl"
```

Resolved IDs and instances:

| ID | Instances | Result |
|---|---:|---|
| `build-36` | 1 | Failed at `approve greet`: exit 2, expected 0; stdout was empty instead of the approval line; stderr was `kogen: implementation bootstrap; command routes are not wired`. No queue, crash recovery, or status assertion ran. |
| `build-38` | 1 | Same approval failure. No signal or status assertion ran. |
| `cli-29` | 1 | Same approval failure. No SIGINT assertion ran. |
| `custody-03` | 1 | Failed at `run kogen intent approve greet`: exit 2, expected 5; stdout empty, stderr was the bootstrap diagnostic. No check process ran. |
| `custody-04` | 1 | Same approval failure as `build-36`. No queue or child process ran. |
| `custody-05` | 2 (`1:TERM`, `2:INT`) | Both instances failed at approval with exit 2, expected 0 and the bootstrap diagnostic. Neither signal assertion ran. |

The run reached no guardian or project-child effect. There are **no behavior-compatible passes**. The exact historical v1.2 expectations above are recorded as failures, not reclassified as passes or as a semantic v1.2/v1.3 conflict. No semantic conflict was observed because all selected cases stopped before their guardian assertions; this run is not a conflict audit. In particular, no destructive-cleanup behavior was added to satisfy `build-36`.

An early local integration experiment temporarily routed `Supervisor.Run` through the guardian before the final ownership boundary was confirmed. Its package test run failed with `private guardian launch handshake timed out` across the runner tests because the first implementation emitted readiness only on helper exit. The handshake was changed to emit readiness immediately after the anchor handshake. The temporary `run.go` edit was reverted; the final guardian component tests use the private session directly and pass. This local failed run is not the frozen-oracle result and did not replace it.

## Closure gates and gaps

- **Component gate only.** I3 must wire the guardian through the production process runner and command path, then rerun the selected behavior cases on macOS. The current CLI is the unchanged bootstrap stub, so no black-box behavior is accepted here.
- **Linux runtime remains open.** Linux amd64 and arm64 test binaries cross-compiled, but no Linux real-child execution or I6 evidence was available.
- **Draft recovery remains separate.** `CHANGES-v1.3.md` §3 / spec §3.10 requires crashed-work preservation before cleanup. This component does not implement recovery publication or claim D3 coverage. Planned D-* fixtures are not frozen v1.2 cases; wait for shared frozen v1.3 IDs.
- **No `R(slice)` replay was run.** The coherent migrated shared Quint cohort and same-revision private binary were unavailable; no 500 traces ×25 steps for seeds 17, 23, or 41 were run, and no divergence is claimed.
- Package 00 remains a real foundation task after the bootstrap. Optional runtime, live comparison, and account-backed evidence were not produced.
