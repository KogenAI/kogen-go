# 07-rooted-path-safety evidence

## Revisions and environment

- Worker implementation commit: `120005f78585ccfe4305817b054586abe337b9c0` (`Implement rooted filesystem safety`), branch `kgo/07-rooted-path-safety`. The focused tests, `make check`, and oracle command used the exact source content later committed as this revision. The branch HEAD at the time of the oracle command was `9f982d22f856a3f1d453194d9a1ac19281fc715b`.
- CLI route source: `fba340e5928b2a6c6644bb056ff76b3eb1276616` (`Bootstrap the Go implementation`). `cmd/kogen` still reports `kogen: implementation bootstrap; command routes are not wired` and exits 2. The package-00 contract/input commit is `93f7a74e15ff13b0e42b64bd66f9a564fd452be3`; its evidence receipt is `9f982d22f856a3f1d453194d9a1ac19281fc715b`.
- Production adapter revision: none; no provider/CLI production adapter is wired.
- Target spec: `kogen-spec` commit `e19dd1c21c19c5be1201c3b6a42c59c28b5c2887`, v1.3-draft. The spec checkout has unrelated dirty Quint/harness edits; authoritative clauses and `CHANGES-v1.3.md` were read from this exact committed revision with `git show`.
- Rust reference: `kogen-rs` commit `a402540b39cedc7f788472297add7ae2f8a6631a`; inspected `crates/kogen-core/src/safe_fs.rs`, provider tool path resolution and its tests.
- Frozen oracle: `~/cx/kgo/inputs/conformance-v1.2`, sourced from `kogen-conformance` commit `0f93bad988fb8d7a8eff4e94954d1db0a046c89d`.
- Host/toolchain: macOS 26.7.1, `darwin/arm64`; Go 1.27.1; Git 2.54.0; Python runner invoked from the pinned 3.14.7 path. `golang.org/x/sys v0.48.0` is the existing vendored dependency.

## Implementation and local fixtures

`internal/safefs` now implements `contract.RootedFS` and `contract.RootOpener`. The Unix implementation opens the root once and traverses parents through directory descriptors with `O_NOFOLLOW`. Relative symlinks are resolved against the opened directory stack and rejected when absolute or when `..` would leave the root. Input and symlink paths reject NUL bytes, `.git` segments, Windows device-name aliases, and non-canonical paths.

Reads require regular files and use nonblocking no-follow opens, so FIFO/device leaves are refused without waiting on another process. Publication writes and syncs a temporary file, then uses descriptor-relative link/rename and syncs the parent directory; replacement operates on a final symlink entry itself. Existing hard-linked publication leaves are refused. Append is copy-on-write, so it never mutates an existing inode through a hardlink. Directory creation, removal, rename and symlink creation use the same rooted descriptor policy.

Unit fixtures cover `..`, `.git`, NUL/device aliases, an absolute symlink escape, a contained relative symlink, symlink-leaf replacement, FIFO refusal, device-kind refusal, hardlink append refusal, create-only/replace publication, directory operations, and parent-directory replacement while publishing. The race fixture repeatedly renames a real parent and swaps an external symlink into its name; it asserts the outside directory receives no publication. The host denied `mknod` with `operation not permitted`; the test then exercised the descriptor-relative kind check against `/dev/null`, and passed. A real in-root character-device fixture remains for a host that permits `mknod`.

## Commands and results

All Go commands used the pinned PATH. The focused filesystem test and `make check` ran while holding the mkdir lock `~/cx/kgo/gates.lock`; the lock was released afterward.

