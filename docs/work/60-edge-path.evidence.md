# 60-edge-path evidence

## Revision and scope

- Worker branch: `kgo/60-edge-path`.
- Edge component commit: `10ee16d` (`Implement optional edge test path`). Worker base before implementation: `ed6389692343bb16cae954014972954f261b097d`.
- Target spec: `kogen-spec` `e19dd1c21c19c5be1201c3b6a42c59c28b5c2887`; Rust reference: `kogen-rs` `a402540b39cedc7f788472297add7ae2f8a6631a`.
- Frozen suite source checkout: `kogen-conformance` `0f93bad988fb8d7a8eff4e94954d1db0a046c89d`. The provided input copy is not a Git checkout; the runner identified it as `v1.2+unknown`.
- The `kogen` CLI was built from the worker worktree at base commit `ed6389692343bb16cae954014972954f261b097d`, using Go 1.27.1. The edge package is not wired into the public CLI. There is no production edge generator/runner adapter revision in this worktree.
- Scope: `internal/optional/edge/**` only, plus this evidence and gate receipt. The package provides a fixed shared recipe, byte-preserving Request generation port, strict generated-suite assessment, and deterministic bounded parallel cross-checks. Failed, empty, skipped, unavailable, timed-out, malformed, or tree-mutating checks cannot pass. Edge success remains conjunctive with approved acceptance.

## Commands

Local component fixtures:

```sh
GOMAXPROCS=2 go test -p=2 -parallel=2 ./internal/optional/edge
```

Required check on the final implementation source (the source was committed unchanged immediately afterward):

```sh
GIT_CONFIG_GLOBAL=/dev/null GOMAXPROCS=2 make check
```

The check passed: formatting, vendor fingerprints, `go vet`, the full Go test suite, and both binary builds.

Frozen v1.2 command, using the exact selected IDs from `docs/work/60-edge-path.cases`:

```sh
make build
SUITE="$HOME/cx/kgo/inputs/conformance-v1.2"
EVIDENCE="$HOME/cx/kgo/evidence/60-edge-path"
mkdir -p "$EVIDENCE"
PYTHONDONTWRITEBYTECODE=1 PATH="$HOME/.local/share/mise/installs/python/3.14.7/bin:$PATH" \
  "$SUITE/bin/kogen-conformance" run --kogen "$PWD/bin/kogen" \
  --profile cli,state,approval,shape,build,ladder,provider,custody,format,v1.2 --case 'v1.2-102-ladder-34,v1.2-71-ladder-03' \
  --jobs 2 --time-scale 0.02 --workdir "$EVIDENCE/work" \
  --out "$EVIDENCE/results.jsonl"
```

## Frozen results

The runner resolved 2 cases and 2 total instances. Summary: `implemented=2`, `pass=0`, `fail=2`, `error=0`, `skip=0`, `unimpl=0`. Neither selected behavior case passed.

| ID | Instances | Result | Exact observed failure |
|---|---:|---|---|
| `v1.2-102-ladder-34` | 1 | fail | Step 3 `kogen queue start`: exit 70, expected 1. Stdout: `controller/internal_error: queue execution is wired in the Build integration round`. |
| `v1.2-71-ladder-03` | 1 | fail | Step 3 `kogen queue start`: exit 70, expected 0; one stdout line instead of the expected three (`building greet` was expected first). Stdout: `controller/internal_error: queue execution is wired in the Build integration round`. |

Both case records say no provider request reached the fake server because the `KOGEN_PROVIDER_URL` seam was missing. The run stopped at the unwired queue handler before exercising ladder or edge behavior. These are retained as actual v1.2 failures, not counted as passes and not classified as a v1.3 semantic conflict. No retry was made. Full JSONL and workdirs are preserved at `/Users/almirsarajcic/cx/kgo/evidence/60-edge-path/results.jsonl` and `/Users/almirsarajcic/cx/kgo/evidence/60-edge-path/work/`.

## Local effects and remaining gates

- The edge tests used fake generator and runner ports only. They exercised raw Request preservation, the single shared resource recipe, strict zero-test and incomplete-result refusal, timeout/unavailable/tree-mutation blocking, output bounds, stable directed cross-check ordering, concurrency limits, peer-failure attribution, cancellation, and acceptance remaining mandatory. No live provider call, project checkout mutation, or external account access occurred.
- `make build` wrote the normal ignored `bin/kogen` and `bin/kogen-xspec` outputs. `make check` reported no source-tree mutation.
- No Quint replay was run: the shared coherent migrated draft cohort and its release manifest are not frozen for this task. Therefore seeds 17, 23, and 41 have no trace counts or divergence observations here; the required scratch-copy replay of 500 traces ×25 steps per seed remains open.
- Planned D-* fixtures are not frozen v1.2 cases. Wait for shared frozen v1.3 IDs before claiming those gates. No additional standard-suite edge case was asserted.
- Production edge generator/runner wiring, pre-landing integration, and `+edge` cross-check wiring remain for the integration owner. I6 still needs its complete behavior closure. Linux, optional runtime, and live comparison evidence are absent.
- The exact selected v1.2 cases need a later integration rerun only after queue execution is wired on a changed revision; this retained failed run must remain in the record. Package 00 still owns the shared foundation and draft-suite freeze.

## Draft conflicts

No v1.3 behavioral conflict was observed because both frozen cases stopped before provider or ladder execution. The two actual v1.2 failures above are the outstanding compatibility results for this worker invocation.
