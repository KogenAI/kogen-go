# 40 Moved-Base Integration Evidence

Recorded 8 October 2026 in the assigned worktree
`/Users/almirsarajcic/Areas/Kogen/kogen-go-wt/40-moved-base-integration`, branch
`kgo/40-moved-base-integration`.

## Revisions and scope

- Source base revision: `94fadcca67b794fc0530e0dd6787d3b699306e61`. The implementation
  and local integration tests were run from this revision with a modified worktree.
  Implementation commit: `fb6d0bdf7b1dd1db2a5349985e770c8d50af4c66`.
- Target spec: `kogen-spec` `e19dd1c21c19c5be1201c3b6a42c59c28b5c2887`
  (`v1.3-draft`), `spec/03-build.md` §§3.4, 3.7, 3.9.2–3.10, and
  `CHANGES-v1.3.md` §§3, 6.
- Rust reference: `kogen-rs` `a402540b39cedc7f788472297add7ae2f8a6631a`;
  inspected `git/landing/engine.rs`, `rebase.rs`, `repository.rs`, and the
  moved-tip integration fixtures.
- Frozen oracle: `$HOME/cx/kgo/inputs/conformance-v1.2`; runner source revision
  `0f93bad988fb8d7a8eff4e94954d1db0a046c89d`, installed runner reports
  `v1.2+unknown`, SHA-256
  `0b53a3b60956cbe8dde7af54e274c46b3e8d430c45662d7dff67445f440b39f9`.
- Toolchain: Go `1.27.1`, Git `2.54.0`, Darwin `25.6.0` arm64. The CLI and
  adapter were built from source revision `94fadcca67b794fc0530e0dd6787d3b699306e61`
  with `vcs.modified=true`. `bin/kogen` SHA-256 is
  `29b6f87c67fc9986d347d001e2392e4ddc55dfa832040055ae3aa10f4e471fd5`;
  `bin/kogen-xspec` SHA-256 is
  `ca589300cbfe4f7bc3e7c7cb9776dcb9e047c8cb8366106c58cf23eecf943c88`.
- Scope is `internal/landing/integrate/**`, its historical `.cases` list, and
  this package's evidence and gate receipt. No shared contract, command route,
  Makefile, spec, suite, golden, or external replay harness was changed.

## Implemented boundary and local effects

`Refresh` fetches the current target tip into a private ref before Build work,
records `base_moved_at_start`, runs the configured complete check set once on a
moved tree through the injected baseline runner, and merges the resulting exact
tree-bound baseline into persisted history. The check set is validated for
missing, duplicate, unknown, and invalid-status rows.

`Land` retains every candidate under create-only refs before publication. A
publication `base_moved` result fetches the latest target tip, checks ancestry,
and rebases only the private workspace. Conflict paths are passed to the same
repairer value with a separate ten-minute default allowance and no fixed repair
count. An impossible rebase, spent allowance, or still-not-landable full gate
parks the retained candidate. A real landable gate receipt is required to make
the refreshed one-parent candidate. The supplied publication callback remains
the only operation that can CAS the target branch; integration never overwrites
the base directly.

Focused real-Git tests cover moved-start baseline journaling and merge, same-path
conflict repair followed by full `gate.Run` and CAS publication, clean rebase
followed by full gate verification, and impossible ancestry parking while
preserving the candidate and leaving the target unchanged.

## Commands and results

| Command | Result |
|---|---|
| `GIT_CONFIG_GLOBAL=/dev/null GOMAXPROCS=2 go test -count=1 -p=2 -parallel=2 ./internal/landing/integrate` | Passed all four focused moved-base integration tests (14.405s), including durable impossible-rebase parking evidence. |
| `GIT_CONFIG_GLOBAL=/dev/null make check` | Passed format, vendor fingerprints, `go vet ./...`, `go test -count=1 -parallel=2 ./...`, and both CLI/adapter builds. Exit 0. It ran while holding this worker's `~/cx/kgo/gates.lock/port1455` and `~/cx/kgo/gates.lock/custody` locks. |
| `make build` | Passed; exit 0. |
| Assigned frozen v1.2 command | Runner exit 1: 0 passed, 6 failed, 0 errors, 0 skipped; each literal ID resolved to one instance. Every instance stopped before Build behavior at approval because public CLI command routes are unwired. Results and workdirs are retained at `/Users/almirsarajcic/cx/kgo/evidence/40-moved-base-integration/results.jsonl` and `/Users/almirsarajcic/cx/kgo/evidence/40-moved-base-integration/work/`. No provider request reached its fake server. |

