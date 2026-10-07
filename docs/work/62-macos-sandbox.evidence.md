# 62 macOS sandbox evidence

## Revisions and host

- Worker implementation commit: `daec9a36c196eee35caf347a1490600be7d5ccb7` (`Implement macOS sandbox policy and probe`), branch `kgo/62-macos-sandbox`; parent/base `00430fc472c3fb21197acf0da8b16425fda5247e`.
- CLI source revision: `fba340e5928b2a6c6644bb056ff76b3eb1276616` (`Bootstrap the Go implementation`). The public command remains unwired and reports `kogen: implementation bootstrap; command routes are not wired`.
- CLI artifact rebuilt from the committed tree: `bin/kogen`, SHA-256 `aaaf7c6e12ee79568acf1e2d9866a04a707b250169a648c4d2fa6ee759e84673`; `bin/kogen-xspec`, SHA-256 `1b83a19b7dd68fee8247bc028382bd9f936c7399471227964174fbc1a058c83c`.
- Production sandbox/CLI adapter revision: none; there is no app route to this component yet.
- Target spec: v1.3-draft `e19dd1c21c19c5be1201c3b6a42c59c28b5c2887`; read `spec/05-sandbox-custody.md` §§5.1–5.5 and `CHANGES-v1.3.md` §3. Rust reference: `kogen-rs` `a402540b39cedc7f788472297add7ae2f8a6631a`, `crates/kogen-core/src/run/sandbox.rs`, `sandbox/platform.rs`, and `sandbox_tests.rs`.
- Frozen suite: `$HOME/cx/kgo/inputs/conformance-v1.2`, upstream revision `0f93bad988fb8d7a8eff4e94954d1db0a046c89d`; the supplied copy reports `v1.2+unknown` because it has no Git metadata. No suite or golden files were changed.
- Host: macOS 26.7.1 build 25G241, `darwin/arm64`; Go 1.27.1, Git 2.54.0, Python 3.14.7, Node 24.21.0. `sandbox-exec` was present at `/usr/bin/sandbox-exec`.

## Component behavior and local effects

`internal/sandbox` now provides separate Build and checkout policies. Build allows the candidate workspace, private run directories, `/tmp`, and declared tool caches, while denying writes to the checkout and origin and requiring an integrity snapshot when execution is unconfined. Shape and approval use the checkout policy, which leaves checkout writes allowed. Host seams select `off`, already `confined`, or forced `unconfined` (`KOGEN_SANDBOX=unavailable`) without conflating those states.

On macOS the component creates a mode-0600 Seatbelt file using `safefs` create-only publication under a private mode-0700 `run/tmp`, invokes `/usr/bin/sandbox-exec`, and removes the policy through the rooted filesystem API. The real probe must observe writes in the workspace, run temp, `/tmp`, and each existing declared cache, and must observe denial of a canary write and canary secret read. `Runner` caches only the probe result for its immutable policy; each confined child gets a fresh profile. The unconfined Build path requires two matching integrity snapshots and stops before execution if the observer is missing or fails.

The final `make check` ran the package tests on the real macOS host. Both the direct Seatbelt probe and the policy-bound Build runner passed: workspace/cache/temp writes succeeded; checkout writes and protected-secret reads were denied; secret bytes did not appear in output; the profile was private and cleaned up. Unit fixtures also passed for `off`, already-confined, forced-unavailable, missing integrity observer, snapshot error, changed snapshots, and stable snapshots. The integrity observer is an injected port matching the Rust boundary; a production Git-backed observer remains an I3 integration task.

## Commands and results

The pinned mise PATH from `WORKER-RULES.md` was exported before each Go/Git command.

```sh
GOMAXPROCS=2 go test -p=2 -parallel=2 -count=1 -v ./internal/sandbox/...
GIT_CONFIG_GLOBAL=/dev/null make check
make build
```

The focused sandbox tests passed. `GIT_CONFIG_GLOBAL=/dev/null make check` passed on the committed component tree: format, vendor fingerprints, `go vet ./...`, all tests with `-parallel=2`, and both binaries built.

