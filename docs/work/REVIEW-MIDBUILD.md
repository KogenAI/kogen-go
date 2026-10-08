# Go Kogen mid-build review findings

Source: read-only review of `main` at `8ddf36efcf4c5e742dc1b6bec8748579335ca10a`, reviewed 2026-10-08. Findings are static evidence, not newly reproduced runtime failures. Fix assignments below preserve the review's owners and closure gates.

## Coordinator spec update

Authoritative coordinator update: `kogen-spec` `96cefde`, 8 Oct.

- When provider requests are exhausted, the Build stops, the Intent stays queued, and the drain exits 4. There is no per-Intent provider failure, so `failed_provider` continuing the drain is obsolete.
- Only a `login` failure triggers token refresh; `usage_limit` pauses with no refresh.

## Prioritized findings

### 1. P1 — Public project commands bypass confinement and Build integrity checking

**Evidence:** `internal/app/foundation.go:151–156`; `internal/app/approval_routes.go:63–75`; `internal/app/build_routes.go:147–158,375–376,416`; `internal/build/single/single.go:373`.

**Assigned fix:** I4 corrective integration handoff to I3/62; carry both-platform adversarial checks into 70/I6. Wrap project-code execution with per-workspace policy and real integrity observation; keep controller Git and mise's specified unconfined probe distinct. Test denied credential reads, checkout/origin writes, unavailable mode, and off/already-confined modes through the public binary.

### 2. P1 — Crash recovery does not wait for remaining writers before snapshot/deletion

**Evidence:** `internal/app/build_routes.go:126–128,203–209`; `internal/recovery/recovery.go:349–365`; `internal/process/guardian.go:340–372,472–487`.

**Assigned fix:** corrective I3/10/25/D3 handoff, regression in 70 and replay in 67/I7. Persist/observe run-owned custody identities and wait for confirmed termination before preservation. Exercise immediate recovery after SIGKILL while a child keeps writing through TERM; gate preservation on a barrier, not a fixed sleep.

### 3. P1 — Build checks and fixers receive no child environment

**Evidence:** `internal/build/single/approval.go:263–294`; `internal/app/build_routes.go:378–379,405–416`; `internal/gate/gate.go:707,727`; `internal/process/run.go:305–314`; `internal/gate/gate_test.go:361–362`.

**Assigned fix:** I4 corrective handoff to I3/41/36, then I5 adapter integration. Construct the specified environment for each execution workspace and bind it to all fix/check specs. Add a public fake-wire Build with real configured checks asserting PATH, project overrides, and run-local temp state.

### 4. P1 — Production Build and public provider dispatch remain unreachable after I3

**Evidence:** `internal/app/foundation.go:113`; `internal/app/build_routes.go:142–145,300–313`; `internal/app/build_routes_test.go:25–30`; `cmd/kogen-xspec/main.go:8`.

**Assigned fix:** I4 must close the I2/I3 wiring debt with an explicit ownership handoff; production plan/build adapter work belongs with 27/28/30/34/35/41. Validate using an external fake HTTP server and the built public executable. I5 closes Shape; I7 closes the separate xspec entrypoint, currently still Bootstrap at `cmd/kogen-xspec/main.go:8`.

### 5. P1 — Status and scheduling ignore durable Build outcomes

**Evidence:** `internal/app/status_routes.go:25–26,107–111`; `internal/app/build_routes.go:224–245`.

**Assigned fix:** I4 report/watch regression plus corrective I3/21/42 handoff; 66/I7 replay. Load run records and live ownership into production status/scheduling. Test a failed Build, crash recovery, provider stop, and reapproval across separate CLI invocations.

### 6. P1 — A moved base prevents the next queued Build from starting

**Evidence:** `internal/app/build_routes.go:147–158,494,588–591`; `internal/build/single/single.go:391–394`.

**Assigned fix:** I4 with corrective 40/41 integration; I5/D2 for exact-tree baseline/cache validation. Wire current-base materialization and verification without weakening protected-manifest or approval binding. Include two preapproved Intents and a separately moved external base in the public pipeline test.

### 7. P2 — Linux confinement component exists but shared runner never selects it

**Evidence:** `internal/sandbox/availability.go:55–56,137`; `internal/sandbox/linux/linux.go:57–104`.

**Assigned fix:** 63 corrective wiring handoff and I6, with 70 real Linux positive/negative enforcement and custody probes. Do not count cross-compilation or isolated Linux component tests as production confinement parity.

### 8. P2 — Retry cap exception is broader than the explicit no-fallback configuration

**Evidence:** `internal/provider/retry/retry.go:380–384`; spec §4.5 (`spec/04-provider.md:93–95`).

**Assigned fix:** corrective 27 with I4 and 68/I7. Distinguish explicitly disabled fallback from a role without fallback or an absent transport hook. Add fake-clock planner-overload and disabled-fallback cases; record the agreed clause in the shared delta ledger.

### 9. P2 — REQUIRED: Obsolete failed_provider transition explicitly continues the drain

**Evidence:** `internal/queue/schedule/scheduler.go:21,28,253–259`; `internal/queue/drain/drain.go:428–437`; `internal/queue/schedule/scheduler_test.go:117–125`.

**Assigned fix:** corrective 24/42; I4 and 66/68/I7. Remove or reject the obsolete outcome and require exhaustion to retain the current approval, stop the drain, and exit 4. Retire any replay event/golden that encodes the historical policy through the shared migration process. This finding is required by the authoritative coordinator spec update above.

### 10. P2 — Public approval permanently disables setup and baseline cache reuse

**Evidence:** `internal/app/approval_routes.go:80–83`; `internal/setupcache/setup.go:230`; `internal/approval/prepare/prepare.go:501,520,537`; `internal/process/environment.go:142–152`.

**Assigned fix:** I5/54/D2, with 72 measuring actual public cache evidence. Resolve trustworthy toolchain identities and test card→approve and repeated exact-tree calls. Preserve misses on changed source tree, env, tools, checks, or unknown identity.

## Additional quality and evidence findings

### 11. P2 — Recovery skips any missing run snapshot as if it were an empty approval directory

**Evidence:** `internal/app/build_routes.go:188–193`.

**Assigned fix:** I3/25 corrective handoff and 70: distinguish preparation state structurally and surface nonempty orphan Build state; preserve before cleanup.

### 12. P2 — Concurrency test can pass without concurrent lock contention

**Evidence:** `internal/auth/refresh/refresh_test.go:62–66`.

**Assigned fix:** 34/70/I6. Use an observed lock-wait barrier and bounded channel waits in 34/70/I6.

### 13. P2 — Controller failure exit contract is inconsistent, but public drain repairs it

**Evidence:** `internal/build/single/single.go:601–602`; `internal/queue/drain/drain.go:296–303,465–470`; `internal/queue/schedule/scheduler.go:263–264`.

**Assigned fix:** 41/I4. Normalize boundary errors in 41/I4 and test both controller results and public exits. Raw retry.Failure also needs explicit mapping to contract.Failure at the future adapter boundary.

### 14. P2 — Ignored temporary-workspace cleanup errors

**Evidence:** `internal/app/build_routes.go:342`.

**Assigned fix:** I4/36/37/70. Route such failures into journal cleanup_pending/cleanup_failure through I4/36/37/70.
