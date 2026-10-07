# D3 durable crashed-work preservation evidence

## Revisions and environment

- Worker branch: `kgo/D3-durable-crashed-work-preservation`. Implementation commit: `39eb6f1bb0fac0b46f1492b583a34bccd349a928`; starting base: `280a500e63d061a885616aea2d21bc2a0b384fd1`.
- Target: `kogen-spec` v1.3-draft `e19dd1c21c19c5be1201c3b6a42c59c28b5c2887`; relevant clauses are §2.5.3 and §2.8 in `spec/02-formats.md`, §3.10 in `spec/03-build.md`, and §5.4 in `spec/05-sandbox-custody.md`. `CHANGES-v1.3.md` §3 defines the blocker and planned cases.
- Rust reference: `kogen-rs` `a402540b39cedc7f788472297add7ae2f8a6631a`; read `recovery/project.rs`, `recovery/replay.rs`, `recovery/project/tests.rs`, and the landing crash-recovery effect test. Those references precede the v1.3 durable-preservation behavior and are historical context only.
- Host: macOS 26.7.1 (25G241), Darwin 25.6.0 arm64. Pinned tools: Git 2.54.0, Go 1.27.1, Python 3.14.7, Node 24.21.0.
- CLI and private adapter source revision: the same implementation commit. `make check` built `kogen` and `kogen-xspec`; neither command route nor recovery adapter was run for behavior acceptance. Public command integration remains I3 work.
- Frozen v1.2 input is `~/cx/kgo/inputs/conformance-v1.2`, reference `0f93bad988fb8d7a8eff4e94954d1db0a046c89d`. `docs/work/D3-durable-crashed-work-preservation.cases` is empty, so no v1.2 runner invocation or resolved case instances are assigned to this component.

## Component effects and local evidence

`internal/recovery/preserve` implements the existing `recovery.PreservationPort`. It freezes a candidate against the immutable base through `gitio.BuildCandidateTree`, which uses trusted base-tracked paths and native Git ignore evaluation. It copies the exact tree into the origin object database and publishes the deterministic `refs/kogen/candidates/<run>/recovery-<workspace>` ref create-only, pointing directly to the tree object. It adopts a matching existing ref after a publication/record crash and never replaces a ref that names earlier work.

When origin ref publication fails or that deterministic ref already names a different tree, the effect writes a deterministic content-addressed archive under the private state root. The archive has a versioned manifest, raw path names, Git modes, symlink targets, exact bytes, per-entry SHA-256 values, and a full-file checksum. `safefs` publishes it create-only with mode 0600 and fsyncs the file and parent directories; archive directories are private. If workspace Git cannot form a candidate tree, the fallback archives all non-`.git` workspace files, including ignored files, to avoid losing bytes while Git is unavailable. Both record forms are `unverified`.

Local real-Git tests cover:

- `TestPreserveCapturesLatestBaseRelativeBytesModesAndSymlinks`: tracked edit/deletion, nonignored untracked data, ignored-file exclusion, binary bytes, executable mode, and symlink target.
- `TestCreateOnlyRefAdoptionAndLaterWorkGetSeparateArchiveIdentity`: record-crash ref adoption; later differing bytes use a separate archive while the earlier ref remains.
- `TestRefPublicationFailureFallsBackToAdoptableLosslessArchive`: ref failure, private archive content, and deterministic archive adoption.
- `TestGitTreeFailureStillArchivesWorkspaceWithoutLosingIgnoredFiles`: raw archive fallback after workspace Git failure.
- `TestBothPublicationFailuresLeaveWorkspaceAndPriorCandidateRef` and `TestControllerRetainsPendingWorkspaceOnDualFailureAndRetriesTerminalCleanup`: both publication failures retain latest workspace bytes and a prior candidate; the controller records terminal `failed` plus `cleanup_pending`, then retries through the real archive store, cleans up, and preserves the original terminal outcome.
- `TestArchiveEncodingIncludesManifestAndChecksum`: archive corruption detection.

The component does not choose landing status. The controller's existing post-CAS reconciliation remains responsible for keeping a reachable candidate `landed`; preservation records are never treated as verified landing trees.

## Commands and results

| Command | Result |
| --- | --- |
| `GOMAXPROCS=2 go test -p=2 -parallel=2 ./internal/recovery/preserve` | Passed; seven local tests. |
| `GOMAXPROCS=2 GOFLAGS=-p=2 GIT_CONFIG_GLOBAL=/dev/null make check` | Passed: formatting, vendor fingerprints, vet, all Go tests, and both CLI builds. The new preservation tests passed in the full run. |
| Frozen v1.2 conformance | No command assigned: the D3 `.cases` file is empty. Resolved IDs/instances: 0/0. |
| Quint R(recovery) | Not run. The shared coherent migrated v1.3 Quint cohort and same-revision private binary observation path are not available for this package. |

The standalone package test was also included in the successful `make check` run. No failed acceptance run is omitted. No source oracle, suite, or golden was changed.

## Conflicts, replay, and closure gates

- Historical v1.2 semantic conflicts observed: none. No v1.2 cases were assigned or executed, so this is not a compatibility pass.
- Planned `D-REC-01`–`D-REC-06` are not frozen v1.2 cases and have no shared v1.3 IDs yet. Their planned coverage includes latest state before cleanup, adoption after publication/record crash, changed later bytes, dual publication failure, repeated recovery/live-owner safety, and post-CAS landed status with later work unverified. These IDs are not counted as passing.
- R(recovery) seeds 17, 23, and 41 remain deferred; each requires 500 traces ×25 steps on a scratch copy of the coherent migrated cohort and full observations from the same-revision private binary.
- I3 must wire this production preserver into the executable approval → queue → provider → gate → CAS → status path, then run the newly frozen D-REC fixtures. Package 00 remains a real foundation task. Linux runtime, optional runtime, and live comparison evidence remain external closure gates.

This is **component-ready only**. It does not confer behavior acceptance or I3 closure.
