# Shared Go contracts and effect ports

These signatures are the package-00 handoff for the frozen target identified in
[INPUTS.md](INPUTS.md). They expose observations and effects; they do not
implement command routing, behavior policy, or a replay controller. Package
owners use `internal/contract` and do not define competing shared records.
Interface changes are serialized coordinator changes.

## Layer and ownership rules

- `contract` may depend only on the Go standard library. App composition is the
  only layer that sees all production packages. Lower packages do not import
  `internal/app` or `internal/xspec`.
- Adapters report observed effects. A process exit, Git ref read/CAS, HTTP
  response, check result or filesystem publication is not replaced by a
  caller-supplied “pass” boolean. Replay drives the same production transitions
  with those effect inputs.
- The acceptance adapter returns each check's actual `CheckResult`; the gate
  owns base comparison, excusal, count/rank and landability policy. A
  `VerificationReceipt` binds the checked base tree, candidate tree, approval
  bytes and protected-manifest digest.
- `StateObserver` reports an immutable `StateObservations` snapshot. Status and
  queue packages derive their output from facts; observers do not return a
  precomputed public status or queue ordering.

## Process and Git ports

`ProcessRunner.Run(ctx, ProcessSpec)` is the supervised child-process boundary.
The implementation owns process groups, deadlines, TERM/KILL cleanup, reaping,
bounded logs, and parent-death behavior. A missing executable is recorded as
`Unavailable` (with the spec's 126/127 equivalence); a check's nonzero exit is a
result, not an adapter error.

`GitPort.Exec(ctx, args, stdin, GitPolicy)` is the only controller-facing Git
boundary. It must supervise every Git command, enforce deadlines and output
limits, reject oversized argv elements, and carry commit messages/path lists in
stdin or private files. Production identity and signing come from trusted
origin policy; workspace config cannot select helpers, hooks, filters,
textconv, fsmonitor or identity. Hermetic fixture policy is test-only.

Candidate tree collection is relative to the immutable Build base, regardless
of workspace `HEAD` or index. Tracked base paths stay included if newly ignored;
untracked eligibility comes from native Git ignore rules (including nested
ignore files and negation), with workspace/global excludes disabled. Use a
private index plus Git's `check-ignore --no-index -z --stdin`,
`hash-object --no-filters`, `update-index -z` and `write-tree`; do not replace
Git ignore rules with glob matching.

## Rooted filesystem port

`RootOpener.OpenRoot` returns `RootedFS`; its names are canonical
`io/fs`-style relative paths. The implementation must anchor all operations to
rooted descriptors, prevent parent-symlink races and escapes, reject FIFO/device
opens and hardlink alias writes, and use no-follow metadata for publication
leaves. `Publish` implements explicit replace or create-only behavior, fsyncs
the file and parent directory, and replaces a symlink leaf itself without
opening its target. `Append`, `Rename`, `Remove`, `Symlink` and `SyncDir` are
root-relative operations under the same policy.

Go's `os.Root` can confine relative symlinks, but it is not an OS sandbox and
does not alone enforce the required no-follow leaf or hardlink policy. Use
descriptor operations from the pinned `golang.org/x/sys` where needed. State,
journal, cache, approval staging, tool handles and protection restoration all
use this port; a safe model-file write does not make the other controller writes
safe.

## Provider session and evidence

`ProviderPort.Respond(ctx, *ConversationSession, ProviderRequest)` receives the
same session pointer for every turn, finish guard, validation and style repair
within one conversation. `ConversationSession.History` is ordered and
append-only: each item stores a copied raw JSON value and a kind (`input`, raw
response item, controller message or tool output). Do not rebuild history from
the last response. Passes 1–3 share their Shape session; fallback passes 4–6 use
a fresh thread with the same Shape-run affinity.

`ConversationIdentity` holds persisted opaque protocol identifiers and stage
coordinates; IDs contain no paths or secrets. Build cache affinity stays one
key for the run. Threads are stable per stage/attempt/rung/epoch and separate
between them. Model switches retain run affinity and drop prior-model encrypted
reasoning. `RoutingState.CodexTurnState` retains the opaque
`x-codex-turn-state` value internally for that thread; evidence records only
whether it was present.

`RoleManifest` holds the effective provider/model/effort tuple for each role
after project → machine → default merging. The effective `fallback_shaper`
copies the resolved shaper tuple and cannot be configured independently;
cross-provider model overrides are rejected by the resolver. Every measured
transport request carries its effective role tuple so arm mismatches invalidate
the measurement.

`RequestEvidence` records endpoint host/path, request/response times, body byte
count, static-prefix digests, cache/thread IDs, provider/model/effort, routing
header names, nullable usage and routing-state presence. It never stores
credential/header values or prompt contents. Usage pointers preserve unknown as
null rather than reporting an invented zero. The immutable generic instructions
and ordered union of seven schemas precede role-specific instructions and
variable append-only history; `allowed_tools`/`none` changes call permissions,
not the shared schemas.

`ProviderRequest`, `ProviderResponse`, `SessionItem`, `RoutingState` and
`ConversationSession` diagnostic string forms omit header values, raw bodies,
history contents and opaque routing values. Other logging should follow the
same rule; request metadata records only names, sizes, digests and presence.

## Acceptance, state and landing ports

`AcceptanceAdapter.Run` returns check status, process evidence, parsed finding
identities and before/after tree identities. It does not decide whether a
failure is excused. `VerificationReceipt` carries `BaseTree`, `CandidateTree`,
`ProtectedManifestSHA256`, `ApprovalSHA256`, ordered check results and timing.

`LandingPort` exposes commit-tree creation, actual ref reads, atomic
compare-and-swap and ancestry queries. CAS results include the observed target;
callers re-read/re-verify according to the frozen policy after a lost race.
`StateObserver` receives a `StateQuery` and returns facts from durable state and
ownership probes. It is intentionally distinct from status derivation.

`Clock` and `JitterSource` make waits and random retry delay draws injectable.
They supply time/random observations; retry limits and whether to retry remain
with the owning provider/queue policy.

## Draft decisions not encoded by this scaffold

The unresolved serializer/`tool_choice`, provider terminal, status-schema,
post-CAS reconciliation, and other findings listed in [INPUTS.md](INPUTS.md)
remain shared decisions. These interfaces leave raw wire requests, observed
state and effect results available without selecting one interpretation. The
scaffold does not claim conformance and does not turn an unavailable command or
adapter into a passing case.

The package-49 prompt owner still needs to include the complete required Intent
format example and raw Request preservation, retain the first-user template and
role marker, and exercise rejected `syn-06` `{}` / `syn-20` missing-size inputs
with exact repair feedback. Queue start remains an I3 executable integration
gate: once approval, fake provider, one verified rung and landing exist, prove
approve → queue → provider → gate → CAS → status using those production routes.
