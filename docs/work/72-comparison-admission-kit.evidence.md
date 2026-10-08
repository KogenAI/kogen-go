# 72-comparison-admission-kit evidence

## Scope and revisions

- Branch/worktree: `kgo/72-comparison-admission-kit` at
  `/Users/almirsarajcic/Areas/Kogen/kogen-go-wt/72-comparison-admission-kit`.
- Worker source base revision: `d368d2796eb77eb79b8c4ad5ee55e131f63e3c8b`.
  Package changes are limited to `tools/compare-manifest.py`,
  `tools/cache-report.py`, `tools/compare-results.py`, this note, the gate
  receipt, and `docs/work/COMPARISON.md`.
- CLI/xspec/adapter source revision: same Go base revision; no Go source was
  changed. Built `bin/kogen` SHA-256
  `23bb01a0b6b3a1dcf0848996702bd1028f4ef00d0f73c60a120133c7a74d2ae4`
  (9,958,626 bytes); `bin/kogen-xspec` SHA-256
  `93793fab90c336c96e4f5ebb9aba6d2e433dd2d9302df02abfa46d50fbc8ac72`
  (8,915,874 bytes).
- Provider/journal adapter inspected at the same Go base: `internal/provider/session/session.go`,
  `internal/provider/wire/prefix.go`, `internal/journal/telemetry.go`,
  `internal/contract/contract.go`, `internal/shape/session/session.go`,
  `internal/app/adapter_wiring.go`, and `internal/build/recipe/recipe.go`.
- Read-only Rust reference revision: `kogen-rs` commit
  `a402540b39cedc7f788472297add7ae2f8a6631a`; inspected
  `crates/kogen-core/src/provider/session.rs`,
  `crates/kogen-core/src/provider/http/wire.rs`,
  `crates/kogen-core/src/run/orchestration/recipe.rs`,
  `crates/kogen-core/src/intent/shaping/journal.rs`, and
  `crates/kogen-core/src/build/provider_journal.rs`.
- Target spec is committed `kogen-spec` revision
  `e19dd1c21c19c5be1201c3b6a42c59c28b5c2887`, content manifest
  `52388b2be73aade4b4fb2535cef475baefae1d3f662c1f2221afed737154468f`.
  Its working tree contains unrelated draft Quint/harness changes; they were
  not read as frozen oracle content or modified.

## Implemented

- `tools/compare-manifest.py` validates a complete prospective study and
  create-only freezes canonical JSON. It requires exact task/request/acceptance
  and blinded-grader digests, Go/Rust/Bun source/binary/provider/host/resolved
  role/recipe/cap pins, both lanes and an exact denominator, verified rate
  snapshots or measured subscription usage, full v1.2 and shared-v1.3/G/replay
  references, I7/71/D1/D2/D3 behavior receipts, P1–P13 evidence mappings, and
  literal D-CACHE-01–06 IDs. No manifest was frozen because those source inputs
  are not available.
- `tools/cache-report.py` validates the frozen adapter-specific eligibility
  sequence before launch; checks every designated warm request for complete
  allowlisted telemetry and observed per-request ≥95%; reports arbitrary-task
  weighted raw cache rate separately from eligible-prefix reuse; retains
  unknowns, excess cached counts, zero-eligible inapplicability, and partial
  totals; and checks eligibility-rule ID/digest plus static-prefix/cache/thread
  scope identities. It makes no network requests. Report artifacts publish
  create-only so a prior result cannot be overwritten.
- `tools/compare-results.py` preserves exact historical case/instance failures
  and validates future cell rows against the frozen task, arm, host, role,
  recipe, cap, provider, grader, and build/Shape accounting. Unknown usage stays
  null and partial. It summarizes speed and token-cost evidence without a
  winner/significance claim, cross-checks Shape role/conversation counters and
  Build-stage request totals against their accounting receipts, and requires
  one revision-bound worker/coordinator effort record per arm before complete
  admission.
- `docs/work/COMPARISON.md` records the current preparation state, required
  freeze inputs, cache thresholds, historical results/conflicts, P1–P13 and
  D-CACHE fixture obligations, and deferred gates.

The local cache accounting smoke used 4,096 cached plus 1,000 uncached tokens.
Preflight rejected the designated warm request because theoretical eligibility
is `4096/5096 = 80.38%`, while arbitrary-task metrics reported raw hit rate
80.38%, eligible-prefix reuse 100%, and `descriptive_no_universal_gate`.
No production cache gate was failed by this example.

## Commands and results

Pinned tool PATH was exported before commands:

```sh
export PATH="$HOME/.local/share/mise/installs/git/2.54.0/bin:$HOME/.local/share/mise/installs/go/1.27.1/bin:$HOME/.local/share/mise/installs/python/3.14.7/bin:$HOME/.local/share/mise/installs/node/24.21.0/bin:$PATH"
```

