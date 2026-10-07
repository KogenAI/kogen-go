# 30 shell/tool budget evidence

## Revisions and inputs

- Worker implementation commit: `122ed932025a87764732c2581ee62223b01ce980` on `kgo/30-shell-tool-budget`.
- CLI build revision: `122ed932025a87764732c2581ee62223b01ce980`. `cmd/kogen` remains the bootstrap handler and reports `kogen: implementation bootstrap; command routes are not wired`.
- Provider adapter revision: none. No production command route composes `internal/provider/tools.ToolContext.Dispatch` with the provider/Build session.
- Target: `kogen-spec` v1.3-draft `e19dd1c21c19c5be1201c3b6a42c59c28b5c2887`. Read `CHANGES-v1.3.md`, `spec/03-build.md` §3.6, `spec/04-provider.md` §§4.7 and 4.9.3, `spec/05-sandbox-custody.md` §§5.1.5 and 5.5.8, and `spec/CONFORMANCE-v1.2-CASES.md` P4–P5. The draft changes in `CHANGES-v1.3.md` do not add a shell/result-budget behavior delta for this package.
- Rust reference: clean `kogen-rs` `a402540b39cedc7f788472297add7ae2f8a6631a`; inspected `run/script.rs`, `provider/tools.rs`, `provider/tools/operations.rs`, `provider/tools/budget.rs`, and `run/orchestration/finish.rs`.
- Frozen oracle: `~/cx/kgo/inputs/conformance-v1.2`, source `0f93bad988fb8d7a8eff4e94954d1db0a046c89d`; runner reports `v1.2+unknown`.
- Host: macOS 26.7.1 arm64; Go 1.27.1; Git 2.54.0; Python 3.14.7.

## Component changes and local effects

- Added `ToolContext.Dispatch` for the builder shell tools, finish guard, tool output retrieval, and the existing direct tools. Successful non-finish results pass through the same byte budget; zero selects the 2,000-token default.
- Shell commands are published as mode-0600 scripts through `contract.RootedFS` and passed only as the `sh` script-path argument. The supervised process uses the workspace, prepared child environment, null-equivalent EOF stdin, a 120-second scaled timeout, merged output, a 64 MiB log bound, and the 16 KiB captured tail fallback. Script and transient raw process-log files are removed after use.
- Added redaction before result exposure/hash, content-addressed SHA-256 result storage, byte-based head/tail truncation with UTF-8 boundaries, whole-payload base64 conversion for non-UTF-8 output, and `tool_output` range reads. Handle reads refuse symlinks and non-private/non-regular files, verify the opened inode and digest, and require already-redacted stored text.
- Added strict standalone `{}` finish validation and the two-empty-finish policy. The canonical seven-schema union already defines `finish` as strict with no arguments; tests assert that existing schema.
- Unit fixtures used temporary workspace/run directories and a fake process port, with one supervised local shell command to check merged stdout/stderr. No live provider/account, user checkout, origin, global configuration, suite, or golden was changed. `make build` wrote the ignored local binaries; conformance output/workdirs were written only beneath `/Users/almirsarajcic/cx/kgo/evidence/30-shell-tool-budget`.

## Commands and results

All commands used the pinned worker PATH from `WORKER-RULES.md`.

| Command | Result |
|---|---|
| `GOMAXPROCS=2 GOFLAGS='-mod=vendor -p=2' GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off go test -count=1 -parallel=2 ./internal/provider/tools` | Passed on the final implementation. |
| `GIT_CONFIG_GLOBAL=/dev/null GOMAXPROCS=2 make check` | Passed: format, vendor fingerprints, vet, all tests with `-parallel=2`, and both builds. |
| `git diff --check` | Passed before the implementation commit. |
| `make build` | Passed on implementation revision `122ed93`. |
| Assigned frozen v1.2 overlay command below | Runner exit 1: 4 case IDs / 4 instances; 0 pass, 4 fail, 0 error, 0 skip, 0 unimplemented. All stopped at approval before any assigned tool behavior. |

