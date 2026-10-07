# 01 Public Parser/Help evidence

Recorded 7 October 2026 for `/Users/almirsarajcic/Areas/Kogen/kogen-go-wt/01-public-parser-help` on branch `kgo/01-public-parser-help`.

## Revisions and inputs

- Worker starting revision: `9f982d22f856a3f1d453194d9a1ac19281fc715b`.
- Parser/render source tree at the final passing component check: `273c1a17097f7d71c2c87eb0ffb88a2621553655` (`git write-tree` with the owned Go files staged).
- CLI executable entrypoint revision: `cmd/kogen` and `internal/app.Bootstrap` remain unchanged from the starting revision. `Bootstrap` still refuses commands as unwired, and it does not import the new parser; no CLI adapter revision is available yet.
- Rust reference read-only revision: `kogen-rs` `a402540b39cedc7f788472297add7ae2f8a6631a`; reviewed `kogen-cli/src/{parse,arguments,moved,help,request,main}.rs`.
- Target spec: `kogen-spec` `e19dd1c21c19c5be1201c3b6a42c59c28b5c2887`; relevant authority was `CLI-RULE.txt`, `spec/01-cli.md` §§1.2–1.5 and `CHANGES-v1.3.md` §4 for the shape output/flag statement.
- Frozen oracle: `~/cx/kgo/inputs/conformance-v1.2`, `kogen-conformance` commit `0f93bad988fb8d7a8eff4e94954d1db0a046c89d`.
- Pinned worker tools: Git 2.54.0, Go 1.27.1, Python 3.14.7 and Node 24.21.0 on macOS 26.7.1 arm64.

## Implementation and component results

- Added a typed parsed-request/command representation and explicit parsing for the fixed command tree. Moved forms load from the frozen `moved.json`; prefix forms win before moved flags, and moved forms run before tree parsing.
- Component tests exercise all 15 moved forms, including prefix-over-flag ordering and the provider-only `--as` exception.
- Option scanning preserves the specified precedence: boolean values, unknown options, route-disallowed options, missing option values, missing positionals, unexpected positionals, then value validation. `--` ends option parsing, repeated value options retain the last value, and a lone `-` remains positional. Slug shape is left to the command handler.
- `--help` is not special. Provider validation admits only `chatgpt` and `grok`. Project and origin values resolve against the supplied working directory without canonicalizing their spelling; only provider-use resolves `--project` when it is present.
- Usage errors render the selected fixed page with a blank separator; moved forms render one line. The renderer uses target-draft help bytes for the changed primary pages and embedded bytes for the remaining pages. A component test verifies SHA-256 for all 15 pages against `spec/data/help/` at the target spec revision. The frozen oracle's `data/v1.2/help/` overlay also matches all 15 target pages byte-for-byte.
- `GIT_CONFIG_GLOBAL=/dev/null make check` — **passed on the final source tree**: format, vendor fingerprints, `go vet`, tests and both builds. `internal/cli/parse` and the existing data corpus tests passed.
- An earlier `GIT_CONFIG_GLOBAL=/dev/null make check` attempt failed only because two expected SHA values in the new help-page test were transcribed incorrectly (`status` and `queue-start`). The fixture values were corrected from the frozen spec hashes; the passing final check is retained, and no oracle run was repeated.
- `git diff --cached --check` — passed.

## Frozen v1.2 command and results

Commands run with the required pinned PATH:

```sh
GIT_CONFIG_GLOBAL=/dev/null make check
make build
SUITE="$HOME/cx/kgo/inputs/conformance-v1.2"
EVIDENCE="$HOME/cx/kgo/evidence/01-public-parser-help"
mkdir -p "$EVIDENCE"
PYTHONDONTWRITEBYTECODE=1 PATH="$HOME/.local/share/mise/installs/python/3.14.7/bin:$PATH" \
  "$SUITE/bin/kogen-conformance" run --kogen "$PWD/bin/kogen" \
  --profile cli,state,approval,shape,build,ladder,provider,custody,format,v1.2 --case 'cli-10,cli-15,cli-18,cli-19,cli-20,cli-22,format-06,format-09,v1.2-01-fixed-cli-help-and-grok,v1.2-07-cli-03-unknown-command,v1.2-08-cli-04-unknown-subcommand,v1.2-09-cli-05-unknown-option,v1.2-10-cli-06-missing-positionals,v1.2-11-cli-07-unexpected-argument,v1.2-12-cli-08-option-needs-value,v1.2-13-cli-09-boolean-takes-no-value,v1.2-14-cli-11-unknown-provider,v1.2-15-cli-12-watch-with-json,v1.2-16-cli-13-double-dash,v1.2-17-cli-14-options-before-command,v1.2-18-cli-16-short-option,v1.2-19-cli-17-help-bad-topic,v1.2-20-cli-21-invalid-slug,v1.2-21-cli-24-help-after-positionals,v1.2-33-shape-json-is-unsupported' \
  --jobs 2 --time-scale 0.02 --workdir "$EVIDENCE/work" \
  --out "$EVIDENCE/results.jsonl"
```

The command resolved **25 cases / 166 instances**: `cli` 6/35, `format` 2/30, and `v1.2` 17/101. All 25 cases failed, with 0 passed, 0 errors, 0 skipped and 0 unimplemented instances. For every case the public binary emitted `kogen: implementation bootstrap; command routes are not wired` on stderr, while the expected CLI contract requires command output on stdout and empty stderr. This is an entrypoint/wiring gap; the run did not exercise this parser or renderer, so it establishes no parser behavior result.

Resolved IDs:

```text
cli-10, cli-15, cli-18, cli-19, cli-20, cli-22,
format-06, format-09,
v1.2-01-fixed-cli-help-and-grok,
v1.2-07-cli-03-unknown-command,
v1.2-08-cli-04-unknown-subcommand,
v1.2-09-cli-05-unknown-option,
v1.2-10-cli-06-missing-positionals,
v1.2-11-cli-07-unexpected-argument,
v1.2-12-cli-08-option-needs-value,
v1.2-13-cli-09-boolean-takes-no-value,
v1.2-14-cli-11-unknown-provider,
v1.2-15-cli-12-watch-with-json,
v1.2-16-cli-13-double-dash,
v1.2-17-cli-14-options-before-command,
v1.2-18-cli-16-short-option,
v1.2-19-cli-17-help-bad-topic,
v1.2-20-cli-21-invalid-slug,
v1.2-21-cli-24-help-after-positionals,
v1.2-33-shape-json-is-unsupported
```

The complete, unmodified result is `/Users/almirsarajcic/cx/kgo/evidence/01-public-parser-help/results.jsonl`; the corresponding scratch work directory is `/Users/almirsarajcic/cx/kgo/evidence/01-public-parser-help/work`. The oracle files and goldens were not changed. No selected historical assertion conflicts with the target CLI/help contract; the failed run is attributable to the unavailable public route.

## Local effects and closure gates

- `make build` produced `bin/kogen` and `bin/kogen-xspec`. The conformance runner wrote only its requested external evidence/work paths. No provider, OAuth port, account, credential store, user project or origin was accessed.
- This is **component-ready only**, not behavior-accepted. I1 must wire the parser and renderer into the public foundation and rerun compatible CLI/help cases. Project- and handler-dependent rows remain open through I3 as assigned.
- No frozen shared v1.3 conformance IDs or coherent migrated Quint cohort are available. Planned D-* fixture labels are not v1.2 cases and are not claimed. `R(slice)` was not run; it requires a scratch cohort copy with 500 traces × 25 steps for each seed 17, 23 and 41, then same-revision private-binary conformance.
- Linux, optional-runtime, race/custody, live provider comparison and release-manifest gates were not run and remain external evidence gaps.
- Package 00 remains a real foundation task after this bootstrap; its scaffold does not freeze interfaces or assert conformance.