The acceptance command was issued with the literal requested IDs
`v1.2-56-build-22,v1.2-57-build-23,v1.2-58-build-24,v1.2-59-build-25,v1.2-60-build-26,v1.2-87-ladder-19`,
profile `cli,state,approval,shape,build,ladder,provider,custody,format,v1.2`,
`--jobs 2`, and `--time-scale 0.02`. The runner started at
`2026-10-07T22:11:37Z`, reported `v1.2+unknown`, and wrote six result rows plus
its metadata row. The exact command was:

```sh
make build
SUITE="$HOME/cx/kgo/inputs/conformance-v1.2"
EVIDENCE="$HOME/cx/kgo/evidence/40-moved-base-integration"
mkdir -p "$EVIDENCE"
PYTHONDONTWRITEBYTECODE=1 PATH="$HOME/.local/share/mise/installs/python/3.14.7/bin:$PATH" \
  "$SUITE/bin/kogen-conformance" run --kogen "$PWD/bin/kogen" \
  --profile cli,state,approval,shape,build,ladder,provider,custody,format,v1.2 --case 'v1.2-56-build-22,v1.2-57-build-23,v1.2-58-build-24,v1.2-59-build-25,v1.2-60-build-26,v1.2-87-ladder-19' \
  --jobs 2 --time-scale 0.02 --workdir "$EVIDENCE/work" \
  --out "$EVIDENCE/results.jsonl"
```

| Resolved case | Instances | Result |
|---|---:|---|
| `v1.2-56-build-22` | 1 | Failed at step 2 approval: expected exit 0, got exit 2 and `kogen: implementation bootstrap; command routes are not wired`. |
| `v1.2-57-build-23` | 1 | Failed at step 2 approval: expected exit 0, got exit 2 and the CLI bootstrap response. |
| `v1.2-58-build-24` | 1 | Failed at step 2 approval: expected exit 0, got exit 2 and the CLI bootstrap response. |
| `v1.2-59-build-25` | 1 | Failed at step 2 approval: expected exit 0, got exit 2 and the CLI bootstrap response. |
| `v1.2-60-build-26` | 1 | Failed at step 3 approval: expected exit 0, got exit 2 and the CLI bootstrap response. |
| `v1.2-87-ladder-19` | 1 | Failed at step 2 approval: expected exit 0, got exit 2 and the CLI bootstrap response. |

All six had empty stdout and stderr `kogen: implementation bootstrap; command
routes are not wired`; no selected moved-base assertion was reached. Compatible
black-box passes: **none**. The JSONL and all six case workdirs remain at the
absolute paths above. The post-check build digest and runner metadata in that
JSONL bind the attempted CLI to the dirty source revision stated above.

## Historical conflicts and closure gates

The frozen v1.2 reference metadata classifies B22, B23, and B24 as `ref-bug`:
respectively, the reference omits `base_moved_at_start`; its appendix stops when
moved-base items are already green, although v1.3-draft §§3.4 and 3.8.1 absorb
them and allow Intent-only landing; and it omits verification events despite
landing on the moved base. These reference bugs do not weaken the draft contract.
The v1.2 reference metadata classifies B25, B26, and L19 as `R-gap`: its
historical implementation parks a moved-base conflict without integration
repairs, and B26/L19 stop the drain. v1.3-draft §3.9.2 instead requires
same-conversation repairs under the separate landing allowance, with parking
only after an impossible rebase or a red result after repairs. The run above
stopped at approval, so none of those historical behaviors was observed in this
execution.

- This is component evidence only. The public approval → queue → provider → gate
  → CAS → status flow is not wired in this worktree, so no unwired case can count
  as a behavior pass. I3 owns that end-to-end integration; I4 owns ladder repair
  parity and the public repair wiring.
- The shared coherent migrated v1.3 suite and Quint cohort are unavailable.
  No `R(rebase)` scratch-copy spec phase or 500 × 25 trace replay for seeds 17,
  23, and 41 was run; replay seeds and divergence are therefore none/not
  measured. Planned D-* fixtures are unavailable as frozen v1.2 cases; wait for
  shared frozen v1.3 IDs. No source oracle or golden was changed.
- Linux evidence is unavailable in this macOS worker. No optional-runtime,
  live-provider, live-comparison, race, or custody-stress evidence is claimed.
- D3 latest-crashed-work preservation and integration cleanup/claim handoff
  remain external closure gates. This package retains candidate Git objects and
  parks runs; it does not own workspace cleanup or recovery.

## Final revision

Implementation source commit: `fb6d0bdf7b1dd1db2a5349985e770c8d50af4c66`
(`Implement moved-base landing integration`). The acceptance binary was built
from the same source content before the commit, with `vcs.modified=true`.
