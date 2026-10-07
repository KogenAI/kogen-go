# 28-canonical-wire-session evidence

## Revision and scope

- Worker branch: `kgo/28-canonical-wire-session`; base commit: `e641b4e8b18a4e2d548cd2da88765493706578e9`.
- Target specification: `kogen-spec` commit `e19dd1c21c19c5be1201c3b6a42c59c28b5c2887` (`CHANGES-v1.3.md`). The read-only source worktree had shared session-slice changes in `quint/prototype/harness/xspec.py`, `quint/slices/session/spec/session.qnt`, scenarios `06`/`07`, and their hand goldens. Those files were copied to the scratch replay area; the source worktree and goldens were not edited.
- Frozen conformance input: `$HOME/cx/kgo/inputs/conformance-v1.2`, version `1.2`; runner SHA-256 `0b53a3b60956cbe8dde7af54e274c46b3e8d430c45662d7dff67445f440b39f9`; profile manifest SHA-256 `aef0b5f8787f9d151f3c77aca6ff8edc67ca89a2f6a55ee10fb6326303260273`.
- Toolchain/host: Go `go1.27.1 darwin/arm64`; pinned Git/Python/Node/Go paths were used.
- Final `make build` binaries: `bin/kogen` SHA-256 `104983b316000d63b724ee57b5f1fbe58e90e8f895a896117159edef18d8c383`; `bin/kogen-xspec` SHA-256 `da4928db73e09751b89517dfb9d6426f1754a777e7ccc5d24062d6982a6b6e56`. The private xspec command still reports the bootstrap stub.
- Owned implementation: `internal/provider/session/**`, `internal/provider/wire/**`. Shared `contract`, journal, app, and transport files were not changed.

The session package now creates opaque random run and cache IDs, derives stable thread IDs from run/stage/attempt/rung/epoch, persists protocol identity/history/routing/nullable attempt usage through snapshots, appends raw response/controller/tool items, keeps sticky `x-codex-turn-state`, and omits prior-model encrypted reasoning from later wire copies without rewriting retained raw history.

The wire package freezes one generic prompt and the complete ordered seven-schema union, keeps role-call restrictions in `tool_choice`, encodes owned/injected/Lite controls deterministically with `input` last, separates cache/session/thread IDs, and returns a safe static-prefix digest. Requests are redacted under diagnostic formatting. The process prefix registry refuses changed bytes under a reused provider/prompt/schema version tuple.

## Local verification

Final successful command:

```sh
GIT_CONFIG_GLOBAL=/dev/null make check
```

Result: format, vendored dependency fingerprints, vet, all tests, and both builds passed. `make build` also passed after the final source change. The suite includes 8 session tests and 10 wire tests.

Earlier `make check` attempts are retained in the turn record and summarized here. One stopped on `gofmt required: internal/provider/wire/request_test.go`; the file was formatted. Two later attempts hit unrelated existing timing failures: `internal/queue/lock TestConcurrentAcquireCreatesOneOwner` reported `queue.pid is not a safe regular owner file`, and `internal/gitio TestRunnerSeparatesStreamsAndTimesOutHangingGit` captured empty stdout instead of `stdout-before-hang\n`. The next full run passed, and the final full run after the last source change passed. No other package files were changed to address those flakes.

## Frozen v1.2 command and result

The supplied command was run against the frozen suite. Its output is preserved at `/Users/almirsarajc/cx/kgo/evidence/28-canonical-wire-session/results.jsonl`; SHA-256 is `6b62d4563665a50d849bf060f1c4310d4b5d0528ae68a8f7347b49b6ee6d4a69`.

```sh
make build
SUITE="$HOME/cx/kgo/inputs/conformance-v1.2"
EVIDENCE="$HOME/cx/kgo/evidence/28-canonical-wire-session"
mkdir -p "$EVIDENCE"
PYTHONDONTWRITEBYTECODE=1 PATH="$HOME/.local/share/mise/installs/python/3.14.7/bin:$PATH" \
  "$SUITE/bin/kogen-conformance" run --kogen "$PWD/bin/kogen" \
  --profile cli,state,approval,shape,build,ladder,provider,custody,format,v1.2 --case 'provider-19,v1.2-03-consecutive-request-byte-prefix,v1.2-04-cache-key-session-headers,v1.2-05-missing-usage,v1.2-104-provider-01,v1.2-105-provider-02,v1.2-106-provider-03,v1.2-107-provider-04,v1.2-117-provider-20' \
  --jobs 2 --time-scale 0.02 --workdir "$EVIDENCE/work" \
  --out "$EVIDENCE/results.jsonl"
```

Resolved selection: `provider-19`, `v1.2-03`, `v1.2-04`, `v1.2-05`, `v1.2-104`, `v1.2-105`, `v1.2-106`, `v1.2-107`, and `v1.2-117`; one instance each. Result: provider `0/1`, v1.2 `0/8`, total `0/9` passed; 9 failed, 0 errors, 0 skipped, 0 unimplemented. No provider request reached the fake server.