The assigned frozen-oracle command was run once. Exact command:

```sh
make build
SUITE="$HOME/cx/kgo/inputs/conformance-v1.2"
EVIDENCE="$HOME/cx/kgo/evidence/62-macos-sandbox"
mkdir -p "$EVIDENCE"
PYTHONDONTWRITEBYTECODE=1 PATH="$HOME/.local/share/mise/installs/python/3.14.7/bin:$PATH" \
  "$SUITE/bin/kogen-conformance" run --kogen "$PWD/bin/kogen" \
  --profile cli,state,approval,shape,build,ladder,provider,custody,format,v1.2 --case 'custody-01,custody-02,custody-03,custody-04,custody-05,v1.2-119-custody-06,v1.2-120-custody-07,v1.2-122-custody-09' \
  --jobs 2 --time-scale 0.02 --workdir "$EVIDENCE/work" \
  --out "$EVIDENCE/results.jsonl"
```

The result metadata started at `2026-10-07T21:43:21Z` on `macOS-26.7.1-arm64-arm-64bit-Mach-O`. Full JSONL is retained at `/Users/almirsarajcic/cx/kgo/evidence/62-macos-sandbox/results.jsonl`; workdirs remain under `/Users/almirsarajcic/cx/kgo/evidence/62-macos-sandbox/work`.

Resolved IDs and instances:

| ID | Instances | Result | First blocker |
|---|---:|---|---|
| `custody-01` | 1 | Failed | `intent approve greet`: exit 2, expected 5. |
| `custody-02` | 1 | Failed | `intent approve greet`: exit 2, expected 5. |
| `custody-03` | 1 | Failed | `intent approve greet`: exit 2, expected 5. |
| `custody-04` | 1 | Failed | `intent approve greet`: exit 2, expected 0. |
| `custody-05` | 2 | Failed | `intent approve greet`: exit 2, expected 0 in both TERM and INT instances. |
| `v1.2-119-custody-06` | 1 | Failed | `intent approve greet`: exit 2, expected 0. |
| `v1.2-120-custody-07` | 1 | Failed | `intent approve greet`: exit 2, expected 0. |
| `v1.2-122-custody-09` | 1 | Failed | `intent approve greet`: exit 2, expected 0. |
| **Total** | **9** | **0 passed, 8 failed, 0 errors, 0 skipped, 0 unimplemented** | All stopped at the public CLI route before the sandbox behavior ran. |

The exact stderr at the first failing command was `kogen: implementation bootstrap; command routes are not wired`. The runner reported that no fake-provider request reached its server. These are scaffold reachability failures, not sandbox behavior results; no selected case is counted as behavior-compatible or passed. No retry was made to erase this run.

## Conflicts and closure gates

- Exact v1.2 versus v1.3-draft assertion conflicts identified for the selected sandbox cases: none. Since the oracle stopped before those assertions, this does not establish behavioral compatibility.
- I3 remains open: wire policy selection into approval/Shape and Build process composition; provide the Git-backed checkout/index/tracked-byte/origin-ref integrity snapshot; journal `sandbox_unavailable`, report the sandbox state, and prove approve → queue → provider → gate → CAS → status. Then rerun the selected cases on the integrated revision while retaining this failed result.
- Linux confinement/runtime parity was not run; package 63 owns that gate. Optional-runtime, live-provider, and live-comparison gates were not run.
- No shared frozen v1.3 suite with the planned D-* fixtures is available. D-REC recovery/preservation fixtures and their implementation closure remain outside this package; no D-* case is claimed.
- `R(slice)` was not run because no shared coherent migrated Quint cohort is available. No 500×25 traces for seeds 17, 23, or 41, full observations, or divergence comparison are claimed.
- `KOGEN_SANDBOX=unavailable` warning/event/report CLI behavior remains unverified because the public route is unwired; only component mode selection and its observation were tested.

Accepted gate: **component** only. `make check` and live macOS probes are component evidence, not behavior acceptance.
