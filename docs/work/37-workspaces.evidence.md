# 37-workspaces evidence

Date: 2026-10-07  
Branch: `kgo/37-workspaces`  
Worker revisions: `b04020e` (workspace implementation), `478f516` (clone-flag assertion)  
Target spec: `kogen-spec` `e19dd1c21c19c5be1201c3b6a42c59c28b5c2887`, v1.3-draft; read `CHANGES-v1.3.md` §3 and spec §3.4 B5, §3.10, §5.4. The spec checkout has pre-existing unrelated Quint/harness edits; it was not changed.

The component adds supervised `--local --no-hardlinks --no-checkout --template=` cloning followed by a verified detached checkout; rooted setup-output seeding with APFS/Linux COW attempts and per-file plain-copy fallback; exact approved regular-file byte/mode installation with optional staged-source removal; whole-sequence setup retry once with per-attempt process results; and first-rung base-runner availability classification. Clone failure cleanup is limited to a newly created workspace that has not been returned to its Build owner.

## Tool and executable identity

- Host: `macOS-26.7.1-arm64-arm-64bit-Mach-O`.
- Pinned tools: Go `1.27.1`, Git `2.54.0`, Python `3.14.7`.
- Public CLI artifact used by the oracle command: SHA-256 `19af2299b3f0313d2aa870fc5e6aebf05fb14237872e5ca71f09be59c850dbde`. Its `cmd/kogen` source was unchanged through the worker commits; the CLI currently prints that command routes are not wired and does not import `internal/workspace`.
- Frozen suite input: `$HOME/cx/kgo/inputs/conformance-v1.2`, `VERSION`=`1.2`; the read-only copy has no `.git`, so the launcher records `suite_version: v1.2+unknown`. `docs/work/PLAN.md` identifies the frozen upstream suite as `0f93bad988fb8d7a8eff4e94954d1db0a046c89d`.
- The Go private `kogen-xspec` adapter was built by `make check` but no Quint slice adapter was run.

## Commands and results

Pinned worker PATH was set before each command:

```sh
export PATH="$HOME/.local/share/mise/installs/git/2.54.0/bin:$HOME/.local/share/mise/installs/go/1.27.1/bin:$HOME/.local/share/mise/installs/python/3.14.7/bin:$HOME/.local/share/mise/installs/node/24.21.0/bin:$PATH"
```

`GOMAXPROCS=2 go test -p=2 -parallel=2 ./internal/workspace` — passed.  
`GIT_CONFIG_GLOBAL=/dev/null make check` — passed after the final package and test changes: format, vendor fingerprints, `go vet ./...`, `go test -count=1 -parallel=2 ./...`, and both binary builds. The existing recovery tests also passed; `internal/recovery/preserve` still has no production tests/effect.

The assigned frozen-oracle command was run once. `make build` passed; the runner failed the selected cases before they reached workspace, setup, provider, gate, or landing behavior. Full observations are retained at `/Users/almirsarajc/cx/kgo/evidence/37-workspaces/results.jsonl` (6 JSONL lines including metadata):

```sh
make build
SUITE="$HOME/cx/kgo/inputs/conformance-v1.2"
EVIDENCE="$HOME/cx/kgo/evidence/37-workspaces"
mkdir -p "$EVIDENCE"
PYTHONDONTWRITEBYTECODE=1 PATH="$HOME/.local/share/mise/installs/python/3.14.7/bin:$PATH" \
  "$SUITE/bin/kogen-conformance" run --kogen "$PWD/bin/kogen" \
  --profile cli,state,approval,shape,build,ladder,provider,custody,format,v1.2 --case 'build-39,build-40,shape-16,v1.2-123-custody-10,v1.2-37-build-02' \
  --jobs 2 --time-scale 0.02 --workdir "$EVIDENCE/work" \
  --out "$EVIDENCE/results.jsonl"
```

Resolved historical cases and instances:

| ID | Instances | Result | First failure |
|---|---:|---|---|
| `shape-16` | 2 | 0/2 passed | Both shape requests returned exit 2 and stderr `kogen: implementation bootstrap; command routes are not wired`; expected exit 0 for the red check and 3 for missing runner. |
| `build-39` | 1 | 0/1 passed | Step 3 `intent approve` returned exit 2 with the same bootstrap stderr; expected approval to succeed before setup retry. |
| `build-40` | 1 | 0/1 passed | Step 2 `intent approve` returned exit 2 with the same bootstrap stderr; expected approval to succeed before base-runner check. |
| `v1.2-123-custody-10` | 1 | 0/1 passed | Step 2 `intent approve` returned exit 2 with the same bootstrap stderr; expected exit 0. |
| `v1.2-37-build-02` | 1 | 0/1 passed | Step 2 `intent approve` returned exit 2 with the same bootstrap stderr; expected exit 0. |
| **Total** | **6** | **0 passed, 5 failed, 0 errors** | Runner hint: no provider request reached the fake server. |

The exact failure text for each affected command is in the retained JSONL. These are unimplemented CLI-route failures, not established v1.2/v1.3 behavioral conflicts. No v1.2 compatibility pass is claimed; no exact draft conflict was established because execution stopped before the assigned behavior. The failed oracle result was not rerun.

## Local effects and safety boundary

- Package tests exercise detached base verification, inode separation, required clone flags, refusal to reuse an existing workspace, byte/mode installation and safe replacement of a symlink leaf, staged-source removal, seeding without inode sharing, bounded COW/plain strategy accounting, rejection of escaping setup symlinks, whole-sequence setup retry, and first-rung tool-missing handling.
- `SeedReport` records COW and plain file counts. The local test accepts whichever strategy the filesystem supports; it does not claim a Linux run.
- Existing `recovery.Controller` stops writers, requires a `PreservationPort`, validates and records preservation before it removes a workspace, and does not mark a workspace removable when preservation fails. This worker did not edit recovery-owned files. The production `internal/recovery/preserve` effect for deterministic create-only ref/archive publication and adoption is still a separate D3 closure gate; component tests do not establish durable crash preservation.
- No app/CLI/Makefile/shared contract wiring was changed. The public route failure above means the acceptance command provides no behavior acceptance for this component.

## Draft conflicts and deferred closure

- **Exact v1.3-draft behavior conflicts established:** none. D-REC-01–06 are planned draft fixtures, not available frozen v1.2 cases; no D-REC claim is made.
- **I3:** public approve → queue → provider → gate → CAS → status integration remains unwired. The preserved oracle run is 0/6 and must remain visible to the coordinator.
- **D3:** implement/test the production preservation effect, create-only snapshot adoption, lossless archive fallback, pending cleanup retry, and failure retention before I3. Keep the workspace and existing refs when preservation fails.
- **H16/ExUnit:** the assigned command does not select an ExUnit profile/runtime. The local approved-file unit test is not an ExUnit runtime result.
- **R(slice):** not run. No scratch copy of a shared coherent migrated Quint cohort was available for this package; the spec checkout contains pre-existing uncommitted Quint/harness changes. Seeds 17, 23, and 41 (500 traces × 25 steps each), full observations, and first-divergence comparison are all outstanding; no oracle/golden files were changed.
- Linux parity, optional runtimes, live provider comparison, and shared frozen v1.3 D-* cases remain external closure evidence.

Accepted gate: **component** only. `make check` and local package effects are code-ready evidence; they do not promote the package to behavior acceptance.