- `GIT_CONFIG_GLOBAL=/dev/null make check` — **failed** in
  `internal/testkit/TestAdversarialSigningHelperHangIsSupervised` (make exit
  2). It reported that `signer-started` did not exist because the helper did
  not run. The log is retained at
  `/Users/almirsarajcic/cx/kgo/evidence/72-comparison-admission-kit/make-check.log`
  (SHA-256 `192f811ec59f31332a96de601bdf88d16d9c2147dcd1cf3e362d49348ba7e941`).
  No retry was run. The other listed Go packages printed as passing; this does
  not make `make check` green.
- Exact assigned frozen-oracle command (with stdout additionally tee'd to the
  retained log):

  ```sh
  make build
  SUITE="$HOME/cx/kgo/inputs/conformance-v1.2"
  EVIDENCE="$HOME/cx/kgo/evidence/72-comparison-admission-kit"
  mkdir -p "$EVIDENCE"
  PYTHONDONTWRITEBYTECODE=1 PATH="$HOME/.local/share/mise/installs/python/3.14.7/bin:$PATH" \
    "$SUITE/bin/kogen-conformance" run --kogen "$PWD/bin/kogen" \
    --profile cli,state,approval,shape,build,ladder,provider,custody,format,v1.2 \
    --case 'v1.2-28-provider-13-planner-no-fallback' \
    --jobs 2 --time-scale 0.02 --workdir "$EVIDENCE/work" \
    --out "$EVIDENCE/results.jsonl"
  ```

  `make build` passed. The suite resolved the literal ID
  `v1.2-28-provider-13-planner-no-fallback` to one case / one instance:
  0/1 cases and 0/1 instances passed. At step 3, `kogen queue start`
  exited 70 with `stopped greet: controller/approval_invalid; it stays queued`
  instead of landing the Build. The planner-no-fallback assertion was not
  reached. This is an integration failure, not evidence of a provider-policy
  conflict or compatible provider pass. Results JSONL SHA-256
  `529cad0c3a685e753edf511820bc6b30abab5baddc4a2912d545a864ea7cce05`;
  retained output log SHA-256
  `9858b844985c08472ab32c05f6f772353cc7930d569fe38ad5ee70fed8376e0b`.
- Full historical I7 result summarization:

  ```sh
  PYTHONDONTWRITEBYTECODE=1 python3 tools/compare-results.py historical \
    --results "$HOME/cx/kgo/evidence/I7-production-replay-and-draft-reconciliation/results.jsonl" \
    --expected-cases 236 --expected-instances 570 --exclude-profile exunit \
    --output "$HOME/cx/kgo/evidence/72-comparison-admission-kit/historical-236.json"
  ```

  Passed denominator validation: 236 standard cases / 570 instances; 61 case
  rows pass, 175 fail; 370 instances pass, 200 fail, zero skipped/errors.
  Six ExUnit cases / six instances are excluded from that denominator. The
  historical source result hash is
  `955f34003a9a950ae2ce74d599c11f83e3abd900938370818095a16b39e77ce1`;
  generated full failure-detail report SHA-256 is
  `8c2e3c7be8cb4e70e8c3d3c2cdd83cc33354863aef947a5a16750b5b6ae04b70`.
  The 61 standard v1.2 pass IDs were `cli-10`, `cli-15`, `cli-18`, `cli-19`,
  `cli-20`, `cli-22`, `cli-23`, `state-01`, `state-04`, `state-05`,
  `state-07`, `state-08`, `state-09`, `state-10`, `state-13`, `state-16`,
  `state-24`, `state-25`, `state-29`, `state-30`, `approval-02`,
  `approval-03`, `approval-04`, `approval-05`, `approval-06`, `approval-07`,
  `approval-09`, `approval-11`, `approval-12`, `approval-15`, `approval-16`,
  `approval-19`, `approval-20`, `approval-21`, `approval-22`, `approval-23`,
  `build-01`, `format-02`, `format-03`, `format-04`, `format-06`,
  `v1.2-07-cli-03-unknown-command`, `v1.2-08-cli-04-unknown-subcommand`,
  `v1.2-09-cli-05-unknown-option`, `v1.2-10-cli-06-missing-positionals`,
  `v1.2-11-cli-07-unexpected-argument`, `v1.2-12-cli-08-option-needs-value`,
  `v1.2-13-cli-09-boolean-takes-no-value`, `v1.2-14-cli-11-unknown-provider`,
  `v1.2-15-cli-12-watch-with-json`, `v1.2-16-cli-13-double-dash`,
  `v1.2-17-cli-14-options-before-command`, `v1.2-18-cli-16-short-option`,
  `v1.2-19-cli-17-help-bad-topic`, `v1.2-20-cli-21-invalid-slug`,
  `v1.2-21-cli-24-help-after-positionals`, `v1.2-25-state-20-status-next`,
  `v1.2-26-state-22-status-next`, `v1.2-33-shape-json-is-unsupported`,
  `v1.2-35-state-02-schema-errors`, and `v1.2-36-state-06-lint-card-warnings`.
  These are historical v1.2 passes, not compatible v1.3 behavior claims.
  I7 did not acquire the required custody/race lock, so its custody rows are
  retained historical output, not isolated accepted evidence.
