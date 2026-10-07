# 38 Landing commit evidence

## Gate and revisions

This package is **component-ready only**. It creates a base-relative, single-parent commit in the winning workspace and leaves ref publication to package 39. The worker branch is `kgo/38-landing-commit`, based on `a06e49328067c8efb74358d18b33b082a86a3731`. Verification ran against that base plus this package's uncommitted task delta; the resulting worker commit is the commit containing this note. The CLI was built from the same checkout at the base revision; `bin/kogen` SHA-256 was `52665dd40b90ceff54d026c16a0094bf6b772c2753e7880d0acee915b1d8ea97`.

- Target spec / `CHANGES-v1.3.md`: `e19dd1c21c19c5be1201c3b6a42c59c28b5c2887`.
- Read-only Rust reference: `a402540b39cedc7f788472297add7ae2f8a6631a`, landing implementation in `crates/kogen-core/src/git/landing/repository.rs` and `commit.rs`.
- Toolchain: Go `go1.27.1 darwin/arm64`; Git `2.54.0`; host Darwin arm64.
- Oracle input: `$HOME/cx/kgo/inputs/conformance-v1.2`; the frozen source revision recorded in `PLAN.md` is `0f93bad988fb8d7a8eff4e94954d1db0a046c89d`. The installed input is a non-Git snapshot, and its runner reports `v1.2+unknown`; runner SHA-256 `0b53a3b60956cbe8dde7af54e274c46b3e8d430c45662d7dff67445f440b39f9`.
- Literal selected case IDs are in [38-landing-commit.cases](38-landing-commit.cases).

## Implementation and local effects

`internal/landing/commit` now requires a landable gate report, checks its base/candidate tree and approval hash, checks the approval-bound protected manifest through a rooted workspace, and refuses a present source acceptance copy. It snapshots the candidate from immutable base metadata, so builder HEAD and index do not select the commit contents. A private index composes that verified snapshot with exact approved Intent and candidate-test blobs, which also keeps those bytes when native ignore rules exclude their paths. It creates a commit with the exact `<title>\n\nKogen-Intent: <slug>\n` message and sole parent `BaseCommit`, using `gitio.NewOrigin` and supervised Git operations. It verifies the final tree and raw commit object. It does not move HEAD or publish a ref.

Focused real-Git tests passed for builder-commit squashing, ignored approved files, approved byte integrity, source-copy removal, exact message/tree/parent, post-gate mutation refusal, no hook execution, and use of the global identity/signing helper instead of a workspace-local helper. Test Git fixtures use isolated identity/config and a fake signer; there were no live provider calls or account access.

## Commands and results

- `GOMAXPROCS=2 go test -p=2 -parallel=2 ./internal/landing/commit` — passed; retained at `$HOME/cx/kgo/evidence/38-landing-commit/unit-004.log`.
- `GIT_CONFIG_GLOBAL=/dev/null make check` — passed formatting, vendor fingerprints, vet, all tests, and both builds; retained at `$HOME/cx/kgo/evidence/38-landing-commit/make-check-001.log`.
- `make build` — passed immediately before the assigned frozen-oracle command.
- The exact assigned oracle command was run once, without changing the selected IDs, profiles, jobs, time scale, workdir, or output path:

```sh
make build
SUITE="$HOME/cx/kgo/inputs/conformance-v1.2"
EVIDENCE="$HOME/cx/kgo/evidence/38-landing-commit"
mkdir -p "$EVIDENCE"
PYTHONDONTWRITEBYTECODE=1 PATH="$HOME/.local/share/mise/installs/python/3.14.7/bin:$PATH" \
  "$SUITE/bin/kogen-conformance" run --kogen "$PWD/bin/kogen" \
  --profile cli,state,approval,shape,build,ladder,provider,custody,format,v1.2 --case 'v1.2-123-custody-10,v1.2-37-build-02,v1.2-64-build-30,v1.2-67-build-43' \
  --jobs 2 --time-scale 0.02 --workdir "$EVIDENCE/work" \
  --out "$EVIDENCE/results.jsonl"
```

The runner resolved four instances, one for each requested case. **Zero passed; all four failed before landing was reached.** Each stopped at step 2 (`approve`): `kogen intent approve greet {hash8:greet}` exited 2 instead of 0; stdout was empty; stderr was exactly `kogen: implementation bootstrap; command routes are not wired\n`. The runner also reported `no provider request reached the fake server (KOGEN_PROVIDER_URL seam missing?)`. Complete observations and per-case workdirs are retained at `/Users/almirsarajcic/cx/kgo/evidence/38-landing-commit/results.jsonl` and `/Users/almirsarajcic/cx/kgo/evidence/38-landing-commit/work/`.

| Resolved case | Instances | Result |
|---|---:|---|
| `v1.2-123-custody-10` | 1 | Failed at approval bootstrap, before custody/landing |
| `v1.2-37-build-02` | 1 | Failed at approval bootstrap, before Build/landing |
| `v1.2-64-build-30` | 1 | Failed at approval bootstrap, before landing tree assertions |
| `v1.2-67-build-43` | 1 | Failed at approval bootstrap, before commit identity/hooks assertions |

Compatible conformance passes: **none**. These failures do not demonstrate that the landing implementation passes or fails once invoked. No normative v1.2-versus-v1.3 behavior conflict was reached or observed; `conflicts` is therefore empty. No retry was made after the oracle failure.

The focused-test history is retained without overwriting: `unit-initial-001.log` contains a fixture Intent syntax failure; `unit-003.log` contains the first fake-signer protocol failure; the corrected focused run is `unit-004.log`. The oracle result remains in its original JSONL and workdirs.

## Deferred closure gates and gaps

- **I3 behavior integration is open.** The public approval/queue/provider/gate/landing/CAS/status path is not wired; the observed bootstrap error prevents all four assigned behavior cases from reaching this package. I3 must run them on the integrated revision and retain a behavior receipt. This component receipt is not behavior acceptance.
- I3 also depends on the separate D3 crashed-work preservation gate. This package does not implement or claim recovery behavior.
- The shared frozen v1.3 conformance/replay cohort and migrated Quint fixtures are not available. Planned D-* fixtures are not cases and are not counted as passes. No R(slice) replay was assigned or run.
- Linux evidence is unavailable in this macOS worker. No optional runtime or live-provider evidence is claimed.
- The controller must stop and reap workspace writers before `Create`; process custody is owned by the Build controller/integration path.

No files outside `internal/landing/commit/**` and this package's unique `docs/work/38-landing-commit.*` handoff artifacts were changed.
