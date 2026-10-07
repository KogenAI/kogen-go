# 26-sse-decoder evidence

Status: **component-ready; behavior acceptance remains open**.

## Revisions and inputs

- Worker branch: `kgo/26-sse-decoder`.
- Decoder implementation: `ce2353b27b4736329b3985e5d81f23d927681bc1`.
- CLI source at the oracle run: the pre-final-follow-up worktree based on `b19ed1c9af20fd147892090d6b20a491cc346659`, before it was committed. The exact run failed at approval, before reaching provider code. The final `make check` rebuilt `bin/kogen` from implementation commit `ce2353b27b4736329b3985e5d81f23d927681bc1`; that binary's SHA-256 is `2ac60928091d54c1f01f47a48c5168ced645ca3822fb6624b5a9045399d52134`. The pre-final binary hash was not captured before the final check rebuilt it.
- Adapter source revision: `ce2353b27b4736329b3985e5d81f23d927681bc1`. The final `bin/kogen-xspec` SHA-256 is `73cfa58997b1fe4c00724aa0ffd46592e09921d9fed8cb442de817bb193c016f`. This package adds no xspec slice or provider-route wiring, and the CLI run did not invoke this adapter.
- Spec target: `kogen-spec` `e19dd1c21c19c5be1201c3b6a42c59c28b5c2887`; the frozen provider spec digest is recorded in [INPUTS.md](INPUTS.md).
- Read-only Rust reference: `kogen-rs` `a402540b39cedc7f788472297add7ae2f8a6631a`, `crates/kogen-core/src/provider/sse.rs`, its tests, and `provider/mod.rs` usage mapping.
- Frozen oracle source: `kogen-conformance` `0f93bad988fb8d7a8eff4e94954d1db0a046c89d`, v1.2 snapshot manifest digest `8d76a7ec7fbf042ef526e959f1de6d652ec1f3ade43b33ce5587f3586372a815`. The snapshot runner reports `v1.2+unknown` because the copied input has no `.git` directory.
- Host/toolchain: macOS arm64, Go 1.27.1, Git 2.54.0.

## Implementation and local verification

`internal/provider/sse` now provides an incremental `Assembler` with arbitrary byte-chunk handling, CR/LF normalization, multiline `data:` assembly, `[DONE]` skipping, final partial-frame flushing, and a 16,000,000-byte body cap. Assembly applies stream-failure-before-malformed precedence, rejects duplicate completion events, prefers nonempty `completed.output`, and otherwise uses collected items in arrival order. Function arguments must be a JSON object or a JSON string containing one; ambiguous duplicate keys and non-object values are refused. Missing usage counts remain null, and cached input above total input is malformed.

The 12 package test functions cover byte-at-a-time mixed line endings, multiline and partial frames, `[DONE]`, completed/collected precedence, function-call argument forms, error ordering/classification, incomplete responses, nullable/invalid usage, invalid UTF-8, duplicate/missing completion, and exact/over-limit bodies. These are local component fixtures only; no provider request or tool execution occurred.

Final check command (pinned tool PATH was exported; `GOMAXPROCS=2`):

```sh
GIT_CONFIG_GLOBAL=/dev/null make check
```

Result: passed. Formatting, vendor fingerprints, vet, all repository tests, and both builds passed; `internal/provider/sse` tests passed.

## Frozen v1.2 CLI run

The assigned command was run once against the frozen snapshot. Its result is preserved at `/Users/almirsarajcic/cx/kgo/evidence/26-sse-decoder/results.jsonl` (work directory `/Users/almirsarajcic/cx/kgo/evidence/26-sse-decoder/work`).

```sh
make build
SUITE="$HOME/cx/kgo/inputs/conformance-v1.2"
EVIDENCE="$HOME/cx/kgo/evidence/26-sse-decoder"
mkdir -p "$EVIDENCE"
PYTHONDONTWRITEBYTECODE=1 PATH="$HOME/.local/share/mise/installs/python/3.14.7/bin:$PATH" \
  "$SUITE/bin/kogen-conformance" run --kogen "$PWD/bin/kogen" \
  --profile cli,state,approval,shape,build,ladder,provider,custody,format,v1.2 --case 'v1.2-108-provider-06,v1.2-109-provider-07,v1.2-110-provider-08,v1.2-111-provider-09' \
  --jobs 2 --time-scale 0.02 --workdir "$EVIDENCE/work" \
  --out "$EVIDENCE/results.jsonl"
```

Resolved IDs and actual results: each ID expanded to one instance; **0 passed, 4 failed, 0 errors, 0 skipped**.

| Case | Title | Result |
|---|---|---|
| `v1.2-108-provider-06` | SSE edge cases | fail, 0/1 |
| `v1.2-109-provider-07` | completed.output takes precedence | fail, 0/1 |
| `v1.2-110-provider-08` | A missing completed is malformed and retried | fail, 0/1 |
| `v1.2-111-provider-09` | Bad arguments are malformed | fail, 0/1 |

All four stop at step 2, before a provider request. The exact common runner failures are:

```text
step 2 (approve): `kogen intent approve greet {hash8:greet}`: exit 2, expected 0
step 2 (approve): `kogen intent approve greet {hash8:greet}`: stdout does not match /approved greet [0-9a-f]{8} \\(approval [0-9a-f]{8}\\); it is queued\\n/
step 2 (approve): `kogen intent approve greet {hash8:greet}`: stderr carries lines outside the §1.1 list: ['kogen: implementation bootstrap; command routes are not wired']
step 2 (approve): stdout was: ''
step 2 (approve): stderr was: 'kogen: implementation bootstrap; command routes are not wired\\n'
Runner hint: no provider request reached the fake server (KOGEN_PROVIDER_URL seam missing?)
```

These are retained CLI wiring failures, not observed SSE assertion failures or a v1.2/draft behavioral conflict. No black-box compatible case is claimed as passing. `conflicts` is empty because no behavior-level v1.2/draft conflict was reached.

## Deferred closure gates and scope

- **I2 component integration:** wire this package into provider/account routes and the shared component gate. This worker's package tests do not close I2 integration.
- **I3 CLI behavior:** the four assigned cases must be rerun on a changed revision after the public approval/provider path is wired; the current run is retained as failed evidence.
- **R(stream):** not run. It requires a scratch copy of the shared coherent migrated Quint cohort, 500 traces × 25 steps for each seed 17, 23, and 41, followed by full-observation conformance against a same-revision private binary. No replay result or divergence is claimed.
- **Draft fixtures:** planned D-* names are not frozen v1.2 cases. No v1.3 replacement IDs are available here; await the shared frozen v1.3 suite before claiming those gates.
- Linux, optional-runtime, and live comparison evidence was not produced. No live provider or account access was used.

No files outside `internal/provider/sse/**` and the assigned evidence/gate receipts were changed. The package API remains local to this subtree; shared contracts and CLI wiring were untouched.
