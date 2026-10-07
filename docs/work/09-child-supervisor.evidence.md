# 09-child-supervisor evidence

## Revisions and environment

- Worker implementation commit: `642de9455280df020d3cd25f265a15b72a839d40` on `kgo/09-child-supervisor`.
- CLI source revision: `c6f1b4a01dd7d8d860d43f174a4a8897c50246b7`; `cmd/kogen` is still the bootstrap stub (`kogen: implementation bootstrap; command routes are not wired`).
- Production adapter revision: none. The implementation satisfies `contract.ProcessRunner`, but no app route composes or invokes it.
- Target spec: `kogen-spec` `e19dd1c21c19c5be1201c3b6a42c59c28b5c2887` (v1.3-draft); relevant clauses are `spec/05-sandbox-custody.md` §§5.1 and 5.5. `CHANGES-v1.3.md` §3 and §5.4 add preservation-before-workspace-cleanup; that recovery/cleanup policy is outside this component.
- Frozen suite: `kogen-conformance` `0f93bad988fb8d7a8eff4e94954d1db0a046c89d` (v1.2); runner metadata reports `v1.2+unknown`.
- Rust reference: `kogen-rs` `a402540b39cedc7f788472297add7ae2f8a6631a`; inspected `crates/kogen-core/src/run/process.rs`, `run/process/logging.rs`, `run/watchdog.rs`, and `run/process_tests.rs`.
- Host: macOS 26.7.1, Darwin arm64; Go `go1.27.1 darwin/arm64`; Git 2.54.0.

## Implementation and local effects

`Supervisor.Run` implements the shared `contract.ProcessRunner` interface. It validates explicit argv, child environment, working directory and private absolute log path; resolves bare executable names only through the supplied child `PATH`; and does not inherit the supervisor environment. Every argv element is limited to 4 KiB.

The child starts in a new process group. A reader continuously drains a shared stdout/stderr pipe into an unlinked, private log file descriptor, so output volume does not block the deadline. The log is capped at 16 MiB by default (64 MiB maximum); the returned tail defaults to 16 KiB. At completion the bounded log is published create-only using rooted directory descriptors, a 0600 staged file, file fsync, hard-link publication and parent-directory fsync. Existing leaves, including symlinks, are not opened or replaced.

Timeout, context cancellation and normal leader exit terminate remaining group members with TERM, a 200 ms grace period, then KILL. The direct child is waited and signal exits map to `128+signal`. Missing or non-executable programs return unavailable with status 127 or 126. Caller cancellation returns the observed result with `context.Canceled`; a context deadline is reported as `TimedOut`.

Local fixtures exercise explicit env/argv and child-PATH lookup, unset host variables, bounded log and tail, stdout/stderr ordering, a continuously writing deadline case, a hanging helper, a TERM-trapping helper, normal-exit grandchild cleanup, context cancellation/deadline, signal status, unavailable programs, invalid cwd, oversized argv and refusal to follow an existing log symlink. Effects are confined to test temporary directories and local helper children. No live provider, account, user checkout or origin was accessed.

## Commands and results

All commands used the pinned worker PATH from `WORKER-RULES.md`.

| Command | Result |
|---|---|
| `GOMAXPROCS=2 GOFLAGS='-mod=vendor -p=2' GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off go test -count=1 -parallel=2 ./internal/process` | Passed; the final `make check` below also reran the package after the last cwd preflight change. |
| `GIT_CONFIG_GLOBAL=/dev/null GOMAXPROCS=2 make check` | Passed on the final source: format, vendor fingerprints, vet, full tests (`-parallel=2`) and both builds. |
| `git diff --cached --check` | Passed before the source commit. |
| Assigned frozen v1.2 command below | Runner exit 1; 5 selected IDs / 6 instances, 0 passed, 6 failed, 0 errors, 0 skipped, 0 unimplemented. All failures occurred at the bootstrap CLI before a check, child process, queue, or shell tool was invoked. |

