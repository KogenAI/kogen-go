# I3-first-executable-approval-landing

Goal: First executable approval→landing. Time allowance: 90 minutes including verification and commit.

Dependencies: I2,25,36,37,38,39,40,41,42,62,D3.

## Owned files and deliverable

`internal/app/build_routes.go`, `internal/app/build_routes_test.go`, `internal/process/entry.go` guardian registration, `docs/work/I3.md`; evidence: docs/work/I3-first-executable-approval-landing.evidence.md

## Acceptance cases

Public approve→queue→fake plan/builder→gate→CAS→status, max_rungs1 green; S10 no request; V03–06,V37–38,V43–64,V67,V104–118,V130–131, provider/macOS/queue/signals, D-REC-01–06 and new draft schema receipts. Run historical anchors with explicit incompatible cleanup/schema assertions, later audit/ladder open. **Unwired handler blocks gate.**

Exact command against the frozen v1.2 oracle (literal effective IDs, with overlay):

```sh
make build
SUITE="$HOME/cx/kgo/inputs/conformance-v1.2"
EVIDENCE="$HOME/cx/kgo/evidence/I3-first-executable-approval-landing"
mkdir -p "$EVIDENCE"
PYTHONDONTWRITEBYTECODE=1 PATH="$HOME/.local/share/mise/installs/python/3.14.7/bin:$PATH" \
  "$SUITE/bin/kogen-conformance" run --kogen "$PWD/bin/kogen" \
  --profile cli,state,approval,shape,build,ladder,provider,custody,format,v1.2 --case 'approval-01,approval-02,approval-03,approval-04,approval-05,approval-06,approval-07,approval-08,approval-09,approval-10,approval-11,approval-12,approval-13,approval-14,approval-15,approval-16,approval-17,approval-19,approval-20,approval-21,approval-22,approval-23,approval-24,build-01,build-32,build-35,build-36,build-37,build-38,build-39,build-40,build-41,build-42,cli-10,cli-15,cli-18,cli-19,cli-20,cli-22,cli-23,cli-28,cli-29,cli-30,custody-01,custody-02,custody-03,custody-04,custody-05,format-02,format-03,format-04,format-06,format-08,format-09,provider-10,provider-19,provider-22,provider-24,provider-26,shape-09,shape-16,shape-24,shape-25,state-01,state-03,state-04,state-05,state-07,state-08,state-09,state-10,state-13,state-16,state-17,state-18,state-19,state-21,state-24,state-25,state-28,state-29,state-30,v1.2-01-fixed-cli-help-and-grok,v1.2-02-approval-hash-intent-and-test-bytes,v1.2-03-consecutive-request-byte-prefix,v1.2-04-cache-key-session-headers,v1.2-05-missing-usage,v1.2-06-crash-after-base-cas,v1.2-07-cli-03-unknown-command,v1.2-08-cli-04-unknown-subcommand,v1.2-09-cli-05-unknown-option,v1.2-10-cli-06-missing-positionals,v1.2-103-ladder-35,v1.2-104-provider-01,v1.2-105-provider-02,v1.2-106-provider-03,v1.2-107-provider-04,v1.2-108-provider-06,v1.2-109-provider-07,v1.2-11-cli-07-unexpected-argument,v1.2-110-provider-08,v1.2-111-provider-09,v1.2-112-provider-11,v1.2-113-provider-12,v1.2-114-provider-14,v1.2-115-provider-17,v1.2-116-provider-18,v1.2-117-provider-20,v1.2-118-provider-25,v1.2-119-custody-06,v1.2-12-cli-08-option-needs-value,v1.2-120-custody-07,v1.2-121-custody-08,v1.2-122-custody-09,v1.2-123-custody-10,v1.2-124-build-31,v1.2-125-ladder-36,v1.2-126-state-15,v1.2-127-build-10,v1.2-128-format-05,v1.2-129-cli-27,v1.2-13-cli-09-boolean-takes-no-value,v1.2-130-state-11,v1.2-131-state-12,v1.2-132-state-23,v1.2-133-state-26,v1.2-136-format-10,v1.2-137-format-12,v1.2-14-cli-11-unknown-provider,v1.2-15-cli-12-watch-with-json,v1.2-16-cli-13-double-dash,v1.2-17-cli-14-options-before-command,v1.2-18-cli-16-short-option,v1.2-19-cli-17-help-bad-topic,v1.2-20-cli-21-invalid-slug,v1.2-21-cli-24-help-after-positionals,v1.2-22-cli-25-status-overview,v1.2-23-cli-26-status-slug,v1.2-24-state-14-grok-account-row,v1.2-25-state-20-status-next,v1.2-26-state-22-status-next,v1.2-27-approval-18-status-next,v1.2-28-provider-13-planner-no-fallback,v1.2-29-provider-15-idle-stall,v1.2-30-provider-16-total-cap,v1.2-31-provider-23-tool-result-budget,v1.2-32-provider-21-login-flow,v1.2-33-shape-json-is-unsupported,v1.2-34-format-11-account-selection,v1.2-35-state-02-schema-errors,v1.2-36-state-06-lint-card-warnings,v1.2-37-build-02,v1.2-38-build-03,v1.2-39-build-04,v1.2-43-build-08,v1.2-44-build-09,v1.2-45-build-11,v1.2-46-build-12,v1.2-47-build-13,v1.2-48-build-14,v1.2-49-build-15,v1.2-50-build-16,v1.2-51-build-17,v1.2-52-build-18,v1.2-53-build-19,v1.2-54-build-20,v1.2-55-build-21,v1.2-56-build-22,v1.2-57-build-23,v1.2-58-build-24,v1.2-59-build-25,v1.2-60-build-26,v1.2-61-build-27,v1.2-62-build-28,v1.2-63-build-29,v1.2-64-build-30,v1.2-65-build-33,v1.2-66-build-34,v1.2-67-build-43,v1.2-87-ladder-19,v1.2-90-ladder-22,v1.2-92-ladder-24,v1.2-93-ladder-25' \
  --jobs 2 --time-scale 0.02 --workdir "$EVIDENCE/work" \
  --out "$EVIDENCE/results.jsonl"
```

## Notes

Read WORKER-RULES.md, PLAN.md, QUEUE-source.md, the relevant authoritative spec clauses and CHANGES-v1.3.md, and relevant Rust modules before implementing. Run `GIT_CONFIG_GLOBAL=/dev/null make check` plus the acceptance command. Write commands, resolved IDs/instances, revision, compatible passes, exact draft conflicts and deferred closure gates to your unique evidence note; never count unwired cases as passing. Package 00 remains a real foundation task after this bootstrap. This scaffold does not freeze interfaces or assert conformance.

R(slice) requires a scratch copy of the shared coherent migrated Quint cohort: spec, then 500 traces ×25 steps for each seed 17,23,41, and conform against the same-revision private binary; full observations only. Never change the source oracle or goldens. Planned D-* fixtures are not available v1.2 cases; record this gap and wait for shared frozen v1.3 IDs before claiming their gates. Linux, optional runtime and live comparison gates require their stated external evidence. Worker commit does not confer behaviour acceptance; compiled component evidence may be code-ready until its closure round.
