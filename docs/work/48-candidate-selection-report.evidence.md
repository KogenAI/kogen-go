# 48 Candidate selection/report evidence

## Revision and gate

- Branch: `kgo/48-candidate-selection-report`.
- Starting revision: `84a334ade43cddc04c6eca17d1935416be828184`.
- Tested selector revision: `870ff2c28e62fa7aba1abbbbdf9abf1339ab0c49` (`Keep exact candidate blocking counts`), following `a8d4b140561043791c4de624634bbc7f7e441293` and `9ba347dbaecc012c017804e8a611031ea55c92cd`.
- Target spec: `kogen-spec` revision `e19dd1c21c19c5be1201c3b6a42c59c28b5c2887`; `CHANGES-v1.3.md` §1 and `spec/02-formats.md` §§2.8, 2.10 / `spec/03-build.md` §§3.0, 3.5, 3.7, 3.8.2–3, 3.10. The Rust selector/report references were read at `a402540b39cedc7f788472297add7ae2f8a6631a`.
- CLI binary: built from tested revision `870ff2c`; SHA-256 `a92b5b651124f47b768658f5051b32a0e20dcb115475b4f46a850436e2369e06`. `cmd/kogen` and `internal/app` have no changes from the starting revision. The selection component is not wired into the command flow; `kogen intent approve` still returns `kogen: implementation bootstrap; command routes are not wired`.
- Adapter revision: no Build/selection adapter is wired. The selected-case adapter therefore cannot reach selection, provider, or landing behavior.
- Oracle: frozen local suite at `$HOME/cx/kgo/inputs/conformance-v1.2`, runner SHA-256 `0b53a3b60956cbe8dde7af54e274c46b3e8d430c45662d7dff67445f440b39f9`; runner label `v1.2+unknown`, Git `2.54.0`, platform `macOS-26.7.1-arm64-arm-64bit-Mach-O`.
- Gate: **component**. The component and repository checks pass. No behavior acceptance case passed.

## Implemented component

`internal/build/select/**` snapshots each rung's candidate ref, captured diff bytes, gate counts/verdict, cap reason, and stable rank/order. It validates ref/rung names, retains one per-rung diff path, returns the selected diff and a best-unverified candidate, and emits a journal selection event. Candidate artifact order is stable by rung rank and attempt order, independent of completion order.

The score follows draft §3.8.3: most approved items passing, then the exact §3.5 blocking count, added plus removed implementation lines, and earliest rung. Gate reports with inconsistent approved-item totals are rejected. A missing gate has an unknown blocking count; an observed gate preserves `repair.RedCount` exactly, including zero for an unverified suite failure that has no §3.5 red finding. Stable attempt order resolves a remaining tie. Audit advice is not a selector input; the report fixes `audit_mode` to `observational`, `demoted` to `false`, and `advisory_items` to an empty list.

Local tests cover score ordering, exact blocking counts, observed gate counts, audit-advice independence, copied diff bytes, best-unverified and cap snapshots, per-rung artifact names, ref/rung validation, deterministic ordering, and the journal event. Selection itself has no external effects. Test fixtures used temporary Git repositories; no provider/account call, source-oracle edit, golden edit, or live comparison occurred.

## Commands and results

Pinned tools were placed first on `PATH` for every shell command:

```sh
export PATH="$HOME/.local/share/mise/installs/git/2.54.0/bin:$HOME/.local/share/mise/installs/go/1.27.1/bin:$HOME/.local/share/mise/installs/python/3.14.7/bin:$HOME/.local/share/mise/installs/node/24.21.0/bin:$PATH"
```

Final source checks on `870ff2c`:

```sh
GOMAXPROCS=2 go test -p=2 -parallel=2 ./internal/build/select
GOMAXPROCS=2 GIT_CONFIG_GLOBAL=/dev/null make check
GOMAXPROCS=2 make build
```

The selector test passed. `make check` reported format, vendor fingerprints, vet, tests, and both builds passed. `make build` succeeded for `bin/kogen` and `bin/kogen-xspec`.

The exact assigned v1.2 command was then run against the binary from `870ff2c`:

```sh
SUITE="$HOME/cx/kgo/inputs/conformance-v1.2"
EVIDENCE="$HOME/cx/kgo/evidence/48-candidate-selection-report"
mkdir -p "$EVIDENCE"
PYTHONDONTWRITEBYTECODE=1 PATH="$HOME/.local/share/mise/installs/python/3.14.7/bin:$PATH" \
  "$SUITE/bin/kogen-conformance" run --kogen "$PWD/bin/kogen" \
  --profile cli,state,approval,shape,build,ladder,provider,custody,format,v1.2 --case 'v1.2-81-ladder-13,v1.2-82-ladder-14,v1.2-83-ladder-15,v1.2-84-ladder-16,v1.2-85-ladder-17,v1.2-86-ladder-18,v1.2-87-ladder-19,v1.2-91-ladder-23,v1.2-92-ladder-24,v1.2-93-ladder-25' \
  --jobs 2 --time-scale 0.02 --workdir "$EVIDENCE/work" \
  --out "$EVIDENCE/results.jsonl"
```

