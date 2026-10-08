# 68-stream-session-replay evidence

## Revision and scope

- Worker branch: `kgo/68-stream-session-replay`; base revision: `a2f70ff78bf5d4156161423d4c932606790d82fb`; implementation commit: `bc0c74e`.
- Target spec: `kogen-spec` `e19dd1c21c19c5be1201c3b6a42c59c28b5c2887`; Rust reference: `kogen-rs` `a402540b39cedc7f788472297add7ae2f8a6631a`.
- The spec checkout has uncommitted Quint/harness changes, including the migrated `stream` and `session` slices. The source checkout was read only. Replay used a scratch copy at `$HOME/cx/kgo/evidence/68-stream-session-replay/replay/draft-e19dd1c-snapshot`; this is diagnostic input, not a frozen shared v1.3 cohort.
- Host/toolchain: macOS 26.7.1 arm64, Go `go1.27.1 darwin/arm64`; pinned Git/Go/Python/Node paths were first on `PATH`.
- Frozen suite: `$HOME/cx/kgo/inputs/conformance-v1.2`; runner SHA-256 `0b53a3b60956cbe8dde7af54e274c46b3e8d430c45662d7dff67445f440b39f9`; `profiles/v1.2.json` SHA-256 `aef0b5f8787f9d151f3c77aca6ff8edc67ca89a2f6a55ee10fb6326303260273`.
- Built CLI SHA-256: `45357a7d5a1a56f751935de68d9b84b08ee6203471d8538edf84dc658f234616`; private `kogen-xspec` SHA-256: `e53d6860ec5d7dc2d9b8be3a3dc59409fda29cf89d48145f8f3eb986c3783fcd`.
- CLI routes are still incomplete at this revision. Provider operations return `controller/internal_error: provider commands are wired in the provider integration round`; Build queue execution returns `controller/internal_error: queue execution is wired in the Build integration round`. `cmd/kogen-xspec` still calls `app.Bootstrap`, so the package factories are not reachable from the private binary.

The new `streamsession` factories inject deterministic clock, jitter, and provider-result effects into `internal/provider/retry.Run`. They map its actual events and failures to the full Quint observation; retry decisions are not reimplemented in the adapter. The session factory calls `internal/provider/session` identity and conversation methods, `internal/optional/checkpoint` for summarizer/continuation epochs, and `internal/provider/wire.PrefixRegistry` for static-prefix effects. Wire tests independently inspect encoded request bytes, headers, retained history, routing state, and model-switch copies.

## Component checks

| Command | Result |
|---|---|
| `GOMAXPROCS=2 go test -count=1 -p=2 -parallel=2 ./internal/xspec/streamsession` | PASS. Covers production overload switch, refresh capability and Build wait, Grok/no-fallback and wall budget, checkpoint effects, stable conversation/thread transitions, shared affinity and prefix refusal, raw body prefix and identical retry bytes, stateless controls, sticky turn-state, nil-usage response input, and model-switch history. |
| `GIT_CONFIG_GLOBAL=/dev/null GOMAXPROCS=2 make check` | PASS. Format, vendor fingerprints, vet, all tests, and both builds completed. Retained log: `$HOME/cx/kgo/evidence/68-stream-session-replay/make-check.log`. |
| `make build` | PASS. Retained output: `$HOME/cx/kgo/evidence/68-stream-session-replay/acceptance-build.log`. |
| `git diff --check` | PASS before implementation commit. |

The provider attempt callback, fake clock, and ceiling jitter are local; no account, live provider, credential store, or network effect was used. The suite run and full check were serialized under `$HOME/cx/kgo/gates.lock/oauth-port1455`; this worker released its own lock.

## Frozen v1.2 acceptance command

The assigned command ran once against the frozen suite. Resolved selection: **6 IDs / 7 instances**.

| Literal ID | Instances | Result and exact stop |
|---|---:|---|
| `provider-10` | 2 (`1:401`, `2:403`) | 0/2. `kogen provider login chatgpt` exited 70; stdout was `controller/internal_error: provider commands are wired in the provider integration round`. No fake-provider request reached the server. |
| `provider-22` | 1 | 0/1. Same provider-command integration stop at `kogen provider login chatgpt`; refresh concurrency was not reached. |
| `v1.2-03-consecutive-request-byte-prefix` | 1 | 0/1. Failed at `kogen intent approve greet`: exit 3 instead of 5, `environment/approval_check_failed: could not run or load the base check baseline`. No provider request reached the server. |
| `v1.2-04-cache-key-session-headers` | 1 | 0/1. Same approval base-check stop; session headers were not observed. |
| `v1.2-05-missing-usage` | 1 | 0/1. Same approval base-check stop; usage was not observed. |
| `v1.2-28-provider-13-planner-no-fallback` | 1 | 0/1. Failed at `kogen queue start`: exit 70 instead of 0, `controller/internal_error: queue execution is wired in the Build integration round`. No planner request reached the fake server. |

Total: **0/7 passed, 7 failed, 0 errors, 0 skipped, 0 unimplemented**. Runner output and full JSONL are retained at `$HOME/cx/kgo/evidence/68-stream-session-replay/conformance.log` and `$HOME/cx/kgo/evidence/68-stream-session-replay/results.jsonl` (SHA-256 `09d5202923ffc36aa3e45e5d783c7ad1c274a5f614c7ad1fce432a7cbdb359b8`). These are integration stops before the assigned provider assertions, not behavior results. **Compatible black-box passes: none. Exact historical behavior conflicts established: none.**

Exact command:

