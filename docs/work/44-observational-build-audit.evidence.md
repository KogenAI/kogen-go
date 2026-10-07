# 44 — Observational Build audit evidence

## Gate and revisions

**Gate: component-ready. Behavior acceptance remains open.** The audit component is implemented in `internal/build/audit/**`; the public CLI/ladder does not call it yet.

- Branch: `kgo/44-observational-build-audit`.
- Starting revision: `b726da0ca8ed4eb2e1203a5fcc101423b6a8c815` (package 43 base).
- Implementation commits: `dc18c7b` (`Implement observational build audit`) and `4d5d9c1` (`Bind build audit session to cache key`); final source revision `4d5d9c1f8162f504bc40853d19bdf7629ec34da1`.
- Target specification: `kogen-spec` v1.3-draft `e19dd1c21c19c5be1201c3b6a42c59c28b5c2887`; read `CHANGES-v1.3.md` §1, `spec/02-formats.md` §§2.3/2.8, `spec/03-build.md` §§3.0/3.8.2, and `spec/04-provider.md` §§4.7/4.8.2.
- Rust reference: `kogen-rs` `a402540b39cedc7f788472297add7ae2f8a6631a`, especially `crates/kogen-core/src/run/orchestration/auditor.rs`, its tests, `build/provider_prompt.rs`, and the rung execution call site. The historical Rust demotion path was not ported.
- Frozen suite: read-only `~/cx/kgo/inputs/conformance-v1.2`, `VERSION=1.2`; runner SHA-256 `0b53a3b60956cbe8dde7af54e274c46b3e8d430c45662d7dff67445f440b39f9`. Result metadata labels it `v1.2+unknown`.
- Toolchain/host: Go `go1.27.1 darwin/arm64`, Git `2.54.0`, macOS 26.7.1 arm64. Pinned worker paths were exported. The CLI source remained the bootstrap handler; `cmd/kogen` and the conformance adapter were not changed. The final CLI built by `make check` has SHA-256 `014aaf4bb286a654f68bbd27f8948025bed0a0167d985cda54599cdc0e628106`.
- Final package file SHA-256: `audit.go` `8d2240361e317dcc2859ec929751e700827f6fc7f009cc5cc6d6f5188e29a973`; `audit_test.go` `859e66b728893b55521ab7b04afecffe61227edd0b4019203e5066239b0bee65`; `doc.go` `d3a3419efbf15376c3b7fc2eef10f1ed25d27e8cb5850ee92ff92e641c3ca027`.

## Implemented component

`New` uses the resolved `auditor` role from `contract.RoleManifest`; it supplies no local model default. `BeforeRepair` accepts only a controller-declared acceptance-only red opportunity, skips witness mode, checks the fresh session's auditor role, `session-id == Build cache key`, and run/stage/attempt/rung/epoch thread binding, appends the request to that same session, and sends a tool-less prompt with the exact test-auditor marker. The transport contract requires provider retries to reuse that conversation and keep the canonical schema union while restricting calls with `tool_choice=none`.

The request carries the failed item IDs, verbatim Request, acceptance source, failure output, and candidate diff clipped at 60,000 Unicode characters. One shared `Auditor` permits at most one audit request per failed item per Build and at most one request in a rung. `valid`, `over_strict`, and `contradicts` are parsed in approved-item order. Malformed, unknown, duplicate, and missing rows produce warnings and cannot become advice. Transport failure is also warning-only.

Only accepted rows are passed to the gate's display-only `RecordAuditAdvice` channel. The result has `mode=observational`, `demoted=false`, and `advisory_items=[]`; journal output goes through `journal.AuditEvent`, which fixes those fields and emits only an `audit` event. Non-valid advice can be appended before repair, with explicit text that every approved item remains required and must pass verification. There is no API in this component to change a gate verdict, acceptance count, rank, winner, or landing decision, and it emits no `acceptance_demoted` event.

These package tests are component evidence, not frozen D-AUD results. The repository's existing gate tests also exercise that `RecordAuditAdvice` leaves its counts and landability unchanged.

## Commands and results

Commands used the pinned worker PATH.

