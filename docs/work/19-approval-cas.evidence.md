# 19 Approval CAS evidence

## Revisions and scope

- Branch: `kgo/19-approval-cas`.
- Approval publisher source commits: `37a4140eaf6c5eb75daaffd23452b5d763976c7f` and follow-up `f5dc74dcceddb7db34b118dd90093584bbedc56f` (`Refine approval CAS conflict handling`). The follow-up includes the final source and test changes; `git status` was clean after it.
- Starting Go revision: `afb443172c90e3947acace5bfbe8505eeca02042`.
- Target spec: `kogen-spec` `e19dd1c21c19c5be1201c3b6a42c59c28b5c2887` (`v1.3-draft`). Relevant clauses: `spec/02-formats.md` §§2.1.3 and 2.5.1; `spec/01-cli.md` §1.7.2. `CHANGES-v1.3.md` has no approval CAS delta; its broader finding 4 about post-CAS synchronization and durable records remains an integration closure item.
- Rust reference: `kogen-rs` `a402540b39cedc7f788472297add7ae2f8a6631a`. Inspected `approval.rs`, `approval/model.rs`, `approval/support.rs`, `approval/replay.rs`, `approval/replay/approve.rs`, and `git.rs` commit/tree/ref helpers.
- Frozen v1.2 input: `$HOME/cx/kgo/inputs/conformance-v1.2`. The assigned case file is `docs/work/19-approval-cas.cases`.
- Scope is `internal/approval/publish/**` plus this evidence and the package gate receipt. No CLI, shared contract, spec, oracle, or golden files were changed.

## Implementation

- Publishes the prepared exact Intent and acceptance bytes as mode `100644` blobs with a compact schema-2 `approval.json`. It includes a matching `ledger.json` byte for byte and omits missing, malformed, or stale-hash ledgers. The JSON has the schema-2 field set, exact approval and Intent hashes, target branch/base, domains, source acceptance path, protected-manifest digests, check baseline, optional witness, approver, and shared RFC 3339 timestamp.
- Creates the package tree in the origin object database using supervised `hash-object -w --no-filters --stdin` and `mktree -z` calls. The tree contains only the approval package paths. `commit-tree` receives no parent for the first approval and exactly the observed previous approval commit for a reapproval. The commit message has only the required four approval trailers.
- Uses descriptor-rooted reads for the live Intent and acceptance sources. It verifies the prepared digest before object publication, creates the commit, then rereads both sources immediately before the single `update-ref --no-deref` CAS call. A changed or missing source returns `intent/hash_mismatch` without moving the ref.
- Refuses symbolic approval refs. A lost CAS rereads the current ref, rebuilds the commit with that value as its sole parent, and tries once more. A second lost CAS returns `intent/approval_ref_conflict`; the concurrent ref remains unchanged by this publisher.
- Tests use temporary bare origins and real Git object/ref effects. They cover exact blobs/trailers/tree fields, first approval and reapproval parent chains, matching/stale ledger handling, a real stale expected-value CAS after injected concurrent ref movement, refusal on a second lost race without overwriting the concurrent ref, late source mutation after commit creation, and symbolic-ref refusal.

## Commands and results

Pinned worker PATH was set before shell commands as required by `docs/work/WORKER-RULES.md`.

| Command | Result |
|---|---|
| `gofmt -w internal/approval/publish/publish.go internal/approval/publish/publish_test.go` | Passed. |
| `git diff --check` | Passed. |
| `GOMAXPROCS=2 go test -p=2 -parallel=2 -run '^$' ./internal/approval/publish` | Passed compilation only; no test cases ran. |
| `make build` | Passed at source revision `f5dc74dcceddb7db34b118dd90093584bbedc56f`. `bin/kogen` SHA-256 `ab745a2cfb362e9f499517365ed5203af872f273bdca5c1843dc612fd10c4f03`; `bin/kogen-xspec` SHA-256 `54b9d7dd308855ef073c12c6e6e46aa80272ccb129ddb7a789f5bac68be495af`. Both report Go 1.27.1, darwin/arm64, and the same revision; VCS modified is true while this evidence note is untracked. |
| `python3 tools/package-gate.py 19-approval-cas` | Passed receipt validation: accepted compiled component evidence; behavior remains with the closure round. |
| `GIT_CONFIG_GLOBAL=/dev/null make check` | Not run: `~/cx/kgo/gates.lock` already existed and could not be acquired by this worker. No pass is claimed. |
| Assigned frozen v1.2 command | Not run: the lock could not be acquired. No case or instance is counted as passing. |

The exact assigned conformance invocation is:

```sh
SUITE="$HOME/cx/kgo/inputs/conformance-v1.2"
EVIDENCE="$HOME/cx/kgo/evidence/19-approval-cas"
mkdir -p "$EVIDENCE"
PYTHONDONTWRITEBYTECODE=1 PATH="$HOME/.local/share/mise/installs/python/3.14.7/bin:$PATH" \
  "$SUITE/bin/kogen-conformance" run --kogen "$PWD/bin/kogen" \
  --profile cli,state,approval,shape,build,ladder,provider,custody,format,v1.2 --case 'approval-02,approval-03,approval-04,state-08,state-09,state-29,v1.2-02-approval-hash-intent-and-test-bytes' \
  --jobs 2 --time-scale 0.02 --workdir "$EVIDENCE/work" \
  --out "$EVIDENCE/results.jsonl"
```

No conformance run has been made in this worktree, so resolved runner instances, compatible passes, failures/conflicts, and a results JSONL path do not exist yet. The shared lock directory was already present when checked, without an owner file. It was not acquired by this worker or removed. A separate `make check` process was observed while the directory remained present; ownership could not be established.

## Conflicts and closure gates

- Approval CAS has no specific v1.3 draft change in `CHANGES-v1.3.md`; no spec conflict is claimed. The v1.2 oracle result is pending and will be reported exactly, without treating an unwired handler as passing.
- I1 still owns public `intent approve` wiring and behavior acceptance. This package commit alone does not close the CLI cases.
- `R(approve)` remains open for package 65: the shared coherent migrated v1.3 Quint cohort is unavailable here. The private `intentapprove` adapter is still a stub, so no 500 × 25 traces for seeds 17, 23, or 41 are claimed.
- Linux checks, optional runtime evidence, shared frozen v1.3 IDs/models, and broader post-CAS durable record reconciliation remain open closure gates. No live provider or account access was used.
