# 56 Rails adapter evidence

## Revisions and environment

- Worker branch: `kgo/56-rails-adapter`.
- Adapter implementation commit: `a8fb7e584b41f1e7d7717f67bb2f6ff30b8e4090`; parent: `46d9ee92071bebbec71c5d221661411a96d9f66a`.
- Target spec: v1.3-draft `e19dd1c21c19c5be1201c3b6a42c59c28b5c2887`; read `spec/02-formats.md` §§2.4–2.5, `spec/CONFORMANCE-v1.2-CASES.md` P12, `CHANGES-v1.3.md`, `WORKER-RULES.md`, `PLAN.md`, and `QUEUE-source.md`.
- Rust reference: `a402540b39cedc7f788472297add7ae2f8a6631a`, `crates/kogen-core/src/gate/adapters/rails.rs`, `rails/findings.rs`, and `rails_tests.rs`.
- Frozen v1.2 suite: `$HOME/cx/kgo/inputs/conformance-v1.2`, conformance revision `0f93bad988fb8d7a8eff4e94954d1db0a046c89d`.
- Host: macOS 26.7.1 arm64, Go `go1.27.1 darwin/arm64`, Git `2.54.0`, Python `3.14.7`, Ruby `3.4.10`, Bundler `2.6.9`. Rails is not installed (`rails -v` reported that Rails is not currently installed).
- CLI binary SHA-256: `8928f70352041c12a479b1f55232784f331919e2185c1a9b371a247695c825fb`. The public CLI still builds from the bootstrap entrypoint and does not link the Rails package; this hash is not evidence of CLI adapter wiring.
- No production provider/account or OAuth port was used.

## Implemented component

`internal/acceptance/rails` now provides the Rails adapter policy: automatic detection requires regular `Gemfile` and `config/application.rb` files, explicit adapter selection overrides detection, and source/candidate paths are `.kogen/acceptance/<slug>_test.rb` and `test/acceptance/<slug>_test.rb`. Slugs are checked before constructing paths.

The package also defines `bundle exec rails test {path}`, `ruby -c {path}`, offline `bundle install --local`, the `vendor/cache` setup seed, and the Rails child environment (`BUNDLE_PATH` and `RAILS_ENV=test`). Formatter selection scans literal Gemfile declarations: `standardrb -a` wins when `standard` is declared; otherwise it selects `rubocop -a` when `rubocop` is declared. The gate list is `Gemfile`, `Gemfile.lock`, `bin/rails`, `.standard.yml`, and `.rubocop.yml`.

Findings parsing covers Minitest failure locations and RuboCop/Standard-style Ruby lint lines. A versioned minimal Rails tree is checked in at `internal/acceptance/rails/testdata/rails`; package tests use it for detection and formatter selection. Its acceptance source and app marker files passed Ruby syntax checks. It has no vendored runtime gems, and the Rails executable is unavailable on this host.

## Commands and results

The worker shell used the pinned PATH from `WORKER-RULES.md` before commands.

| Command | Result |
| --- | --- |
| `GOMAXPROCS=2 go test -p=2 -parallel=2 -count=1 ./internal/acceptance/rails` | Passed. |
| `ruby -c` on the fixture's `bin/rails`, `config/application.rb`, `config/boot.rb`, `config/environment.rb`, `test/test_helper.rb`, and `.kogen/acceptance/greet_test.rb` | Six `Syntax OK` results. This verifies syntax only, not Rails execution. |
| `GIT_CONFIG_GLOBAL=/dev/null make check` | Passed: format, vendor fingerprints, vet, repository tests, and both builds. |
| `make build` | Passed, including the invocation immediately before the assigned conformance command. |
| `git diff --check` | Passed. |

The assigned frozen-oracle command was run once, without changing the suite or its goldens:

```sh
make build
SUITE="$HOME/cx/kgo/inputs/conformance-v1.2"
EVIDENCE="$HOME/cx/kgo/evidence/56-rails-adapter"
mkdir -p "$EVIDENCE"
PYTHONDONTWRITEBYTECODE=1 PATH="$HOME/.local/share/mise/installs/python/3.14.7/bin:$PATH" \
  "$SUITE/bin/kogen-conformance" run --kogen "$PWD/bin/kogen" \
  --profile cli,state,approval,shape,build,ladder,provider,custody,format,v1.2 --case 'v1.2-113-provider-12' \
  --jobs 2 --time-scale 0.02 --workdir "$EVIDENCE/work" \
  --out "$EVIDENCE/results.jsonl"
```

The run resolved literal ID `v1.2-113-provider-12` to one instance. Its title is “v1.2 replacement for provider-12: Backoff sequence: delay_ms is jittered within 1–2 s, 2–4 s, 4–8 s.” It failed at step 2 (`kogen intent approve greet {hash8:greet}`): exit 2 instead of 0, empty stdout, and stderr `kogen: implementation bootstrap; command routes are not wired`. The runner recorded that no provider request reached the fake server. Result: 0/1 passed, 1 failed. The case did not reach its provider assertion and is not a Rails coverage case. The retained JSONL and work directory are `/Users/almirsarajcic/cx/kgo/evidence/56-rails-adapter/results.jsonl` and `/Users/almirsarajcic/cx/kgo/evidence/56-rails-adapter/work/v1.2-113-provider-12/`.

No exact v1.2/v1.3-draft assertion conflict was observed: the selected case stopped before its provider behavior. There are no compatible conformance passes to report, and this run does not assess Rails P12.

## Local effects and remaining closure gates

- The local package tests exercise Rails marker detection, forced adapter override, slug-safe paths, command/environment/seed/gate defaults, formatter precedence, and Minitest/lint findings. They do not call Shape, approve, or Build.
- The requested Shape → approve → Build fixture closure remains an I5 integration gate. In this revision `cmd/kogen` still routes to `app.Bootstrap`, which emitted the exact unwired-route diagnostic above. Do not count the local fixture or the provider case as an end-to-end Rails pass.
- I6 real Rails runtime evidence remains open: Rails and its local gem bundle are absent. No tool was installed and no live network setup was attempted.
- The standard v1.2 suite makes no Rails coverage claim. The exact selected overlay ID is provider-12, not a Rails case. A Rails-specific shared frozen v1.3 case is not available here.
- R(slice) was not run. It still needs the coherent migrated shared Quint cohort copied to scratch, spec first, then 500 traces × 25 steps for each seed 17, 23, and 41 against the same-revision private binary. No source oracle or golden was changed.
- Linux parity, optional-runtime evidence, live comparison, and full shared v1.3 closure remain outside this component result.

This is **component** evidence only. It does not confer behavior acceptance.
