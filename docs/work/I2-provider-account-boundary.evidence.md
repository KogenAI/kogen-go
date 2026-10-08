# I2 provider/account boundary evidence

## Source and component evidence

- Worker branch: `kgo/I2-provider-account-boundary`.
- Source base revision before this package: `1cf6be267dc336eda52b033a79eae6aea3e8fe25`.
- The CLI binary used by the v1.2 run was built from the package worktree by the command's `make build`: `bin/kogen`, SHA-256 `49b075448e8f1dbe27ba144e238a3222ce10f48645442b965a2de16ddcd47f53`.
- The compiled xspec adapter artifact was `bin/kogen-xspec`, SHA-256 `7db01d922e9f01eff2d48338a927c02a77f5a191d225d3672c1738a76815c616`.
- `internal/app/provider_routes.go` uses the file account store and credential vault for list/use/login/logout. The app test completes ChatGPT discovery, browser authorization, callback, PKCE exchange, RS256 identity validation, credential persistence, account selection/listing, revocation, and logout against a local fake OIDC server. It verifies mode `0600` and that an empty list does not create account state. No live account or provider was used.
- `cmd/kogen-xspec/protocol.go` composes the production intent/approval and queue/status factories behind the strict protocol service. Existing `internal/xspec/protocol` tests cover malformed and unknown requests. The adapter helper is compiled but not the executable entrypoint yet.
- `internal/provider/wire`, `sse`, `transport`, `session`, and `tools`, plus `internal/xspec/protocol`, passed in the final repository check.

## Commands and results

All Go, Git, Python, and Node commands used the pinned worker PATH from the package instructions.

- `go test -count=1 -parallel=2 ./internal/app ./cmd/kogen-xspec` — passed after implementation fixes. Two earlier compile attempts failed and were corrected: an unused `os` import in `provider_routes.go`, then a test reference to undefined `stringPointer`.
- `GIT_CONFIG_GLOBAL=/dev/null make check` — the check itself completed successfully: format, vendor fingerprints, vet, all tests, and both builds passed. The shell acquired its `~/cx/kgo/gates.lock/oauth-port1455` directory before starting; its final `rmdir` reported that the path was already absent. No other lock was removed by this worker.
- The exact assigned command from `docs/work/I2-provider-account-boundary.md` ran once, including `make build`, the full standard profiles plus frozen v1.2 overlay, literal IDs from [`I2-provider-account-boundary.cases`](I2-provider-account-boundary.cases), `--jobs 2`, and `--time-scale 0.02`. The runner returned 1 because cases failed; there was no retry. Suite JSONL: `/Users/almirsarajcic/cx/kgo/evidence/I2-provider-account-boundary/results.jsonl`; console: `/Users/almirsarajcic/cx/kgo/evidence/I2-provider-account-boundary/console.log`; complete per-case workdirs: `/Users/almirsarajcic/cx/kgo/evidence/I2-provider-account-boundary/work`.

Runner metadata: suite `v1.2+unknown`, Git 2.54.0, macOS 26.7.1 arm64, time scale 0.02, started `2026-10-08T00:07:16Z`. All 167 requested IDs resolved. The 497 expanded instances yielded 59 pass, 108 fail, zero errors, zero skipped, and zero unimplemented:

| Profile | Cases | Instances | Pass | Fail |
| --- | ---: | ---: | ---: | ---: |
| cli | 10 | 39 | 7 | 3 |
| state | 18 | 67 | 12 | 6 |
| approval | 23 | 28 | 16 | 7 |
| shape | 4 | 5 | 0 | 4 |
| build | 8 | 8 | 0 | 8 |
| provider | 5 | 15 | 0 | 5 |
| custody | 5 | 6 | 0 | 5 |
| format | 6 | 133 | 4 | 2 |
| v1.2 | 88 | 196 | 20 | 68 |
| **Total** | **167** | **497** | **59** | **108** |

The runner marked these selected rows `pass`:

- `cli-10,cli-15,cli-18,cli-19,cli-20,cli-22,cli-23`
- `state-01,state-04,state-05,state-07,state-08,state-09,state-13,state-16,state-24,state-25,state-29,state-30`
- `approval-02,approval-03,approval-04,approval-05,approval-06,approval-07,approval-09,approval-11,approval-12,approval-15,approval-16,approval-19,approval-20,approval-21,approval-22,approval-23`
- `format-02,format-03,format-04,format-06`
- `v1.2-07-cli-03-unknown-command,v1.2-08-cli-04-unknown-subcommand,v1.2-09-cli-05-unknown-option,v1.2-10-cli-06-missing-positionals,v1.2-11-cli-07-unexpected-argument,v1.2-12-cli-08-option-needs-value,v1.2-13-cli-09-boolean-takes-no-value,v1.2-14-cli-11-unknown-provider,v1.2-15-cli-12-watch-with-json,v1.2-16-cli-13-double-dash,v1.2-17-cli-14-options-before-command,v1.2-18-cli-16-short-option,v1.2-19-cli-17-help-bad-topic,v1.2-20-cli-21-invalid-slug,v1.2-21-cli-24-help-after-positionals,v1.2-25-state-20-status-next,v1.2-26-state-22-status-next,v1.2-33-shape-json-is-unsupported,v1.2-35-state-02-schema-errors,v1.2-36-state-06-lint-card-warnings`

These are oracle row results only; none is represented as a provider profile acceptance pass.

## Failed rows, conflicts, and closure gates

All 108 failing rows and their exact expected/actual messages are retained in the JSONL. Relevant provider failures stop at the existing unowned dispatch placeholders:

- `provider-10`, `provider-22`, `v1.2-32-provider-21-login-flow`, and `v1.2-34-format-11-account-selection` receive `controller/internal_error: provider commands are wired in the provider integration round` before the implemented account route runs.
- `provider-19`, `v1.2-28-provider-13-planner-no-fallback`, `v1.2-29-provider-15-idle-stall`, `v1.2-30-provider-16-total-cap`, `v1.2-31-provider-23-tool-result-budget`, `v1.2-104-provider-01`, and `v1.2-106-provider-03` through `v1.2-118-provider-25` stop at `controller/internal_error: queue execution is wired in the Build integration round`.
- `provider-10`, `provider-22`, `v1.2-105-provider-02`, and `v1.2-34-format-11-account-selection` stop at the provider dispatch placeholder during login; `v1.2-32-provider-21-login-flow` stops at that placeholder during provider list.
- `provider-24` and `provider-26` stop at `controller/internal_error: intent shaping is wired in the Shape integration round`; the runner also reports that no provider request reached its fake server.

No tested I2 assertion reached an implemented provider route and demonstrated a v1.2-versus-draft semantic conflict. Accordingly, this package records no exact historical semantic conflicts; the observed failures above are deferred wiring closures. Failures in the other selected profiles remain visible in the retained JSONL and are not attributed to I2 or counted as compatible passes.

Remaining gates:

1. `internal/app/foundation.go` still owns the CLI route switch and returns the provider integration placeholder. I2's provider method is therefore compiled and directly exercised by its fake OIDC component test, but not dispatched by public `kogen` commands.
2. `cmd/kogen-xspec/main.go` still calls `app.Bootstrap`; wiring `serveProtocol` into the executable requires the relevant integration handoff.
3. Provider profile behavior depends on Build and Shape integration. The assigned run does not satisfy that acceptance, and none of its five provider-profile cases passed.
4. The shared migrated v1.3 Quint cohort and frozen replacement IDs are not available here. No 500-trace ×25-step runs for seeds 17, 23, and 41, nor planned D-* fixture claims, are included.
5. Verification ran on macOS only; external Linux closure remains outstanding where required by the integration gate.

No oracle, golden, or other worktree was modified. No live account was accessed.
