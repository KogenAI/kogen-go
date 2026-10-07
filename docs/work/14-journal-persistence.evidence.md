# 14-journal-persistence evidence

## Revisions and environment

- Worker branch: `kgo/14-journal-persistence`.
- Journal implementation commit: `18d9c6307843002c749dc49cfd8be93c3c01dd5e`.
- CLI source at the required oracle run: `c6f1b4a01dd7d8d860d43f174a4a8897c50246b7`; `cmd/kogen` still calls the bootstrap route in `internal/app/bootstrap.go` and exits 2 with `implementation bootstrap; command routes are not wired`.
- Production adapter revision: none is wired to the journal or CLI. The package exposes a component API only.
- Target: spec `e19dd1c21c19c5be1201c3b6a42c59c28b5c2887` (`v1.3-draft`); read `CHANGES-v1.3.md` §§1, 3 and the relevant `spec/02-formats.md` §§2.8, 2.10, `spec/03-build.md` §§3.8.2, 3.10, and `spec/04-provider.md` §4.9.5.
- Rust reference: `a402540b39cedc7f788472297add7ae2f8a6631a`; inspected `intent/shaping/journal.rs`, `run/persistence.rs`, `build/provider_journal.rs`, and `status/agents.rs`.
- Frozen suite: `0f93bad988fb8d7a8eff4e94954d1db0a046c89d` (`~/Areas/Kogen/kogen-conformance`, v1.2); runner metadata says `v1.2+unknown`.
- Host/toolchain: macOS 26.7.1 arm64, Go 1.27.1, Git 2.54.0, Python runner 3.14.7.

## Implementation and local effects

- `RunStore` takes a `safefs.Root` and validated root-relative run directory. All journal, snapshot, transcript, and agent writes use rooted `safefs` publication. Run events are durably appended before `run.json` is atomically replaced. Snapshots always emit `landing` (including null), `recovery` (including an empty array), and `cleanup_pending`.
- Added draft recovery records and event helpers for `recovery_preserved` and `cleanup_failure`. Recovery identities require `verification: unverified`; a record identifies either a ref/tree pair or an archive identity. Cleanup failure publication sets `cleanup_pending: true`.
- Added event timestamps, string-only `reason` validation, and base64 encoding for non-UTF-8 detail bytes. The audit receipt helper fixes `mode: observational`, `demoted: false`, and `advisory_items: []`.
- Added allowlisted request telemetry and constrained transcript records. URL userinfo, query, and fragment are omitted; credential-like path segments are redacted; only routing-header names are retained; body contents and header values have no transcript fields. Request and response times, body size, prefix SHA-256 digests, model/effort, cache/thread identities, and nullable usage are persisted. Missing usage remains JSON `null`; measured zero remains `0`.
- Agent JSONL records contain only a validated lifecycle event, timestamp, role, status, and 32-hex agent ID. They have no prompt, message, path, tool-argument, or tool-output field.
- Crash-injection hooks cover after event append, before snapshot publication, and after snapshot publication. Tests verify the durable event/old snapshot window and the published new snapshot after the final boundary. These are injected errors at publication boundaries, not a killed-process or power-loss simulation.
- The journal component does not stop writers, publish Git recovery refs/archive bytes, reconcile terminal runs, or perform cleanup. Those remain recovery-controller integration effects; the helpers here persist their records.

## Commands and results

All Go commands used the pinned worker `PATH` from `WORKER-RULES.md`.

| Command | Result |
|---|---|
| `GOMAXPROCS=2 go test -count=1 -parallel=2 ./internal/journal` | Passed on package source commit `18d9c63`. |
| `GIT_CONFIG_GLOBAL=/dev/null make check` | Passed: formatting, vendor fingerprints, vet, full tests with `-parallel=2`, and both builds. |
| `git diff --cached --check` | Passed before the implementation commit. |
| Assigned frozen v1.2 command below | Runner exit 1: 4 cases / 4 instances, 0 pass, 4 fail, 0 error, 0 skipped, 0 unimplemented. All failures occurred at `intent approve` before journal, provider, recovery, or status assertions. |

