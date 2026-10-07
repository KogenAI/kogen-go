# 55-exunit-adapter evidence

## Scope and revisions

- Worker branch: `kgo/55-exunit-adapter`; tested base revision: `1440d3785f7f0a695fc7f3d63f7a4bfbe8d4d60a`.
- Final CLI build after the adapter changes: `bin/kogen` SHA-256 `73c502a7daa2f01a07544c051cc9822d2a90b05198083373193875958b71715e`; `bin/kogen-xspec` SHA-256 `c895b69f470589477763951d8ea1ee106972d06053c69258d26127a1fe85f7ea`.
- Adapter source content SHA-256s at verification: `adapter.go` `7f9eb66bcdc5872822dccaed826a7840bb3ab46a573fd2f7c264c045bb587c57`; `adapter_test.go` `d140fbea9733ee53ce7d083207213c5f33d12d21260a63685535426a5249feda`; `findings.go` `ac500e3283a6153bc36feae2a223341106db86d40140ea16c43696e6fd9be8ac`; `formatter_source.go` `cdc3357d357e47afc0a9ace42cd39b37a8cab1c5cac7d448b4c2fda698514c21`; `ledger_formatter.ex` `c1a1ccc54ec887cbc90feb542104512439067e4450218798d7177b24bd01611e`; `doc.go` `4da885ccd92479ca6645e36c93987e545018f54a32e3227b166223a755644d24`.
- Target spec: `kogen-spec` `e19dd1c21c19c5be1201c3b6a42c59c28b5c2887` (`v1.3-draft`).
- Frozen oracle: `conformance-v1.2` `0f93bad988fb8d7a8eff4e94954d1db0a046c89d`.
- Adapter code is confined to `internal/acceptance/exunit/**`. The public CLI does not yet route Shape/Build commands to the adapter; shared entrypoints were not changed.
- ExUnit component behavior implemented: fixed source/candidate paths and `deps`/`_build` seeds; safe private formatter publication in the run directory; exact `mise exec --` / `elixir -e ... -S mix test` argv; first-20-line missing `erl`/`elixir`/`mix` detection; formatter derivation; ExUnit, compiler, Credo, and Mix format finding parsers.

## Commands and results

Pinned tool path was exported before commands as required:

```sh
export PATH="$HOME/.local/share/mise/installs/git/2.54.0/bin:$HOME/.local/share/mise/installs/go/1.27.1/bin:$HOME/.local/share/mise/installs/python/3.14.7/bin:$HOME/.local/share/mise/installs/node/24.21.0/bin:$PATH"
```

- `make build` — passed; built `bin/kogen` and `bin/kogen-xspec`.
- `GOMAXPROCS=2 go test -count=1 -parallel=2 ./internal/acceptance/exunit` — passed after the final source change.
- `GIT_CONFIG_GLOBAL=/dev/null GOMAXPROCS=2 make check` — passed after the final source change (`format`, vendor fingerprints, vet, all Go tests, and both builds).
- Frozen suite command (the requested literal selection and overlay):

  ```sh
  SUITE="$HOME/cx/kgo/inputs/conformance-v1.2"
  EVIDENCE="$HOME/cx/kgo/evidence/55-exunit-adapter"
  mkdir -p "$EVIDENCE"
  PYTHONDONTWRITEBYTECODE=1 PATH="$HOME/.local/share/mise/installs/python/3.14.7/bin:$PATH" \
    "$SUITE/bin/kogen-conformance" run --kogen "$PWD/bin/kogen" \
    --profile cli,state,approval,shape,build,ladder,provider,custody,format,v1.2,exunit --case 'build-40,exunit-01,exunit-02,exunit-03,exunit-04,exunit-05,exunit-06,shape-16' \
    --jobs 2 --time-scale 0.02 --workdir "$EVIDENCE/work" \
    --out "$EVIDENCE/results.jsonl"
  ```

  The oracle resolved 8 selected cases and 9 instances: `build-40` (`null`), `exunit-01` through `exunit-06` (`null` each), and `shape-16` (`1:red`, `2:127`). Result: **0/8 cases and 0/9 instances passed**. All six optional ExUnit cases are separately **0/6**; they are not included in the standard 236-case / 570-instance denominator. The complete selected output is retained at `/Users/almirsarajcic/cx/kgo/evidence/55-exunit-adapter/results.jsonl`.

  Each ExUnit case stopped at its first Shape/approve command before exercising ExUnit. `build-40` failed at approve and both `shape-16` instances failed at Shape. The exact stderr was `kogen: implementation bootstrap; command routes are not wired`; commands exited 2 where the fixture expected 0 or 3. These are integration gaps, not adapter behavior passes or observed ExUnit contract conflicts.

## Real runtime and local effects

- Runtime present on this macOS arm64 host: Elixir 1.20.2, Erlang/OTP 29, Mix 1.20.2, mise 2026.9.14. No runtime was installed or updated.
- Copied the frozen `fixtures/exunit-hello` source into a scratch workspace under `/Users/almirsarajcic/cx/kgo/evidence/55-exunit-adapter/runtime-workspace-02`, added one tagged acceptance test and the candidate implementation there, and placed the generated formatter and ledger under the sibling `runtime-run-02` directory.
- Ran the following from the scratch Mix project: `KOGEN_LEDGER_REPORT=/Users/almirsarajcic/cx/kgo/evidence/55-exunit-adapter/runtime-run-02/ledger.jsonl KOGEN_INTENT_SLUG=greet mise exec -- elixir -e 'Code.require_file("/Users/almirsarajcic/cx/kgo/evidence/55-exunit-adapter/runtime-run-02/ledger_formatter.ex")' -S mix test --formatter KogenLedgerFormatter --formatter ExUnit.CLIFormatter /Users/almirsarajcic/cx/kgo/evidence/55-exunit-adapter/runtime-workspace-02/test/acceptance/greet_test.exs`. Result: exit 0, 1 ExUnit test passed. Ledger row: `{"tag":"greet/A1","test":"greets Almir","status":"passed"}`. The generated formatter and report remained outside the scratch workspace.
- The initial runtime probe was launched from the repository root by mistake and Mix reported `Could not find a Mix.Project`; this failed setup log is preserved at `/Users/almirsarajcic/cx/kgo/evidence/55-exunit-adapter/runtime.log`. The corrected probe used a separate fresh scratch workspace and its output is retained at `/Users/almirsarajcic/cx/kgo/evidence/55-exunit-adapter/runtime-02.log`.
- Unit coverage also verifies formatter conflict/symlink refusal, formatter placement outside the workspace, exact argv and formatting selection, runtime-unavailable detection, seed directories, and representative finding parser output.

## Conflicts and remaining closure

- No exact v1.2-versus-v1.3 ExUnit contract conflict was established. The frozen cases did not reach the adapter. Planned D-* fixtures are not available v1.2 cases and are not claimed.
- H16/B40 and I5 remain open until the package is wired through the real Shape → approval → Build flow and representative ExUnit fixtures reach it. The selected frozen run is retained as a failure and must not be counted as behavior evidence.
- The local runtime result is macOS component evidence only. Linux parity and the full I6 optional-runtime matrix remain open; this host is Darwin.
- R(slice) was not run: no coherent shared migrated Quint cohort was available in this worktree. Seeds 17, 23, and 41, 500 traces × 25 steps each, full observations, and first-divergence comparison remain outstanding. No oracle, spec, or golden files were changed.
- Gate classification is `component`, not `behaviour`. Integration rerun after coordinator handoff is still required.
