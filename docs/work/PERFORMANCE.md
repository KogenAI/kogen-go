# Offline performance collection

`tools/perf-offline.py` captures reproducible, offline measurements for the Go,
Rust and Bun implementations. It writes one JSONL schema for all three arms and
never makes a live provider request. The fake pipeline uses the frozen suite's
loopback fake server.

## Inputs and pinned identities

Create a manifest from clean source revisions and the frozen inputs:

```sh
export PATH="$HOME/.local/share/mise/installs/git/2.54.0/bin:$HOME/.local/share/mise/installs/go/1.27.1/bin:$HOME/.local/share/mise/installs/python/3.14.7/bin:$PATH"
python3 tools/perf-offline.py snapshot \
  --go-root "$PWD" \
  --rust-root "$HOME/Areas/Kogen/kogen-rs" \
  --bun-root "$HOME/Areas/Kogen/kogen-ts" \
  --suite "$HOME/cx/kgo/inputs/conformance-v1.2" \
  --spec "$HOME/Areas/Kogen/kogen-spec" \
  --output "$HOME/cx/kgo/evidence/71-offline-performance-collector/manifest.json"
```

The manifest binds OS/version, architecture, logical CPU and memory, executable
paths and versions for Python/Git/Go/Rust/Bun, source revisions, language tool
pins, the v1.2 suite revision/runner hash, the target spec revision/tree digest,
and the 50-Intent/200-run workload. The collector refuses uncommitted application
changes. It records this package's uncommitted owned files separately when a
manifest is generated before the package commit.

## Measurement protocol

Run the collector once per selected source/spec/suite revision:

```sh
python3 tools/perf-offline.py collect \
  --manifest "$HOME/cx/kgo/evidence/71-offline-performance-collector/manifest.json" \
  --output "$HOME/cx/kgo/evidence/71-offline-performance-collector/measurements.jsonl" \
  --work-root "$HOME/cx/kgo/evidence/71-offline-performance-collector/work" \
  --pipeline-case v1.2-37-build-02 --repetitions 3 --jobs 2
```

The three source trees are read-only inputs. The script makes detached shared
object clones under the retained evidence work directory; all build products,
compiler caches, logs, status fixtures and conformance results stay there.
Go uses `make build`, Rust uses an offline locked release build, and Bun uses
`make build-foundation`. Each build is run cold and warm against the same private
build cache. `make check` is run cold and warm in a separate checkout and cache.
Cold means empty repository build outputs and private compiler cache. The
preinstalled compiler/toolchain and OS file cache are not flushed, so this is a
build-cache comparison, not a hardware-cache claim. Network fetches are disabled
for Go and Cargo; Bun uses its already provisioned dependencies and no-install
commands.

The public status workload is frozen case `state-30`, which creates 50 Intents
and 200 run records, then invokes public `kogen status` twice. It runs three
times by default per language. `wall_ms` is the mean measured wall time of the
two public status invocations; `case_wall_ms` includes fixture setup and the
whole conformance case. The fake full-pipeline workload defaults to effective
overlay case `v1.2-37-build-02` (Intent → approval → queue → fake
planner/builder → gate → landing). The suite marks this fixture `fake: true`
and directs provider URLs to its local fake server. Login cases are rejected.
This is an offline diagnostic; it does not create a live provider cell or
establish v1.3 behavior acceptance.

For status and pipeline cases, a small external shim measures each public
`kogen` invocation. It captures stdout/stderr to temporary files and forwards
the exact bytes after process exit; timing and resource data are written only to
the collector's separate JSONL. The frozen case assertions compare the public
output. The row records harness wall time, case wall time, summed CLI user/system
CPU, maximum RSS of an individual CLI process, and the CLI binary SHA-256 and
size. Build/check rows record command wall/CPU/RSS and output hashes. `wait4`
resource data is identified in every row; pipeline CPU is the sum of public CLI
invocations and pipeline RSS is the maximum single CLI invocation, not the fake
server's RSS.

Every measurement row uses the same fields: schema version, language and source
revision, toolchain pin, host/spec/suite identity, phase, cache state, workload,
provider mode, binary digest/size, status, wall/CPU/RSS, exit status and
stdout/stderr digests. Null means unavailable or not applicable. A non-pass or
unavailable arm remains in the file with its exact case status; it is never
treated as a passing measurement. Logs and failed case workdirs are retained.

## Interpretation

