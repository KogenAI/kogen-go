# 49-shaper-grammar-prompt evidence

Recorded 7 October 2026 in the assigned worktree
`/Users/almirsarajcic/Areas/Kogen/kogen-go-wt/49-shaper-grammar-prompt`,
branch `kgo/49-shaper-grammar-prompt`.

## Revisions and resolved scope

- Prompt implementation commit: `1507ec3de11e7a410e0b74958de7ad9935937360`.
- Base CLI and adapter tree: `684c4d70a486a48305549b24b91d64b428c74219`.
  The CLI command routes remain bootstrap stubs; `internal/provider/wire`
  remains at that same base revision and was not changed by this package.
- Built CLI: Go `1.27.1`, Darwin arm64; `bin/kogen` SHA-256
  `3498688f0a9e37ff5d79c947eda732144a6d4b0536f2a99535d94321a5a88a51`.
  Pinned Git was `2.54.0`.
- Draft target: `kogen-spec`
  `e19dd1c21c19c5be1201c3b6a42c59c28b5c2887`; read `spec/02-formats.md`,
  `spec/03-build.md` §§3.2.1–3.2.5, and `CHANGES-v1.3.md` §§4–7.
  The spec worktree has separate Quint/harness changes; no spec files were
  edited.
- Rust reference: `kogen-rs`
  `a402540b39cedc7f788472297add7ae2f8a6631a`; inspected shaping prompts,
  runner use, and rejected fixture tests. Both Go regression fixtures match
  the Rust `syn-06`/`syn-20` rejected source bytes (`cmp` passed).
- Frozen oracle: `~/cx/kgo/inputs/conformance-v1.2`, source commit
  `0f93bad988fb8d7a8eff4e94954d1db0a046c89d`. Runner metadata reports
  `v1.2+unknown`, macOS 26.7.1 arm64, Git 2.54.0, time scale 0.02.
- Literal selected cases resolved to 7 cases / 9 instances:
  `shape-03` (1), `shape-04` (1), `shape-05` (2), `shape-06` (1),
  `shape-10` (1), `shape-17` (1), and `shape-23` (2).

## Implemented boundary

`internal/shape/prompts` now provides a stable, generic shaper system prompt
with a complete Intent example and grammar: required frontmatter and legal
optional keys, Brief and legal headings, Acceptance IDs, Verify `test`/`test
keep` meanings and modifiers, size limits, and the exact two output paths.
The system prompt starts with the required `You are Kogen Intent shaper.`
role marker. `InitialMessage` byte-sorts domains and gate paths, keeps the
first-user template exact, and appends Request bytes without newline or text
normalization. Validation repair, finish guard, and fresh-fallback messages
preserve the exact supplied feedback. Parser regressions keep the rejected
syn-06 `{}` and syn-20 missing-`size` sources and assert their exact missing
key feedback; neither fixture is patched with an improvised title or size.

This is a prompt component only. There is no production Shape command route
using it yet, and this commit does not establish I5 behavior acceptance.

## Commands and results

| Command | Result |
|---|---|
| `GOMAXPROCS=2 go test -p=2 -parallel=2 ./internal/shape/prompts` | PASS after correcting one test assertion's expected wording. |
| `GOMAXPROCS=2 GIT_CONFIG_GLOBAL=/dev/null make check` | PASS: formatting, vendor fingerprints, vet, tests, and both CLI builds. |
| `git diff --check` and `git diff --cached --check` | PASS. |
| Frozen v1.2 command below | **FAIL**, 7/7 cases and 0/9 instances passed; see exact result summary. |

The first focused test run failed only because the assertion expected
`Do not change, paraphrase, trim, or normalize the Request bytes`, while the
prompt says `do not write ... or change, paraphrase, trim, or normalize the
Request bytes`. The assertion was corrected to match the actual instruction;
the prompt implementation did not change between the failed and passing
focused runs.

Frozen command run:

```sh
make build
SUITE="$HOME/cx/kgo/inputs/conformance-v1.2"
EVIDENCE="$HOME/cx/kgo/evidence/49-shaper-grammar-prompt"
mkdir -p "$EVIDENCE"
PYTHONDONTWRITEBYTECODE=1 PATH="$HOME/.local/share/mise/installs/python/3.14.7/bin:$PATH" \
  "$SUITE/bin/kogen-conformance" run --kogen "$PWD/bin/kogen" \
  --profile cli,state,approval,shape,build,ladder,provider,custody,format,v1.2 --case 'shape-03,shape-04,shape-05,shape-06,shape-10,shape-17,shape-23' \
  --jobs 2 --time-scale 0.02 --workdir "$EVIDENCE/work" \
  --out "$EVIDENCE/results.jsonl"
```

The actual shell also exported the pinned Git/Go/Python/Node PATH and
`GOMAXPROCS=2` before the shown command. `make build` succeeded. The complete
JSONL result is retained at
`/Users/almirsarajcic/cx/kgo/evidence/49-shaper-grammar-prompt/results.jsonl`.
All selected cases failed before a provider request because stderr was exactly
`kogen: implementation bootstrap; command routes are not wired`. `shape-03`,
`shape-04`, `shape-06`, `shape-10`, `shape-17`, and `shape-23` expected the
Shape route to complete (exit 0); they observed exit 2. `shape-05`'s two
instances expected the `intent/request_unavailable` output for whitespace
stdin and an empty file; they observed empty stdout and the same bootstrap
stderr. The runner reports 7 implemented / 7 failed / 0 error / 0 skipped,
with 9 total instances and no provider request reaching the fake server.
These are unwired-command failures, not prompt behavior passes or observed
v1.2 semantic conflicts.

## Effects, conflicts, and deferred gates

- `make check` and `make build` produced local binaries. The oracle run created
  its assigned work directory and retained `results.jsonl`; it reached no
  provider server and made no account or live-provider calls.
- There are no observed v1.2-versus-draft semantic conflicts attributable to
  this prompt component. The exact v1.2 failure is the bootstrap route message
  recorded above; all selected behavior remains unverified until the Shape
  route is wired. No case is counted as passing.
- I5 closure requires integration to use this prompt package from a persistent
  Shape session, then rerun the selected frozen cases against the integrated
  revision and retain that result. Package 00 remains a required foundation.
- R(slice) was not run. It requires the shared coherent migrated v1.3 Quint
  cohort, scratch-copy spec phase, and 500 traces × 25 steps per seed `17`,
  `23`, and `41`, with full observations against a same-revision private
  binary. The shared cohort is unavailable here.
- Planned D-* fixtures are not v1.2 cases; wait for shared frozen v1.3 IDs and
  models before claiming those gates. Linux, race/custody, optional-runtime,
  and live comparison evidence was not run.

The package is **component-ready only**. It does not confer behavior
acceptance for H03–06, H10, H17, H23, or I5.