The assigned command was run at `2026-10-07T18:20:24Z`, before implementation commit `642de94` (committed at `2026-10-07T21:27:08+03:00`). The CLI source remained the unchanged bootstrap revision and did not link or invoke the supervisor package, so the retained run records the integration boundary, not behavior of that source commit. It was not repeated to replace the failure. Its complete JSONL and workdirs remain at `/Users/almirsarajcic/cx/kgo/evidence/09-child-supervisor/results.jsonl` and `/Users/almirsarajcic/cx/kgo/evidence/09-child-supervisor/work`.

```sh
make build
SUITE="$HOME/cx/kgo/inputs/conformance-v1.2"
EVIDENCE="$HOME/cx/kgo/evidence/09-child-supervisor"
mkdir -p "$EVIDENCE"
PYTHONDONTWRITEBYTECODE=1 PATH="$HOME/.local/share/mise/installs/python/3.14.7/bin:$PATH" \
  "$SUITE/bin/kogen-conformance" run --kogen "$PWD/bin/kogen" \
  --profile cli,state,approval,shape,build,ladder,provider,custody,format,v1.2 --case 'custody-01,custody-02,custody-03,custody-05,v1.2-121-custody-08' \
  --jobs 2 --time-scale 0.02 --workdir "$EVIDENCE/work" \
  --out "$EVIDENCE/results.jsonl"
```

Resolved IDs and actual instances:

| ID | Instances | Result and observed failure |
|---|---:|---|
| `custody-01` | 1 | Failed at `kogen intent approve greet`: exit 2, expected 5; stderr was `kogen: implementation bootstrap; command routes are not wired`. |
| `custody-02` | 1 | Failed at `kogen intent approve greet`: exit 2, expected 5; same bootstrap stderr. |
| `custody-03` | 1 | Failed at `kogen intent approve greet`: exit 2, expected 5; same bootstrap stderr. |
| `custody-05` | 2 (`1:TERM`, `2:INT`) | Both failed at approval: exit 2, expected 0; approval output was absent and stderr was the bootstrap diagnostic. |
| `v1.2-121-custody-08` | 1 | Failed at approval: exit 2, expected 0; approval output was absent and stderr was the bootstrap diagnostic. |

No selected instance reached the process adapter. No case is counted as behavior-compatible or passing. The exact v1.2/draft conflicts observed by this run are none; because all cases stopped at the bootstrap command, this is not a semantic conflict audit or a claim of parity. The 300 KiB heredoc/argv assertion in `v1.2-121-custody-08` remains unexercised by the CLI.

During local implementation, an early focused test caught an `*int`/`int` status conversion mismatch and another caught an incorrect expected literal (`unset` for the absent host variable); both were fixed. Subsequent focused tests and the final full `make check` passed. These local iteration failures do not alter or replace the retained oracle failure.

## Closure gates and gaps

- **I3 behavior remains open.** Wire the runner through the public approval/check route, queue and shell tool; rerun all five selected IDs after integration and retain this failure. `custody-05` parent signal handling and status/event mapping are CLI/controller behavior, beyond this child result mapper.
- **Package 10 remains required for parent death.** This component does not install the guardian handshake or prove that SIGKILL of Kogen stops every live group within two seconds.
- **Linux runtime parity is unavailable here.** Unit and full checks ran on macOS only; no cross-compile is counted as OS behavior evidence.
- **Shared v1.3 gates are unavailable.** Planned D-* fixtures are not frozen v1.2 cases; wait for shared frozen v1.3 IDs. No `R(slice)` run was possible because the coherent migrated shared Quint cohort is unavailable. No 500-trace ×25-step runs for seeds 17, 23 or 41 were performed, and no replay divergence is claimed.
- Optional runtime and live-comparison evidence was not produced. Package 00 remains a real foundation task after bootstrap.

The component unit tests and `make check` support a **component** gate only; this worker commit does not confer behavior acceptance.
