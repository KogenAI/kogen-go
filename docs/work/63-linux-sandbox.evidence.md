# 63 Linux sandbox evidence

## Revisions and host

- Package/base revision before this worker: `5ba8613f28ec57720a6dcf5be4e3d2e1ba326f34`, branch `kgo/63-linux-sandbox`. Implementation commit: pending at evidence drafting; this note will be updated after the package commit.
- CLI source revision: `5ba8613f28ec57720a6dcf5be4e3d2e1ba326f34`. No CLI source files changed. The built public route still reports `kogen: implementation bootstrap; command routes are not wired`.
- Production Linux sandbox/CLI adapter revision: none. `internal/sandbox/availability.go` does not route to the owned Linux package, and shared-file wiring was outside this package's ownership.
- Target: spec v1.3-draft `e19dd1c21c19c5be1201c3b6a42c59c28b5c2887`, `spec/05-sandbox-custody.md` §§5.1, 5.3, 5.5; draft recovery note in `CHANGES-v1.3.md` §3. Rust reference revision `a402540b39cedc7f788472297add7ae2f8a6631a`: `crates/kogen-core/src/run/sandbox.rs`, `sandbox/platform.rs`, and `sandbox_tests.rs`.
- Host: Darwin Mac 25.6.0, `darwin/arm64`; Go 1.27.1, Git 2.54.0, Python 3.14.7, Node 24.21.0. `/usr/bin/bwrap` is absent. No Linux VM/container runtime was provisioned in this worktree environment.
- Frozen input: `$HOME/cx/kgo/inputs/conformance-v1.2`, intended upstream revision `0f93bad988fb8d7a8eff4e94954d1db0a046c89d`. The supplied suite copy has no Git metadata and reports `v1.2+unknown`. No suite or golden files were changed.
- `make build` artifact hashes after the assigned acceptance invocation: `bin/kogen` SHA-256 `7395492807a4528c85b32dc23f5369edd1ab6c1d3c158137c5a0cdfa6b974fcb`; `bin/kogen-xspec` SHA-256 `0cf588dcdeabf38da123f60796c59b828fd4775d8f30f61bcdf516963c621e3f`.

## Component behavior

`internal/sandbox/linux` now provides a fixed `/usr/bin/bwrap` adapter pinned to version 0.13.0. The executable must be a regular executable file with no setuid/setgid bits and no group/world write permission. `Probe` checks the exact version and then exercises namespace separation, shared network, dropped capabilities, allowed writes, denied writes, and masked secret reads through the caller's `contract.ProcessRunner`. It reports the stable unavailable reason `no supported confinement tool is available on this host` whenever the pinned tool or required enforcement cannot be established.

`Prepare` builds a bubblewrap invocation with `--unshare-all`, `--share-net`, `--die-with-parent`, `--new-session`, and `--cap-drop ALL`; it mounts `/` read-only, binds `/tmp` and declared writable stage/cache paths, rebinds Build checkout/origin paths read-only, then masks protected directories with tmpfs and protected files with `/dev/null`. Linux keyrings under `$HOME/.local/share/keyrings` are included. Paths are normalized through existing symlinks, malformed/special paths fail closed, and generated policy arguments are checked against the 4 KiB argument limit. Probe files and logs are removed through rooted filesystem operations.

The package tests on this host passed for policy ordering, fixed tool/version checks, path validation, protected symlink aliases, and the exact unsupported-host fallback. The Linux enforcement/process-custody test is present but skipped on Darwin. The Linux/amd64 test binary cross-compiled successfully; that is compile evidence only and does not establish Linux namespace, bwrap, credential-mask, timeout, or death behavior.