`state-30` is the current historical timing anchor. Keep `cli-20` and
`v1.2-01-fixed-cli-help-and-grok` in the frozen acceptance run; they are
compatibility checks, not performance samples. A runner pass records that the
case assertions passed; it does not by itself prove the implementation
consumed every fixture record. Review workload coverage before using a row as a
performance cell.

The three implementations can be compared descriptively only when the host,
spec content, suite, source revisions, binaries, and tool recipes are recorded
for every arm. The current frozen v1.2 suite is diagnostic for the v1.3-draft
target. No shared frozen migrated v1.3 IDs or production replay manifest are
available here, so these measurements cannot admit I8 or support scored
language claims. Do not mix live-provider latency/cost with this offline file.

## Recorded run: 2026-10-08

The final collector output is
`$HOME/cx/kgo/evidence/71-offline-performance-collector/measurements-03-final.jsonl`;
its pinned manifest is `manifest-03-final.json`. The run used Darwin 26.7.1
(Darwin 25.6.0), arm64, 10 logical CPUs, 32 GiB RAM; Git 2.54.0, Go 1.27.1,
Rust 1.97.1, Bun 1.4.2, Node 24.21.0 and Python 3.14.7. The Go, Rust and Bun
source revisions were `117a61dd986346da76f4cc09e65de76684dac01e`,
`a402540b39cedc7f788472297add7ae2f8a6631a` and
`3145004a305d0db3c71ce326ab1071f4b526ae88`, respectively. The target spec was
`e19dd1c21c19c5be1201c3b6a42c59c28b5c2887`; its working tree was dirty and is
bound by the manifest's content digest. The v1.2 export was bound by suite
revision `0f93bad988fb8d7a8eff4e94954d1db0a046c89d`, input digest
`527c68f52015b2f1628169c6c22dde992aa4c7642db4c974218b337b3db22b89` and runner
digest `0b53a3b60956cbe8dde7af54e274c46b3e8d430c45662d7dff67445f440b39f9`.

| Language | Cold compile | Warm compile | Cold check | Warm check | Binary size and SHA-256 |
| --- | --- | --- | --- | --- | --- |
| Go | pass, 11,342 ms | pass, 454 ms | pass, 416,700 ms | fail, 322,016 ms | 9,958,626 B, `18069e1873da6ec5195044bbfd1e503864cec19c3caac84ba65e96281eb89fbc` |
| Rust | fail, 287 ms | fail, 58 ms | fail, 573 ms | fail, 507 ms | unavailable |
| Bun | pass, 683 ms | pass, 503 ms | pass, 76,536 ms | pass, 72,932 ms | 62,623,218 B, `b22718cfbbf8b5e8a516df27e685d8dbe2f15c94492eca961a7d506685c379d8` |

All four Rust build/check commands stopped offline because the local Cargo
cache had no `jsonwebtoken` package. Go's warm check had two failures in
`internal/app` (`TestQueueInvalidApprovalDoesNotCallTheAgent` and
`TestRealApprovalCardPublishStatusAndDraftRemoval`): each reported that it
could not construct the approval child environment. The cold Go check passed.
The required check in the source worktree is reported separately in the
package evidence note.

The collector ran `state-30` three times per available binary. Go's public
status invocation means were 838, 484 and 480 ms; Bun's were 2,920, 2,372 and
2,413 ms. The case passed each time. These are runner results on a fixture that
creates 50 Intents and 200 run records. Go's current public status handler only
joins Intent and approval data; its source comment says run records and live
queue ownership are added by the Build integration round. Therefore the Go
numbers do not demonstrate work over the 200 run records and are excluded from
any 50/200 comparison. The JSONL preserves the case pass and measurement values
separately from this interpretation.

The effective fake Build case was `v1.2-37-build-02`. Go recorded 13,564 ms,
2,496 ms user CPU, 4,215 ms system CPU, 20,791,296 B peak CLI RSS and the Go
binary above. Bun recorded 36,874 ms, 1,465 ms user CPU, 1,686 ms system CPU,
37,355,520 B peak CLI RSS and the Bun binary above. Both cases were
`incompatible`, each made two public CLI calls, and neither reached the local
fake provider (zero fake requests). Go stopped at queue start with “no run
found for greet”; Bun reported `environment/command_unavailable: queue start`.
Rust had no binary and was unavailable. These are not successful pipeline
measurements.

Another repository check/conformance job was active on the host during the
collection. Per-process CPU and RSS remain recorded, but wall time is subject
to that host contention and should not be used for precise cross-language
ranking. Earlier collector attempts are retained alongside this output as
`measurements-01-initial.jsonl` and `measurements-02-corrected.jsonl`; neither
is substituted for this final run.
