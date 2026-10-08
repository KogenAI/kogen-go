# Comparison admission

This kit prepares a reproducible Go/Rust/Bun comparison. It does not launch
real-task cells, live cache requests, or score a language. A successful tool
exit validates evidence shape; it is not an I8 release or behavior gate.

## Current admission state

The normative target is the committed v1.3-draft `e19dd1c21c19c5be1201c3b6a42c59c28b5c2887`, content manifest
`52388b2be73aade4b4fb2535cef475baefae1d3f662c1f2221afed737154468f`.
The frozen historical oracle is conformance v1.2 revision
`0f93bad988fb8d7a8eff4e94954d1db0a046c89d`, 236 effective cases and 570
instances. The standard v1.2 profiles and overlay remain a historical
compatibility denominator only.

No canonical shared v1.3 full-oracle/G manifest or production-replay manifest
is available. The conformance checkout has an uncommitted migration, so it is
not an oracle. The planned D-CACHE-01–06 names have no frozen shared case IDs.
The held-out real-task byte set and independent grader rubrics are also not
provided. Consequently this worktree has no frozen scored-cell manifest and
there are no real-task, cost, speed, build-effort, or live-cache scores.
`compare-manifest.py freeze` refuses a proposal until those inputs are
complete, and `compare-results.py cells` refuses an unfrozen manifest.

Package 00 remains required. I7, D1, D2, and D3 have component receipts, but
their integration/replay closures are open. The frozen v1.2 full run recorded
by I7 is retained as historical evidence: 236/236 standard cases, 570/570
instances, 61 case passes and 175 case failures; 370 instances passed, 200
failed, with zero skipped/errors. Six ExUnit-only cases (six instances) were
also in that JSONL and are excluded from the v1.2 denominator. The report
command below verifies the denominator and preserves every failed case's
original failure messages. These results include failures that stopped before
the intended product behavior and do not assert v1.3 behavior acceptance.

I7 did not acquire the required custody/race lock. Custody rows are preserved
as historical output but are not isolated acceptance evidence. Do not delete
or replace that run.

The current Go request journal does not yet expose all fields needed for a
public cross-session cache report: it has no stable request ID, adapter/prompt
version, security-namespace ID, separately identified static-prefix digest,
eligibility-rule ID/digest, or stage/attempt/rung/epoch thread-scope IDs. Its
transcript also omits the session ID; the current provider session derives a
run-level session ID distinct from the cache key. The journal carries general
prefix digests and cache/thread IDs. The cache report therefore refuses to
qualify actual requests until the production evidence boundary supplies the
frozen identities and required session/cache-key relationship; synthetic
telemetry only checks the accounting rules. This is the assigned mid-build
cache-evidence finding and remains an I5/D-CACHE closure gate.

## Freeze before any cell

Create a draft JSON manifest from the schema enforced in
`tools/compare-manifest.py`, then freeze it create-only. The manifest must bind:

- **Authority:** target spec commit and content digest; full shared v1.3 oracle
  and all eight G slice manifests; same-revision production replay manifest;
  v1.2 suite/input digest and its full 236-case/570-instance results digest;
  literal historical conflict ledger; behavior-accepted I7, 71, D1, D2, D3
  closure receipts; pinned package-71 manifest and measurements; literal
  compatible case/evidence mappings for each P1–P13 requirement and each of
  D-CACHE-01–06.
- **Tasks and graders:** exact held-out task-tree, request, and acceptance
  bytes by SHA-256; lane and domain; held-out split; independent blinded grader
  identity/version, rubric digest, and result schema. The Kogen gate receipt is
  not the independent task grade. Proposed task strata from PLAN are command,
  Rails, ExUnit, syn-06/syn-20 frontmatter regressions, changed-file/migration/
  dependency work, and difficult multi-step work; these categories are not a
  substitute for frozen task bytes and rubrics.