Resolved IDs and outcomes (one instance each):

| Effective ID | Instances | Result | Observed stop |
| --- | ---: | --- | --- |
| `v1.2-81-ladder-13` | 1 | Fail | Step 2, `intent approve` |
| `v1.2-82-ladder-14` | 1 | Fail | Step 2, `intent approve` |
| `v1.2-83-ladder-15` | 1 | Fail | Step 2, `intent approve` |
| `v1.2-84-ladder-16` | 1 | Fail | Step 2, `intent approve` |
| `v1.2-85-ladder-17` | 1 | Fail | Step 2, `intent approve` |
| `v1.2-86-ladder-18` | 1 | Fail | Step 2, `intent approve` |
| `v1.2-87-ladder-19` | 1 | Fail | Step 2, `intent approve` |
| `v1.2-91-ladder-23` | 1 | Fail | Step 2, `intent approve` |
| `v1.2-92-ladder-24` | 1 | Fail | Step 2, `intent approve` |
| `v1.2-93-ladder-25` | 1 | Fail | Step 2, `intent approve` |

Totals: **0 passed, 10 failed, 0 errors, 0 skipped, 10 instances**. Every case reported exit 2 instead of 0, empty stdout, and stderr `kogen: implementation bootstrap; command routes are not wired`. The runner reported that no provider request reached the fake server. The requested assertions were unreachable, so these are not compatible behavior passes and do not establish a selector behavior conflict.

The final result is retained at `/Users/almirsarajc/cx/kgo/evidence/48-candidate-selection-report/results.jsonl` (SHA-256 `8c8292aa1774a7b8a8c399f9e6f3ad5380720c5f7dc758b5f6fcfa5fe1c52c93`), with all ten per-case workdirs under `/Users/almirsarajc/cx/kgo/evidence/48-candidate-selection-report/work`. Three preceding oracle results were also preserved rather than replaced: `results-first-run.jsonl` (SHA-256 `5294848dd7363bcecdf234f732fd411a028ecd9d8640aef0ceb32256a09933c4`), `results-second-run.jsonl` (SHA-256 `159b1a9debef964f6ff3a1f88158530d5516eacb7c87b5c3729ba45e24f01c24`), and `results-third-run.jsonl` (SHA-256 `bac4b5c55b91956ef4dca37f426a7718b0bea11d4745a9b38284a1a960bf9af3`), each with its corresponding `work-*-run` directory. Those runs also resolved the ten IDs and stopped at approval before provider behavior.

During earlier local check attempts, one `internal/testkit` signer-marker test failed and one full check run reported two `internal/gitio` failures (missing `stdout-before-hang\n` capture and missing global signing-helper marker). The targeted signer test and later full check passed; the failing oracle results above were retained independently. No unrelated package was changed to mask these transient failures.

## Conflicts and deferred closure

- Measured conflicts among the assigned IDs: **none**. They all stopped at the unwired approval command. L13–19 and L23–25 remain behavior-unverified.
- The shared migration record identifies a separate historical v1.2/draft conflict: `v1.2-73-ladder-05`, `v1.2-74-ladder-06`, `v1.2-75-ladder-07`, and `v1.2-79-ladder-11` expect the v1.2 auditor to demote A2 before repair. Draft §3.8.2–3 makes audit observational, so those historical demotion assertions conflict with the draft. They were not in this package's selected command. `v1.2-76-ladder-08` records `infeasible` as an unknown, non-demoting v1.2 verdict; `v1.2-77-ladder-09` and `v1.2-78-ladder-10` also require fixture migration. Keep the individual historical results visible in I4/I8; do not reinterpret them as v1.3 acceptance.
- `D-AUD-03–05` are planned fixture labels, not frozen v1.2 IDs. No shared frozen v1.3 IDs or coherent migrated Quint/golden cohort were available. Await that suite and its manifest before claiming these gates.
- I4 remains open until the public Build/ladder flow wires selection, artifacts, and report/status output and reruns the integrated behavior cases. Package 00 remains a foundation task after the bootstrap. The shared observational migration is required where the old model demotes.
- R(slice) was not run: no scratch copy of the shared coherent migrated Quint cohort was available. Seeds `17`, `23`, and `41` were not run for 500 traces × 25 steps each against a same-revision private binary; divergence is unmeasured. The source oracle and goldens were not changed.
- Linux, optional-runtime, and live-comparison gates require their external evidence and remain open. I8 also needs the shared v1.3 and production replay manifests; neither is produced by this component run.
- Scope stayed within `internal/build/select/**` and this package's evidence/gate files. No spec, suite, golden, replay harness, gate policy, other worktree, or global configuration was changed.