Bubblewrap option semantics are based on the upstream [v0.13.0 manual](https://github.com/containers/bubblewrap/blob/v0.13.0/bwrap.xml), including namespace selection, filesystem operation ordering, capability dropping, and `--die-with-parent`.

## Commands and results

Pinned mise tools were placed first on `PATH` before each command.

```sh
GOMAXPROCS=2 go test -p=2 -parallel=2 -count=1 -v ./internal/sandbox/linux
```

Result: passed on Darwin; the real Linux probe/process-custody test skipped with `requires a real Linux host`.

```sh
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 GOMAXPROCS=2 go test -p=2 -parallel=2 -c -o /tmp/kogen-linux-sandbox.test ./internal/sandbox/linux
```

Result: linux/amd64 test binary compiled successfully. It was not executed.

```sh
GOMAXPROCS=2 GIT_CONFIG_GLOBAL=/dev/null make check
```

Result: passed. `make check` reported format, vendor fingerprints, `go vet`, all tests, and both builds passed.

Assigned frozen-oracle command (run once; full result and workdirs retained):

```sh
make build
SUITE="$HOME/cx/kgo/inputs/conformance-v1.2"
EVIDENCE="$HOME/cx/kgo/evidence/63-linux-sandbox"
mkdir -p "$EVIDENCE"
PYTHONDONTWRITEBYTECODE=1 PATH="$HOME/.local/share/mise/installs/python/3.14.7/bin:$PATH" \
  "$SUITE/bin/kogen-conformance" run --kogen "$PWD/bin/kogen" \
  --profile cli,state,approval,shape,build,ladder,provider,custody,format,v1.2 --case 'custody-01,custody-02,custody-03,custody-04,custody-05,v1.2-119-custody-06,v1.2-120-custody-07,v1.2-121-custody-08,v1.2-122-custody-09,v1.2-123-custody-10' \
  --jobs 2 --time-scale 0.02 --workdir "$EVIDENCE/work" \
  --out "$EVIDENCE/results.jsonl"
```

Result JSONL: `/Users/almirsarajcic/cx/kgo/evidence/63-linux-sandbox/results.jsonl`. Metadata records suite version `v1.2+unknown`, platform `macOS-26.7.1-arm64-arm-64bit-Mach-O`, Git 2.54.0, and start time `2026-10-07T21:57:36Z`. All 10 resolved cases / 11 instances failed, with 0 passes, 0 errors, 0 skipped, and 0 unimplemented. `custody-05` resolved to separate TERM and INT instances. Every case stopped at approval/CLI setup before sandbox assertions. The first stderr was exactly `kogen: implementation bootstrap; command routes are not wired`; the oracle also reported that no fake-provider request reached its server. No retry was made. Workdirs remain under `/Users/almirsarajcic/cx/kgo/evidence/63-linux-sandbox/work`.

| Case | Instances | Result | First blocker |
|---|---:|---|---|
| `custody-01` | 1 | Failed | `kogen intent approve greet`: exit 2, expected 5; public route is unwired. |
| `custody-02` | 1 | Failed | `kogen intent approve greet`: exit 2, expected 5; public route is unwired. |
| `custody-03` | 1 | Failed | `kogen intent approve greet`: exit 2, expected 5; public route is unwired. |
| `custody-04` | 1 | Failed | approve exits 2, expected 0; public route is unwired. |
| `custody-05` | 2 | Failed | TERM and INT approve steps exit 2, expected 0; public route is unwired. |
| `v1.2-119-custody-06` | 1 | Failed | approve exits 2, expected 0; public route is unwired. |
| `v1.2-120-custody-07` | 1 | Failed | approve exits 2, expected 0; public route is unwired. |
| `v1.2-121-custody-08` | 1 | Failed | approve exits 2, expected 0; public route is unwired. |
| `v1.2-122-custody-09` | 1 | Failed | approve exits 2, expected 0; public route is unwired. |
| `v1.2-123-custody-10` | 1 | Failed | approve exits 2, expected 0; public route is unwired. |

No selected case reached Linux or macOS sandbox behavior; none is counted as compatible or passing.

## Conflicts and closure gates

- Exact v1.2-versus-v1.3-draft assertion conflicts observed: none. The selected oracle cases stopped before sandbox assertions, so this run does not establish behavioral compatibility.
- Required real-Linux gate remains open: run the pinned bwrap positive/negative namespace and credential probes, timeout and process/death tests, and the selected custody matrix on a real Linux host with bubblewrap 0.13.0. The local host was Darwin and had no bwrap executable.
- I6 integration remains open: route shared `sandbox.Probe`/`sandbox.Runner` to this adapter, connect the policy to the production process supervisor and effective Build/checkout policy, then rerun the selected cases on the integrated revision while retaining this pre-integration failure.
- Planned D-* fixtures are not available v1.2 cases; wait for shared frozen v1.3 IDs. No D-* behavior is claimed. No coherent migrated Quint cohort was available for `R(slice)`; seeds 17, 23, and 41 at 500 traces ×25 steps were not run.
- Optional-runtime, live-provider/live-comparison, race-detector, and shared v1.3 gates were not run. No account access or live provider call was made.

Accepted gate: **component only**. Build and unit/cross-compile evidence do not accept Linux runtime behavior or the unwired public CLI cases.
