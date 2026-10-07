# I1 public foundation evidence

Worker branch: `kgo/I1-public-foundation`. Final source revision: `d09f8139a0937f606e63e3c46b92ec2a55f0e0ad`. The initial implementation revision was `cebede06fc392bbcf238e7afd74e0bc3438b28c1`; its complete failed run is retained separately.

The final binary reports `kogen d09f8139 (2026-10-07)`, SHA-256 `c6a3f0132f81c39ce32c8b1940dc189a4ba0fd66d0ba73ae3d78fcd128034140`. The approval adapter version is `kogen-go-i1`. Frozen v1.2 input: `$HOME/cx/kgo/inputs/conformance-v1.2`; source suite revision `0f93bad988fb8d7a8eff4e94954d1db0a046c89d`.

## Commands and results

- `GOMAXPROCS=2 go test -count=1 -parallel=2 ./internal/app` — PASS after the invalid-slug fix.
- `GIT_CONFIG_GLOBAL=/dev/null make check` — PASS on final revision `d09f813`; format, vendor fingerprints, vet, tests, and both builds passed.
- Blank-HOME hostile-global Git `make check` — PASS on source revision `cebede0`, with a hostile global identity, signing setting, default branch and pre-commit hook; marker remained absent. The later `d09f813` change only validates command slugs. Hostile-check files are under `$TMPDIR/kogen-i1-hostile.fyWIC9`.
- The required frozen-v1.2 command and full literal case list are in [I1-public-foundation.md](I1-public-foundation.md). It was run twice with all listed profiles, IDs, `--jobs 2 --time-scale 0.02`, preserving both results and workdirs:
  - `cebede0`: exit 1, 134 cases / 454 instances, 57 case passes and 77 failures. Results: `/Users/almirsarajcic/cx/kgo/evidence/I1-public-foundation/results.jsonl`.
  - `d09f813`: exit 1, 134 cases / 454 instances, 59 case passes and 75 failures. Results: `/Users/almirsarajcic/cx/kgo/evidence/I1-public-foundation-d09f813/results.jsonl`.
- Runner reported zero errors, skips, or unimplemented cases in both runs. A case is counted as passing only when the runner marked it pass.

Final per-profile cases (literal effective IDs):

| Profile | Pass | Fail |
|---|---|---|
| cli | cli-10, cli-15, cli-18, cli-19, cli-20, cli-22, cli-23 | cli-28, cli-29 |
| state | state-01, state-04, state-05, state-07, state-08, state-09, state-13, state-16, state-24, state-25, state-29, state-30 | state-03, state-17, state-18, state-19, state-21, state-28 |
| approval | approval-02, approval-03, approval-04, approval-05, approval-06, approval-07, approval-09, approval-11, approval-12, approval-15, approval-16, approval-19, approval-20, approval-21, approval-22, approval-23 | approval-01, approval-08, approval-10, approval-13, approval-14, approval-17, approval-24 |
| shape | — | shape-16 |
| build | — | build-01, build-32, build-35, build-36, build-38, build-40, build-41, build-42 |
| provider | — | provider-26 |
| custody | — | custody-01, custody-02, custody-03, custody-04, custody-05 |
| format | format-02 (65/65), format-03 (13/13), format-04 (24/24), format-06 (15/15) | format-08, format-09 (9/15) |
| v1.2 overlay | v1.2-07-cli-03-unknown-command, v1.2-08-cli-04-unknown-subcommand, v1.2-09-cli-05-unknown-option, v1.2-10-cli-06-missing-positionals, v1.2-11-cli-07-unexpected-argument, v1.2-12-cli-08-option-needs-value, v1.2-13-cli-09-boolean-takes-no-value, v1.2-14-cli-11-unknown-provider, v1.2-15-cli-12-watch-with-json, v1.2-16-cli-13-double-dash, v1.2-17-cli-14-options-before-command, v1.2-18-cli-16-short-option, v1.2-19-cli-17-help-bad-topic, v1.2-20-cli-21-invalid-slug, v1.2-21-cli-24-help-after-positionals, v1.2-25-state-20-status-next, v1.2-26-state-22-status-next, v1.2-33-shape-json-is-unsupported, v1.2-35-state-02-schema-errors, v1.2-36-state-06-lint-card-warnings | v1.2-01-fixed-cli-help-and-grok, v1.2-02-approval-hash-intent-and-test-bytes, v1.2-05-missing-usage, v1.2-06-crash-after-base-cas, v1.2-112-provider-11, v1.2-121-custody-08, v1.2-122-custody-09, v1.2-123-custody-10, v1.2-124-build-31, v1.2-126-state-15, v1.2-127-build-10, v1.2-128-format-05, v1.2-129-cli-27, v1.2-130-state-11, v1.2-131-state-12, v1.2-132-state-23, v1.2-133-state-26, v1.2-136-format-10, v1.2-137-format-12, v1.2-22-cli-25-status-overview, v1.2-23-cli-26-status-slug, v1.2-24-state-14-grok-account-row, v1.2-27-approval-18-status-next, v1.2-31-provider-23-tool-result-budget, v1.2-39-build-04, v1.2-44-build-09, v1.2-45-build-11, v1.2-46-build-12, v1.2-47-build-13, v1.2-48-build-14, v1.2-49-build-15, v1.2-50-build-16, v1.2-51-build-17, v1.2-52-build-18, v1.2-53-build-19, v1.2-54-build-20, v1.2-55-build-21, v1.2-64-build-30, v1.2-65-build-33, v1.2-66-build-34, v1.2-67-build-43, v1.2-92-ladder-24, v1.2-93-ladder-25 |