1. `gofmt -w internal/build/audit/*.go` — passed.
2. `GOMAXPROCS=2 go test -p=2 -parallel=2 ./internal/build/audit` — passed (`ok kogen-go/internal/build/audit`).
3. `GIT_CONFIG_GLOBAL=/dev/null make check` — passed twice. The later run includes the final session/cache-key validation; format, vendor fingerprints, vet, all tests, and both builds passed. Full log: [`make-check-cache-binding.log`](/Users/almirsarajc/cx/kgo/evidence/44-observational-build-audit/make-check-cache-binding.log), SHA-256 `e8bf754289f824b6e034f48f8e39e583bc1976aad7c9149793149cb74680f7a8`. The earlier successful log remains at [`make-check.log`](/Users/almirsarajc/cx/kgo/evidence/44-observational-build-audit/make-check.log).
4. Required frozen v1.2 command (run once):

   ```sh
   make build
   SUITE="$HOME/cx/kgo/inputs/conformance-v1.2"
   EVIDENCE="$HOME/cx/kgo/evidence/44-observational-build-audit"
   mkdir -p "$EVIDENCE"
   PYTHONDONTWRITEBYTECODE=1 PATH="$HOME/.local/share/mise/installs/python/3.14.7/bin:$PATH" \
     "$SUITE/bin/kogen-conformance" run --kogen "$PWD/bin/kogen" \
     --profile cli,state,approval,shape,build,ladder,provider,custody,format,v1.2 --case 'v1.2-68-build-44,v1.2-73-ladder-05,v1.2-74-ladder-06,v1.2-75-ladder-07,v1.2-76-ladder-08,v1.2-77-ladder-09,v1.2-78-ladder-10,v1.2-79-ladder-11,v1.2-96-ladder-28' \
     --jobs 2 --time-scale 0.02 --workdir "$EVIDENCE/work" \
     --out "$EVIDENCE/results.jsonl"
   ```

   Resolved IDs and actual results: each of `v1.2-68-build-44`, `v1.2-73-ladder-05`, `v1.2-74-ladder-06`, `v1.2-75-ladder-07`, `v1.2-76-ladder-08`, `v1.2-77-ladder-09`, `v1.2-78-ladder-10`, `v1.2-79-ladder-11`, and `v1.2-96-ladder-28` resolved to one instance. The run exited 1: **9 cases / 9 instances, 0 passed, 9 failed, 0 errors, 0 skipped**. Every instance stopped at step 2 (`intent approve greet`) with exit 2 instead of 0 and stderr `kogen: implementation bootstrap; command routes are not wired`; no provider request reached the fake server. Thus there are **zero compatible public behavior passes**. Results JSONL: [`results.jsonl`](/Users/almirsarajc/cx/kgo/evidence/44-observational-build-audit/results.jsonl), SHA-256 `596cb1223f37f388697cc3d71aa3904f37f5cb2eb9153cf0088beb8c70e69ce5`.

The oracle command ran before the final per-item-across-Build de-duplication and session/cache-key validation adjustments. The final source was then tested by the focused package test and full `make check`; the CLI route still does not import or call this component. The first oracle output is retained and was not replaced by a retry.

## Historical v1.2 conflicts and compatible assertions

These are fixture/spec differences identified from the frozen overlay; the public run above did not reach any of these assertions.

- `v1.2-68-build-44` expects the auditor request to expose `finish`, `shell`, and `tool_output`, and expects an `acceptance_upheld` event. Draft §4.7 gives auditors no tools; draft §2.8 records observational `audit` receipts and does not use `acceptance_upheld`.
- `v1.2-73-ladder-05` and `v1.2-75-ladder-07` expect `over_strict` A2 to be demoted without a citation, allowing an advisory landing. Draft §3.8.2 keeps A2 required and makes every verdict advisory.
- `v1.2-74-ladder-06` expects `land: green` to park a candidate as `green-with-advisory-tests` after demoting failed A2 without repair. Draft policy cannot demote A2; the candidate must pass every approved item.
- `v1.2-76-ladder-08` expects unsupported `infeasible` to be treated as valid/upheld. Draft accepts only `valid`, `over_strict`, or `contradicts`; unknown verdicts warn.
- `v1.2-78-ladder-10` expects the auditor to demote the failed change item. Draft keeps every approved item in verification and landing eligibility. The old fixture's no-landing result itself is not a conflict; the demotion assertion is.
- `v1.2-79-ladder-11` expects demoting A2 to let R1 land without repair. Draft requires actual passing verification after repair.
- `v1.2-77-ladder-09`'s invalid-JSON/no-demotion assertion is compatible with the draft's observational behavior; the draft additionally requires a warning. `v1.2-96-ladder-28`'s at-most-once-per-item-per-Build assertion is compatible and is implemented. Neither was behavior-verified by the public run.

## Effects and deferred closure

- Local effects: the final `make check` built `bin/kogen` and `bin/kogen-xspec`; the assigned runner created its isolated work directories and the retained JSONL under `/Users/almirsarajc/cx/kgo/evidence/44-observational-build-audit`. No source suite, golden, spec, other worktree, or external replay harness was changed. No live provider/account access occurred.
- The Build/ladder controller and concrete provider/wire adapter do not call this package yet. Its caller must derive `AcceptanceOnlyRed` from the immutable gate result and call `BeforeRepair` before repair. Public approval → queue → provider → gate → repair → selection/landing behavior remains for I4/D1 integration.
- The shared `internal/provider/session.Bind` currently derives a separate Lite session ID. This component enforces the worker-rule contract that the Build `session-id` equals its persisted cache key; the session/wire integration owner must reconcile that helper before wiring this API. No files outside the owned package were changed.
- `D-AUD-01–05` are planned fixture names, not frozen cases. Do not claim they passed; wait for shared frozen v1.3 IDs and run their replacements green. `I4` and `D1` remain open.
- No coherent shared v1.3 Quint cohort was available, so `R(slice)` was not run: no spec replay or 500 traces × 25 steps for seeds 17, 23, and 41; no same-revision private-binary conformance or divergence claim.
- Linux, optional-runtime, and live comparison gates require their external evidence and remain open. This component receipt does not confer behavior acceptance.
