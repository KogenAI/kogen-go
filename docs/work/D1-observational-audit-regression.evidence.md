# D1 — Observational audit regression evidence

## Revisions and gate

- Branch: `kgo/D1-observational-audit-regression`.
- Starting revision: `49c3a327aeacad924417021f0fae3a8530893e6e`.
- D1 regression/test commit: `4e0855f4d1c56671b0e584c5b9538751c8d158f4` (`Add observational audit regressions`).
- CLI production source revision: `49c3a327aeacad924417021f0fae3a8530893e6e`; D1 changes are tests and docs only. `make build` produced `bin/kogen` SHA-256 `d8914905089e6b6f7bae3672904e06d5d3943493216d1cf2ab5ca2cc2fda395e` and `bin/kogen-xspec`. The public command adapter still returns `kogen: implementation bootstrap; command routes are not wired`; no Build/ladder adapter is connected.
- Frozen oracle: read-only `$HOME/cx/kgo/inputs/conformance-v1.2`, `VERSION=1.2`. Runner label: `v1.2+unknown`; runner binary SHA-256 `0b53a3b60956cbe8dde7af54e274c46b3e8d430c45662d7dff67445f440b39f9`. Git reported by the run: `2.54.0`; host: `macOS-26.7.1-arm64-arm-64bit-Mach-O`.
- Target: spec v1.3-draft `e19dd1c21c19c5be1201c3b6a42c59c28b5c2887`; read `CHANGES-v1.3.md` §1 and `spec/02-formats.md` §§2.3/2.8 and `spec/03-build.md` §§3.0/3.8.2–3. Rust reference revision `a402540b39cedc7f788472297add7ae2f8a6631a`; reviewed `run/orchestration/auditor.rs`, `auditor_tests.rs`, `selector.rs`, `selector_tests.rs`, and rung parallel execution.
- Toolchain: Go `go1.27.1 darwin/arm64`, Git `2.54.0`, pinned Python `3.14.7`, Node `24.21.0`; Makefile uses `GOMAXPROCS=2`, `-p=2`, and `-parallel=2`.
- Gate: **component**. The package regressions pass, but full `make check` had one unrelated failure and public behavior acceptance did not reach Build/audit. No compatible v1.2 behavior pass is claimed.

## Regression coverage

- `internal/build/audit/observational_test.go` checks both `over_strict` and `contradicts` for A2. Each remains an audit observation and repair suggestion; the journal event stays `mode=observational`, `demoted=false`, `advisory_items=[]`, and contains no `acceptance_demoted` field.
- `internal/build/select/observational_test.go` sends those replies through `audit.Auditor.BeforeRepair` into actual red `gate.GateReport` values. A2 stays in the 2-item approved set: one pass, an unverified gate, and no landing eligibility. It checks an audit before selection, an audit after an initial selection, and reversed candidate input order. The parallel completion permutation preserves candidate artifact order, score counts, rank, and R2 winner. The real green gate fixture represents a repaired candidate whose A2 then passes verification.
- D-AUD-01's public config refusal is covered by the already-owned `internal/project/project_test.go::TestDraftAuditorDemotionFixture`; it requires `build.auditor_demotion has no admitted calibration`. The project test package passed during `make check`.
- Existing `internal/build/audit/audit_test.go` covers malformed/duplicate/unknown replies warning without advice and checks observational receipt fields. Existing selector and gate tests also check that advice cannot change gate counts or landability. The audit and selector packages passed during the required check.
- No production file changed. No provider/account access, live comparison, source-oracle edit, golden edit, replay-harness edit, or other-worktree edit occurred.

## Commands and results

Pinned tools were first on `PATH` for every shell command:

```sh
export PATH="$HOME/.local/share/mise/installs/git/2.54.0/bin:$HOME/.local/share/mise/installs/go/1.27.1/bin:$HOME/.local/share/mise/installs/python/3.14.7/bin:$HOME/.local/share/mise/installs/node/24.21.0/bin:$PATH"
```

1. `gofmt -w internal/build/audit/observational_test.go internal/build/select/observational_test.go` — completed.
2. `GIT_CONFIG_GLOBAL=/dev/null make check` — **exit 2**. The new `internal/build/audit` and `internal/build/select` packages passed (`ok`, 0.600s and 35.889s). The full suite reported this unrelated failure in `internal/gitio`:

   ```text
   --- FAIL: TestRunnerSeparatesStreamsAndTimesOutHangingGit (1.45s)
       gitio_test.go:149: captured stdout = "", want "stdout-before-hang\n"; stderr=""
   FAIL kogen-go/internal/gitio
   make: *** [check] Error 1
   ```

   This failed check was not rerun. The required check result is retained here; no unrelated production package was changed.