The exact acceptance command ran once at `2026-10-07T20:22:51Z`. Its complete result and workdirs are retained at `/Users/almirsarajcic/cx/kgo/evidence/30-shell-tool-budget/results.jsonl` and `/Users/almirsarajcic/cx/kgo/evidence/30-shell-tool-budget/work`. No retry was made.

```sh
make build
SUITE="$HOME/cx/kgo/inputs/conformance-v1.2"
EVIDENCE="$HOME/cx/kgo/evidence/30-shell-tool-budget"
mkdir -p "$EVIDENCE"
PYTHONDONTWRITEBYTECODE=1 PATH="$HOME/.local/share/mise/installs/python/3.14.7/bin:$PATH" \
  "$SUITE/bin/kogen-conformance" run --kogen "$PWD/bin/kogen" \
  --profile cli,state,approval,shape,build,ladder,provider,custody,format,v1.2 --case 'v1.2-118-provider-25,v1.2-121-custody-08,v1.2-31-provider-23-tool-result-budget,v1.2-43-build-08' \
  --jobs 2 --time-scale 0.02 --workdir "$EVIDENCE/work" \
  --out "$EVIDENCE/results.jsonl"
```

Resolved IDs and outcomes:

| Effective ID | Instances | Result and first blocker |
|---|---:|---|
| `v1.2-118-provider-25` | 1 | Failed at step 2 (`approve`): exit 2, expected 0; stdout empty; stderr was `kogen: implementation bootstrap; command routes are not wired`. No provider request reached the fake server. |
| `v1.2-121-custody-08` | 1 | Same step-2 approval failure. The 300 KiB script/argv assertion did not run. |
| `v1.2-31-provider-23-tool-result-budget` | 1 | Same step-2 approval failure. Budget, redaction, range, and base64 assertions did not run. |
| `v1.2-43-build-08` | 1 | Same step-2 approval failure. The empty-finish guard assertion did not run. |

There are no compatible black-box passes. The four failures are integration reachability failures, not results for the shell/tool behaviors.

## Historical assertions and conflicts

The frozen overlay selects versioned replacements for the historical `provider-23`, `provider-25`, `custody-08`, and `build-08` IDs. The assigned run did not reach their assertions. The exact known stale assertions are:

- `provider-23-shell-tool-results` expects a 15,030-character shell result to expose exactly 10,000 characters and retain `END-OF-OUTPUT`; it also expects marker `[non-UTF-8 output tail, base64 encoded]` before `//5hYmM=`. Current §4.9.3 uses the configured 4-bytes-per-token cap with a range notice and marker `[non-UTF-8 output, base64 encoded]` for the full payload. The overlay replacement is `v1.2-31-provider-23-tool-result-budget`.
- `build-08-empty-done-refused` treats a text-only `Done.` reply as an empty completion claim. Current §3.6 says text is progress and only `finish` alone with `{}` requests completion. The overlay replacement is `v1.2-43-build-08`.

These are historical case/spec conflicts preserved by the frozen overlay; this run did not observe either behavior because the CLI stopped at approval. No new v1.3-draft semantic conflict was observed. `provider-25` and `custody-08` have no additional conflict identified here; their overlay cases were also blocked before the tool assertions.

## Closure gates and gaps

- **Component gate only.** Unit tests and `make check` pass, but no behavior acceptance is claimed. I3 must wire approval → queue → provider → Build tool dispatch → gate and rerun the four selected cases on the changed integration revision while retaining this failed result.
- The public command route and production provider adapter remain unwired. This worker did not change shared contracts, app composition, CLI entrypoints, Makefile, modules, or another owned package.
- The coherent migrated v1.3 Quint cohort and shared frozen v1.3 IDs are unavailable. No D-* fixture or release pass is claimed; no `R(slice)` replay or 500 ×25 traces for seeds 17, 23, and 41 were run, and no replay divergence is claimed.
- Linux runtime parity was not run. No optional-runtime or live-comparison evidence was produced. Package 00 remains a real foundation task.