| Command / attempt | Result |
|---|---|
| `GOMAXPROCS=2 go test -count=1 -parallel=2 ./internal/safefs` — initial compile attempt | Failed before tests: `nofollow_unix.go:95: undefined: last` and `nofollow_unix.go:144: unix.CloseOnExec(keptParent) (no value) used as value`. Both compile errors were fixed. |
| `GOMAXPROCS=2 go test -count=1 -parallel=2 ./internal/safefs` — after compile fix | Passed. |
| `GOMAXPROCS=2 go test -count=1 -parallel=2 -v -run TestRootRejectsFIFOAndDeviceFilesWithoutBlocking ./internal/safefs` | Passed. FIFO refusal ran. `mknod` was denied; the fallback checked `/dev/null` through `openRegularAt` and got `ErrUnsafeFile`. |
| `GIT_CONFIG_GLOBAL=/dev/null GOMAXPROCS=2 make check` | Passed on the final implementation/test source: format check, vendor fingerprints, `go vet ./...`, all tests with `-parallel=2`, both `CGO_ENABLED=0` builds, and the source-mutation guard. |
| Frozen v1.2 provider-26 command below | Runner exit 1; `provider-26` failed, 0/1 instances passed. Failure occurred before a provider request because the public command route is unwired. The result JSONL is retained at the absolute path shown below. |
| `git diff --check` | Passed. |

The exact assigned command was run once:

```sh
make build
SUITE="$HOME/cx/kgo/inputs/conformance-v1.2"
EVIDENCE="$HOME/cx/kgo/evidence/07-rooted-path-safety"
mkdir -p "$EVIDENCE"
PYTHONDONTWRITEBYTECODE=1 PATH="$HOME/.local/share/mise/installs/python/3.14.7/bin:$PATH" \
  "$SUITE/bin/kogen-conformance" run --kogen "$PWD/bin/kogen" \
  --profile cli,state,approval,shape,build,ladder,provider,custody,format,v1.2 --case 'provider-26' \
  --jobs 2 --time-scale 0.02 --workdir "$EVIDENCE/work" \
  --out "$EVIDENCE/results.jsonl"
```

Resolved case: `provider-26` (“Path escape”), one instance. Runner summary: provider 1 case, 1 implemented, 0 pass, 1 fail, 0 error, 0 skipped. The exact failure was step 6 (`kogen intent shape greet ...`): exit 2, expected 0; stdout empty; stderr was `kogen: implementation bootstrap; command routes are not wired`. `fake-requests.json` records `"requests": []`; no provider call reached the fake server. The complete run is preserved at `/Users/almirsarajcic/cx/kgo/evidence/07-rooted-path-safety/results.jsonl` and its workdir remains at `/Users/almirsarajcic/cx/kgo/evidence/07-rooted-path-safety/work/provider-26`.

No behavior case is counted as passing. The local filesystem fixtures and green `make check` establish component evidence only.

## Historical conflicts and closure gates

- No P26/v1.3-draft conflict was identified: frozen provider-26 exercises §4.7 path escape refusal, which is compatible with the v1.3 path boundary.
- The frozen v1.2 reference metadata labels `provider-26` `ref-bug`, cause `a13`: “shaping pass ends on the first successful write (generated_file_missing; measured 207 failures) instead of on a reply without tool calls, with no finish guard [task 01-shaping-pass-ends-on-done]”. This is a reference limitation, not a Go behavior result. The Go run stopped at the unwired CLI before testing path handling.
- I3 remains open. Its approval → queue → fake provider → gate → CAS → status route requires a wired production adapter and a behavior rerun of provider-26 among its anchors. The current bootstrap exit is an explicit integration blocker; no I3 result is claimed.
- I6 remains open. This package supplies macOS component fixtures only. The broader adversarial matrix (including D-REC cases, custody, publication and full workflow effects) and Linux execution remain for package 70/I6. The host could not create a device node; a Linux or otherwise provisioned device fixture is still needed.
- Planned D-* fixtures are not frozen v1.2 IDs. Wait for the shared coherent v1.3 suite before claiming those gates. No `R(slice)` is assigned to this package; no Quint replay was run. The shared migrated v1.3 cohort required for R(slice) is not available in the frozen inputs.
- Consumers across state, journal, cache, approval staging, tool handles and protection restoration still need integration against this port. No end-to-end caller wiring is claimed.
- No live provider or account access was used. The v1.2 fixture workdir and result JSONL were retained; no oracle or golden files were changed.

This receipt is **component** only. Behavior acceptance requires the wired integration, retained provider-26 rerun, I3/I6 closure evidence, and the shared frozen v1.3 cases for draft-specific claims.
