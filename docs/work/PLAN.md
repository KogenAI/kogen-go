# Kogen in Go: implementation and comparison plan

Planning snapshot: 7 October 2026. This document and [QUEUE.md](QUEUE.md) are the deliverables. No implementation repository, worker, installation, live provider call or benchmark is created or launched by this job. All paths below outside this plan directory are **future** implementation or evidence paths.

Build one Go implementation of the same language-neutral contract as Rust and TypeScript/Bun. Keep the public `kogen` command tree fixed; ship a separate private `kogen-xspec` executable. Require both black-box conformance and production-backed Quint replay before comparing real-task success, cost, speed and build effort. Rust is a structural and debugging reference, never an alternative oracle.

## Authority and reproducible inputs

| Input | Observed revision / state | Use |
|---|---|---|
| `~/Areas/Kogen/kogen-spec` | `main`, **`e19dd1c21c19c5be1201c3b6a42c59c28b5c2887`**, v1.3-draft; working tree has separate uncommitted Quint/harness changes | Draft appeared during this planning job, superseding initial `1118f7f`. `CLI-RULE.txt`, current `spec/01`–`06`, `CHANGES-v1.3.md`, normative data, classification and adapter are authoritative. Freeze coherent complete contents; record unrelated dirty patches separately. |
| `~/Areas/Kogen/kogen-conformance` | clean `v1.2`, `0f93bad988fb8d7a8eff4e94954d1db0a046c89d` | Read-only external oracle. Standard profiles plus overlay select **236 cases / 570 instances**. This was counted using `runner.load_cases` and `runner.instances`, not inferred from README. |
| `~/Areas/Kogen/kogen-rs` | clean `a402540b39cedc7f788472297add7ae2f8a6631a` | Crate boundaries, actual adapter, integration failures, provider/cache/Shape/publication fixes. Do not mechanically port its historical replay helpers or error handling. |
| Rust evidence | `/tmp/claude-501/krs/*.md`, `/tmp/claude-501/cache-ab/*.md` | `SHAPE-ROOT.md` reports 236/236 and 570/570 at the frontmatter fix; this is historical evidence, not a fresh verification of the later safety commit. |
| Dispatch reference | `~/Library/Mobile Documents/com~apple~CloudDocs/Areas/Kogen/careful-rebuild/build/WORKFLOW.md`, `tools/krs-dispatch.sh` | Isolated worktrees, Luna/max workers, prerequisites merged before dispatch, serialized rebase/check/fast-forward integration, retained logs. |

The effective suite distribution is 99 surviving old cases (`cli` 10, `state` 19, `approval` 23, `shape` 20, `build` 10, `provider` 5, `custody` 5, `format` 7) plus 137 overlay cases. All 36 ladder cases have versioned replacements. ExUnit's six additional black-box cases are outside the standard denominator; the ExUnit adapter itself remains in scope.

### v1.3 draft handling

The draft is now available at [CHANGES-v1.3.md](/Users/almirsarajcic/Areas/Kogen/kogen-spec/CHANGES-v1.3.md), commit `e19dd1c`. **Target this draft from day one.** Its three blocker fixes are:

| Fix | Go design / worker | Required new evidence |
|---|---|---|
| Observational Build auditor | 04,36,43–44,48 + D1: every approved item remains in verification/count/rank/landing. `green` default; legacy `green-or-advisory` has identical eligibility. Audit receipts mode observational, demoted=false, advisory_items empty. `auditor_demotion:true` refuses with `build.auditor_demotion has no admitted calibration`. | A1 pass/A2 required failure cannot land after over_strict/contradicts advice; only actual repaired passing bytes can. Audit timing, bad/duplicate replies and parallel order do not change counts/winner. Next shared suite replaces historical ladder05–11 and wider demotion assertions; gate scenario04 must migrate. |
| Independent verification-baseline key | 18,54 + D2: setup remains v2/narrowable; baseline is v3 with **exact checked base tree**, setup key, full ordered checks+deadlines, child env/toolchain/platform/adapter version. Unknown identities disable reuse; legacy keys miss. Checks run on resolved base tree, in scratch checkout when necessary. | Source-only base change with same lockfile/setup_inputs hits setup but misses baseline, replaces old red rows with new green, and does not excuse a reintroduced defect. Exact-tree/context repeats hit; changed tools/checks/env miss. approve model gains baseTree and tuple-key observation. |
| Preserve crashed work before cleanup | 14,25,37 + D3: stop writers; publish create-only latest lossless candidate/ref or fallback archive, marked **unverified**. Record identity/base in run.json.recovery and recovery_preserved before deletion. Failure retains workspace/sole refs, cleanup_pending=true; terminal recovery retries safely. | Kill before first snapshot, after a later edit, between publication/record, after CAS; preserve deletions/untracked non-ignored/modes/symlinks and prior snapshots. Fault both ref/archive publication, retry, idempotence, live-owner no-op; preservation never grants landing. |

