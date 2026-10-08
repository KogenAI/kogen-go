# Release and comparison admission

`tools/release-check.py` assembles evidence for the Go release and the later
language comparison. It reads retained reports and manifests, makes no provider
requests, and launches no scored cells. Missing evidence is a blocker. A
historical v1.2 pass is recorded as a v1.2 assertion result; it does not qualify
as v1.3 behavior evidence.

## Audit command

Start with the complete frozen v1.2 run. The checker expands cases and rows
through the frozen runner, verifies the exact 236-case / 570-instance
denominator and pinned input tree digest, checks the required profile/time-scale
metadata, records all historical case statuses and instance counts, and
preserves each nonpass row's failures and hints.

```sh
PYTHONDONTWRITEBYTECODE=1 python3 tools/release-check.py audit \
  --historical-results "$HOME/cx/kgo/evidence/I8-release-comparison-admission/results.jsonl" \
  --output "$HOME/cx/kgo/evidence/I8-release-comparison-admission/release-report.json"
```

The command returns 2 while required evidence is missing or invalid. It still
writes a create-only JSON report with the exact historical results, documented
conflicts, and blockers. Use a new output path for each audit. The report never
converts failures into passes or changes the denominator.

When the coordinator has frozen the shared inputs and both platform runs are
available, supply them along with the comparison, platform, timing, effort, and
cache evidence:

```sh
PYTHONDONTWRITEBYTECODE=1 python3 tools/release-check.py audit \
  --historical-results "$HOME/cx/kgo/evidence/I8-release-comparison-admission/results.jsonl" \
  --shared-v13-suite "$HOME/cx/kgo/inputs/conformance-v1.3" \
  --shared-v13-manifest "$HOME/cx/kgo/inputs/conformance-v1.3/manifest.json" \
  --v13-run "darwin=$HOME/cx/kgo/evidence/I8-release-comparison-admission/v1.3-darwin.jsonl" \
  --v13-run "linux=$HOME/cx/kgo/evidence/I8-release-comparison-admission/v1.3-linux.jsonl" \
  --cohort-manifest "$HOME/Areas/Kogen/kogen-spec/quint/shared-v1.3-cohort.json" \
  --production-replay-manifest "$HOME/cx/kgo/evidence/I8-release-comparison-admission/production-replay.json" \
  --platform-receipt "darwin=$HOME/cx/kgo/evidence/I8-release-comparison-admission/darwin-check.json" \
  --platform-receipt "linux=$HOME/cx/kgo/evidence/I8-release-comparison-admission/linux-check.json" \
  --comparison-manifest "$HOME/cx/kgo/evidence/72-comparison-admission-kit/comparison-manifest.json" \
  --performance-manifest "$HOME/cx/kgo/evidence/71-offline-performance-collector/manifest.json" \
  --performance-results "$HOME/cx/kgo/evidence/71-offline-performance-collector/measurements.jsonl" \
  --build-effort "$HOME/cx/kgo/evidence/72-comparison-admission-kit/build-effort.json" \
  --live-cache-replay "$HOME/cx/kgo/evidence/72-comparison-admission-kit/cache-replay.json" \
  --live-cache-requests "$HOME/cx/kgo/evidence/72-comparison-admission-kit/live-requests.jsonl" \
  --output "$HOME/cx/kgo/evidence/I8-release-comparison-admission/release-report-final.json"
```

The v1.3 suite and manifests above are coordinator-owned inputs. The paths show
the expected handoff shape; they are not assertions that these files exist.
`--v13-run` must be supplied once per OS and each result file must cover every
case and expanded instance in that suite. The suite's `VERSION` must identify
v1.3. Its runner metadata must identify the matching OS.

## Admission rules

The checker accepts an admission only when all of these conditions hold:

1. The historical v1.2 result file matches the frozen runner's exact 236 IDs
   and 570 instances. Compatible assertions pass. A nonpass can be classified
   as an allowed conflict only when the coordinator's external
   `~/cx/kgo/gate-policy.json` maps that exact ID and the I8 receipt preserves
   it. Case results and conflicts stay visible in the report.
2. The shared frozen v1.3 oracle manifest, suite, and comparison authority
   agree by digest. Every v1.3 case and instance passes on macOS and Linux;
   result IDs are complete and unique, with no skips, unimplemented cases, or
   unmatched requests.