- `PYTHONDONTWRITEBYTECODE=1 python3 -c 'import ast, pathlib; [ast.parse(pathlib.Path(p).read_text(), filename=p) for p in ("tools/compare-manifest.py", "tools/cache-report.py", "tools/compare-results.py")]'` — passed.
- `python3 -m json.tool docs/work/72-comparison-admission-kit.gate.json >/dev/null` — passed; each tool's `--help` also exited 0.
- In-memory Python smoke — passed: cache preflight rejects the 80.38%-eligible
  warm request and accepts a feasible replay with observed per-request ≥95%;
  arbitrary raw/eligible-prefix metrics remain descriptive; missing usage and
  eligibility stay partial/unknown; zero eligibility is inapplicable; the
  Shape/Build accounting and effective provider/model/effort/rung validators
  accept a consistent sample; both report writers refuse to replace existing
  output artifacts. The 4,096 cached + 1,000 uncached diagnostic reports
  80.38% raw rate and 100% eligible-prefix reuse. One initial in-memory
  assertion failed because its unknown-usage fixture omitted the eligibility
  count; the corrected fixture preserved that count and the final smoke passed.
  This was a fixture setup issue, not a conformance run.
- `git diff --check` — passed.

No live provider, credential/account access, or scored cell was used. The
acceptance case ran only against the frozen v1.2 fake conformance harness.

## Exact conflicts and closure gaps

- Historical audit-model conflicts are `v1.2-73-ladder-05`,
  `v1.2-74-ladder-06`, `v1.2-75-ladder-07`, `v1.2-76-ladder-08`,
  `v1.2-78-ladder-10`, and `v1.2-79-ladder-11`: those old assertions expect
  demotion/eligibility/landing or citation-bearing demotion effects that
  v1.3-draft §3.8.2 makes observational. All six recorded I7 failures stopped
  at queue start before auditor behavior. `v1.2-77-ladder-09`'s malformed-JSON
  no-demotion assertion is compatible at that level, with a draft warning
  requirement. None is claimed as a measured Go-vs-draft behavior divergence.
- D2 exact baseline representation mismatch: Go fixture digest
  `8a9d09574395261b1633f2324b97c4a4faf8f164484aad4e5de1e2f352ce8fe6` versus
  draft-canonical digest
  `fe65716657b6320a5fcdf9972e16b08748c92c26ae54fa70c6987926fdb891d9`;
  Go uses struct-order keys and a sorted `KEY=value` child-env array, while
  draft §2.9 requires canonical sorted JSON and a child-env object.
- D3 cohort gap: recovery hand facts omit required `work`, `preserved`, and
  `preserveOk`; this is a fixture/schema mismatch, not a v1.2 behavior pass.
- I7 local stream mismatch remains exact: `11-auth-capability-and-provider-fallback`
  with `refreshable=false` records Go Build `provider_wait`/pause (`waited=300000`,
  exit 0, `refreshed=false`) while the draft scenario expects stopped/provider-login
  (exit 4). This is not a v1.2 oracle conflict.
- Current Go journal validation checks `cached_input <= input`, although
  `input` is already uncached input under the specified usage convention. Valid
  cache-heavy telemetry can be rejected; package 72 cannot modify the
  out-of-scope journal producer.
- Public cache evidence is not available from this package: current normalized
  journal rows lack request ID, adapter/prompt version, security namespace ID,
  a dedicated static-prefix digest, eligibility-rule ID/digest, and
  stage/attempt/rung/epoch thread scopes. The transcript omits session ID; the
  current provider session derives a run-level session ID distinct from the
  cache key. The assigned REVIEW-MIDBUILD finding
  therefore remains with I5/54/D2/D-CACHE closure; no request was reached in
  this package's selected provider case.
- Shared v1.3 full oracle/G manifest and production replay manifest are absent;
  D-CACHE-01–06 have no frozen case IDs. I7/D1/D2/D3 are component-only, and
  package 00 remains required. Held-out real-task bytes/rubrics and verified
  rate snapshots are not supplied. P1–P13 have not been accepted as a complete
  suite. R(slice) seeds 17, 23, 41 × 500 traces × 25 steps were not run because
  no coherent shared migrated cohort exists. Linux, optional runtime, live
  replay, and all scored comparison gates remain deferred to their named
  closures.

Component tooling is reviewable; behavior and comparison acceptance are not
claimed.