The same draft also settles four should-fixes: **Shape accounting/fallback boundaries**, **provider-aware fallback alias**, **feasible frozen cache replay**, and **cross-session static prefix**. Packages 04,28,49–53,57,68,72 implement these, not historical Rust defaults. Default fallback inherits the effective primary shaper's provider/model/effort; it is not a configurable role. Primary turn or pass exhaustion starts it once; provider/environment failures do not. Publish shape-accounting.json on success/failure with logical turns separate from HTTP attempts/auditor turns, actual traversals/repair kinds, unknown usage and all-in elapsed time. Static generic instructions/tools remain identical across independent Shapes, Builds and Shape-to-Build; variable data follows them. Run-scoped affinity is a permissible initial policy; broader safe affinity needs measured evidence.

Package 00 freezes a delta ledger with exact clauses, affected workers, shared next-suite IDs and migrated Quint inputs. The draft has **not** created a new frozen conformance suite or migrated all hand scenarios/goldens/adapters. Separate committed models from unrelated existing dirty edits, and require the spec/oracle owners to freeze a coherent draft replay cohort before release claims. D1–D3 are concrete regression/preservation tasks below, not guesses. The unchanged 236-case v1.2 suite is diagnostic baseline evidence for this target: report every case and incompatibility, but do not implement old demotion or destructive cleanup merely to get 236/236. No private oracle rewrites or implementation-specific policy switches.

Before scored comparisons, Rust, Go and Bun must use the **same frozen spec content digest, draft delta, help/data corpus, suite revision, Quint models and benchmark recipe**. The draft explicitly leaves independent-review findings 4,5,8,11–16 open. Relevant tensions include provider terminal policy, serializer/tool_choice rules, recipe text versus experimental_r4 cases and reachable landed trailers versus slug reuse. Package 00 records shared decisions; unresolved affected gates remain gaps. This draft does not close those findings, and Go must not invent its own interpretation.

## Toolchain and dependencies