3. `make build` — passed as part of each conformance invocation, building `bin/kogen` and `bin/kogen-xspec`.
4. An initial partial oracle invocation accidentally omitted IDs `v1.2-77-ladder-09` and `v1.2-78-ladder-10`. It resolved five selected cases and failed 0/5 at the unwired approval route. Its output and workdirs were preserved separately at [`results-partial-omitted-77-78.jsonl`](/Users/almirsarajcic/cx/kgo/evidence/D1-observational-audit-regression/results-partial-omitted-77-78.jsonl) (SHA-256 `8571f3aa29c6ee6b3814ae5a1cae27f43180fc930c6efb7283546f4267b18f5c`) and `work-partial-omitted-77-78/`. This partial run is not the assigned acceptance result.
5. The complete assigned frozen v1.2 command was then run once with the literal effective IDs:

   ```sh
   make build
   SUITE="$HOME/cx/kgo/inputs/conformance-v1.2"
   EVIDENCE="$HOME/cx/kgo/evidence/D1-observational-audit-regression"
   mkdir -p "$EVIDENCE"
   PYTHONDONTWRITEBYTECODE=1 PATH="$HOME/.local/share/mise/installs/python/3.14.7/bin:$PATH" \
     "$SUITE/bin/kogen-conformance" run --kogen "$PWD/bin/kogen" \
     --profile cli,state,approval,shape,build,ladder,provider,custody,format,v1.2 --case 'v1.2-73-ladder-05,v1.2-74-ladder-06,v1.2-75-ladder-07,v1.2-76-ladder-08,v1.2-77-ladder-09,v1.2-78-ladder-10,v1.2-79-ladder-11' \
     --jobs 2 --time-scale 0.02 --workdir "$EVIDENCE/work" \
     --out "$EVIDENCE/results.jsonl"
   ```

   All seven IDs resolved to one instance each. Each failed at step 2 (`kogen intent approve greet {hash8:greet}`): exit 2 instead of 0, empty stdout, stderr `kogen: implementation bootstrap; command routes are not wired`. No case reached the auditor, repair, selector, or landing assertions; no fake provider request was made.

   | Effective ID | Instances | Result | Reached behavior |
   | --- | ---: | --- | --- |
   | `v1.2-73-ladder-05` | 1 | fail | step 2, unwired `intent approve` |
   | `v1.2-74-ladder-06` | 1 | fail | step 2, unwired `intent approve` |
   | `v1.2-75-ladder-07` | 1 | fail | step 2, unwired `intent approve` |
   | `v1.2-76-ladder-08` | 1 | fail | step 2, unwired `intent approve` |
   | `v1.2-77-ladder-09` | 1 | fail | step 2, unwired `intent approve` |
   | `v1.2-78-ladder-10` | 1 | fail | step 2, unwired `intent approve` |
   | `v1.2-79-ladder-11` | 1 | fail | step 2, unwired `intent approve` |

   Totals: **7 cases / 7 instances; 0 passed, 7 failed, 0 errors, 0 skipped**. The complete JSONL is retained at [`results.jsonl`](/Users/almirsarajc/cx/kgo/evidence/D1-observational-audit-regression/results.jsonl), SHA-256 `b3a0d21767690a78ddbacf22e6cf0e1d3a9c9b60dcbe785f90555258f804ec5a`; all generated case workdirs remain under `/Users/almirsarajc/cx/kgo/evidence/D1-observational-audit-regression/work/`.

## Historical conflicts and closure gates

The frozen case definitions identify the old L05–11 policy being replaced. These are **known fixture/spec conflicts, not assertions reached by the public run above**:

- `v1.2-73-ladder-05`, `v1.2-74-ladder-06`, and `v1.2-75-ladder-07` expect `over_strict` to demote failed A2 before a repair, including without a citation. Draft §3.8.2 keeps A2 required under either land-policy spelling.
- `v1.2-76-ladder-08` expects an unsupported `infeasible` verdict to be upheld as valid. Draft accepts only `valid`, `over_strict`, and `contradicts`; unknown verdicts warn and cannot change eligibility.
- `v1.2-78-ladder-10` supplies a citation-bearing demotion and expects the candidate never to land. Draft's Build reply has no citation field and never demotes; the common no-landing result does not make the old demotion assertion compatible.
- `v1.2-79-ladder-11` expects pre-repair demotion to re-score and land R1 without repairing A2. Draft requires actual passing verification.
- `v1.2-77-ladder-09` expects malformed JSON to demote nothing, which is compatible at the eligibility level; draft additionally requires a warning. Its full historical result is still unverified and it belongs to the replacement set. Do not count it as a pass.

Deferred gates:

- Planned D-AUD-01–05 fixtures are not frozen v1.2 cases. Shared frozen v1.3 replacement IDs and migrated gate scenarios, goldens, adapters, and Quint models were unavailable; no D-AUD behavior case is claimed green.
- I4 remains open until the public Build/ladder flow wires audit, repair, selection, and reporting, then reruns the migrated replacements and records compatible behavior evidence.
- `R(slice)` was not run: no scratch copy of a coherent shared migrated Quint cohort was available. Seeds 17, 23, and 41 × 500 traces × 25 steps and same-revision private-binary conformance remain unmeasured; divergence is unknown.
- Linux, optional-runtime, and live-comparison gates require external evidence. I8's `shared_v13_manifest` and `production_replay_manifest` are also unavailable.
- Package 00 remains a real foundation task after this bootstrap. The public CLI route gap is the direct reason the frozen cases did not reach D1 behavior.
