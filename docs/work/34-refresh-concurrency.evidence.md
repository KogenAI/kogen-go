# 34-refresh-concurrency evidence

Recorded 7 October 2026 in the assigned worktree on branch
`kgo/34-refresh-concurrency`.

## Revisions and authority

- Worker implementation: `d68039b46f1bf87eb51bd6a43c9ad1bd6a3a7e44` (with
  initial component commit `73b081f99f296f1b0cc900dc320dd18670bdb459`).
- Base Go tree: `f5b78d9adb137ac13983e97c2663d0553506c38d`. Public CLI and
  adapter files were not changed by this package. `cmd/kogen` still returns
  `implementation bootstrap; command routes are not wired`; the private xspec
  binary built, but no adapter or replay was invoked by the assigned command.
- Host/toolchain: macOS 26.7.1 arm64, Git 2.54.0, Go 1.27.1. The pinned
  worker PATH was exported for commands.
- Target spec: `kogen-spec` commit
  `e19dd1c21c19c5be1201c3b6a42c59c28b5c2887`. Read `spec/04-provider.md`
  §§4.5–4.6 and §§4.10.2–4.10.3, `spec/data/constants.json` lock and refresh
  constants, and `CHANGES-v1.3.md`. The draft does not change refresh policy;
  §4.6 remains authoritative for the lock and owned/injected credential rules.
- Rust reference: clean `kogen-rs` commit
  `a402540b39cedc7f788472297add7ae2f8a6631a`; read
  `crates/kogen-core/src/provider/auth/refresh.rs`, `auth/lock.rs`,
  `auth/grok.rs`, `auth/grok/tests.rs`, and the auth request/retry boundary.
- Frozen oracle: `~/cx/kgo/inputs/conformance-v1.2`; the plan records source
  commit `0f93bad988fb8d7a8eff4e94954d1db0a046c89d`. Runner metadata identified
  `v1.2+unknown` on macOS 26.7.1 arm64.

Read `docs/work/WORKER-RULES.md`, `PLAN.md`, `QUEUE-source.md`, and the package
task note before implementation.

## Component delivered

`internal/auth/refresh` now coordinates ChatGPT and Grok saved credentials.
The lock is `.kogen/locks/<provider>-<label>.lock`, created with rooted
`mkdirat`/`openat` operations under owner-only directories. Its owner file is
0600 and records pid, millisecond timestamp, and a random ownership token.
Empty or malformed owner contents use the directory modification time;
otherwise staleness uses the recorded timestamp. Wait is capped at the scaled
90-second limit, staleness is scaled from 60 seconds, and polling remains 25 ms
unscaled. Scaled durations have a 24-hour hard ceiling for extreme environment
values. A short advisory file lock serializes stale takeover and release;
refresh callbacks do not hold it, so an expired owner remains reclaimable.

`ChatGPTForRequest` and `GrokForRequest` refresh saved credentials expiring
within 300 seconds, then reread under the lock before calling the refresher.
The 20-second callback context bounds OAuth work. The new credential is saved
before releasing the directory lock. The forced-401 methods reread under lock
and refresh once only when the stored access token still equals the rejected
token; otherwise they return the newer credential without refreshing. ChatGPT
subject/client/host identity and Grok client/endpoint identity must remain
stable. Missing rotated refresh tokens preserve the stored one. The APIs accept
only `vault.Store` saved credential types; `vault.InjectedCredential` is not a
refresh input.

The refresher callback is the provider network boundary and is a fake seam in
these component tests. Production provider routing and the real refresh HTTP
callbacks are not wired in this package commit.

## Commands and results

| Command | Result |
|---|---|
| `GOMAXPROCS=2 GOFLAGS='-mod=vendor -p=2' go test -count=1 -parallel=2 ./internal/auth/refresh` | PASS, final run 0.869 s. Covers parallel forced refresh, expiry refresh reread for Grok, per-provider/label lock isolation, empty/timestamped stale owner recovery, stale takeover ownership, bounded/cancelable waits, corrupt credential refusal, and identity preservation. |
| `GOMAXPROCS=2 GOFLAGS='-mod=vendor -p=2' go vet ./internal/auth/refresh` | PASS. |
| `GIT_CONFIG_GLOBAL=/dev/null make check` | PASS on final source revision: formatting, vendor fingerprints, `go vet ./...`, all tests, and both binaries. |
| `git diff --check` | PASS before both implementation commits. |
| `make build` | PASS on both assigned oracle runs; built `bin/kogen` and `bin/kogen-xspec`. |

