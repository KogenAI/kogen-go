# 21-status-derivation evidence

## Revisions and scope

- Worker branch: `kgo/21-status-derivation`; base revision before this package: `e641b4e8b18a4e2d548cd2da88765493706578e9`; implementation/source commit: `e508ea1` (`Implement status derivation`).
- Target: `kogen-spec` v1.3-draft, `e19dd1c21c19c5be1201c3b6a42c59c28b5c2887`; status precedence from `spec/02-formats.md` §2.11 and display/order requirements from `spec/01-cli.md` §1.7.5. Reviewed `CHANGES-v1.3.md` at the same revision.
- Rust reference: `a402540b39cedc7f788472297add7ae2f8a6631a`, especially `crates/kogen-core/src/status/model.rs`, `status/project.rs`, and `crates/kogen-cli/src/handlers/status/{report,render}.rs`.
- Go: `go1.27.1 darwin/arm64`. `bin/kogen` SHA-256 at the acceptance run: `104983b316000d63b724ee57b5f1fbe58e90e8f895a896117159edef18d8c383`. `bin/kogen-xspec` SHA-256: `da4928db73e09751b89517dfb9d6426f1754a777e7ccc5d24062d6982a6b6e56`; it is still the bootstrap binary. No status adapter is wired (`internal/xspec/queuestatus` contains only its package stub).
- Oracle: frozen v1.2 input copy, `VERSION=1.2`; runner SHA-256 `0b53a3b60956cbe8dde7af54e274c46b3e8d430c45662d7dff67445f440b39f9`. The input copy is not itself a Git checkout.

`internal/status/derive` is a pure transition over already collected facts. It selects the latest run per slug, applies reachable-trailer / claim / current-approval / interrupted precedence, derives invalid, unknown, cyclic, and pending dependency blocks, and sorts the queue by descending priority, approval time, then slug. Reached landing trailers are independent of the checkout's current Intent bytes, as required by §2.11. No I/O or CLI wiring was added outside the owned subtree.

## Commands and results

- `GOMAXPROCS=2 go test -count=1 -parallel=2 ./internal/status/derive` — passed.
- `GIT_CONFIG_GLOBAL=/dev/null make check` — passed, including format, vendor fingerprints, vet, tests, and both builds. Repeated after the final implementation edit.
- `git diff --check` — passed.
- Ran the assigned command against all 12 literal selected case IDs, with the full standard profiles and v1.2 overlay. It expanded to 13 instances (`v1.2-24-state-14-grok-account-row` has two). Result: **0 passed, 12 failed, 0 errors, 0 skipped**. The retained JSONL is `/Users/almirsarajcic/cx/kgo/evidence/21-status-derivation/results.jsonl`.

Exact acceptance command:

```sh
make build
SUITE="$HOME/cx/kgo/inputs/conformance-v1.2"
EVIDENCE="$HOME/cx/kgo/evidence/21-status-derivation"
mkdir -p "$EVIDENCE"
PYTHONDONTWRITEBYTECODE=1 PATH="$HOME/.local/share/mise/installs/python/3.14.7/bin:$PATH" \
  "$SUITE/bin/kogen-conformance" run --kogen "$PWD/bin/kogen" \
  --profile cli,state,approval,shape,build,ladder,provider,custody,format,v1.2 --case 'state-17,state-18,state-19,state-21,state-24,state-30,v1.2-132-state-23,v1.2-22-cli-25-status-overview,v1.2-23-cli-26-status-slug,v1.2-24-state-14-grok-account-row,v1.2-25-state-20-status-next,v1.2-26-state-22-status-next' \
  --jobs 2 --time-scale 0.02 --workdir "$EVIDENCE/work" \
  --out "$EVIDENCE/results.jsonl"
```

Every case stopped at an unwired bootstrap route (`kogen: implementation bootstrap; command routes are not wired`). Approval cases fail before reaching status; status cases fail when invoking `kogen status`; the two account-list instances fail at `kogen provider list`. These are not status-derivation behavior passes or direct derivation failures. No selected v1.2 case is counted compatible-pass from this run.

The exact historical conflict is `v1.2-132-state-23`: after a reachable commit carries `Kogen-Intent: greet`, the case replaces the current Intent and expects `greet: draft; review it with kogen intent approve greet`, then expects it to become queueable after reapproval. Draft §2.11 instead says the current checkout's Intent is not compared and a reused slug remains landed until the trailer is gone. The run failed at the first `kogen status greet` because the command route is absent, so that conflicting assertion was not dynamically reached. The implementation follows the current draft clause; it does not reinstate v1.2 slug demotion.

## Deferred evidence and closure gates

- I1 owns status CLI/render wiring and synthetic-state integration. Until that wiring lands, C25–26, S17–24/S30 and V22–26/V132 cannot establish behavior acceptance here. I3 still owns the live approval/queue/status path.
- `R(status)` is deferred to package 66. No Quint replay was run: the `kogen-spec` checkout at the target commit has 41 working-tree changes, including the shared `quint/prototype/harness/xspec.py` and `quint/slices/queue/spec/queue.qnt`; therefore there is no confirmed coherent shared cohort to copy. The Go `kogen-xspec` status adapter is also unwired. Do not count any hand scenarios or the 500×25 traces for seeds 17, 23, and 41 until package 00 freezes the shared cohort and package 66 supplies the same-revision production adapter. No divergence is claimed.
- New shared v1.3 status case IDs and migrated model/golden observations are not available; package 00 remains the foundation for that closure. D-* fixtures are not claimed.
- This is a component-ready result only. Linux/runtime evidence and live provider/account calls were not applicable or attempted for this pure package.