- **Arms:** Go, Rust, and Bun source revisions, toolchain/build commands,
  executable SHA-256 and byte size, sandbox policy, provider mode and endpoint
  host/path, non-secret account/security namespace identifier, exact host OS
  release/architecture/CPU/memory/runtime manifest, and every resolved
  role's provider/model/effort.
- **Recipes and caps:** recipe name, ordered rungs, each rung's provider/model/
  effort/input/tools, and caps for Shape turns/passes/style repairs, Build
  wall time/rungs, generation/tool-result budgets, HTTP attempts/idle/total
  timeouts, and verification time. Include planner, shaper, builder, auditor,
  context, reviewer, and fallback roles. `fallback_shaper` must equal the
  effective shaper tuple. Recipe escalation is part of the manifest; an
  all-Luna arm must select a compatible recipe or explicitly retain its
  escalation models.
- **Design and denominator:** both lanes (`build`: identical approved
  Intent/acceptance bytes; `end_to_end`: request through Shape, approval, and
  Build), repetitions, paired/interleaved order, stopping rule, maximum spend,
  failure taxonomy, and exact cell denominator. A planned cell remains in the
  denominator when it fails, stops, is interrupted, hits infrastructure, or
  never starts. Freeze worker/coordinator time sources, model-usage source,
  rework definition, and first-green/final-parity milestones. Each arm labels
  its development-history context; Rust's accumulated implementation effort
  is not presented as directly equivalent to a new Go port with a mature
  specification/reference. A pilot cannot declare a winner.
- **Rates:** provider/model/endpoint-specific rate snapshots with capture time,
  currency, source and source digest, uncached input, cached input, cache
  writes, output and reasoning rates per million; or an explicit
  `subscription_measured_usage` basis. Subscription usage is not converted to
  a fictional per-run invoice. No public USD ledger is added here.

The freeze command is deterministic and refuses to overwrite an existing
artifact:

```sh
PYTHONDONTWRITEBYTECODE=1 python3 tools/compare-manifest.py validate --manifest comparison-proposal.json
PYTHONDONTWRITEBYTECODE=1 python3 tools/compare-manifest.py freeze --input comparison-proposal.json --output comparison-manifest.json
PYTHONDONTWRITEBYTECODE=1 python3 tools/compare-manifest.py validate --manifest comparison-manifest.json --require-frozen
```

The SHA-256 covers canonical sorted-key JSON excluding only its own
`manifest_sha256` field. A task, grader, role, recipe, host, cap, denominator,
rate, authority digest, or cache policy change requires a new manifest and
newly identified study; never edit a frozen manifest in place.

## Shape and Build evidence

Real-task cell JSONL rows use `kogen-real-task-cell/v1`. Each row references
one frozen lane/task/repetition/arm and carries the exact task, request,
acceptance, grader, source, binary, provider, role, recipe, host, cap, and
sandbox identities. Keep every transport request in an allowlisted
`model_requests` array with request/invocation/conversation IDs, stage and
effective role/provider/model/effort, endpoint host/path, cache namespace,
request/response times, body byte count, prefix and static-prefix digests,
persisted cache/session/thread identities, routing-header **names only**, and
nullable usage counters. The session ID must equal the persisted Build cache
key, while each thread ID remains distinct. Every request freezes its nullable
attempt/rung/epoch fields and an opaque `thread_scope_id`; the same scope must
keep the same thread and a thread cannot span scopes. IDs must be opaque and
path/secret-free. Never include prompt or response contents, credentials, or
header values.

The end-to-end lane requires schema-1 `shape_accounting.json` data: profile,
outcome, primary/fallback role and conversation counters, validation passes and
traversals, finish guards, repairs, elapsed time, token totals, and
`unknown_usage_attempts`. The checker verifies the role/conversation aggregates
and Shape HTTP attempts against the request ledger. Build accounting must
separately capture terminal state, elapsed/provider-wait time, rung count,
request count, and unknown usage. Its model-request count must match actual
Build-stage requests and stay within frozen caps. The checker keeps logical
turns, HTTP attempts, continuations, validation passes, and grader outcomes as
different quantities.

