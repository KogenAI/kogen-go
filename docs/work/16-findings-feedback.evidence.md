# 16-findings-feedback evidence

Recorded 7 October 2026 for `/Users/almirsarajcic/Areas/Kogen/kogen-go-wt/16-findings-feedback` on branch `kgo/16-findings-feedback`.

## Revisions and inputs

- Package implementation commit: `a2344615173ae353a8ce328a4cc3ff3cb5e021c6`.
- CLI binary: built from the same Go worktree. `cmd/kogen` and `internal/app.Bootstrap` remain the bootstrap implementation from the `9f982d22f856a3f1d453194d9a1ac19281fc715b` base; the command prints `kogen: implementation bootstrap; command routes are not wired` and exits 2. The acceptance run stopped at approval/CLI routing before reaching this package.
- Production adapter revision: none wired to `internal/findings` yet.
- Target spec: `kogen-spec` commit `e19dd1c21c19c5be1201c3b6a42c59c28b5c2887`, manifest SHA-256 `52388b2be73aade4b4fb2535cef475baefae1d3f662c1f2221afed737154468f`.
- Diagnostic oracle: frozen `~/cx/kgo/inputs/conformance-v1.2`, sourced from `kogen-conformance` commit `0f93bad988fb8d7a8eff4e94954d1db0a046c89d`, manifest SHA-256 `8d76a7ec7fbf042ef526e959f1de6d652ec1f3ade43b33ce5587f3586372a815`.
- Rust reference: `kogen-rs` commit `a402540b39cedc7f788472297add7ae2f8a6631a` (`gate/checks.rs`, `build/support.rs`, `approval/checks.rs`, `approval/manifest.rs`, and `approval/render.rs`).
- Normative clauses read: `spec/02-formats.md` §§2.4.4, 2.5.1, 2.9; `spec/03-build.md` §§3.3, 3.7.2–3.7.4; `CHANGES-v1.3.md` §§1–2. GNU identities are `(path, tool/rule, symbol)`; position and message are excluded. Acceptance IDs occupy the symbol field. Base-red checks require matching status and a current identity subset, with equal exit status as fallback when either side has no identities.

## Component work

Implemented under `internal/findings/**`:

- `ParseGNU` preserves full finding details and presentation positions, parses test symbols only for test tools, and exposes the stable `(path, rule, symbol)` identity. `AcceptanceIdentity` puts the approved item ID in the symbol field.
- `IsExcused` applies the base-red status, identity-subset, and exit-status fallback rules.
- Approval-card helpers render the red-baseline warning block and unavailable/timeout/mutating status rows.
- `RenderGateFeedback` applies the per-tool 10 and total 20 finding bounds, 200-character finding messages, raw log references, last-eight-line/600-character tails with `$TMPDIR`/`$HOME` redaction, acceptance rows, base-red warnings, and the final gate line. It also retains every parsed finding in a JSON artifact. `GateFeedback.Publish` uses `contract.RootedFS.Publish` with create-only mode and `0600` permissions.
- Unit tests cover GNU parsing and identity, acceptance symbols, baseline comparison and card rows, feedback caps/redaction/artifact retention, and rooted artifact publication.

These helpers are component-ready but are not called from public approval or Build routes. I1 must connect the card helpers; I3 must connect gate feedback and artifact publication.

## Commands and results

The worker used the pinned Git 2.54.0, Go 1.27.1, Python 3.14.7, and Node 24.21.0 toolchain on `darwin/arm64`, with `GOMAXPROCS=2`.

| Command | Result |
|---|---|
| `GOMAXPROCS=2 GOFLAGS='-mod=vendor -p=2' go test -count=1 -parallel=2 ./internal/findings` | Passed after correcting the module import path and a test expectation for `$HOME` redaction. Two earlier focused attempts failed locally: first the import path was wrong; second the assertion expected the literal home path. Neither was an oracle run. |
| `GOMAXPROCS=2 GIT_CONFIG_GLOBAL=/dev/null make check` | Passed on the final implementation: format check, vendor fingerprints, `go vet ./...`, tests with `-parallel=2`, and both CGO-disabled builds. `internal/findings` tests passed. |
| `make build` | Passed; built `bin/kogen` and `bin/kogen-xspec` from the final implementation. |
| Assigned frozen v1.2 command below | Resolved 15 cases / 15 instances (one each): 0 passed, 15 failed, 0 errors, 0 skipped, 0 unimplemented. |
| `git diff --check` and staged diff check | Passed. |

