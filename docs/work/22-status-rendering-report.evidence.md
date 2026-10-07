# 22-status-rendering-report evidence

## Revisions and scope

- Worker branch: `kgo/22-status-rendering-report`.
- Base revision: `f1079d5e61f858bbbf19b64ea63216bcdee2f535`.
- Implementation revision: `04cc668ef6d4a3c058dc44fc436e36f8fde7840c` (`Implement status rendering and reporting`).
- Target spec: `kogen-spec` v1.3-draft commit `e19dd1c21c19c5be1201c3b6a42c59c28b5c2887`, tree `6dee6ffde869979640da757cad203ace84c86f8e`; relevant clauses are `spec/01-cli.md` §1.7.5, `spec/02-formats.md` §§2.10–2.11, `spec/04-provider.md` §4.9.5, and `CHANGES-v1.3.md` §§1, 6.
- Rust reference: clean source revision `a402540b39cedc7f788472297add7ae2f8a6631a`; inspected `crates/kogen-cli/src/handlers/status/{render.rs,report.rs,render/detail.rs}` and `crates/kogen-core/src/status/{model.rs,project.rs,agents.rs}`.
- Toolchain: Go `go1.27.1 darwin/arm64`; Git `2.54.0`.
- CLI binary SHA-256: `dd855a8e7d5648d1a3ede2775bbd952368a2770ba4a8673625d72eb2f22fa51b`.
- `kogen-xspec` binary SHA-256: `3cbd3f3faedb70096364074faa91ec3f89542639f77e178ed8cd38135077acdd`. The status adapter in `internal/xspec/queuestatus` is not implemented; this is the bootstrap binary and is not replay evidence.
- Frozen oracle: `$HOME/cx/kgo/inputs/conformance-v1.2`, `VERSION=1.2`, source revision `0f93bad988fb8d7a8eff4e94954d1db0a046c89d`; runner SHA-256 `0b53a3b60956cbe8dde7af54e274c46b3e8d430c45662d7dff67445f440b39f9`.

Implementation is limited to `internal/status/render/**` and `internal/status/report/**`. Rendering and report assembly are pure functions over `derive.Board` and journal observations. They cover the overview sections and landed window, slug details, deterministic per-Intent JSONL and agent rows, Build report assembly, watch frame change detection/separators, and watch idle/exit decisions. Missing token usage stays null; the report's default land policy is the draft value `green`.

## Commands and results

`GIT_CONFIG_GLOBAL=/dev/null GOMAXPROCS=2 make check` — **passed** after the final source and test changes. This includes format checks, vendor fingerprints, vet, all package tests, and both builds.

`GOMAXPROCS=2 go test -count=1 -parallel=2 ./internal/status/render ./internal/status/report` — **passed**.

`GOMAXPROCS=2 go test -run='^$' -bench='BenchmarkStatusRender50Intents200Runs' -benchtime=100x -count=1 ./internal/status/render` — **passed**, 100 iterations, `250837 ns/op` on Apple M1 Max. This measures overview plus JSONL rendering over 50 Intent rows with 200 run records; it is component timing, not the full `status` command S30 measurement.

`git diff --check` and the staged diff check — **passed**.

The assigned frozen v1.2 command was run once against the binary built from the implementation tree later committed as `04cc668ef6d4a3c058dc44fc436e36f8fde7840c`. Its complete selected case set resolved to **10 IDs / 10 instances**:

- `cli-28`
- `state-24`
- `state-30`
- `v1.2-129-cli-27`
- `v1.2-131-state-12`
- `v1.2-137-format-12`
- `v1.2-22-cli-25-status-overview`
- `v1.2-23-cli-26-status-slug`
- `v1.2-92-ladder-24`
- `v1.2-93-ladder-25`

Result: **0 passed, 10 failed, 0 errors, 0 skipped** (`cli`: 1 fail; `state`: 2 fails; `v1.2`: 7 fails). Every selected instance stops before reaching this renderer/report component: approval-dependent cases hit `kogen: implementation bootstrap; command routes are not wired`, and `state-24` / `state-30` hit the same message at `kogen status`. No black-box case is counted as a compatible pass.

The retained full JSONL result is `/Users/almirsarajcic/cx/kgo/evidence/22-status-rendering-report/results.jsonl`.
Its SHA-256 is `551d1f18d3e689277779f93275fdea8ccca7a7f6bfaebbae2334b9e9f3be83ab`.

Exact command:

```sh
make build
SUITE="$HOME/cx/kgo/inputs/conformance-v1.2"
EVIDENCE="$HOME/cx/kgo/evidence/22-status-rendering-report"
mkdir -p "$EVIDENCE"
PYTHONDONTWRITEBYTECODE=1 PATH="$HOME/.local/share/mise/installs/python/3.14.7/bin:$PATH" \
  "$SUITE/bin/kogen-conformance" run --kogen "$PWD/bin/kogen" \
  --profile cli,state,approval,shape,build,ladder,provider,custody,format,v1.2 --case 'cli-28,state-24,state-30,v1.2-129-cli-27,v1.2-131-state-12,v1.2-137-format-12,v1.2-22-cli-25-status-overview,v1.2-23-cli-26-status-slug,v1.2-92-ladder-24,v1.2-93-ladder-25' \
  --jobs 2 --time-scale 0.02 --workdir "$EVIDENCE/work" \
  --out "$EVIDENCE/results.jsonl"
```

## Historical assertions and draft conflicts

- `v1.2-131-state-12` expects the default Build report field `"land_policy": "green-or-advisory"`. The target draft changes the default to `green` in `CHANGES-v1.3.md` §1. The report preserves an explicitly recorded `started.land` value, but defaults to `green` when it is absent. The assertion was not dynamically reached because the public route is unwired.
- The superseded pre-overlay `ladder-24` assertion requires an `audit[].citation` field. The target §2.10 report row is `rung`, `id`, `verdict`, and `reason`; effective replacement `v1.2-92-ladder-24` expects the citation-free shape. The old case was not selected by the frozen overlay.
- The superseded pre-overlay `ladder-25` assertion requires a `verdict: unverified` text line. Target §1.7.5 forbids a `verdict:` line in slug detail, and effective replacement `v1.2-93-ladder-25` explicitly requires its absence. The old case was not selected by the frozen overlay.

These are exact historical differences, not passes or dynamically observed failures in the assigned run. No legacy report or verdict behavior was added.

## Local effects and deferred closure

- Added the public status rendering and Build report component APIs and focused tests under the owned directories only.
- No spec, conformance suite, golden, external replay harness, shared contract, CLI route, Makefile, or module file was changed. No live provider/account call was made.
- I1 must wire static status output; I3 must wire and verify watch frames, separators, termination, and the full CLI timing target; I4 must wire the rung report path.
- Package 66 must implement the production-backed `R(status)` adapter before status replay. No Quint replay ran: there is no confirmed coherent frozen migrated v1.3 cohort, and the status adapter is absent. Seeds 17, 23, and 41 at 500 traces × 25 steps each remain open.
- The shared frozen v1.3 case IDs and migrated status model/goldens are unavailable. Status schema findings listed as open by the draft still need coordinator resolution before release claims.
- Linux execution was not available in this package round. The component has no OS-specific behavior; this does not establish Linux integration evidence.
- Behaviour acceptance remains open. This commit is component-ready only.