The exact assigned command was run twice, first on `73b081f` and then on final
source revision `d68039b`:

```sh
export PATH="$HOME/.local/share/mise/installs/git/2.54.0/bin:$HOME/.local/share/mise/installs/go/1.27.1/bin:$HOME/.local/share/mise/installs/python/3.14.7/bin:$HOME/.local/share/mise/installs/node/24.21.0/bin:$PATH"
make build
SUITE="$HOME/cx/kgo/inputs/conformance-v1.2"
EVIDENCE="$HOME/cx/kgo/evidence/34-refresh-concurrency"
mkdir -p "$EVIDENCE"
PYTHONDONTWRITEBYTECODE=1 PATH="$HOME/.local/share/mise/installs/python/3.14.7/bin:$PATH" \
  "$SUITE/bin/kogen-conformance" run --kogen "$PWD/bin/kogen" \
  --profile cli,state,approval,shape,build,ladder,provider,custody,format,v1.2 --case 'cli-30,provider-10,provider-22' \
  --jobs 2 --time-scale 0.02 --workdir "$EVIDENCE/work" \
  --out "$EVIDENCE/results.jsonl"
```

The package test was initially attempted once with a 1 ms scaled stale age; the
timeout fixture's lock correctly became stale before its assertion and that
attempt failed. The fixture now uses a fresh future owner timestamp for the
bounded-wait assertion and separately checks context cancellation. Final
package tests and `make check` pass; the failed test was not represented as a
pass.

The assigned command was run on source revision `73b081f` and repeated on final
source revision `d68039b`. The first JSONL and work tree were preserved as
`/Users/almirsarajcic/cx/kgo/evidence/34-refresh-concurrency/results-73b081f.jsonl`
and `.../work-73b081f/` before the final run. The final result JSONL is
`/Users/almirsarajcic/cx/kgo/evidence/34-refresh-concurrency/results.jsonl`.
Both runs resolved the literal cases to **3 case IDs / 4 instances**:

| Case / instance | Result | Exact blocker |
|---|---|---|
| `cli-30` (1 instance) | FAIL | Step 2 approve exited 2 instead of 0; stderr: `kogen: implementation bootstrap; command routes are not wired`. |
| `provider-10`, `1:401` | FAIL | Step 1 `provider login chatgpt` exited 2 instead of 0; same bootstrap stderr. |
| `provider-10`, `2:403` | FAIL | Step 1 `provider login chatgpt` exited 2 instead of 0; same bootstrap stderr. |
| `provider-22` (1 instance) | FAIL | Step 1 `provider login chatgpt` exited 2 instead of 0; same bootstrap stderr. |

Each run totals 0 pass, 3 fail, 0 error, 0 skip, 4 instances. The CLI failed
before reaching refresh or the fake provider; the runner reported no request
reached its fake server. There are **no compatible v1.2 black-box passes** to
count. These are unwired behavior gates, not evidence of a v1.2/spec semantic
conflict; `conflicts` is empty in the gate receipt.

The only test effects used temporary HOME directories, local credential files,
fake refresh callbacks, and lock directories. No live provider, account, or
real credential was accessed. The assigned fake OAuth run was serialized with
and released its own `~/cx/kgo/gates.lock/34-refresh-concurrency-provider1455*`
lock.

## Remaining closure gates

- I2/I3 must wire provider/account routes and production refresh HTTP callbacks.
  I3 owns the assigned CLI behavior cases. It must also prove injected auth is
  reread per request and bypasses refresh end to end; this component's type
  boundary alone is not behavior evidence.
- `R(slice)` was not run. It requires a scratch copy of the shared coherent
  migrated v1.3 Quint cohort, spec phase first, then 500 traces × 25 steps for
  each seed `17`, `23`, and `41`, comparing every full observation with the
  same-revision private binary. That cohort/manifest is not frozen here.
- Planned D-* fixtures are not frozen v1.2 cases. Wait for shared frozen v1.3
  IDs; no D-* gate is claimed.
- Linux runtime, race-detector, optional-runtime, and live comparison evidence
  were not produced. `make check` is local Darwin component evidence only.
- No account or network behavior acceptance follows from the worker commit or
  the passing component tests.
