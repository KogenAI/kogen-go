# 51 — Shape deterministic validation evidence

## Revisions and scope

- Worker branch: `kgo/51-shape-deterministic-validation`.
- Worker/source revision: `d0cb3559cb654013645f4f888ba96737c96acbdd` (`Implement deterministic Shape validation`).
- CLI binary revision: built from worker revision `d0cb3559cb654013645f4f888ba96737c96acbdd` by the acceptance command's `make build`.
- Adapter revision: the adapter interface and validation component are at worker revision `d0cb3559cb654013645f4f888ba96737c96acbdd`. No production adapter or Shape command route is wired yet.
- Target spec and change record: `kogen-spec` revision `e19dd1c21c19c5be1201c3b6a42c59c28b5c2887` (`v1.3-draft`).
- Rust reference read: `kogen-rs` revision `a402540b39cedc7f788472297add7ae2f8a6631a`.
- Frozen oracle source revision: `kogen-conformance` base revision `0f93bad988fb8d7a8eff4e94954d1db0a046c89d`; the provided `~/cx/kgo/inputs/conformance-v1.2` copy is not a Git checkout. Runner metadata reports `v1.2+unknown`. The source checkout already had uncommitted v1.3 draft files; neither source nor input copy was edited.
- Owned production files changed: `internal/shape/validate/**`. Evidence and gate receipt are the only other package deliverables changed.

## Commands and results

Pinned worker `PATH` was exported before commands as required.

1. `GOMAXPROCS=2 go test -p=2 -parallel=2 ./internal/shape/validate ./internal/intent ./internal/shape/prompts` — passed. This includes exact parser feedback for rejected `syn-06` empty-map frontmatter (missing `title`) and `syn-20` frontmatter (missing `size`).
2. `GIT_CONFIG_GLOBAL=/dev/null GOMAXPROCS=2 make check` — passed: formatter, vendor fingerprints, vet, all repository tests, and both builds.
3. `make build` — passed as the first line of the acceptance command.
4. Frozen v1.2 command (exit 1):

```sh
SUITE="$HOME/cx/kgo/inputs/conformance-v1.2"
EVIDENCE="$HOME/cx/kgo/evidence/51-shape-deterministic-validation"
mkdir -p "$EVIDENCE"
PYTHONDONTWRITEBYTECODE=1 PATH="$HOME/.local/share/mise/installs/python/3.14.7/bin:$PATH" \
  "$SUITE/bin/kogen-conformance" run --kogen "$PWD/bin/kogen" \
  --profile cli,state,approval,shape,build,ladder,provider,custody,format,v1.2 --case 'shape-01,shape-03,shape-04,shape-11,shape-15,shape-16,shape-17,shape-18' \
  --jobs 2 --time-scale 0.02 --workdir "$EVIDENCE/work" \
  --out "$EVIDENCE/results.jsonl"
```

Resolved cases and results: `shape-01` 1 instance, `shape-03` 1, `shape-04` 1, `shape-11` 1, `shape-15` 1, `shape-16` 2 (`1:red`, `2:127`), `shape-17` 1, `shape-18` 1. Total: **8 cases / 9 instances; 0 passed, 9 failed, 0 skipped, 0 errors**. All failures stop at `kogen: implementation bootstrap; command routes are not wired`; the fake provider received no requests. No selected case reached this validation component. The complete unmodified result is retained at `/Users/almirsarajcic/cx/kgo/evidence/51-shape-deterministic-validation/results.jsonl`.

Compatible behavior passes: **none**. The unit tests exercise the component boundary only and are not counted as conformance passes.

## Component behavior and local effects

The validation component delegates source/candidate path selection to `Adapter`; it does not choose an extension. It clears stale ledger/warning artifacts before invoking setup, uses `safefs` for worktree reads, normalization writes, staging, and restoration, and checks that staged and base acceptance files retain their exact bytes and modes. It normalizes Notes, appends raw Request bytes, parses and lints, checks gate declarations, reports a missing formatter, stages the candidate test with create-only semantics, detects tree mutation, reclassifies base results, and reports `all_items_keep` after persisting the reclassification. If an adapter candidate directory did not exist, staging can leave its empty parent directories; they add no Git tree entries.

Focused tests use temporary directories. They cover CRLF and invalid UTF-8 Request preservation, rejected frontmatter and exact repair feedback, no extension assumption, stale-artifact cleanup ordering, formatter warnings, style repair/warnings, staged-path conflicts/restoration, staged/base source mutation, reclassification and `all_items_keep`. The oracle command created only its prescribed evidence work directory and results file; its cases did not reach provider or Shape effects.

## Conflicts and remaining closure

- Exact v1.2/draft behavioral conflicts identified: **none**. Since every black-box case stopped before Shape ran, this is not evidence that historical behavior matches the draft.
- I5 remains open: wire the Shape route and concrete acceptance adapter, then run the selected IDs on the integrated revision and retain a behavior receipt. The current JSONL is a failed component-bootstrap run and cannot close I5.
- R(slice) was not run. Closure requires a scratch copy of the shared, coherent migrated Quint cohort, the spec model, 500 traces × 25 steps for each seed `17`, `23`, and `41`, and full observations against a same-revision private binary. No divergence was measured.
- Planned `D-SHAPE-*` cases are not frozen v1.2 IDs. Wait for shared frozen v1.3 IDs before claiming those gates.
- Linux, optional runtime, live comparison, I7 replay, and I8 release gates remain unverified and require their stated external evidence.

Accepted gate: **component**. Compiled component evidence does not confer behavior acceptance.