The cases stopped at the bootstrap CLI before their provider assertions:

- `provider-19`, `v1.2-104`, `v1.2-106`, `v1.2-107`, and `v1.2-117` failed approval with exit 2 and stderr `kogen: implementation bootstrap; command routes are not wired`.
- `v1.2-03`, `v1.2-04`, and `v1.2-05` failed the initial approval hash probe with exit 2 instead of 5 and the same bootstrap stderr.
- `v1.2-105` failed `kogen provider login chatgpt` with exit 2 and the same bootstrap stderr.

No case was counted as a compatible pass. Frozen v1.2 assertions that conflict with the v1.3-draft static prefix are identifiable from the case definitions, but were not reached by this run:

- `v1.2-104-provider-01` expects builder-specific text in top-level `instructions` and exactly 3 builder schemas. Draft §4.9.2 requires generic instructions plus the complete shared schema union before role-specific instructions.
- `v1.2-105-provider-02` expects exactly 3 schemas in the owned `additional_tools` item; the assigned draft target carries all 7 shared schemas.
- `v1.2-106-provider-03` expects exactly 3 schemas for tool-less roles; the assigned draft target keeps the complete 7-schema union and uses `tool_choice: "none"` to disable calls.

The byte-prefix, cache/thread identity, nullable usage, and raw-history assertions in `v1.2-03`, `v1.2-04`, `v1.2-05`, `v1.2-107`, and `v1.2-117` remain unverified at the CLI boundary. Component tests cover request bytes and session behavior only.

## Session Quint replay

Scratch root: `$HOME/cx/kgo/evidence/28-canonical-wire-session/replay-session`. It contains the copied prototype/session cohort and isolated per-seed full-observation corpora under `seeds/{17,23,41}/golden`; each has 7 hand traces and 500 generated traces. The current-revision private binary was `bin/kogen-xspec` with SHA-256 `da4928db73e09751b89517dfb9d6426f1754a777e7ccc5d24062d6982a6b6e56`.

Hand specification command:

```sh
XSPEC_SLICE=../slices/session python3 harness/xspec.py spec
```

Result: 7/7 hand scenarios agreed with the spec.

Each seed used full observations with no `--project` projection:

```sh
XSPEC_SLICE=../slices/session python3 harness/xspec.py gen --traces 500 --steps 25 --seed 17
XSPEC_SLICE=../slices/session python3 harness/xspec.py conform -- "$KGO_BIN/kogen-xspec" session
XSPEC_SLICE=../slices/session python3 harness/xspec.py gen --traces 500 --steps 25 --seed 23
XSPEC_SLICE=../slices/session python3 harness/xspec.py conform -- "$KGO_BIN/kogen-xspec" session
XSPEC_SLICE=../slices/session python3 harness/xspec.py gen --traces 500 --steps 25 --seed 41
XSPEC_SLICE=../slices/session python3 harness/xspec.py conform -- "$KGO_BIN/kogen-xspec" session
```

Generated counts were 12,500 events per seed; invariant-checked accepted/refused counts were 9,548/2,952 (17), 9,653/2,847 (23), and 9,604/2,896 (41). Each private-binary conform run compared all 507 traces and recorded `0/507`, diverging at reset with `adapter gave no answer ... kogen-xspec: implementation bootstrap; command routes are not wired`; 0 events were applied. Per-seed full-observation corpora and final logs are retained in `replay-session/seeds/<seed>/`; the original conform logs are retained in `replay-session/quint/session-seed-<seed>-conform.log`.

## Closure gaps

- This is component-ready, not behavior-accepted. I2 provider-byte integration, I3 public CLI/provider execution, and I5 independent Shape/Build/Shape-to-Build wiring remain open.
- The explicit GPT-6/GPT-5.6 cache-breakpoint marker required by draft §4.9.2 is not emitted. Its adapter wire placement is not established in the current Go transport or Rust reference; freeze that encoding before claiming the cache-breakpoint portion.
- D-CACHE-04 live feasible replay and its request-level ≥95% telemetry gate were not run; no live provider call or account access was used. D-CACHE-05/06 are planned names, not frozen v1.2 cases; wait for the shared v1.3 IDs before claiming those gates.
- `R(session)` generation and hand spec pass, but conformance remains blocked by the unwired private xspec adapter. It is not a release pass.
- Linux execution and the frozen live cache replay need their external environments/evidence. This worker ran on macOS arm64.
- P04 finish behavior, P19 expired-injected-JWT CLI behavior, aggregate status cache-hit reporting, and the remaining provider/transport integration are outside the owned component boundary and remain with I2/I3.

Worker commit does not grant behavior acceptance. Dispatcher/I2/I3/I5 must rerun their gates on the integrated revision and retain the existing failures above.