```sh
make build
SUITE="$HOME/cx/kgo/inputs/conformance-v1.2"
EVIDENCE="$HOME/cx/kgo/evidence/68-stream-session-replay"
mkdir -p "$EVIDENCE"
PYTHONDONTWRITEBYTECODE=1 PATH="$HOME/.local/share/mise/installs/python/3.14.7/bin:$PATH" \
  "$SUITE/bin/kogen-conformance" run --kogen "$PWD/bin/kogen" \
  --profile cli,state,approval,shape,build,ladder,provider,custody,format,v1.2 --case 'provider-10,provider-22,v1.2-03-consecutive-request-byte-prefix,v1.2-04-cache-key-session-headers,v1.2-05-missing-usage,v1.2-28-provider-13-planner-no-fallback' \
  --jobs 2 --time-scale 0.02 --workdir "$EVIDENCE/work" \
  --out "$EVIDENCE/results.jsonl"
```

## Quint hand-scenario and generated replay

Commands ran only in the scratch copy. `spec` passed **11/11 stream** and **7/7 session** hand scenarios. Each generation passed its model invariants and emitted 500 traces × 25 steps:

| Slice | Seed | Accepted / refused events | Conform result |
|---|---:|---:|---|
| stream | 17 | 10,963 / 1,537 | 0/511 traces, 0 steps; reset received no observation from the bootstrap binary |
| stream | 23 | 10,960 / 1,540 | 0/511 traces, 0 steps; same reset failure |
| stream | 41 | 10,907 / 1,593 | 0/511 traces, 0 steps; same reset failure |
| session | 17 | 9,548 / 2,952 | 0/507 traces, 0 steps; reset received no observation from the bootstrap binary |
| session | 23 | 9,653 / 2,847 | 0/507 traces, 0 steps; same reset failure |
| session | 41 | 9,604 / 2,896 | 0/507 traces, 0 steps; same reset failure |

Every conform log reports `adapter gave no answer to {"op": "reset"} kogen-xspec: implementation bootstrap; command routes are not wired`. No full observations were compared, so these runs do not establish an R(slice) pass or a model divergence. Per-command logs and generated corpora remain under `$HOME/cx/kgo/evidence/68-stream-session-replay/replay/draft-e19dd1c-snapshot`.

Commands used, in order for each slice, are the assigned command sequence:

```sh
XSPEC_SLICE=../slices/stream python3 harness/xspec.py spec
XSPEC_SLICE=../slices/stream python3 harness/xspec.py gen --traces 500 --steps 25 --seed 17
XSPEC_SLICE=../slices/stream python3 harness/xspec.py conform -- "$KGO_BIN/kogen-xspec" stream
XSPEC_SLICE=../slices/stream python3 harness/xspec.py gen --traces 500 --steps 25 --seed 23
XSPEC_SLICE=../slices/stream python3 harness/xspec.py conform -- "$KGO_BIN/kogen-xspec" stream
XSPEC_SLICE=../slices/stream python3 harness/xspec.py gen --traces 500 --steps 25 --seed 41
XSPEC_SLICE=../slices/stream python3 harness/xspec.py conform -- "$KGO_BIN/kogen-xspec" stream
XSPEC_SLICE=../slices/session python3 harness/xspec.py spec
XSPEC_SLICE=../slices/session python3 harness/xspec.py gen --traces 500 --steps 25 --seed 17
XSPEC_SLICE=../slices/session python3 harness/xspec.py conform -- "$KGO_BIN/kogen-xspec" session
XSPEC_SLICE=../slices/session python3 harness/xspec.py gen --traces 500 --steps 25 --seed 23
XSPEC_SLICE=../slices/session python3 harness/xspec.py conform -- "$KGO_BIN/kogen-xspec" session
XSPEC_SLICE=../slices/session python3 harness/xspec.py gen --traces 500 --steps 25 --seed 41
XSPEC_SLICE=../slices/session python3 harness/xspec.py conform -- "$KGO_BIN/kogen-xspec" session
```

The available stream/session slice files and prototype harness were dirty at the spec checkout, and no coherent shared v1.3 manifest was available. The scratch results are diagnostic. The source spec checkout, hand goldens, and replay harness were not changed.

One local production-backed effect exposes an exact draft mismatch in stream scenario `11-auth-capability-and-provider-fallback`: with `refreshable=false`, `retry.Run` returns a Build `provider_wait` (`phase=idle`, `decision=pause`, `waited=300000`, `exit=0`, `refreshed=false`); that scenario expects a terminal provider/login stop (`phase=stopped`, `decision=stop`, `exit=4`). The adapter preserves the production result. This is a draft-model/production mismatch for the retry owner to resolve; it is not a frozen v1.2 case result and is not entered as a historical conflict. No other model divergence was observed because the private adapter stopped before reset.

## Deferred closure gates

- I7 must register `StreamFactory` and `SessionFactory` in the private xspec executable, use full observations, and rerun the coherent shared v1.3 stream/session cohort against a same-revision CLI/private binary. That integration round owns the command registration; this package does not edit shared entrypoints.
- I2/I3 provider and Build routes must be wired before the listed CLI cases can reach their fake-provider assertions; v1.2-03/04/05 also need the upstream approval base-check path to pass before provider observations.
- The available Quint hand/golden changes are not a frozen shared v1.3 cohort. Do not claim R(slice) behavior acceptance from the scratch model generation or from the 0-step conform failures.
- Planned D-* cache/cross-session fixtures are not frozen v1.2 cases. Wait for shared frozen v1.3 IDs; no D-* gate is claimed.
- Linux parity, optional runtime checks, custody stress/race gates, and live feasible cache comparison require their stated external evidence. No live provider/account call was made.

This worker receipt is **component** only. It does not confer CLI behavior acceptance, R(stream), R(session), P13, I7, or I8 acceptance.
