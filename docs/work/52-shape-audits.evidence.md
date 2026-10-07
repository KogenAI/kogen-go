# 52-shape-audits evidence

Recorded 8 October 2026 in the assigned worktree
`/Users/almirsarajcic/Areas/Kogen/kogen-go-wt/52-shape-audits`, branch
`kgo/52-shape-audits`.

## Revisions and scope

- Component revision under test: `14c95aa6d17467e2960285bd797ca515252fbc6d`
  (`Implement Shape audit component`). This is the exact source revision used
  by the build and frozen-suite command below.
- Target spec: `kogen-spec` `e19dd1c21c19c5be1201c3b6a42c59c28b5c2887`;
  read `CHANGES-v1.3.md` §§4–5 and `spec/03-build.md` §§3.2.1, 3.2.3 steps
  7–9 and 3.2.8, plus the relevant role and warning records in
  `spec/02-formats.md`.
- Worker rules and plan: `docs/work/WORKER-RULES.md`, `PLAN.md`,
  `QUEUE-source.md`, and `52-shape-audits.md` in this worktree.
- Rust reference: `kogen-rs`
  `a402540b39cedc7f788472297add7ae2f8a6631a`; inspected
  `crates/kogen-core/src/intent/shaping/audit.rs`, `prompts.rs`,
  `runner/validate.rs`, and `runner/execute.rs`.
- CLI route revision: `fba340e5928b2a6c6644bb056ff76b3eb1276616`
  (`Bootstrap the Go implementation`). `internal/app/bootstrap.go` still
  returns the bootstrap diagnostic for Shape and approval routes.
- Provider wire/session revision: `f5b78d9adb137ac13983e97c2663d0553506c38d`;
  provider retry/transport revision:
  `7c42bf293f10da4364f5f246b7e350380ecc7575`. Command acceptance adapter
  revision: `837c1a5bd16f067f24f7870ac26c539655024b46`.
- Host/toolchain: Go `1.27.1`, Git `2.54.0`, Darwin arm64. The runner recorded
  `macOS-26.7.1-arm64-arm-64bit-Mach-O`. Built `bin/kogen` SHA-256:
  `da07c9b598c6853f13d1cbd34e4d704aa08ba758e8ac90f9f7b716909dbd1bc9`.
- Frozen oracle: `$HOME/cx/kgo/inputs/conformance-v1.2`, source baseline
  `kogen-conformance` `0f93bad988fb8d7a8eff4e94954d1db0a046c89d`. The frozen
  input copy has no Git metadata; it reports `v1.2+unknown`.

## Component behavior and local verification

`internal/shape/audit` now resolves the effective auditor tuple from
`contract.RoleManifest`; it has no local provider/model default. Each eligible
traversal sends the requirement-ledger request and then the acceptance-test
audit as separate tool-less auditor turns. The transport reports each HTTP
attempt and nullable usage through the existing Shape accounting port. Ledger
coverage checks Request numbers, backticked identifiers and quoted strings,
and requires every map to name an Acceptance item or a non-empty
`untestable:` reason. Test-audit citations count only when they are exact byte
substrings of the raw Request.

The evaluator waits for both audit results before selecting repair feedback.
Simultaneous coverage and cited non-valid test-audit findings yield one
`RepairCoverageAndAudit` result with both details, so a caller can request one
new validation traversal. Each repair is requested at most once for the Shape
run; persistent coverage and test-audit findings become deduplicated
`coverage_gap` and `audit_<verdict>` warnings. The ledger and warning artifact
encoders retain the approval hash and the on-disk JSON/newline format. Malformed
ledger JSON still permits the second audit request before the parse failure is
returned.

`Recheck` reads assumption/shared-contract predicates only through a rooted
filesystem opened at the checked base, and checks every `blocks_on` slug on the
target branch. Missing/changed predicates and dependencies return stale
observations and make `CanBuild()` false. The component returns artifact bytes
and observations; it does not write files or wire the CLI/build controller.

Focused command:

```sh
GOMAXPROCS=2 go test -p=2 -parallel=2 ./internal/shape/audit
```

Result: PASS. Tests cover ledger literals and mappings, exact citations,
resolved role pins, tool-less requests, audit order, separate attempt/turn
accounting, combined repair feedback, warning transitions, artifact schema,
rooted predicate reads, stale predicates and dependency checks.

Repository check:

```sh
GIT_CONFIG_GLOBAL=/dev/null make check
```