The exact assigned acceptance command was run once:

```sh
make build
SUITE="$HOME/cx/kgo/inputs/conformance-v1.2"
EVIDENCE="$HOME/cx/kgo/evidence/16-findings-feedback"
mkdir -p "$EVIDENCE"
PYTHONDONTWRITEBYTECODE=1 PATH="$HOME/.local/share/mise/installs/python/3.14.7/bin:$PATH" \
  "$SUITE/bin/kogen-conformance" run --kogen "$PWD/bin/kogen" \
  --profile cli,state,approval,shape,build,ladder,provider,custody,format,v1.2 --case 'approval-08,approval-09,v1.2-136-format-10,v1.2-39-build-04,v1.2-45-build-11,v1.2-46-build-12,v1.2-47-build-13,v1.2-48-build-14,v1.2-49-build-15,v1.2-50-build-16,v1.2-51-build-17,v1.2-52-build-18,v1.2-53-build-19,v1.2-54-build-20,v1.2-55-build-21' \
  --jobs 2 --time-scale 0.02 --workdir "$EVIDENCE/work" \
  --out "$EVIDENCE/results.jsonl"
```

Resolved IDs and results:

| IDs | Instances | Result |
|---|---:|---|
| `approval-08`, `approval-09` | 2 | Both failed at `kogen intent approve greet`: exit 2 instead of expected 5; stdout empty and stderr was `kogen: implementation bootstrap; command routes are not wired`. |
| `v1.2-136-format-10`, `v1.2-39-build-04`, `v1.2-45-build-11`–`v1.2-55-build-21` (each literal ID in the command) | 13 | All failed at their first approval step: exit 2 instead of expected 0; stdout empty and stderr was `kogen: implementation bootstrap; command routes are not wired`. The builder/gate steps were not reached. |
| **Total** | **15** | **0/15 passed; no findings behavior is claimed from these results.** |

The retained result file is `/Users/almirsarajcic/cx/kgo/evidence/16-findings-feedback/results.jsonl`, SHA-256 `842637dc54b1af3d8190427fef5053161897ea9b4be038bb56bba8c967484c40`. The oracle input and goldens were not changed. No retry was made after this failed oracle run.

## Effects, conflicts, and deferred gates

- Local source changes and the worker commit are limited to `internal/findings/**`; evidence files are added separately. The builds produced the ignored `bin/` outputs. The conformance runner wrote workdirs and `results.jsonl` only under the assigned external evidence directory. No live provider/account, OAuth port 1455, project repository, or user checkout was accessed.
- Exact historical conflict among the assigned IDs: none identified. The observed failures are the shared bootstrap route gap, not v1.2/v1.3 behavior conflicts. `CHANGES-v1.3.md` §2's independent baseline-v3 key is owned by packages 18/54 and was not implemented or tested here.
- The planned D-* / D-BASE cases are not available v1.2 IDs. Wait for shared frozen v1.3 case IDs and migrated models/goldens before claiming those gates.
- No R(slice) replay was assigned or run. The coherent shared migrated Quint cohort and frozen v1.3 IDs are not available; the required scratch-copy run (500 traces × 25 steps for each seed 17, 23, and 41, conforming against the same-revision private binary) remains open.
- I1 card wiring and I3 executable approval→queue→provider→gate→CAS→status behavior remain open. Linux, optional runtime, race/custody, production replay, live provider/cache comparison, and I8 release manifests have no external evidence here.

The gate receipt is **component** only. Passing `make check`, compilation, or worker exit does not confer behavior acceptance.
