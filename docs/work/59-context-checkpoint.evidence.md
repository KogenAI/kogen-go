# 59-context-checkpoint evidence

## Revision and scope

- Worker branch: `kgo/59-context-checkpoint`; base revision: `002a719ca70e4b6ff997dd8f3cd57e5388daad0a`; component implementation commit: `8c2c9a38dad6f45d2068aa362aa4d686eb25d380`.
- Target specification: `kogen-spec` `e19dd1c21c19c5be1201c3b6a42c59c28b5c2887` (v1.3-draft). Read P13, provider §4.9.1–4.9.2 and §4.9.6, status §1.7.5, and the checkpoint/prefix changes in `CHANGES-v1.3.md`.
- Rust reference: `kogen-rs` `a402540b39cedc7f788472297add7ae2f8a6631a`; reviewed `provider/session.rs`, `provider/session/replay.rs`, and the retry replay checkpoint transition.
- Toolchain/host: Go `go1.27.1 darwin/arm64`, Git `2.54.0`, Python `3.14.7`, macOS `26.7.1 arm64`. Pinned tool paths were placed first on `PATH`.
- Frozen CLI input: `$HOME/cx/kgo/inputs/conformance-v1.2`; runner SHA-256 `0b53a3b60956cbe8dde7af54e274c46b3e8d430c45662d7dff67445f440b39f9`; profile manifest SHA-256 `aef0b5f8787f9d151f3c77aca6ff8edc67ca89a2f6a55ee10fb6326303260273`.
- Built CLI SHA-256: `80f66ba6a0e6cb20683f36a90b7d8e7a974660af89bc33161c6785cfaaaa74c3`; private adapter SHA-256: `d839a650c20b3aed57e8721217be418521e285cc44f7dafa46430640b83c9323`.
- CLI and private adapter sources were unchanged in this component commit; both binaries were built from the same worker tree. The private `kogen-xspec` adapter remains the bootstrap stub.

The owned package now provides an explicit byte-threshold predicate, a new `checkpoint-<turn>` summarizer session that copies raw history and retains the builder role/model/effort/run affinity, a no-tools request configuration that keeps the shared immutable prefix, canonical bounded checkpoint creation, and a digest-epoch continuation containing only the verbatim approved request/plan items plus the checkpoint. Invalid, empty, and oversized results return `continuation_failed`. `context_continuation` is the journal event already counted by status rendering. No credentials or prompt text are emitted in diagnostics.

This is component evidence. The Build controller and public command do not yet invoke this package; journal publication, actual summarizer dispatch and status integration remain with the I6 wiring owner.

## Commands and results

Pinned PATH for commands:

```sh
export PATH="$HOME/.local/share/mise/installs/git/2.54.0/bin:$HOME/.local/share/mise/installs/go/1.27.1/bin:$HOME/.local/share/mise/installs/python/3.14.7/bin:$HOME/.local/share/mise/installs/node/24.21.0/bin:$PATH"
```

- `GOMAXPROCS=2 go test -p=2 -parallel=2 ./internal/optional/checkpoint` — passed. Fixtures cover threshold opt-in, raw-history preservation, no callable tools with all seven shared schemas, new thread epochs/same cache affinity, unchanged approved bytes, canonical digest prefix, invalid UTF-8/empty/oversized terminal results, and routing-state isolation.
- `GOMAXPROCS=2 GIT_CONFIG_GLOBAL=/dev/null make check` — passed under the exclusive `$HOME/cx/kgo/gates.lock/oauth-port1455` mkdir lock. The check completed format, vendor fingerprints, vet, all package tests, and both builds. The lock was acquired and released by this worker's shell trap. Retained log: `$HOME/cx/kgo/evidence/59-context-checkpoint/make-check.log`, SHA-256 `dadb333176584d6d1d1de6d8723e0c6a45dab7487919e5d72d7f0d706cf04fd3`.
- `make build` — passed. Built `bin/kogen` and `bin/kogen-xspec` at the hashes above.
- Frozen v1.2 command — run once with the exact assignment profile, literal ID and overlay:

  ```sh
  SUITE="$HOME/cx/kgo/inputs/conformance-v1.2"
  EVIDENCE="$HOME/cx/kgo/evidence/59-context-checkpoint"
  mkdir -p "$EVIDENCE"
  PYTHONDONTWRITEBYTECODE=1 PATH="$HOME/.local/share/mise/installs/python/3.14.7/bin:$PATH" \
    "$SUITE/bin/kogen-conformance" run --kogen "$PWD/bin/kogen" \
    --profile cli,state,approval,shape,build,ladder,provider,custody,format,v1.2 --case 'v1.2-28-provider-13-planner-no-fallback' \
    --jobs 2 --time-scale 0.02 --workdir "$EVIDENCE/work" \
    --out "$EVIDENCE/results.jsonl"
  ```

  The runner resolved `v1.2-28-provider-13-planner-no-fallback` to one instance. Result: **0/1 passed, 1 failed, 0 errors, 0 skipped**. It failed at step 3 (`kogen queue start`): exit 70 instead of 0; stdout was `controller/internal_error: queue execution is wired in the Build integration round` rather than the expected `building greet` response. The fake provider received no request. Results SHA-256: `d34fe9e7eaf1e51b204c5446ec24ca80ef2c83bf451ffc0f3bc8cd6e0da949ac`; work artifacts remain under `$HOME/cx/kgo/evidence/59-context-checkpoint/work/v1.2-28-provider-13-planner-no-fallback`. No compatible CLI pass is claimed and the result was not retried.

## Replay, conflicts, and effects

- `R(session)` was not run. The available session model/cohort has not been frozen as a coherent migrated v1.3 input set, and the private xspec command is still a bootstrap stub in the dependency evidence. Therefore there is no scratch-copy spec result, no 500 traces × 25 steps for seeds 17/23/41, no full-observation comparison, and no divergence claim. Do not count historical Rust session replay results as Go passes.
- Planned D-* fixtures do not exist as frozen v1.2 cases. Wait for the shared frozen v1.3 IDs and migrated cohort before claiming those gates.
- Exact measured historical conflicts: **none**. The selected case stopped before its planner/provider assertion; the queue-handler failure is an integration gap, not an observed v1.2/draft behavior conflict.
- Local effects: Go tests and `make build` used no live provider or account; local fake servers remain test-only. The conformance runner created the retained results/work directories above. The frozen suite, spec, goldens, replay harness, global Git configuration, and other worktrees were not modified.

## Remaining closure gates

- I6 must wire opt-in `build.context_bytes` thresholding, same-builder/no-tools summarizer dispatch, checkpoint journal event, continuation session, and status count through the real Build controller; then run the P13 effects fixture and assigned integrated cases.
- R(session) requires the shared coherent migrated Quint cohort, same-revision private binary, all hand cases, and 500×25 traces for each seed 17, 23, 41 with full observations. Wait for the frozen shared v1.3 IDs before D-* claims.
- Linux optional/safety parity, provisioned Rails/ExUnit runtime evidence, and live comparison gates remain unclaimed; no live provider or account was used.

This receipt is **component** only. It does not confer P13 behavior acceptance or I6 closure.
