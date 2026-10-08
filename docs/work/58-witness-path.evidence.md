# 58 — Witness path evidence

## Gate and revisions

**Gate: component-ready. Behavior acceptance and I6 remain open.** The
`internal/shape/witness` component is committed at
`0ba8f4f8e14c27ce07714a0f000e1108387c8fc5` on `kgo/58-witness-path`.

- Starting Go revision: `16c7fe990824e5b7de61fe63dedca8124cc44ec0`.
- Target spec: `kogen-spec` v1.3-draft
  `e19dd1c21c19c5be1201c3b6a42c59c28b5c2887`.
- Read clauses: `spec/02-formats.md` §§2.3, 2.5.1, 2.5.3;
  `spec/03-build.md` §§3.2.7, 3.4 B3, 3.8.2; `spec/04-provider.md` §4.8.2;
  `spec/CONFORMANCE.md` H26 and L32–33; `CHANGES-v1.3.md` §§1 and 4.
- Read worker references: `docs/work/WORKER-RULES.md`, `PLAN.md`,
  `QUEUE-source.md`, and `58-witness-path.md`.
- Rust reference: `kogen-rs`
  `a402540b39cedc7f788472297add7ae2f8a6631a`; inspected
  `build/mod.rs`, `build/provider.rs`, `build/provider_prompt.rs`,
  `build/single_rung/execute.rs`, `approval/manifest.rs`,
  `approval/model.rs`, `approval/replay/approve.rs`, and
  `intent/shaping/runner/execute.rs`.
- CLI source stayed at `16c7fe990824e5b7de61fe63dedca8124cc44ec0`; its Shape
  route still reports that Shape is wired in the integration round. The
  acceptance build's `bin/kogen` SHA-256 was
  `2a75ec5df6419c2376cb6d902af0bedf876a7c70239e8e8d854c8f0c9e67478d`.
- Frozen oracle: `$HOME/cx/kgo/inputs/conformance-v1.2`, source revision
  `0f93bad988fb8d7a8eff4e94954d1db0a046c89d`; the runner identifies itself as
  `v1.2+unknown`, SHA-256
  `0b53a3b60956cbe8dde7af54e274c46b3e8d430c45662d7dff67445f440b39f9`.
- Host/toolchain: Go `1.27.1 darwin/arm64`, Git `2.54.0`, macOS
  `26.7.1-arm64-arm-64bit-Mach-O`. The pinned worker PATH was exported.

## Implemented component

`Run` is a no-op unless `shaping.proof` resolves to `witness`. In witness
mode it passes the exact Intent, raw Request and acceptance bytes to a
throwaway-session effect port, requests R1 plus R2 for a hard plan, and runs
only on a real `gate.GateReport`. Failed acceptance IDs receive one
tool-less test-auditor adjudication per red round. `TEST-WRONG` scopes repair
to the test, `WITNESS-WRONG` scopes it to the implementation, and
`UNDECIDED` becomes `feasibility_concern`. No judgment can change a gate
result; a later real landable report is required before publishing proof.

Proof publication validates the exact witness ref and base, and hashes the
returned binary diff bytes into the schema-2 `Proof`. `ValidateApproval`
refuses missing/unproven proofs with `intent/unproven` and checks the direct
witness ref and exact Git diff against the record. `Reverify` has only apply,
real-gate-verify, and close effects: it checks the gate receipt's base tree,
returns `UseWitness` only for a landable report, and otherwise returns
`ContinueLadder`; it has no provider or model capability. The shared output
budget supports concurrent hard rungs and stops dispatch after unknown usage
or the 60,000-token cap; the probe context is capped at 20 minutes.

The production effect adapter and public Shape/approval/Build call sites are
not in this owned directory and remain unwired. Therefore the throwaway Git
workspace, sandbox, provider sessions, approval publication, and B3-before-
ladder ordering are interface contracts here, not observed production
effects. The local tests cover opt-in behavior, adjudication parsing and
repair scopes, budget accounting, exact proof/ref/diff validation, and
conflict fallthrough. They do not claim public behavior conformance.

## Commands and results

Commands used the pinned worker PATH.

1. `GOMAXPROCS=2 go test -count=1 -p=2 -parallel=2 ./internal/shape/witness` —
   passed.