3. The frozen Quint cohort is `kogen-shared-v1.3-cohort/v1`, binds draft
   `e19dd1c`, includes the eight migrated G slices and four separately
   classified D slices, and agrees with `kogen-production-replay/v1`. Every G
   slice must pass its spec check and 500 traces × 25 steps for seeds 17, 23,
   and 41, with every hand scenario included and full observations. D slices
   remain outside the G denominator.
4. I7, package 71, D1, D2, and D3 have behavior receipts whose evidence
   digests match the comparison authority. P1–P13 and D-CACHE-01–06 resolve to
   literal shared v1.3 IDs in the full runs. Planned labels alone do not count.
5. The macOS and Linux platform receipts bind the same Go source revision and
   retained logs for both `GIT_CONFIG_GLOBAL=/dev/null make check` and
   `GIT_CONFIG_GLOBAL=/dev/null make race`, each with exit code zero.
6. Offline performance measurements and their pinned manifest match the
   comparison authority. Build effort includes measured worker/coordinator
   time, usage, queue failures, rebases, rework, and parity milestones for all
   three arms. Report unavailable or failed measurements as such.
7. A separately authorized live replay is bound to its frozen feasible replay
   definition. The cache reporter requires complete telemetry and observed
   cached-input / total-input of at least 95% on every designated warm request.
   Eligibility is checked before launch. Arbitrary-task cache rates remain
   descriptive. This audit itself does not launch the replay.

The comparison manifest is checked by `tools/compare-manifest.py` and must be
frozen before admission. It carries effective roles, recipes and escalations,
provider and host identities, caps, P1–P13 mappings, D-CACHE mappings, and
artifact digests. The report preserves each arm's effective provider/model/
effort for all seven roles and its ordered recipe rung roles. `fallback_shaper`
must match the resolved shaper provider, model, and effort.
`tools/compare-results.py` validates measured requests against those roles and
validates per-arm build-effort records. Offline timing details in the report
retain each measurement's phase, status, wall/CPU time, RSS, and binary size;
the pinned manifest provides tool and host context.

Platform receipts use this structure; each log path must exist and match its
SHA-256:

```json
{
  "schema": "kogen-platform-check/v1",
  "os": "darwin",
  "source_revision": "<same checked source revision>",
  "commands": {
    "make_check": {
      "command": "GIT_CONFIG_GLOBAL=/dev/null make check",
      "exit_code": 0,
      "log_path": "/absolute/path/make-check.log",
      "log_sha256": "<sha256>"
    },
    "make_race": {
      "command": "GIT_CONFIG_GLOBAL=/dev/null make race",
      "exit_code": 0,
      "log_path": "/absolute/path/make-race.log",
      "log_sha256": "<sha256>"
    }
  }
}
```

The report's `scored_cells_launched` is always false. Scored-cell admission
remains false until every gate above qualifies. Freeze a new comparison
manifest after any source, task, role, recipe, provider, host, cap, rate, or
oracle change.

## Historical conflicts

The report lists these exact v1.2 audit IDs as documented assertion conflicts
with v1.3-draft §3.8.2. It includes each current run row when present and leaves
behavior reachability unknown unless the result establishes it:

- `v1.2-73-ladder-05`: expects pre-repair demotion of over-strict A2.
- `v1.2-74-ladder-06`: expects parking after audit demotion without repair.
- `v1.2-75-ladder-07`: expects demotion of A2 without a citation.
- `v1.2-76-ladder-08`: accepts an unsupported `infeasible` verdict.
- `v1.2-78-ladder-10`: expects citation-bearing demotion using the old schema.
- `v1.2-79-ladder-11`: permits landing after pre-repair demotion.

`v1.2-77-ladder-09` is eligibility-compatible at that level; the draft also
requires a warning for an unknown verdict. The checker does not label it a
pass based on this classification.

The checker reads `gate-policy.json` when present. Workers do not create or
edit it. A documented conflict ID with an ordinary integration failure stays
unresolved until the recorded result and coordinator mapping support that
classification.

D2's baseline representation mismatch and D3's recovery-model migration gap
are recorded separately from the v1.2 denominator. D2 must use canonical
sorted JSON with `child_env` as an object under draft §2.9. D3's migrated
recovery facts include work, preservation state, and preservation outcome.
Neither planned D fixture names nor an unwired assertion may be counted as a
shared frozen v1.3 pass.
