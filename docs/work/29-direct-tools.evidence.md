# 29-direct-tools evidence

## Revisions and environment

- Worker implementation commit: `aa4299b2de8fd77fd1d7a348e3e97ec590f17c3d` on `kgo/29-direct-tools`; base revision was `ff08b7108221fe398acfe1fd57a4c3c03d6b0ca4`.
- CLI source revision during the frozen conformance run: `ff08b7108221fe398acfe1fd57a4c3c03d6b0ca4`. The command route remains `app.Bootstrap`; `cmd/kogen` prints `kogen: implementation bootstrap; command routes are not wired`.
- Provider/Shape adapter revision: none. No route composes `internal/provider/tools` with the public Shape command or provider session; no selected case reached a tool call.
- Target spec: `kogen-spec` `e19dd1c21c19c5be1201c3b6a42c59c28b5c2887` (v1.3-draft), including `spec/04-provider.md` §4.7, `spec/03-build.md` §§3.2.1 and 3.2, and `CHANGES-v1.3.md`.
- Rust reference: `kogen-rs` `a402540b39cedc7f788472297add7ae2f8a6631a`; inspected `provider/tools.rs`, `provider/tools/operations.rs`, `provider/tools/path.rs`, `provider/tools/tests.rs`, and the Shape dispatch in `intent/shaping/provider.rs`.
- Frozen suite: v1.2 source revision `0f93bad988fb8d7a8eff4e94954d1db0a046c89d` per the frozen-input ledger; supplied runner metadata reports `kogen-conformance v1.2+unknown`.
- Host: macOS 26.7.1 / Darwin 25.6 arm64; Git 2.54.0; Go 1.27.1 darwin/arm64; Python 3.14.7.

## Component implementation

`ToolContext` exposes `read`, `search`, `write`, and `edit` through the rooted filesystem and supervised process ports. Builder-direct and Shape allowlists match §4.7. Argument parsing applies read defaults (offset 1, limit 200), validates the 1–400 limit and positive offset, supplies search path `.`, ignores additional fields, and renders the contract's exact invalid-argument and invalid-limit text.

Reads follow contained symlinks through `contract.RootedFS`, reject escapes, invalid UTF-8 and NUL-containing files, and format numbered lines with the exact continuation marker. Writes publish complete content with `safefs` atomic replace; writes preserve an existing regular file's permission bits. A symlink leaf is checked for an in-root target and replaced as a leaf, leaving its target unchanged. Shape writes are restricted to the two configured relative paths and to 200,000 bytes; oversized input returns `ERROR: Write content exceeds 200000 bytes.` Edits replace the first nonempty exact match and publish the full result.

Search validates the requested rooted path, runs `rg` through `contract.ProcessRunner`, and falls back to `grep` when ripgrep is unavailable or its supervised call fails. It has no 200-line cap and returns `No matches.` for an empty result. Each call writes output to a unique file under the run's private rooted `logs` directory, reads the full captured output, then removes the log. Output budgeting remains the caller's responsibility; file writes are never shortened.

Local tests cover allowlists, argument types/defaults, exact read results/errors, outside and contained symlinks, Shape scope and size, full writes, atomic symlink-leaf replacement, edit behavior/mode preservation, no-match search, fallback argv, 350 result lines, and search-log cleanup. Fixtures use temporary workspaces and run directories only.

## Commands and results

Commands used the pinned worker PATH from `WORKER-RULES.md`.

| Command | Result |
|---|---|
| `GIT_CONFIG_GLOBAL=/dev/null GOMAXPROCS=2 GOFLAGS='-mod=vendor -p=2' GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off go test -count=1 -parallel=2 ./internal/provider/tools` | Passed during local development. The final full check below reran the package after the last source change. |
| `GIT_CONFIG_GLOBAL=/dev/null GOMAXPROCS=2 make check` | Passed on the final implementation: format, vendor fingerprints, `go vet ./...`, tests with `-parallel=2`, and both builds. |
| Assigned frozen v1.2 command below | Runner exit 1: 4 cases / 13 instances, 0 passed, 4 failed, 0 errors, 0 skipped, 0 unimplemented. Every failure stopped at the bootstrap CLI before a provider request or direct tool invocation. |
| `git diff --cached --check` | Passed before each worker commit. |

The exact acceptance command ran once at `2026-10-07T19:57:28Z`. Its full JSONL and generated workdirs remain at `/Users/almirsarajcic/cx/kgo/evidence/29-direct-tools/results.jsonl` and `/Users/almirsarajcic/cx/kgo/evidence/29-direct-tools/work`. The command built `bin/kogen` from the unchanged bootstrap route; the direct-tools package was not imported by that command route. No retry was made.

```sh
make build
SUITE="$HOME/cx/kgo/inputs/conformance-v1.2"
EVIDENCE="$HOME/cx/kgo/evidence/29-direct-tools"
mkdir -p "$EVIDENCE"
PYTHONDONTWRITEBYTECODE=1 PATH="$HOME/.local/share/mise/installs/python/3.14.7/bin:$PATH" \
  "$SUITE/bin/kogen-conformance" run --kogen "$PWD/bin/kogen" \
  --profile cli,state,approval,shape,build,ladder,provider,custody,format,v1.2 --case 'provider-24,provider-26,shape-09,v1.2-118-provider-25' \
  --jobs 2 --time-scale 0.02 --workdir "$EVIDENCE/work" \
  --out "$EVIDENCE/results.jsonl"
```

Resolved IDs and actual results:

| Effective ID | Instances | Result and first blocker |
|---|---:|---|
| `provider-24` | 10 | All failed at `kogen intent shape` (step 3): exit 2, expected 0; stderr was `kogen: implementation bootstrap; command routes are not wired`. No fake-provider request reached the server. |
| `provider-26` | 1 | Failed at `kogen intent shape` (step 6): exit 2, expected 0; same bootstrap diagnostic. The external-symlink tool call was not reached. |
| `shape-09` | 1 | Failed at `kogen intent shape` (step 2): exit 2, expected 0; same bootstrap diagnostic. The out-of-scope write was not reached. |
| `v1.2-118-provider-25` | 1 | Failed at `approve` (step 2): exit 2, expected 0; same bootstrap diagnostic. The provider allowlist call was not reached. |

The complete runner total was 4 selected cases, 13 instances, 0 pass, 4 fail, 0 error, 0 skip, and 0 unimplemented. These are integration reachability failures, not direct-tool behavior results. No behavior-compatible case is counted as passing.

## Effects, conflicts, and closure gates

- Implementation effects were limited to the four assigned Go files. Unit fixtures used temporary directories. `make build` wrote ignored `bin/kogen` and `bin/kogen-xspec`; the acceptance runner wrote only beneath its documented external evidence directory. No source oracle, suite, golden, global configuration, other worktree, live provider, or account was changed or accessed.
- Exact historical v1.2 versus v1.3-draft conflicts observed: none. The run stopped before the behaviors and therefore does not establish semantic compatibility.
- This commit is **component-ready only**. I3 must wire the public Shape/provider route and rerun the retained cases. I4 must supply direct-recipe coverage. I5 must verify public Shape, tool effects, and the related end-to-end session/cache path. The selected failures must remain in the evidence record.
- Planned D-SHAPE-* fixtures are not available v1.2 cases; wait for literal IDs in a shared frozen v1.3 suite. `R(slice)` was not run because the coherent migrated cohort is unavailable; no seeds or divergence are claimed. Linux runtime, optional runtime, and live-comparison gates also remain unverified.
- No draft behavior is accepted by this worker commit. Package 00 and the named integration closure rounds remain required.
