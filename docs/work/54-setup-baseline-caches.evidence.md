# 54-setup-baseline-caches evidence

## Revision and scope

- Worktree/branch: `/Users/almirsarajcic/Areas/Kogen/kogen-go-wt/54-setup-baseline-caches`, `kgo/54-setup-baseline-caches`.
- Parent revision used for the worker tree: `48946b7ff7f0982c1c7cca1c4513ea86bf8c9f5b`.
- `internal/setupcache` source tree used for the final checks: `acf34c11242a3257d7aa523edc109d35b2bfcb05`.
- CLI binary SHA-256 after the final `make build`: `f4d40f340eb3b3393040ba0f1909cd892f5e23da9a9d332382fec634a7ad6aa2`.
- Host: macOS 26.7.1 arm64; pinned Git 2.54.0 and Go 1.27.1.
- The target draft is identified as `e19dd1c` in the work queue. This checkout contains no `spec/02-formats.md`, `spec/03-build.md`, `CHANGES-v1.3.md`, or Rust modules. `docs/work/INPUTS.md` provides the available D2 summary and says the shared v1.3 cases are not frozen.

## Implementation

`Cache` implements the existing `prepare.SetupCachePort` and `prepare.BaselineV3Port` interfaces. Its setup v2 identity hashes the declared `setup_inputs` bytes and modes, output paths, setup commands/deadlines, filtered setup-key environment, toolchain, and platform. It deliberately excludes `BaseTree`, so an unrelated source-only base change can hit the setup cache. Unknown input/toolchain/environment identities receive a unique volatile key and cannot hit the reusable v2 or v3 cache. Setup products are published beneath a rooted cache directory; directory snapshots and restores use the workspace COW seeder, with byte-copy fallback where the platform does not support COW. The persistent setup LRU retains at most three reusable v2 entries.

Baseline results use a separate `baseline/v3` namespace and the canonical key produced by `prepare.NewBaselineKey`. The cache validates the digest against the full checked tree, setup key, ordered checks and deadlines, environment, toolchain, platform, and adapter version. Unknown identities and non-v3 entries run checks without reuse. Cached rows preserve finding line data needed by card rendering. The baseline compute callback runs outside the cache mutex so a mutating check can call `Restore` without deadlocking.

Local tests cover source-only tree changes reusing setup products, input changes and three-entry eviction, filtered environment identity, COW product isolation, baseline tree/deadline/context misses, row preservation, and unknown/legacy misses. These are component tests; they do not close D2 or establish cross-language fixture parity.

## Commands and results

Pinned-tool prefix used for worker commands:

```sh
export PATH="$HOME/.local/share/mise/installs/git/2.54.0/bin:$HOME/.local/share/mise/installs/go/1.27.1/bin:$HOME/.local/share/mise/installs/python/3.14.7/bin:$HOME/.local/share/mise/installs/node/24.21.0/bin:$PATH"
```

Final component test command passed:

```sh
GIT_CONFIG_GLOBAL=/dev/null GOMAXPROCS=2 go test -p=2 -parallel=2 ./internal/setupcache
```

Final full check passed on the source tree above:

```sh
GIT_CONFIG_GLOBAL=/dev/null GOMAXPROCS=2 make check
```

An earlier full check on this same final source tree failed once in the unrelated `internal/queue/lock` test `TestConcurrentAcquireCreatesOneOwner` (`queue.pid is not a safe regular owner file`; its TempDir cleanup also reported a nonempty directory). The subsequent full check above passed, including format, vendor fingerprints, vet, tests, and both builds. The queue package is outside this worker's ownership.

The assigned frozen v1.2 command was run exactly once to the required results path:

```sh
make build
SUITE="$HOME/cx/kgo/inputs/conformance-v1.2"
EVIDENCE="$HOME/cx/kgo/evidence/54-setup-baseline-caches"
mkdir -p "$EVIDENCE"
PYTHONDONTWRITEBYTECODE=1 PATH="$HOME/.local/share/mise/installs/python/3.14.7/bin:$PATH" \
  "$SUITE/bin/kogen-conformance" run --kogen "$PWD/bin/kogen" \
  --profile cli,state,approval,shape,build,ladder,provider,custody,format,v1.2 --case 'approval-12,approval-13,state-28,v1.2-133-state-26,v1.2-134-state-27' \
  --jobs 2 --time-scale 0.02 --workdir "$EVIDENCE/work" \
  --out "$EVIDENCE/results.jsonl"
```

Results: [`results.jsonl`](/Users/almirsarajcic/cx/kgo/evidence/54-setup-baseline-caches/results.jsonl). The runner resolved these five literal IDs, one instance each: `approval-12`, `approval-13`, `state-28`, `v1.2-133-state-26`, `v1.2-134-state-27`. Outcome: **0 passed, 5 failed, 0 errors, 0 skipped**.

Every failure stopped at the public `kogen intent approve` command, which returned exit 2 and stderr `kogen: implementation bootstrap; command routes are not wired`. The expected exits were 5 for `approval-12` and `state-28`, 3 for `approval-13`, and 0 for both v1.2 replacements. The v1.2 rows also report that no provider request reached the fake server. The production CLI does not wire or call this cache component, so these cases are not counted as component passes.

After the final source change, the same selection was run again with a separate work directory and results path, preserving the original failure:

```sh
make build
FINAL_EVIDENCE="$HOME/cx/kgo/evidence/54-setup-baseline-caches/final-revision"
mkdir -p "$FINAL_EVIDENCE"
PYTHONDONTWRITEBYTECODE=1 PATH="$HOME/.local/share/mise/installs/python/3.14.7/bin:$PATH" \
  "$HOME/cx/kgo/inputs/conformance-v1.2/bin/kogen-conformance" run --kogen "$PWD/bin/kogen" \
  --profile cli,state,approval,shape,build,ladder,provider,custody,format,v1.2 --case 'approval-12,approval-13,state-28,v1.2-133-state-26,v1.2-134-state-27' \
  --jobs 2 --time-scale 0.02 --workdir "$FINAL_EVIDENCE/work" \
  --out "$FINAL_EVIDENCE/results.jsonl"
```

That final-revision run also failed all five cases at the same unwired CLI route. Its results are preserved at `/Users/almirsarajcic/cx/kgo/evidence/54-setup-baseline-caches/final-revision/results.jsonl`.

## Conflicts and closure gates

- No historical v1.2 spec conflict was observed; the selected cases failed before reaching setup or baseline cache behavior. No v1.2 failure is treated as a pass.
- Planned `D-BASE-01–05`, v2/v3 cross-language canonical fixtures, and the approve `baseTree` trace are not frozen v1.2 cases. They were not run and remain deferred to shared frozen v1.3 IDs and D2/I5 closure.
- I5 must wire `Cache` into the production preparation composition and close the public approval route, then rerun the assigned cases on the integrated revision while retaining these results.
- D2 still owns the reserved exact-base regressions: source-only base change (setup hit and baseline miss), changed red/green rows, full-context/unknown/legacy misses, dirty checkout checked in exact-base scratch, and same-tree card/hash reuse.
- No coherent migrated Quint cohort or Rust reference modules are in this checkout. R(slice) and the cross-language canonical fixtures were not run.
- Linux runtime, optional-runtime, and live comparison evidence remain external gates. No account access or live provider calls were made.

The component gate is ready; behavior acceptance is not claimed.