| Tool | Proposed exact pin | Reason / treatment |
|---|---|---|
| Go | **1.27.1**; `go 1.27.1`, `toolchain go1.27.1`, `mise.toml` same pin | Installed locally and verified with its absolute binary. Supported darwin/arm64, linux/amd64 and linux/arm64 targets; no implicit toolchain download during checks. [Official release history](https://go.dev/doc/devel/release#go1.27.1). |
| Sole Go module dependency | **`golang.org/x/sys v0.48.0`** | Maintained Unix syscalls for no-follow descriptor operations, process identity and signals where the standard API is insufficient. Commit `go.sum` and `vendor/`; freeze transitive graph, no `@latest`. [Versioned upstream package](https://pkg.go.dev/golang.org/x/sys@v0.48.0/unix). |
| Git | **2.54.0** upstream on benchmark hosts; local observed **2.54.0 (Apple Git-157)** recorded as a distinct build | Already required by the oracle; SHA-1 and SHA-256 repositories. Record executable path, build string and config policy. Never compare Apple/upstream timing without identifying the difference. |
| Python | **3.14.7** | Installed via mise; external runner uses only stdlib. Invoke the pinned executable rather than macOS's observed `/usr/bin/python3` 3.9.6. |
| Quint | **`@informalsystems/quint 0.33.0`** | Installed prototype version is 0.33.0, although its package.json uses `^0.33.0`. Freeze exact package/lock contents in a separate verification workspace; do not modify the source checkout. |
| Node (Quint only) | **24.21.0** | Installed local version, separate from the eventual Bun contestant. Node/Quint are verification dependencies, never Go runtime dependencies. |
| Make / shell | macOS **GNU Make 3.81**, Linux **GNU Make 4.3**; POSIX `/bin/sh` for checks; dispatch **Bash 5.3.20** | Portable Makefile; Bash dispatch needs `mapfile`, so macOS Bash 3.2 is unsuitable. Record OS-provided sh build. |
| Linux confinement | **bubblewrap 0.13.0**, non-setuid, preinstalled by host provisioning | OS process sandbox, not a Go dependency. Probe actual namespace support; a binary on PATH is not proof. [Upstream release](https://github.com/containers/bubblewrap/releases/tag/v0.13.0). |
| macOS confinement | OS `/usr/bin/sandbox-exec`, OS version and build pinned in host manifest | No independent version exists. Probe policy enforcement on Studio; availability may change with macOS. |

Package 00 verifies pins on Studio and Linux, records archive/checksum or OS build fingerprints and vendors the one module. Security/toolchain updates produce a new recorded baseline for all comparison arms; these pins are reproducibility choices, not claims of being the newest patch. Build host compilers for `-race` are captured with exact Xcode/Command Line Tools or GCC/Clang version during provisioning; release binaries use `CGO_ENABLED=0`. Race verification uses `CGO_ENABLED=1` in a distinct target.

| Concern | Choice | Justification and required work |
|---|---|---|
| CLI | Small explicit parser, `embed`, `fmt`, `io` | Cobra/urfave defaults conflict with fixed grammar, moved-form precedence, no `--help`, byte-exact help and stream rules. |
| HTTP / SSE | `net/http`, `crypto/tls`, `context`, `bufio`, custom incremental SSE assembler | No provider SDK hides body order, raw items, routing headers, first-body-byte/idle timers or cancellation. Bound bodies to normative 16,000,000 bytes; avoid Scanner's default 64 KiB limit. |
| JSON | `encoding/json`, `json.RawMessage`, `bytes`, a small ordered request encoder | Structs fix outer control order and put `input` last; recursively sorted canonical objects fix nested bytes. Store immutable canonical history bytes once. Raw wire response items retain semantic fields and stable replay encoding. Decode numbers without float64 loss. Reject malformed provider data; ignore unknown on-disk fields where specified. No jsoniter or alternate JSON-v2 defaults. |
| YAML | Implement **only** §2.6's strict subset with line-aware lexer/parser and schema validation | General YAML libraries accept anchors, coercions, indentless lists and forbidden syntax. Rust's lexical/structural split is a useful reference; use normative yaml-error corpus. Bounded 1 MiB input/depth 64; scalars are strings until schema conversion. |
| Git | Supervised `os/exec` CLI calls through one `gitio` port | Native Git already supplies object formats, ignore semantics, refs/CAS and identity/signing. No go-git/libgit2 duplicate edge cases. Content and messages use stdin/private files. |
| OAuth/OIDC | `net/http`, `net/url`, `crypto/rand`, `sha256`, `rsa`, `crypto`, `encoding/base64`, `math/big` | Narrow PKCE flow and RS256/JWKS verifier match the specified discovery issuer, audience, nonce, expiry, subject and scope requirements. No generic OAuth library auto-refresh/retry layer. Algorithm whitelist, bounded JWKS, key-id selection and signature before claim trust require focused security review. |
| Credentials | `os`, `safefs`, private JSON files, 0700 dirs / 0600 files on both OSes | Spec permits implementer choice; file store is also the required test seam. Avoid adding Keychain/cgo libraries or passing secrets to `security -w` argv. If encrypted macOS storage is later adopted, scope/version that work separately without changing the CLI. Never read another tool's login. |
| Process custody | `os/exec`, `os/signal`, `context`, timers, `syscall.SysProcAttr`, `x/sys/unix`, private guardian entry point | Go contexts kill only the immediate process by default. Explicit sessions/groups, TERM→200 ms→KILL, reaping and parent-death supervision are mandatory. |
| Sandbox | Platform wrapper around sandbox-exec / bwrap | Keep process custody separate from confinement. Network allowed; deny credential locations, keep checkout/origin read-only in Build, permit declared workspace/run/temp/cache writes. |
| Hashes, IDs, tests | `crypto/sha256`, `crypto/rand`, `testing`, `httptest`, `time` | No UUID/assert/mock/framework dependencies needed. Golden fake-wire bytes and effect/clock ports test the actual policy. |

## Repository and package boundaries

Future repository: `~/Areas/Kogen/kogen-go`; module name `kogen-go` (local initially; settle publication path before creation). **One module**, no go.work, no public Go library promise. Both binaries import the same internal production packages. Keep files around 400 lines or less; split by responsibility. Only wiring knows the whole application.

```text
go.mod go.sum vendor/ mise.toml Makefile
cmd/kogen/main.go                 public process entry
cmd/kogen-xspec/main.go           private xspec/1 entry
internal/contract/               shared records, errors, clocks/effect interfaces
internal/cli/                    parse/, render/, embedded normative data/
internal/app/                    composition and separate command routes
internal/yamlmini/               lexical, block/flow syntax, locations
internal/project/                schema, role merge, project/origin/base resolution
internal/intent/                 exact source parsing, hashes, lint
internal/safefs/                 rooted paths, durable publication, snapshot restore
internal/process/               supervisor, guardian, environment, platform identity
internal/gitio/                 supervised CLI, private metadata/index, tree/ignore
internal/journal/               events, snapshots, transcript and request telemetry
internal/acceptance/             ledger, command/, exunit/, rails/
internal/findings/               identity parsers and clipped feedback
internal/protection/             manifest/globs, restore and integrity guard
internal/approval/               prepare/, publish/, remove/
internal/status/                 derive/, render/, report/
internal/queue/                  lock/, schedule/, drain/
internal/recovery/               durable crash reconciliation
internal/provider/              transport/, sse/, session/, wire/, retry/, tools/
internal/auth/                   vault/, jwt/, oauth/, refresh/, accounts/, grok/
internal/gate/                   base-relative verification and receipts
internal/workspace/              detached clones and safe seeding
internal/landing/                commit/, publish/, integrate/
internal/build/                  single/, repair/, audit/, recipe/, ladder/, parallel/, select/
internal/shape/                  prompts/, session/, validate/, audit/, run/, witness/
internal/setupcache/             canonical key, COW restore and publication
internal/sandbox/                darwin/, linux/, shared availability policy
internal/optional/               checkpoint/, edge/, staged/
internal/xspec/                  protocol/, intentapprove/, queuestatus/, landingrecovery/,
                                streamsession/, diagnostic/
internal/testkit/                isolated HOME/Git, effect fakes, child helpers
docs/work/                      frozen inputs, interfaces, packages, evidence
tools/                          check, conformance selection, replay, benchmark collection
```

Layers: contract/safefs/process → git/provider/acceptance → approval/gate/state → Build/Shape/drain → app/CLI. Lower packages never import app or xspec. Narrow injected ports carry observations of effects, not pre-decided policy outcomes. Production transitions return state and ordered effects; CLI interprets them, replay feeds the same transitions. Avoid a monolithic core or duplicated replay controllers.

Freeze interfaces in package 00: typed class/reason/exit errors; `ProcessRunner.Run(ctx, spec)`; `GitPort.Exec(ctx, args, stdin, policy)`; rooted filesystem operations; `Clock` plus deterministic jitter source; provider `Respond(ctx, session, request)`; acceptance adapter; verification receipt binding tree and protected manifest; landing effects; state observations. Central shared records live in `contract`; all later ownership is disjoint. Interface corrections are serialized coordinator changes, never three workers editing shared types.

## Lessons implemented before the first real task

1. **Intent format in the shaper prompt.** The system instructions contain a complete example with required `title`, `size`, `domains`, legal optional keys, Brief, Acceptance, Verify and Notes; explain that a body title cannot replace YAML title. Include Verify change/keep semantics and raw Request preservation. Keep the exact first user template and role marker. Test the rejected syn-06 `{}` and syn-20 missing-size sources and exact repair feedback. Their Rust failures came from missing prompt context, not overly strict parsing.
2. **One persistent session object per conversation.** Input, raw response items, controller messages, tool outputs, identities and sticky response routing state belong to it. Shape must reuse that object through normal turns, finish guards, validation and style repairs. Passes 1–3 share it; fallback passes 4–6 use a fresh thread with the same Shape-run affinity. Never reconstruct history from returned items alone.
3. **Cacheable immutable prefix.** Canonical generic instructions and the ordered union of seven schemas precede role instructions and append-only history. `allowed_tools`/`none` restrict calls without changing schemas. One persisted opaque cache key per Build, `session-id` equal to it; separate stable thread per stage/attempt/rung/epoch. Capture/replay opaque `x-codex-turn-state` internally for that thread; log presence, not value. Shared prefix across stages follows the spec; freeze any additional shared approved context in all three language arms before measuring it.
4. **Wire evidence from day one.** Raw B1 without its final `]}` must prefix B2; identical retries preserve complete body bytes. Turn notes/feedback append; instructions never acquire counters, clocks or paths. Model switches retain run affinity and drop prior-model encrypted reasoning. Journal endpoint host/path, request/response times, body size, prefix digests, cache/thread IDs, model/effort, routing-header names and nullable usage; omit credential/header values and prompt contents. Persist protocol identities without putting paths/secrets in IDs.
5. **Role resolution everywhere.** Project → machine → default merge per role/field; shaper/planner/auditor default Sol/high, builder Luna/max. Build audit, shaping audits, context and reviewer use the resolver, not local literals. v1.3 fallback_shaper aliases the effective shaper provider/model/effort; default ChatGPT stays Sol/high, overridden Luna stays Luna, Grok stays Grok. Reject explicit fallback_shaper role keys and cross-provider model overrides. Every measured transport request must match the arm's resolved role manifest or the cell is invalid. Default ladder escalation models are part of the recipe; an all-Luna comparison must select a compatible recipe or record escalation.
6. **All controller writes are safe.** Use rooted descriptors; reject escapes and unsafe publication leaves; atomic replacement unlinks/replaces a symlink itself rather than opening its target. File fsync and parent-directory fsync at publication boundaries. Prevent parent-symlink races, hardlink alias writes and FIFO/device hangs. State/journal/cache/approval staging/tool handles/protection restoration all use `safefs`, not isolated patches around model writes. [Go's Root API](https://pkg.go.dev/os#Root) confines relative symlinks but is not an OS sandbox and does not alone enforce no-follow leaf/hardlink policy; use `x/sys` descriptor operations as necessary.
7. **Native Git ignore semantics.** Compute the candidate tree relative to the immutable build base, regardless of builder HEAD/index. Tracked base paths stay included even if newly ignored. Respect nested `.gitignore`, negation, directories, dotfiles and executable/symlink modes; ignore workspace `.git/info/exclude`, global excludes, hooks, filters, textconv and fsmonitor. Use trusted private metadata/index, Git's `check-ignore --no-index -z --stdin` for untracked eligibility, and `hash-object --no-filters`/`update-index -z`/`write-tree` for exact bytes. Do not reimplement ignores as glob matching. Reject unsafe .git replacement; approval uses exact byte objects even if draft paths are ignored.
8. **Every Git invocation is supervised.** Same deadlines/group cleanup/bounded logs/parent-death behaviour as checks, including status, clone/fetch, signing helpers, commit-tree, update-ref, recovery and ignore detection. Origin identity and signing apply to landing; workspace config never selects helper programs. Disable hooks explicitly. Messages and large path lists use stdin/files, argv ≤4 KiB per element. Separate production identity policy from hermetic test policy.
9. **Executable integration early.** As soon as approval, fake provider, one verified rung and landing exist, wire real `queue start`; prove approve → queue → provider → gate → CAS → status. A passing replay is not permission to leave a command handler unavailable.

## Custody and durable safety design

Launch project commands and controller Git helpers through the common supervisor. A private guardian mode of the installed binary is selected through inherited control FDs/internal environment, never a new public option. Guardian launches the child group only after establishing a parent-liveness pipe and handshake, monitors EOF even when Kogen is SIGKILLed, enforces independent wall timers, pumps logs without unbounded buffers, TERM/KILLs groups and waits/reaps. Each helper gets a bounded log/tail and explicit environment; avoid arbitrary goroutine process leaks. Linux's parent-death signal/subreaper can reinforce the design; macOS requires the external guardian. Test escaped sessions/stray grandchildren and narrow the guarantee explicitly if OS limitations remain; never claim SIGKILL custody from a context alone.

Use native pid start identity (`/proc` on Linux, Darwin kernel process metadata through the platform port) with pid to prevent reuse mistakes in claim/recovery. Queue detach launches the installed binary in a new session with `/dev/null` stdin and queue.log, and the drain owns its lock. Queue stop requests drain after the current Build. Before recovery snapshots, stop run-owned writers and freeze the latest workspace. Create-only deterministic recovery identities permit adoption after a publication/record crash; if later bytes differ, retain the prior snapshot and publish another. Try lossless durable archive if Git publication fails, retain workspace/refs if both fail, and retry cleanup_pending even on terminal runs. Release owned claim after recording the outcome/preservation result; no destructive cleanup without durable completeness. Landed post-CAS outcome and later unverified edits remain separate records.

macOS sandbox profiles are private files, passed by path. Linux bwrap uses a read-only root, explicit writable workspace/run/temp/tool-cache mounts and masking of credentials, SSH/GPG/Codex/Keychain/injected auth. Give the workspace a trusted writable Git metadata arrangement without exposing origin/checkout. Test positive allowed writes and negative denied reads/writes; do not treat sandbox launch success as sufficient. On unavailable confinement, warn once, journal `sandbox_unavailable`, build unconfined, and compare checkout HEAD/index/tracked bytes and origin refs against pre-Build integrity snapshots. Detection stops with controller bug semantics. `sandbox:false` and already-confined mode remain distinct.

Approval hashes exact Intent + NUL + test bytes, checks mismatch before any expensive work, re-reads immediately before CAS, and chains immutable approval commits. Verified receipts bind the exact candidate tree. Landing has one base parent and user's signing policy; record landing + incoming ref durably before base CAS. A moved base triggers rebase and full guard/gate under separate landing allowance, never overwrites the base. Recovery marks reachable candidate landed after a post-CAS crash, releases only owned claims and treats cleanup failure as nonfatal after landing.

## Quint adapter and gates

`cmd/kogen-xspec` accepts exactly one private slice argument, maintains a long-lived process, reads UTF-8 JSONL and writes one observation per request with flush. `reset` returns initial full observation; `apply` decodes an event, invokes production transition/effects and maps observation. Unknown op/tag/type, bad JSON or oversize protocol input fails nonzero with diagnostics on stderr. It reads no production HOME, account, credentials or network. Effectful approval/Intent/landing can use a temporary bare origin and real source bytes through `testkit`; fake clocks/HTTP/check results are explicit injected effects.

Required G slices: **intent, approve, queue, status, recovery, rebase, stream, session**. Initial pre-draft hand counts were 6,18,9,8,5,7,10,7 respectively; a new dirty Intent scenario and stream scenario now exist, and draft approve/recovery/session schemas require migration. Package 00 freezes actual coherent counts after shared migration; do not replay old goldens against changed models and call it Go failure. For each slice: all frozen hand scenarios plus 500 traces ×25 steps for **each** seed 17,23,41; compare reset and every full observation, preserve first divergence. That is 1,500 generated traces /37,500 events per slice. Include the draft's embedded baseTreeCacheTest, observationalTest, preservation/idempotence tests and crossSessionPrefixTest alongside black-box real-I/O evidence.

Use the shared harness in a scratch copy, because `spec`/`gen` write artifacts. Per-slice/per-seed output isolation avoids concurrent workers clobbering generated traces. Example, from the scratch `quint/prototype`:

```sh
XSPEC_SLICE=../slices/queue python3 harness/xspec.py spec
XSPEC_SLICE=../slices/queue python3 harness/xspec.py gen --traces 500 --steps 25 --seed 17
XSPEC_SLICE=../slices/queue python3 harness/xspec.py conform -- /absolute/bin/kogen-xspec queue
```

Repeat generation/conform for seeds 23 and 41. Adapter and CLI binaries come from the same source revision. `--project` projection, `no_seam`, skip and unimplemented are never passes. `gate`, `orchestration`, `accounts`, `setup-cache` full slices remain diagnostic under current L/E classification; their required black-box behaviours still must pass. Historical `resilience` and prototype landing are excluded from release totals.

Rust's original approval replay diverged on symbolic versus real digests. Design a documented symbol→actual-bytes/ref mapping, preserving symbol equality and observable names while production computes actual hash/prefix and late-read stability. Never construct a matching prefix from supplied `prefixOk`, copy a supplied expected observation or maintain a second policy machine. If the frozen symbolic model cannot express the real effect boundary, report the gap and request a shared model revision; do not manufacture a green replay with Rust's boolean-driven `modeled_claim` pattern. Independently exercise real-byte mismatches, changed test/Intent and failed CAS via the public oracle.

## Hermetic verification and integration

`make check` must work with a blank temporary HOME, no credentials, no model traffic, no network and arbitrary global Git settings. It uses the pinned Go binary (`GOTOOLCHAIN=local`, `GOFLAGS=-mod=vendor`, `GOWORK=off`), verifies vendored graph/corpus fingerprints, checks gofmt without editing, runs go vet and `go test -count=1 ./...`, builds both binaries with `CGO_ENABLED=0`, and refuses unexpected generated/source changes. Each test child gets explicit empty global/system Git config, local identity and disabled signing; no assumptions about developer .gitconfig, mise, keychain or HOME. Do not erase the user's environment globally. Constants marked scaled follow KOGEN_TIME_SCALE; project timeouts never scale. Avoid hidden library retry layers.

`make check-full` adds race tests and external conformance/replay on frozen scratch oracles with bounded concurrency. It does not download tools/dependencies or modify upstream repos. Fake OAuth is serialized on port 1455 across all active checks. Conformance is never retried to turn a fail into a pass; diagnose/change then record a new revision/run. Scaled-time load flakiness is not a reason to omit custody assertions.

Always select the overlay, even for narrow cases:

```sh
SUITE=/absolute/frozen/kogen-conformance
"$SUITE/bin/kogen-conformance" run --kogen /absolute/bin/kogen \
  --profile cli,state,approval,shape,build,ladder,provider,custody,format,v1.2 \
  --case 'approval-03,state-08' --jobs 2 --time-scale 0.02 \
  --workdir /absolute/evidence/package/work --out /absolute/evidence/package/results.jsonl
```

The runner maps selected superseded old IDs to their replacements. Log resolved IDs. Omitting `--case` gives the complete **236/570** historical standard run, including login; v1.3 incompatibilities remain reported as observed, not passes. Local spec P1–P13 and draft fixtures supplement gaps but do not inflate that denominator or replace a shared frozen v1.3 suite. Rails/ExUnit fixture validation, filesystem race tests, signing-helper supervision and Linux parity are separately named evidence.

Dispatch follows the Rust pattern with **three** Luna/max workers initially, one isolated worktree each, only disjoint files, no worker merges or pushes. Copy no script verbatim: replace pgrep counting and STARTED/MERGED/FAILED text flags with pid/start-time plus task-state records; readiness requires prerequisite accepted merge SHA, not completion. Use one merge lock, clean-base check and CAS fast-forward; rebase, hermetic check and targeted CLI gate before MERGED. Split/requeue a package that reaches 90 minutes, preserving its branch/evidence. No force-delete of unresolved worktrees. Default nice 10; coordinator reports useful progress and host/orphan state without asking the owner to poll. Integration rounds are specified in QUEUE; integration failures return to their file owner.

## Comparison design

Freeze before running cells: shared spec/oracle digests; all implementation and binary SHAs; task trees/requests and independent graders; recipe and every effective model/effort/provider mode; allowed fallback/escalation; wall/turn/repair caps; sandbox policy; tool/result budgets; host OS/Git; cell count, failure taxonomy, stopping rule and dollar-rate snapshot. Reuse compatible prior Rust evidence only if these match; otherwise label descriptive historical data. A v1.2 implementation compared with a v1.3 implementation is not language evidence.

Two real-task lanes: (a) identical manually approved Intent+acceptance bytes to isolate Build, (b) full request → Shape → approval → Build to measure end-to-end success and shaping yield. Use held-out tasks across command, Rails and ExUnit where provisioned; include syn-06/syn-20 frontmatter regression tasks, changed-file/migration/dependency tasks and difficult multi-step work. Independent grader success leads; Kogen's own green receipt is not the task grader. Start with a small predeclared pilot, e.g. 8 tasks ×3 repetitions ×3 languages =72 cells per lane; launch only after admission and a fixed spend cap. Do not launch this in a planning job.

Pair runs on the same host and interleave language order to reduce provider/time/cache bias; independently initialize origin/HOME per cell. Freeze a declared cold/warm protocol; never assume a new directory makes provider cache cold. Run macOS language comparisons on Studio, Linux comparisons on the same Linux machine; do not pool raw latency across venues. Use matched effective roles, including every auditor and fallback. Small offline/fake diagnostic fixtures can pin every configurable role to Luna/medium; that is a separate arm, not silent replacement of default Sol shaping or mandatory Sol fallback.

| Dimension | Report |
|---|---|
| Conformance | Cases and expanded instances passed/failed/error/skipped, exact IDs, G replay counts/divergence, P1–P13/draft coverage, OS/sandbox and binary SHA. All compatible v1.2 assertions green; incompatible demotion/cleanup assertions listed with exact observed failures. Release requires every case of the new shared frozen v1.3 oracle and every G trace, with no skipped/unimplemented/unmatched request. Never label a subset 236/236. |
| Real-task success | Per-task independent grade; full-lane shaping yield; landed/parked/failed/stopped/interrupted/infrastructure counts; declared denominator includes failed tasks. Paired uncertainty; no winner from pilot data alone. |
| Cost | Shape and Build separately: uncached input, cached input, cache writes, output/reasoning, calls/retries and elapsed time. Subscription runs have measured usage, not a fictional per-run invoice. If dollar-equivalent is useful, freeze verified rates/model/endpoint/date externally and label the estimate. No new public USD ledger. Include failures and worker-building cost. |
| Speed | End-to-end and Build/Shape phase times; request TT first body byte, tool/gate/setup/landing time; p50/p95, CPU and peak RSS; offline fake/no-op/status throughput separates Go overhead from provider latency. |
| Build effort | Luna/max worker wall-hours, coordinator/integration hours, model token usage/cost, queue failures/rebases/rework, time to first end-to-end green and final parity, cold/warm compile/check time and binary size. Distinguish Rust's accumulated development from Go's advantage of mature spec/reference. |

Live cache admission uses the draft's **frozen feasible replay**, superseding the workflow's historical universal warm-turn hold. Freeze model/endpoint/adapter/prompt/tokenizer or token counts, account/security namespace, affinity, retention, adapter-specific eligibility/block rules, request sequence, appended-token budget and designated third-and-later requests. Require theoretical eligible-input/total ≥0.95 **before** running, complete telemetry, then observed cached/total ≥0.95 for each designated request. A scripted fake proves accounting only. Arbitrary tasks report weighted raw rate and eligible-prefix reuse, without universal failure: 4096 repeated+1000 new gives 80.38% even with perfect reuse. Unknown usage/eligibility is incomplete measurement, not an observed miss; partial weighted totals say partial. Report excess cached tokens separately and zero eligibility as inapplicable. Compare same-session and separate-session Shapes/Builds/Shape-to-Build. Do not pad prompts or adopt broader affinity without a shared measured adapter decision. No qualifying live result is claimed by the draft or this plan.

## Effort, Studio needs and risks

QUEUE has 73 numbered implementation/verification packages (00–72), three concrete 60–90 minute draft packages and eight bounded integration rounds. Listed nominal allowance totals **114.5 worker-hours**; budget **about 115 hours baseline**, then **25–35%** for conformance, OS custody, remaining draft/oracle migration and repeat workers: **145–160 worker-hours** total. Add **8–12 coordinator/review hours**. Live provider waiting and scored cells are excluded and budgeted separately. These are planning allowances, not measured Rust effort or a promise that a difficult subsystem finishes in one session.

Studio assumption from the user: **10 cores**. The machine used to read inputs reports 12 cores/24 GiB and macOS 26.6.2; that is **not** evidence of Studio's inventory. Verify Studio explicitly. Minimum 16 GiB RAM, recommended 32 GiB; 80–100 GiB free local SSD for worktrees, vendored tools, isolated test clones and retained failures. Use local APFS, not iCloud, for implementation/worktree/build/cache/test execution. Network needed for workers and later live provider cells; fake checks stay offline. Keep two cores for coordinator/OS; start MAX_WORKERS=3, each `GOMAXPROCS=2`, Go compile `-p=2`, tests `-parallel=2`. Pause worker checks during serialized race/custody/final conformance; final runner starts jobs=2–4. Admit four workers only after measuring RSS/swap/check duration. Model work is remote, so worker count need not equal cores.

At three workers, ideal baseline is about 38 active host hours; dependencies, narrow sequential phases and integration make **45–65 elapsed working hours** more plausible, with 6–9 working days at normal attention, excluding external oracle waiting. Provision a Linux VM/native host with 4+ cores, 8–16 GiB RAM, user namespaces and the pinned sandbox for real Linux custody runs; cross-compilation is insufficient. Apple Silicon Linux VM is acceptable for parity; Linux speed comparisons need a matched host for all languages.

| Risk | Control / consequence |
|---|---|
| Moving draft and dirty models | Content-addressed shared baseline, delta owners D1–D3, no impacted dispatch before reconciliation; honest separate v1.2 and v1.3 results. |
| 90-minute packages hide integration cost | Small file ownership, stable ports, early CLI round, reserved integration/rework hours; split overrun branches, never report component-only success as completed feature. |
| macOS parent-death and sandbox limitations | Guardian handshake/SIGKILL tests and real OS runs early; budget extra platform packages if groups escape. |
| Filesystem publication and malicious workspace Git | Rooted no-follow safe publication, private Git metadata/index, native ignore fixtures, supervised helpers and adversarial matrix. |
| OAuth surface and live endpoint variation | Narrow stdlib implementation reviewed against fake OIDC; bounded verified claims, serialized refresh, no credentials in tests; live sign-in is later separate evidence. |
| Cache telemetry/eligibility may be unknown | Reject infeasible smoke definitions; use draft feasible ≥95% replay, raw production metrics separately, complete records before admission. |
| Model contamination / hidden fallback | Immutable effective role manifest; validate every transport request and invalidate contaminated cells, including Shape fallback and parallel escalation. |
| Go encoding/clock/process edge cases | Canonical immutable item bytes, UseNumber, incremental byte framing, fake monotonic clock, race tests and bounded supervised output. |
| Host load invalidates scaled deadlines and timings | Serialized custody/login and matched benchmark windows; record load/swap, do not retry oracle failures without changed evidence. |
| Optional features have weaker measurement support | Implement required same-spec paths for parity, label L/E replay diagnostic; do not enable witness/edge/checkpoint by default or invent Go-only optimizations. |

The deliverable for the later build is an independently conforming executable with reproducible evidence. No language winner is predicted by this plan.