## Conflicts and closure

The final run verifies the public parser/help/version, project selection, raw Intent parse/lint/hash, local approval card/ref publication, and draft/forced removal components. The app tests also exercise real local-origin approval publication, synthetic queued/draft status, hash mismatch, and invalid slugs.

Exact observed historical v1.2 mismatches:

- `approval-01` and `format-08` expect a `Feasibility: not checked` card line. The current card omits that line. `format-08` also expects 18 lines; the current baseline warning card has 16 and no blank line between its warning text and `Approve with:`.
- `v1.2-128-format-05` passes 22/24 instances. For anchor instance 7 the oracle pattern accepts `anchors, aliases, tags, and block scalars are not allowed` or `anchors, aliases, and tags are not allowed`; CLI emits `anchors, aliases, and block scalars are not allowed`. For unterminated-flow instance 16 it expects `unterminated flow collection`; CLI emits `malformed flow collection near <end>`. Full strings and instance paths are in the JSONL.
- Before `d09f813`, `v1.2-16` passed 2/3 and `v1.2-20` passed 4/7 because dispatch did not reject all invalid slugs. The committed fix validates slugs before route dispatch; final results are 3/3 and 7/7.

Other failures are not claimed as historical conflicts or passes. The frozen overlay reports queue/provider/Build-dependent cases against explicit I1 controller deferrals. In particular, `v1.2-01` fails only its provider list/logout/use instances (18/21 pass); `v1.2-02` cannot produce a card because the base check baseline fails to load. The full failure messages are retained in each JSONL.

Remaining closure gates:

- I2: provider command routes and account/provider behavior.
- I3: Shape/Build queue execution, active-Build remove refusal, live Build status, recovery/claims, watch, queue stop/detach, and end-to-end process custody. Foundation status reads current checkout Intents and approval refs and derives synthetic status only.
- I5: public Shape route and the v3 baseline-cache closure. `state-28` remains failed; the frozen fixture's `build/lint-runs` marker is absent.
- No R(slice), D-* or full-observation replay is claimed for this package; those require the shared coherent migrated v1.3 cohort and frozen cases. Linux, optional-runtime, and live-comparison evidence were not run.
- The v1.2 overlay's complete failures remain visible above and in the retained JSONL; behaviour acceptance still requires the named integration closures.
