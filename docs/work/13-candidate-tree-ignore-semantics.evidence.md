# 13-candidate-tree-ignore-semantics evidence

## Revisions and environment

- Worker branch: `kgo/13-candidate-tree-ignore-semantics`.
- Implementation commit: `b8b89d621a6ec4963b246c77dbce3a8b653733aa`.
- Parent revision: `363f15f43829edd9716fe6fdc71a5a4a88b0ea2c`.
- Target spec: v1.3-draft `e19dd1c21c19c5be1201c3b6a42c59c28b5c2887`.
- Rust reference: `a402540b39cedc7f788472297add7ae2f8a6631a`.
- Frozen v1.2 suite reference: `0f93bad988fb8d7a8eff4e94954d1db0a046c89d`; the supplied suite copy has no Git metadata.
- Conformance runner: `kogen-conformance` `v1.2+unknown`; `runner.py` SHA-256 `0135fd9095294b3701fc51ebdcc23e596b75f91cd53c10f365dead6824ed8f0e`; `fake_server.py` SHA-256 `0c23f7b9b6c5603a39ba6fe18d64930bd3d45e443efcacc8c310177f467ce08f`.
- Pinned tools: Go 1.27.1, Python 3.14.7, Git 2.54.0. Host: `macOS-26.7.1-arm64-arm-64bit-Mach-O`.
- CLI built from this implementation: `bin/kogen` SHA-256 `d97704dc8f022bef326a784cf880e13808a4e060c400fde51dd88a0c9aa0912b`; `bin/kogen-xspec` SHA-256 `a69c059ea0da93465b0400a1b21a802a95af5f0eb60a3653466ef1bb6005bf49`.

## Implemented component

`internal/gitio` now loads trusted tracked paths from the immutable base through a private index, captures workspace files through `safefs`, and applies native `git check-ignore --no-index -z --stdin` using isolated private Git metadata. Workspace `.git/info/exclude`, global excludes, inherited Git repository selectors, and unsafe `.git` replacement do not control candidate eligibility. Base-tracked paths remain eligible when newly ignored.

Candidate objects are built with `hash-object --no-filters`, NUL-delimited `update-index --index-info`, and `write-tree` through a private index. Exact tree input bypasses ignore filtering for controller-supplied approved bytes. Tests cover nested negation, dotfiles, `.git` poisoning, staged/index and HEAD movement, ignored tracked paths, info/global excludes, raw bytes, newline paths, executable/symlink modes, deletion, rename, and SHA-256.

## Commands and results

Commands ran with the pinned tool paths required by `WORKER-RULES.md`.

1. `GIT_CONFIG_GLOBAL=/dev/null GOMAXPROCS=2 go test -p=2 -parallel=2 -count=1 ./internal/gitio` — passed twice during implementation. The final newline-path fixture was then included in the full package run below.
2. `git diff --cached --check` — passed before the implementation commit.
3. `GIT_CONFIG_GLOBAL=/dev/null make check` — failed in `internal/gitio` at the existing `TestRunnerSeparatesStreamsAndTimesOutHangingGit` assertion in `gitio_test.go:149`. It expected captured stdout `"stdout-before-hang\n"` and stderr `"stderr-before-hang\n"`; the timed-out helper returned empty stdout and stderr. `tools/check.sh` had passed formatting, vendor fingerprint verification, and `go vet ./...` before the test failure. The test is in package 12's `exec.go` boundary, outside this package's owned files. The repository check did not reach its own temporary binary builds.
4. The assigned acceptance command ran once. `make build` succeeded before the conformance runner started:

```sh
export PATH="$HOME/.local/share/mise/installs/git/2.54.0/bin:$HOME/.local/share/mise/installs/go/1.27.1/bin:$HOME/.local/share/mise/installs/python/3.14.7/bin:$HOME/.local/share/mise/installs/node/24.21.0/bin:$PATH"
make build
SUITE="$HOME/cx/kgo/inputs/conformance-v1.2"
EVIDENCE="$HOME/cx/kgo/evidence/13-candidate-tree-ignore-semantics"
mkdir -p "$EVIDENCE"
PYTHONDONTWRITEBYTECODE=1 PATH="$HOME/.local/share/mise/installs/python/3.14.7/bin:$PATH" \
  "$SUITE/bin/kogen-conformance" run --kogen "$PWD/bin/kogen" \
  --profile cli,state,approval,shape,build,ladder,provider,custody,format,v1.2 --case 'v1.2-122-custody-09,v1.2-123-custody-10,v1.2-64-build-30' \
  --jobs 2 --time-scale 0.02 --workdir "$EVIDENCE/work" \
  --out "$EVIDENCE/results.jsonl"
```

The result JSONL is retained at `/Users/almirsarajcic/cx/kgo/evidence/13-candidate-tree-ignore-semantics/results.jsonl`; work directories are under `/Users/almirsarajcic/cx/kgo/evidence/13-candidate-tree-ignore-semantics/work`. The runner reported 3 selected cases, 3 instances, 0 passed, 3 failed, 0 errors, and 0 skipped.

## Acceptance resolution

| Effective case ID | Instances | Result |
| --- | ---: | --- |
| `v1.2-122-custody-09` | 1 | Failed before custody behavior: `kogen intent approve greet {hash8:greet}` exited 2 with `kogen: implementation bootstrap; command routes are not wired`. |
| `v1.2-123-custody-10` | 1 | Failed before candidate-tree behavior with the same bootstrap error. |
| `v1.2-64-build-30` | 1 | Failed before candidate-tree behavior with the same bootstrap error. |

The fake provider was not reached. These results are not behavior passes. No historical v1.2/v1.3 assertion conflict was observed; the requested behavior was unreachable, so the exact conflict list is empty.

## Local effects and closure gates

- `make build` wrote the ignored binaries under `bin/`. Tests used temporary Git fixtures. The frozen suite, its goldens, and source oracle were not modified. `GIT_CONFIG_GLOBAL=/dev/null` was set per command; no global configuration was changed. No live provider or account was used.
- The component has code and fixture evidence, but behavior acceptance remains with I3. After integration wires the real approve → queue → provider → gate → CAS → status path, rerun these three selected cases on the integrated revision and retain the new JSONL. The recorded scaffold failures remain preserved.
- No coherent migrated v1.3 Quint cohort was available. R(slice) was not run; seeds 17, 23, and 41 each still require 500 traces × 25 steps against a scratch copy and same-revision private binary.
- Planned D-* fixtures are not frozen v1.2 cases. Wait for shared frozen v1.3 IDs before claiming their gates. Linux, optional runtime, live comparison, and full 236-case/570-instance suite evidence were not produced by this package run.
- `GIT_CONFIG_GLOBAL=/dev/null make check` remains failed as recorded above; the passing package run does not replace the required repository check.
