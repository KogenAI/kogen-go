# 08 durable writes and snapshots evidence

## Revisions and environment

- Worker source commit: `10803e79827ab8699f394931dcba6ac19042ed67` on `kgo/08-durable-writes-snapshots`; based on rooted filesystem package 07 commit `7ee73cc7c56758229a46df250c18dd2174c7412c`.
- CLI source revision: `fba340e5928b2a6c6644bb056ff76b3eb1276616` (`Bootstrap the Go implementation`). The binary still exits 2 with `kogen: implementation bootstrap; command routes are not wired`.
- Production adapter revision: none; no state/cache/staging consumer or public CLI route is wired to this package yet.
- Target spec: `kogen-spec` commit `e19dd1c21c19c5be1201c3b6a42c59c28b5c2887`, v1.3-draft. Read `CHANGES-v1.3.md` §3, `spec/02-formats.md` §§2.8–2.9, `spec/03-build.md` §§3.4 and 3.10, and `spec/05-sandbox-custody.md` §5.4.
- Rust reference: `kogen-rs` commit `a402540b39cedc7f788472297add7ae2f8a6631a`; inspected `crates/kogen-core/src/safe_fs.rs` and `crates/kogen-core/src/intent/shaping/snapshot.rs`.
- Frozen oracle: `~/cx/kgo/inputs/conformance-v1.2`, sourced from conformance commit `0f93bad988fb8d7a8eff4e94954d1db0a046c89d`.
- Host/toolchain: macOS 26.7.1, `darwin/arm64`, Go 1.27.1, Git 2.54.0, Python runner 3.14.7, `golang.org/x/sys v0.48.0` vendored.

## Implementation and local effects

- Added `PublishPrivate` and `AppendPrivate`, fixing controller metadata at mode 0600. They use the existing rooted publisher: cryptographically random exclusive 0600 temporary file, file fsync, atomic no-follow leaf replacement/create-only publication, and parent-directory fsync. Existing multiply linked regular leaves are rejected; replacement never writes through a symlink leaf.
- Added rooted `CopyTree`, which opens regular source leaves without following them, publishes independent byte copies, preserves file modes and newly created directory modes, recreates only relative symlinks contained by the copied subtree, rejects special files, and refuses overlapping source/destination trees. A failed direct copy can leave earlier entries; Snapshot/Restore are the all-or-nothing directory APIs.
- Added `Snapshot`, which copies into an exclusive private 0700 sibling directory, syncs file and directory contents, and publishes by exclusive no-replace rename. Added `Restore`, which builds and syncs a private replacement tree first, atomically exchanges it with an existing destination, then removes the old tree without following symlinks. If exchange is unsupported, the old destination remains in place and Restore returns an error.
- Unit fixtures passed for state/cache/staging symlink leaf replacement and symlinked-parent refusal, private file modes, create-only behavior, hardlink refusal, byte/mode copies without shared inodes, contained symlink copies, escape refusal, no-follow source reads, private snapshots, create-only snapshot publication, exact restore, and restore replacing a symlink leaf without changing its external target.
- Publication-path audit in `internal/safefs`: `Publish` and `Append` are the only regular-file publication primitives; `CopyTree` routes files through `Publish`; `Snapshot`/`Restore` route data through `CopyTree` and descriptor-relative directory create/rename/sync operations. No controller file writes were added outside `safefs`.

## Commands and results

Commands used the pinned worker PATH from `WORKER-RULES.md`.

| Command | Result |
|---|---|
| `GOMAXPROCS=2 go test -count=1 -parallel=2 ./internal/safefs` | Passed on the final source. During implementation an earlier focused run caught a temp-parent resolution bug and an incorrect test assumption that `CopyTree` rolled back partial copies; both were corrected before the passing run. |
| `GIT_CONFIG_GLOBAL=/dev/null make check` | Passed on the committed source: format check, vendor fingerprints, `go vet ./...`, full tests with `-parallel=2`, and both builds. |
| `GOOS=linux GOARCH=amd64 CGO_ENABLED=0 GOFLAGS='-mod=vendor -p=2' GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off GOMAXPROCS=2 go test -c -o /tmp/kgo-safefs-linux-amd64.test ./internal/safefs` | Passed as a Linux/amd64 compile-only check. The binary was not run; this is not Linux runtime evidence. |
| `git diff --check` | Passed. |
| Assigned frozen v1.2 command below | Runner exit 1: 5 cases / 5 instances, 0 pass, 5 fail, 0 error, 0 skipped, 0 unimplemented. Exact result JSONL and workdirs retained. |