Each usage field remains nullable. A missing usage object or field increments
an unknown counter; it is never replaced with zero. Known subtotals are marked
partial when any attempts are unknown. Here `input` is uncached input, so
`cached_input` may exceed it; cache hit rate uses
`cached_input / (input + cached_input)`. The currently inspected Go journal
validator rejects `cached_input > input`, contrary to that denominator. That
producer-side issue is owned outside package 72; until its owner closes it,
valid high-cache telemetry can be unavailable. The comparison code does not
reinterpret or discard such evidence.

Before descriptive summaries, every measured request must exactly match its
arm's resolved effective role or frozen recipe rung. A provider/model/effort,
endpoint, host, binary, task, grader, cap, or recipe mismatch invalidates the
cell. Role resolution follows project → machine → default; defaults are
shaper/planner/auditor Sol-high and builder Luna-max. Fallback shaper aliases
the resolved shaper. No result is silently coerced to a convenient arm.

Example after prerequisites close and the manifest is frozen:

```sh
PYTHONDONTWRITEBYTECODE=1 python3 tools/compare-results.py cells \
  --manifest comparison-manifest.json --cells cells.jsonl --effort build-effort.json \
  --output comparison-report.json
```

The result reports per-task grades with failed/unstarted cells in the
denominator, token usage and unknowns by arm/lane, phase timing, cache rates,
and exact validation errors. It makes no winner or significance claim. Cost
estimates are possible only where a matching externally verified rate snapshot
exists; build-effort reporting must also bind worker/coordinator hours,
rework/rebases, time to first end-to-end pass, and the pinned offline build /
check measurements from package 71. `--effort` accepts one sanitized
`kogen-build-effort/v1` record per arm with measured worker/coordinator hours,
model usage, queue failures, rebases, rework count, first-green and final-parity
times (null only if not yet reached), parity state, source revision, and
development-history context. It reports worker token-cost estimates only when
the model/endpoint has an applicable frozen rate snapshot; subscription usage
remains usage-only. Absent effort evidence keeps the report incomplete.

## Cache replay admission

Cache replay is separate from real-task scoring. The frozen replay definition
must identify provider/model/effort, endpoint, adapter and prompt versions,
tokenizer or captured counts, security namespace, affinity and retention,
adapter-specific minimum/block eligibility rules by rule ID and digest, exact
request order, appended-token budget, a stable persisted cache/session ID per
Build, a distinct stable thread ID and opaque scope ID per
stage/attempt/rung/epoch, and each
designated third-and-later warm request.
`eligible_input_tokens` is calculated under that frozen adapter rule; this kit
does not invent a universal block size or infer eligibility from prompt text.

Run the local preflight before any authorized replay:

```sh
PYTHONDONTWRITEBYTECODE=1 python3 tools/cache-report.py preflight --replay cache-replay.json --output cache-preflight.json
```

Every designated warm request must have theoretical eligible-input/total
input at least 0.95 before launch. An infeasible replay exits nonzero and is
not launched. After a separately authorized live run, `cache-report.py report`
requires complete request metadata and usage for each designated request and
the same eligibility-rule ID and digest as the replay manifest; it checks
observed cached-input/total-input at least 0.95 **per request**. It
reports weighted raw hit rate and eligible-prefix reuse separately, labels
partial totals, reports excess cached input, and treats zero eligibility as
inapplicable. Unknown eligibility or usage is incomplete; a fully observed
zero-cache request is a miss.

Arbitrary conversations have no universal 95% gate. The frozen example of
4,096 eligible repeated tokens plus 1,000 appended tokens has perfect raw
reuse of only `4096 / 5096 = 80.38%`; it is a diagnostic, not a failed
production replay. Compare same-session and separate-session Shapes, Builds,
and Shape-to-Build while preserving identical static prefix bytes and distinct
threads. These scripts never call a provider. This package did not run a live
replay.