The assigned oracle command was run once. Results and per-case workdirs are retained at `/Users/almirsarajcic/cx/kgo/evidence/14-journal-persistence/results.jsonl` and `/Users/almirsarajcic/cx/kgo/evidence/14-journal-persistence/work`.

```sh
make build
SUITE="$HOME/cx/kgo/inputs/conformance-v1.2"
EVIDENCE="$HOME/cx/kgo/evidence/14-journal-persistence"
mkdir -p "$EVIDENCE"
PYTHONDONTWRITEBYTECODE=1 PATH="$HOME/.local/share/mise/installs/python/3.14.7/bin:$PATH" \
  "$SUITE/bin/kogen-conformance" run --kogen "$PWD/bin/kogen" \
  --profile cli,state,approval,shape,build,ladder,provider,custody,format,v1.2 --case 'v1.2-05-missing-usage,v1.2-06-crash-after-base-cas,v1.2-130-state-11,v1.2-131-state-12' \
  --jobs 2 --time-scale 0.02 --workdir "$EVIDENCE/work" \
  --out "$EVIDENCE/results.jsonl"
```

Resolved cases and actual results:

| Case | Instances | Result |
|---|---:|---|
| `v1.2-05-missing-usage` | 1 | Failed at `intent approve greet`: exit 2, expected 5; no SHA-256 output; stderr was the bootstrap message. Fake provider received no request. |
| `v1.2-06-crash-after-base-cas` | 1 | Failed at `intent approve greet`: exit 2, expected 5; no SHA-256 output; stderr was the bootstrap message. CAS/recovery was not reached. |
| `v1.2-130-state-11` | 1 | Failed at `intent approve greet`: exit 2, expected 0; approval output was absent and stderr contained the bootstrap message. |
| `v1.2-131-state-12` | 1 | Failed at `intent approve greet`: exit 2, expected 0; approval output was absent and stderr contained the bootstrap message. |

No case is counted as passed or behavior-compatible. The implementation commit changes the journal package only; the selected CLI path remains unwired, so this run is not evidence for package behavior.

## Historical conflicts and closure gates

Static spec/fixture comparison found these exact v1.2 versus v1.3-draft conflicts. Neither assertion was reached by the oracle run:

- `v1.2-130-state-11` sets `run_json.match.$exact: true` and expects only the old schema fields through `started_ms`. Draft `spec/02-formats.md` §2.8 requires `recovery` and `cleanup_pending` as well. The exact run.json object cannot satisfy both schemas.
- `v1.2-131-state-12` expects default `land_policy: "green-or-advisory"`. Draft `CHANGES-v1.3.md` §1 and `spec/02-formats.md` §2.3 make `green` the default; the legacy value has the same eligibility but remains a different report value.
- No static conflict was found for `v1.2-05-missing-usage`: null usage and a null cache-hit rate are consistent with incomplete measurement. No static conflict was found for `v1.2-06-crash-after-base-cas`: the case's landed/claim/incoming-ref assertions do not forbid the draft's required pre-cleanup recovery preservation. Both remain unverified because approval did not run.

Remaining gates:

- I3 approval → queue → provider → verification/gate → CAS → status integration, then a changed-revision behavior rerun retaining this failed run.
- Shared frozen v1.3 schema replacements and D-REC-03–06 fixtures are not available in the frozen v1.2 suite. Do not claim those draft gates until shared literal IDs/models are frozen.
- The `R(slice)` scratch-copy replay against the coherent migrated v1.3 Quint cohort was not run; the required shared cohort is unavailable here. No seeds (17, 23, 41), observations, or divergence are claimed.
- No Linux runtime, optional-runtime, live-provider, or live-comparison evidence was produced. No account access or live provider call was used.
- A real recovery controller must still stop writers, preserve the latest tree with create-only ref/archive publication, reconcile publication-before-record crashes, retry terminal cleanup, and prove the filesystem/Git effects. Component crash hooks do not close those gates.
