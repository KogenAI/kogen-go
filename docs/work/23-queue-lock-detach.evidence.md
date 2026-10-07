# 23-queue-lock-detach evidence

## Revision and inputs

- Worker branch: `kgo/23-queue-lock-detach`; source base at start and during the oracle run: `fc693c06011d9e96bdfadb8a5740b10e80b0a952`. The package code and this note are committed together on this branch.
- Target spec: `kogen-spec` commit `e19dd1c21c19c5be1201c3b6a42c59c28b5c2887`; queue clauses read: §1.7.4 and §3.11. `CHANGES-v1.3.md` has no queue-lock semantic change.
- Rust reference read: `kogen-rs` commit `a402540b39cedc7f788472297add7ae2f8a6631a`, especially `crates/kogen-core/src/queue/ownership.rs` and `crates/kogen-core/src/build/queue.rs`.
- CLI built with the package worktree: `bin/kogen`, SHA-256 `88b3a1b0f96cab47078adc2be7acf6b2b9a3f42e2c8fa674a55e9cf124109cf8`. The public CLI still emits `kogen: implementation bootstrap; command routes are not wired` for these commands.
- Frozen input: `$HOME/cx/kgo/inputs/conformance-v1.2`, `VERSION` is `1.2`; the local copy has no Git metadata and the runner reports `suite_version: v1.2+unknown`. Runner launcher SHA-256 is `0b53a3b60956cbe8dde7af54e274c46b3e8d430c45662d7dff67445f440b39f9`; `kogen_conformance/runner.py` SHA-256 is `0135fd9095294b3701fc51ebdcc23e596b75f91cd53c10f365dead6824ed8f0e`; `kogen_conformance/__main__.py` SHA-256 is `6388bd317776a67958862514405416eb197f51e060c76234be852a1e8863294c`.

## Implementation

`internal/queue/lock/**` now provides exclusive `queue.pid` creation with the exact `<pid>\n` body, stale PID takeover bounded to two attempts, live-owner detection, and release guarded by both PID and the original file identity. A held `flock` serializes stale-file removal; lock and stop files are published through rooted directory descriptors with file and parent-directory syncs. Unsafe lock files and hard-linked stop/log leaves are refused. Acquiring clears an old `queue.stop`; `RequestStop` writes exactly `stop\n` for a live owner. `Detach` launches the requested executable in a new session with `/dev/null` stdin and combined output appended to `queue.log`; it refuses a missing/non-installed current executable and waits for the child to hold `queue.pid`.

Package tests cover acquisition, concurrent starts, dead PID takeover, owner-only release, stop-file behavior, unsafe symlink/hardlink leaves, detach refusal, new-session launch and appended log output. These are component tests, not black-box behavior acceptance.

## Commands and results

| Command | Result |
|---|---|
| `GOMAXPROCS=2 go test -p=2 -count=5 -parallel=2 ./internal/queue/lock` | Pass; five runs. |
| `GIT_CONFIG_GLOBAL=/dev/null make check` | Pass; format, vendor fingerprint, vet, all Go tests and both builds. |
| `GOMAXPROCS=2 GOOS=linux GOARCH=amd64 go test -p=2 -c -o /tmp/kogen-23-queue-lock-linux.test ./internal/queue/lock` | Linux/amd64 test binary compiled; this is compile evidence only, not a Linux runtime pass. |
| `make build` | Pass. |

The required frozen-oracle command was run once on 2026-10-07. Its exact invocation was:

```sh
make build
SUITE="$HOME/cx/kgo/inputs/conformance-v1.2"
EVIDENCE="$HOME/cx/kgo/evidence/23-queue-lock-detach"
mkdir -p "$EVIDENCE"
PYTHONDONTWRITEBYTECODE=1 PATH="$HOME/.local/share/mise/installs/python/3.14.7/bin:$PATH" \
  "$SUITE/bin/kogen-conformance" run --kogen "$PWD/bin/kogen" \
  --profile cli,state,approval,shape,build,ladder,provider,custody,format,v1.2 --case 'build-32,build-35,v1.2-65-build-33,v1.2-66-build-34' \
  --jobs 2 --time-scale 0.02 --workdir "$EVIDENCE/work" \
  --out "$EVIDENCE/results.jsonl"
```

Resolved IDs and results from the retained JSONL at `/Users/almirsarajcic/cx/kgo/evidence/23-queue-lock-detach/results.jsonl`:

| Effective case | Instances | Result | Observed failure |
|---|---:|---|---|
| `build-32` | 1 | Fail | Step 2 approval exited 2 instead of 0; stderr was `kogen: implementation bootstrap; command routes are not wired\n`. No provider request reached the fake server. |
| `build-35` | 1 | Fail | Step 2 `kogen queue start` exited 2 instead of 0; expected `queue: nothing to build\n`, got empty stdout, with the same bootstrap stderr. |
| `v1.2-65-build-33` | 1 | Fail | Step 3 approval exited 2 instead of 0, with the same bootstrap stderr. No provider request reached the fake server. |
| `v1.2-66-build-34` | 1 | Fail | Step 2 approval exited 2 instead of 0, with the same bootstrap stderr. No provider request reached the fake server. |

Totals: 4 cases, 4 instances, 0 pass, 4 fail, 0 error, 0 skip. No acceptance case reached queue-lock behavior. There are no compatible black-box passes to count. B33 and B34 are superseded by the selected v1.2 replacements; neither old case is claimed as run or passed.

## Conflicts and remaining gates

- Exact historical v1.2 or v1.3-draft queue-lock conflicts observed: none. The four failures are caused by the unwired public CLI bootstrap before approval or queue behavior, not by a differing queue interpretation. The selected cases do not establish any product incompatibility.
- I3 remains open: the integration owner must connect queue start/stop/detach to the real approval → queue → provider → gate → CAS → status path and rerun the selected cases after integration/rebase, retaining this failed result.
- Package 00 remains a real foundation task. I3 also requires its listed queue, process-custody, recovery and D3 dependencies; this component result does not close those gates.
- No coherent shared migrated v1.3 Quint cohort or frozen v1.3 case IDs were available here. No R(queue) scratch-cohort replay or planned D-* fixture is claimed. Wait for the shared frozen IDs before claiming draft gates.
- Linux runtime behavior, optional runtimes and live comparison gates remain unavailable/external evidence gaps. Linux compile evidence above does not close them.

This worker is component-ready only. It does not confer queue behavior acceptance or I3 closure.