The planned D-CACHE fixtures remain a six-part closure list, not passing cases:

1. **D-CACHE-01:** report the 4,096+1,000 example as 80.38% raw reuse without
   applying the production replay threshold.
2. **D-CACHE-02:** cover eligibility minimum/block rounding, zero eligibility,
   excess cached tokens, missing usage, and missing eligibility.
3. **D-CACHE-03:** refuse an infeasible frozen replay before its first request.
4. **D-CACHE-04:** require complete telemetry and observed ≥95% on every
   designated third-and-later warm request.
5. **D-CACHE-05:** compare the canonical static generic/schema prefix across
   two Shapes, two Builds, and Shape-to-Build; variable task/run/time/path
   information must not enter that frozen prefix digest.
6. **D-CACHE-06:** preserve a stable cache key at its declared affinity scope,
   distinct conversation/thread identities, restart behavior, and provider,
   model, and security-namespace boundaries.

The replay manifest pins `static_prefix_sha256`; the normalized request log
must carry that digest along with endpoint, timing, body size, prefix digests,
cache/thread IDs, and nullable usage. Cross-session prefix/scope checks are
reportable once frozen request IDs exist, but are not behavior evidence without
the independently frozen D-CACHE replacements.

## Historical v1.2 conflicts and uncovered obligations

The full preserved I7 JSONL and exact row messages are summarized with:

```sh
PYTHONDONTWRITEBYTECODE=1 python3 tools/compare-results.py historical \
  --results "$HOME/cx/kgo/evidence/I7-production-replay-and-draft-reconciliation/results.jsonl" \
  --expected-cases 236 --expected-instances 570 --exclude-profile exunit \
  --output "$HOME/cx/kgo/evidence/72-comparison-admission-kit/historical-236.json"
```

Known historical assertions that conflict with v1.3-draft observational audit
rules are exact overlay IDs `v1.2-73-ladder-05`, `v1.2-74-ladder-06`,
`v1.2-75-ladder-07`, `v1.2-76-ladder-08`, `v1.2-78-ladder-10`, and
`v1.2-79-ladder-11`: they expect demotion/eligibility/landing effects or an
old citation-bearing demotion shape that §3.8.2 no longer admits. The old
malformed-JSON/no-demotion assertion `v1.2-77-ladder-09` is compatible at that
level; draft also requires an unknown-verdict warning. In the retained I7 run
all seven stopped at queue start before auditor behavior. Their run failures
are exact historical results, but do not measure the documented model
differences. D2 separately recorded Go fixture digest
`8a9d09574395261b1633f2324b97c4a4faf8f164484aad4e5de1e2f352ce8fe6` versus
draft-canonical digest `fe65716657b6320a5fcdf9972e16b08748c92c26ae54fa70c6987926fdb891d9`
for baseline JSON representation; D3's recovery hand fixtures omit required
`work`, `preserved`, and `preserveOk` facts. These are not v1.2 product passes
or additional v1.2 cases.

The assigned case `v1.2-28-provider-13-planner-no-fallback` is a historical
provider-planner anchor. If approval/queue wiring stops it first, preserve that
exact failure as an integration gap; do not label the planner behavior
compatible or incompatible from an unreachable assertion.

P1–P13 remain distinct external obligations. Keep their compatibility result
separate from real-task cells: P1/P2 cache and thread identities; P3 raw byte
prefix; P4 finish protocol; P5 tool-output budget; P6 Responses/Lite shapes;
P7 cut continuation; P8 deadlines/retries; P9 nullable usage and cache rate;
P10 Grok wire/account behavior; P11 scheduling/status; P12 Rails detection;
P13 checkpoint epoch. Local evidence supplements gaps but never changes the
236/570 denominator. D-CACHE-01–06 are planned fixture names only until the
coordinator freezes shared v1.3 IDs. I8 still requires the full shared oracle,
all G replay, production replay, D1–D3 closure, Linux/optional runtime evidence,
and offline performance/build-effort evidence.
