# 27 HTTP attempts and retry policy evidence

## Receipt

- Accepted gate: **component**. The transport and retry components are implemented and locally verified; production CLI/provider/ladder wiring is not present at this revision.
- Worker revision: `6c4762f2c2ef6ce8183f0e96373bc995a04e8793` (`Implement provider HTTP retry policy`). The CLI binary was built from this revision. The xspec adapter binary was also built from it, but its stream route still exits at the bootstrap; there is no separately wired provider adapter revision to report.
- Draft spec: `e19dd1c21c19c5be1201c3b6a42c59c28b5c2887`. Rust reference checkout: `a402540b39cedc7f788472297add7ae2f8a6631a`.
- Platform/toolchain: macOS 26.7.1 arm64; Go 1.27.1, Git 2.54.0, Python 3.14.7, Node 24.21.0, Quint 0.33.0.
- Frozen suite input: `$HOME/cx/kgo/inputs/conformance-v1.2`. Its runner reported `v1.2+unknown`; the supplied directory has no Git metadata, so no suite commit could be resolved. Runner SHA256: `0b53a3b60956cbe8dde7af54e274c46b3e8d430c45662d7dff67445f440b39f9`.

The component implements a single-attempt HTTP transport with request clock beginning before preparation/refresh, first-body-byte, post-first-byte idle and total deadlines, cancellation, bounded response capture, classification and redacted attempt diagnostics. The retry executor provides bounded retry budgets, jittered backoff, identical retry requests, partial-response continuation, forced-refresh tracking, Build login/usage waits, overload switching and fallback-role validation. Package tests cover these helpers. These are component behaviors only until the real provider/session and ladder paths call them.

## Commands and results

All commands used the repo-pinned tools in `PATH` as required.

```sh
GOMAXPROCS=2 go test -count=1 -p=2 -parallel=2 ./internal/provider/transport ./internal/provider/retry ./internal/provider/sse
```

Passed for all three packages.

```sh
GIT_CONFIG_GLOBAL=/dev/null make check
```

Passed: formatting/vendor checks, vet, full Go tests and build checks.

```sh
make build
```

Passed. Built binary SHA256 values: `bin/kogen` `c579afd15b2ab06e6e7380735252e227341d04fa330a610a37ef67049a38bbad`; `bin/kogen-xspec` `20f56bdcef1e7bbba2bd0870b338cdaf0801e8cf9a9aac29abe711bdc8d47b7f`.

The frozen v1.2 acceptance command was run exactly as assigned:

```sh
make build
SUITE="$HOME/cx/kgo/inputs/conformance-v1.2"
EVIDENCE="$HOME/cx/kgo/evidence/27-http-attempts-retry-policy"
mkdir -p "$EVIDENCE"
PYTHONDONTWRITEBYTECODE=1 PATH="$HOME/.local/share/mise/installs/python/3.14.7/bin:$PATH" \
  "$SUITE/bin/kogen-conformance" run --kogen "$PWD/bin/kogen" \
  --profile cli,state,approval,shape,build,ladder,provider,custody,format,v1.2 --case 'provider-10,shape-24,shape-25,v1.2-04-cache-key-session-headers,v1.2-103-ladder-35,v1.2-110-provider-08,v1.2-111-provider-09,v1.2-112-provider-11,v1.2-113-provider-12,v1.2-114-provider-14,v1.2-115-provider-17,v1.2-116-provider-18,v1.2-117-provider-20,v1.2-125-ladder-36,v1.2-28-provider-13-planner-no-fallback,v1.2-29-provider-15-idle-stall,v1.2-30-provider-16-total-cap,v1.2-90-ladder-22' \
  --jobs 2 --time-scale 0.02 --workdir "$EVIDENCE/work" \
  --out "$EVIDENCE/results.jsonl"
```

Result: **0/18 cases pass; 18 fail across 19 instances**. The resolved IDs are exactly the 18 IDs in the command above (also recorded in `27-http-attempts-retry-policy.cases`). Every instance stopped at the public CLI route with exit 2, empty stdout, and stderr `kogen: implementation bootstrap; command routes are not wired`; no provider request reached the fake server. `provider-10` expands to two login instances (401 and 403), both failing at `kogen provider login chatgpt`. The retained full results are at `$HOME/cx/kgo/evidence/27-http-attempts-retry-policy/results.jsonl`.

Compatible behavioral passes: **none**. This run does not reach the implementation, so it establishes no retry-policy behavior and diagnoses no historical v1.2/spec conflict. It is not counted as a component failure or a behavior pass.

## R(stream) diagnostic

The mandated replay was run in an isolated scratch copy at `/Users/almirsarajcic/cx/kgo/evidence/27-http-attempts-retry-policy/replay/stream/quint/prototype`, with the copied slice at the adjacent `quint/slices/stream`. The source spec checkout was at the requested revision, but its Quint prototype/slice had uncommitted changes (25 changed/untracked paths); this was therefore diagnostic evidence only, not a frozen-cohort release result. The source checkout and oracle were not modified.

Commands were run in order:

```sh
XSPEC_SLICE=../slices/stream python3 harness/xspec.py spec
XSPEC_SLICE=../slices/stream python3 harness/xspec.py gen --traces 500 --steps 25 --seed 17
XSPEC_SLICE=../slices/stream python3 harness/xspec.py conform -- "$KGO_BIN/kogen-xspec" stream
XSPEC_SLICE=../slices/stream python3 harness/xspec.py gen --traces 500 --steps 25 --seed 23
XSPEC_SLICE=../slices/stream python3 harness/xspec.py conform -- "$KGO_BIN/kogen-xspec" stream
XSPEC_SLICE=../slices/stream python3 harness/xspec.py gen --traces 500 --steps 25 --seed 41
XSPEC_SLICE=../slices/stream python3 harness/xspec.py conform -- "$KGO_BIN/kogen-xspec" stream
```

For the conform commands, `KGO_BIN=/Users/almirsarajcic/Areas/Kogen/kogen-go-wt/27-http-attempts-retry-policy/bin`. Spec check passed all 11 hand scenarios. Each generation completed 500 traces × 25 steps (12,500 events) with model invariants holding: seed 17 accepted/refused 10,963/1,537; seed 23 10,960/1,540; seed 41 10,907/1,593. Each conform run returned **0/511 agreeing traces and 0 steps**, diverging at reset because the adapter returned `kogen-xspec: implementation bootstrap; command routes are not wired`. No full adapter observations were produced; no replay conformance is claimed. Artifacts are retained under the scratch directory above.

## Scope and remaining closure gates

- I3/public behavior integration remains open: wire the transport/retry component through provider/session and real command paths, then rerun the assigned frozen acceptance command. The observed CLI bootstrap currently blocks those cases.
- I4 mid-rung wait behavior remains open until the ladder/controller consumes the Build login/usage wait result and resumes correctly; this package exposes policy hooks but does not own or modify that integration.
- R(stream) belongs to package 68's production stream-session adapter. The current `kogen-xspec` bootstrap cannot provide the required full observations. Repeat against the shared coherent migrated Quint cohort after it is frozen.
- Planned D-* fixtures are unavailable in v1.2. Wait for shared frozen v1.3 IDs; do not treat draft-only cases as passed.
- Linux parity, optional runtime checks and live provider comparison require their stated external evidence. No live account/provider call was made.

No exact historical behavior conflict was established by this run. Component readiness and the passing local checks do not confer behavior acceptance.