The assigned oracle command was run once at `2026-10-07T18:04:13Z`. At that point the CLI was already the unwired bootstrap binary and the safefs implementation was the working-tree draft before its final no-follow-read/atomic-exchange hardening was committed as `10803e7`. The binary did not import or exercise the safefs package. The failed result is retained and was not rerun to replace it.

```sh
make build
SUITE="$HOME/cx/kgo/inputs/conformance-v1.2"
EVIDENCE="$HOME/cx/kgo/evidence/08-durable-writes-snapshots"
mkdir -p "$EVIDENCE"
PYTHONDONTWRITEBYTECODE=1 PATH="$HOME/.local/share/mise/installs/python/3.14.7/bin:$PATH" \
  "$SUITE/bin/kogen-conformance" run --kogen "$PWD/bin/kogen" \
  --profile cli,state,approval,shape,build,ladder,provider,custody,format,v1.2 --case 'approval-19,approval-20,provider-26,v1.2-06-crash-after-base-cas,v1.2-31-provider-23-tool-result-budget' \
  --jobs 2 --time-scale 0.02 --workdir "$EVIDENCE/work" \
  --out "$EVIDENCE/results.jsonl"
```

Resolved instances and actual failures:

| Case | Instances | Result |
|---|---:|---|
| `approval-19` | 1 | Failed at `kogen intent approve greet`: exit 2, expected 3; stderr was the bootstrap stub. |
| `approval-20` | 1 | Failed at `kogen intent approve greet`: exit 2, expected 5; stderr was the bootstrap stub. |
| `provider-26` | 1 | Failed at `kogen intent shape greet`: exit 2, expected 0; no request reached the fake provider. |
| `v1.2-06-crash-after-base-cas` | 1 | Failed at approval before the crash/CAS path: exit 2, expected 5; stderr was the bootstrap stub. |
| `v1.2-31-provider-23-tool-result-budget` | 1 | Failed at approval before queue/provider execution: exit 2, expected 0; stderr was the bootstrap stub. |

The complete result is `/Users/almirsarajcic/cx/kgo/evidence/08-durable-writes-snapshots/results.jsonl`; per-case fixture workdirs remain under `/Users/almirsarajcic/cx/kgo/evidence/08-durable-writes-snapshots/work`. These are execution failures before the assigned filesystem behaviors, not passing evidence or semantic results.

## Conflicts and closure gates

- Exact v1.2-versus-v1.3-draft behavior conflicts identified by this package: none. `v1.2-06` is compatible with the draft's added pre-cleanup preservation requirement, but its run never reached recovery. Do not treat the bootstrap failures as historical spec conflicts.
- No case is counted as behavior-compatible/pass. Local safefs unit tests and `make check` support a **component** gate only.
- I3 remains open: the approval → queue → provider → gate → CAS → status route is unwired. Rerun the assigned cases in the I3 behavior round and retain this failed run.
- I6 remains open: state/cache/journal/approval-staging/tool-handle/protection-restoration consumers and the full adversarial publication matrix need integration evidence. Linux was compile-checked only; no Linux runtime was available here.
- The planned D-REC-* preservation fixtures are not frozen v1.2 IDs. Wait for shared frozen v1.3 IDs; this package does not implement recovery coordination, run-record publication, candidate refs/archives, writer shutdown, or `cleanup_pending` policy.
- No Quint `R(slice)` replay was run. The coherent migrated v1.3 cohort is not available in the frozen inputs; no replay seeds or divergence are claimed. No live provider/account access or optional runtime evidence was used.
- `Snapshot` does not freeze concurrent source writers; callers requiring a point-in-time tree must stop writers first. `CopyTree` refuses reserved `.git` paths and is intended for rooted data/output trees, not Git metadata or whole-worktree recovery.