2. First `GIT_CONFIG_GLOBAL=/dev/null make check` attempt — failed before
   tests because `gofmt required: internal/shape/witness/run.go`. The file was
   formatted; this attempt is retained in the evidence record.
3. `GOMAXPROCS=2 GIT_CONFIG_GLOBAL=/dev/null make check` — passed; format,
   vendor fingerprints, vet, all tests, and both builds passed.
4. `make build` — passed as the first command in the assigned oracle run.
5. Assigned frozen v1.2 command, run once:

   ```sh
   make build
   SUITE="$HOME/cx/kgo/inputs/conformance-v1.2"
   EVIDENCE="$HOME/cx/kgo/evidence/58-witness-path"
   mkdir -p "$EVIDENCE"
   PYTHONDONTWRITEBYTECODE=1 PATH="$HOME/.local/share/mise/installs/python/3.14.7/bin:$PATH" \
     "$SUITE/bin/kogen-conformance" run --kogen "$PWD/bin/kogen" \
     --profile cli,state,approval,shape,build,ladder,provider,custody,format,v1.2 --case 'v1.2-100-ladder-32,v1.2-101-ladder-33,v1.2-135-shape-26' \
     --jobs 2 --time-scale 0.02 --workdir "$EVIDENCE/work" \
     --out "$EVIDENCE/results.jsonl"
   ```

   Runner start: `2026-10-08T00:35:43Z`. Result JSONL:
   `/Users/almirsarajc/cx/kgo/evidence/58-witness-path/results.jsonl`,
   SHA-256 `edea949bf08937586828b8e570df3f0006641d655869d7690ad0e23da6b7e1bd`.
   Result: **0/3 cases and 0/4 instances passed; 3 failed, 0 errors, 0
   skipped, 0 unimplemented**. Each failure stopped at step 2, before the
   witness behavior or a fake-provider request:

   | Effective ID | Instances | Observed boundary |
   | --- | ---: | --- |
   | `v1.2-100-ladder-32` | 1 | `intent shape` exited 70 instead of 0; stdout was `controller/internal_error: intent shaping is wired in the Shape integration round`; expected `Feasibility: PROVEN`. |
   | `v1.2-101-ladder-33` | 1 | Same step-2 exit and exact stdout; expected `Feasibility: PROVEN` before moved-base Build re-verification. |
   | `v1.2-135-shape-26` | 2 (`1:green`, `2:red undecided`) | Both instances got the same step-2 exit/stdout; expected respectively `Feasibility: PROVEN` and `Feasibility: UNPROVEN`. |

   The runner hint was `no provider request reached the fake server
   (KOGEN_PROVIDER_URL seam missing?)`. Thus there are no compatible public
   behavior passes and no v1.2 semantic assertion was observed. These failures
   are the unwired Shape integration boundary, not semantic v1.2 conflicts.

## Conflicts, effects, and deferred closure

- Historical v1.2 conflicts observed: **none**. The selected cases did not
  reach their assertions. No old demotion behavior was added.
- Exact draft text mismatch: `spec/03-build.md` §3.2.7 names
  `shaping.witness_rounds` (default 2), while §2.3's shaping schema and the
  current Go project validator accept only `shaping.proof`. This component
  defaults to two red rounds; a public override needs a shared schema/spec
  decision.
- I6 must wire `Run`, `ValidateApproval`, and `Reverify` through public Shape,
  approval, and Build. It must prove a green B3 lands with zero model requests
  and a moved-base red witness falls through to the ladder without demotion.
  The integration must supply real throwaway workspaces, sandbox, gate,
  supervised Git binding, and provider session/effects, then rerun the literal
  cases and retain the results JSONL.
- No coherent shared migrated v1.3 Quint cohort was available. No scratch-copy
  spec replay or 500 traces × 25 steps for seeds 17, 23, and 41 was run, and no
  same-revision private-binary conformance/divergence claim is made. Planned
  `D-*` fixtures are not frozen v1.2 cases; wait for shared frozen v1.3 IDs.
- Linux, optional-runtime, and live comparison gates remain open for their
  stated external evidence. No live provider or account access occurred.
- Local effects were limited to the witness source commit, `bin/kogen` and
  `bin/kogen-xspec` builds, and the retained external runner work/results.
  The spec, suite, goldens, Rust tree, replay harness, and other worktrees were
  not modified.
