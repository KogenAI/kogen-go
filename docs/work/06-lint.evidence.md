# 06-lint evidence

## Revisions and scope

- Branch: `kgo/06-lint`.
- Lint implementation commit: `95e99db41379944e0b598e3d95e7aeba829b73af`.
- CLI and private adapter were both built from that commit. `go version -m` reported the same revision for `bin/kogen` and `bin/kogen-xspec`, `vcs.modified=false`, Go `1.27.1`, `darwin/arm64`, and `CGO_ENABLED=0`.
- Base before this package: `39b43cf063d69cb13dc03d3b7c2cc6616e8cfba3`.
- Spec: `kogen-spec` `e19dd1c21c19c5be1201c3b6a42c59c28b5c2887` (v1.3-draft). `CHANGES-v1.3.md` does not change the §2.2 lint rules; Shape accounting and repair boundaries remain later integration work.
- Rust reference: `kogen-rs` `a402540b39cedc7f788472297add7ae2f8a6631a`, especially `crates/kogen-core/src/intent/lint.rs`, `style.rs`, `lint-data.json`, and `shaping/validation.rs`.
- Implementation uses the frozen embedded `internal/cli/data/lint.json` for tier thresholds and normative banned/hedge/action lists. Structural errors stay severity `error`; style findings stay severity `style` and map to `lint_<rule>` warnings. Request remains opaque and exempt. Shaping-only Approach and gate-path checks are exposed separately from approval lint.

## Commands

The first `GIT_CONFIG_GLOBAL=/dev/null make check` run failed two new test assertions: a phrase intentionally matched both `appropriate` and `where appropriate`, and normalization retained trailing Notes bytes. The assertions were corrected to match the Rust reference. The second run passed:

```sh
GIT_CONFIG_GLOBAL=/dev/null make check
```

Result: PASS — format, vendored dependency fingerprints, `go vet`, all Go tests, and both builds.

The pinned worker `PATH` was in effect. The build and frozen v1.2 acceptance command were:

```sh
make build
SUITE="$HOME/cx/kgo/inputs/conformance-v1.2"
EVIDENCE="$HOME/cx/kgo/evidence/06-lint"
mkdir -p "$EVIDENCE"
PYTHONDONTWRITEBYTECODE=1 PATH="$HOME/.local/share/mise/installs/python/3.14.7/bin:$PATH" \
  "$SUITE/bin/kogen-conformance" run --kogen "$PWD/bin/kogen" \
  --profile cli,state,approval,shape,build,ladder,provider,custody,format,v1.2 --case 'approval-21,format-02,format-03,format-04,format-08,state-05,state-25,v1.2-36-state-06-lint-card-warnings' \
  --jobs 2 --time-scale 0.02 --workdir "$EVIDENCE/work" \
  --out "$EVIDENCE/results.jsonl"
```

## Resolved cases and actual results

The frozen suite resolved **8 cases / 120 instances**. The sole run exited 1: **0 passed, 120 failed, 0 skipped, 0 errors**. Results are retained at `/Users/almirsarajcic/cx/kgo/evidence/06-lint/results.jsonl`; fixture workdirs are under `/Users/almirsarajcic/cx/kgo/evidence/06-lint/work`.

| Effective case | Resolved instances | Result |
|---|---:|---|
| `state-05` | 14: `missing_brief`, `list_in_brief`, `heading_in_brief`, `code_block_in_brief`, `unknown_size`, `acceptance_count`, `missing_title`, `domain_count`, `duplicate_id`, `sequential_ids`, `missing_verify`, `invalid_verify`, `no_change_item`, `open_question` | 0/14 |
| `state-25` | 1 | 0/1 |
| `approval-21` | 1 | 0/1 |
| `format-02` | 65: all listed banned words and phrases, from `ensure` through `the ability to` | 0/65 |
| `format-03` | 13: `should`, `may`, `might`, `could`, `ideally`, `possibly`, `probably`, `generally`, `typically`, `usually`, `try to`, `where possible`, `as much as possible` | 0/13 |
| `format-04` | 24: small/medium/large paragraph, Brief-word, item-count, and Notes-word boundaries | 0/24 |
| `format-08` | 1 | 0/1 |
| `v1.2-36-state-06-lint-card-warnings` | 1 | 0/1 |

Every failure stopped at the same public `kogen intent approve` boundary: exit 2 with `kogen: implementation bootstrap; command routes are not wired`. The cases therefore did not reach the lint component; they are not evidence of a lint-specific semantic mismatch and are not counted as compatible passes. No v1.2-versus-v1.3-draft semantic conflict was observed.

## Local effects and closure gates

- Component tests cover structural rule severity/messages/lines, the complete frozen banned word/phrase lists, inline-code removal and matching boundaries, hedges, inclusive size thresholds, Notes exemptions, long code blocks, warning codes/item IDs, Request exemption, shaping-only Approach, Notes normalization, and gate-path context. `make check` passed at the lint implementation revision.
- `make build` wrote ignored binaries `bin/kogen` and `bin/kogen-xspec`. The one oracle run wrote its result and fixture workdirs only under the retained evidence directory. The frozen suite and goldens were not modified; no live provider or account was used.
- This is a **component** receipt, not behaviour acceptance. I1 must wire public intent parse/lint/approval before the 120 selected oracle instances can close. I5 must wire shaping repairs, warning persistence/card rendering, and shaping lint context (including gate paths).
- The lint corpus includes adapter-specific `malformed_ref`; §2.2 does not define its reference-token recognition, and the selected frozen cases do not exercise it. This implementation does not invent that tokenizer.
- No Quint `R(slice)` replay was run. It requires the shared coherent migrated cohort and same-revision private binary; those inputs are unavailable in this worktree. Planned D-* and other new v1.3 fixtures are not frozen v1.2 cases; wait for shared frozen v1.3 IDs before claiming their gates.
- Linux, optional-runtime, and live-comparison evidence is unavailable here and remains open. No replay seed or divergence is claimed.