Result: PASS — formatting, vendor fingerprints, vet, tests and both builds
passed. The component source was committed before this run. The command's
`make build` also passed.

## Frozen v1.2 command and result

The assigned command was run once at component revision
`14c95aa6d17467e2960285bd797ca515252fbc6d`:

```sh
make build
SUITE="$HOME/cx/kgo/inputs/conformance-v1.2"
EVIDENCE="$HOME/cx/kgo/evidence/52-shape-audits"
mkdir -p "$EVIDENCE"
PYTHONDONTWRITEBYTECODE=1 PATH="$HOME/.local/share/mise/installs/python/3.14.7/bin:$PATH" \
  "$SUITE/bin/kogen-conformance" run --kogen "$PWD/bin/kogen" \
  --profile cli,state,approval,shape,build,ladder,provider,custody,format,v1.2 --case 'approval-17,shape-21,v1.2-135-shape-26,v1.2-33-shape-json-is-unsupported' \
  --jobs 2 --time-scale 0.02 --workdir "$EVIDENCE/work" \
  --out "$EVIDENCE/results.jsonl"
```

Result: **0/4 cases and 0/9 instances passed**; 4 failed, 0 errors, 0 skipped,
0 unimplemented. Resolved literal IDs and instances:

- `approval-17`: 1 failed instance (`Shape warnings, including
  feasibility_concern, appear on the card`). It expected exit 5 and warning
  rows; it got exit 2, empty stdout, and stderr
  `kogen: implementation bootstrap; command routes are not wired`.
- `shape-21`: 1 failed instance (`Shaping audit: no citation gives no
  repair`). It expected `Validated after 1 round(s).`; it got exit 2, empty
  stdout, and the same bootstrap stderr. The runner confirmed no request
  reached the fake provider.
- `v1.2-135-shape-26`: 2 failed instances (`1:green`, `2:red undecided`). They
  expected `Feasibility: PROVEN` and `Feasibility: UNPROVEN`; both got exit 2,
  empty stdout, and the same bootstrap stderr. No request reached the fake
  provider.
- `v1.2-33-shape-json-is-unsupported`: 5 failed instances (`shape-02`,
  `shape-14`, `shape-19`, `shape-20`, `shape-22`). They expected the 658-byte
  usage error beginning `kogen intent shape: unknown option '--json'`; they
  got empty stdout and the same bootstrap stderr.

The runner started at `2026-10-07T21:54:47Z`, used time scale `0.02`, and
recorded Git `2.54.0`. The failed result and its work directories are retained
at [results.jsonl](/Users/almirsarajcic/cx/kgo/evidence/52-shape-audits/results.jsonl)
and `/Users/almirsarajcic/cx/kgo/evidence/52-shape-audits/work/`. Result
SHA-256: `da8903460abfe618d39b31cad6e0117aff39e62d969fe47337e491204cd91814`.
No oracle retry was made.

No selected behavior assertion reached the audit component. Compatible
component tests and `make check` passed; no conformance case is counted as a
pass. No exact v1.2/draft semantic conflict was observed because the route
failure preceded those assertions. In particular, this run does not establish
H19/H20/H22 audit behavior; the selected overlay `v1.2-33` exercises CLI
`--json` rejection, not those audit assertions.

## Conflicts and deferred closure gates

- `D-SHAPE-02` and `D-SHAPE-03` are planned fixture names, not available frozen
  v1.2 cases. Wait for literal IDs in the shared frozen v1.3 suite; do not
  claim those gates from local unit tests.
- I5 must wire the evaluator, provider transport, persistent Shape session and
  audit accounting, publish `ledger.json`/`shape-warnings.json` through rooted
  file operations, apply the combined feedback once, and rerun the compatible
  Shape/approval behavior cases on the integrated revision. Predicate and
  dependency recheck also remains unwired to the Build decision path.
- I6 must integrate the V135 witness path and supply its stated witness
  evidence. The v1.2 witness result above failed at the bootstrap route and is
  not a semantic conflict or pass.
- No package-specific Shape audit Quint slice or coherent, frozen migrated
  v1.3 Quint cohort was supplied; no R(slice) scratch replay was run. The
  target plan notes that existing Quint slices do not model end-to-end Shape
  counters.
- Linux, optional-runtime and live comparison gates require their external
  evidence; this worker ran only on Darwin arm64.

Conflicts observed: none. No live provider/account calls were made. The frozen
suite and goldens, spec, and other worktrees were not modified.
