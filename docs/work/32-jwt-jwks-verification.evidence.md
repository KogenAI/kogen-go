# 32-jwt-jwks-verification evidence

## Revision and boundary

- Worker branch: `kgo/32-jwt-jwks-verification`.
- JWT component source commit: `28b88b5fe1dac8cd3ef999537643e3a1a828765a` (`Implement JWT and JWKS verification`).
- CLI binary was built from that worker revision. The application route/adapter remains the foundation bootstrap from base `43cda413006202ae80d6327cce4bee19409fe328`; `internal/app.Bootstrap` still reports `implementation bootstrap; command routes are not wired`. No OAuth production adapter is present in this worktree yet.
- Target specification: `kogen-spec` v1.3-draft commit `e19dd1c21c19c5be1201c3b6a42c59c28b5c2887`, content manifest SHA-256 `52388b2be73aade4b4fb2535cef475baefae1d3f662c1f2221afed737154468f`. Relevant authority: `spec/04-provider.md` §4.6 steps 1 and 4–5; the matching `CHANGES-v1.3.md` SHA-256 is `848b40d20be1900494302d318f4b6b106daa37279d4796212a75ec5242b99840` and has no JWT-specific delta.
- Read-only Rust reference: `kogen-rs` commit `a402540b39cedc7f788472297add7ae2f8a6631a`; inspected `crates/kogen-core/src/provider/auth/jwt.rs` and the discovery, scope and subject-consistency paths in `provider/auth/oauth.rs` and `provider/auth/refresh.rs`.
- Frozen v1.2 oracle: `~/cx/kgo/inputs/conformance-v1.2`, sourced from `kogen-conformance` commit `0f93bad988fb8d7a8eff4e94954d1db0a046c89d`.
- Host/toolchain: macOS 26.7.1, `darwin/arm64`, Go 1.27.1, Git 2.54.0, Python 3.14.7.

## Component delivered

`internal/auth/jwt` now verifies compact RS256 ID tokens with strict duplicate-key JSON parsing, exact issuer and audience checks, future integer `exp`, an optional required nonce, and nonempty `sub`. It chooses exactly one matching `kid` from a bounded JWKS, accepts RSA public signing keys only, rejects weak or oversized RSA moduli and token-provided key URLs, and checks the signature before interpreting payload claims. Optional identity fields preserve absence. Discovery and JWKS GETs have a 20-second deadline, 64 KiB / 1 MiB response limits, endpoint scheme and userinfo checks, and redirect refusal. `RequireScope` validates scope syntax and requires `chatgpt.tokens.use.direct`; `CheckSubjectConsistency` rejects missing or changed subjects for an existing credential.

The component tests cover valid identity extraction, altered signatures, unsupported algorithms and missing `kid`, wrong issuer/audience/expiry/nonce/subject, wrong or ambiguous keys, weak RSA keys, duplicate JSON members, token-supplied key URLs, bounded discovery/JWKS responses, redirects, scope validation and subject changes. HTTP tests use local `httptest` servers and RSA keys generated in the test process.

## Commands and results

| Command | Result |
|---|---|
| `GIT_CONFIG_GLOBAL=/dev/null make check` | Passed on the final source state: format, vendor fingerprint check, `go vet ./...`, `go test -count=1 -parallel=2 ./...`, and both `CGO_ENABLED=0` builds. `GOMAXPROCS=2`, `GOFLAGS=-mod=vendor -p=2`; the JWT package tests passed. |
| `make build` | Passed as the first step of the assigned oracle command. |
| Frozen v1.2 command below | Failed before OAuth/JWT execution because the built CLI's `provider list` route is unwired. This is retained as a behavior failure, not counted as a pass. |
| `git diff --check` | Passed before the source commit. |

The exact assigned command was run once under the shared `oauth-port-1455` mkdir lock, which was released afterward:

```sh
make build
SUITE="$HOME/cx/kgo/inputs/conformance-v1.2"
EVIDENCE="$HOME/cx/kgo/evidence/32-jwt-jwks-verification"
mkdir -p "$EVIDENCE"
PYTHONDONTWRITEBYTECODE=1 PATH="$HOME/.local/share/mise/installs/python/3.14.7/bin:$PATH" \
  "$SUITE/bin/kogen-conformance" run --kogen "$PWD/bin/kogen" \
  --profile cli,state,approval,shape,build,ladder,provider,custody,format,v1.2 --case 'v1.2-32-provider-21-login-flow' \
  --jobs 2 --time-scale 0.02 --workdir "$EVIDENCE/work" \
  --out "$EVIDENCE/results.jsonl"
```

Resolved case: literal ID `v1.2-32-provider-21-login-flow`, profile `v1.2`, 1 case / 1 instance. Runner result: `implemented=1`, `pass=0`, `fail=1`, `error=0`, `skip=0`, `unimpl=0`. The first failure is exactly: `step 1 (run kogen provider list): kogen provider list: exit 2, expected 0`; stderr was `kogen: implementation bootstrap; command routes are not wired`. The fake login flow did not proceed beyond this step. The retained JSONL is `/Users/almirsarajcic/cx/kgo/evidence/32-jwt-jwks-verification/results.jsonl` (SHA-256 `de2ce99663ee1e5defae9d1abd20452e965c25d2459cc091e42f93bfa1da95ce`); its generated work directory remains alongside it.

## Effects, conflicts and closure

- The component tests generated RSA keys in memory and used ephemeral local HTTP test servers. The acceptance runner created its requested fake-provider work directory and JSONL outside the worktree. No live provider, real account, or user credential was accessed. No OAuth callback/JWKS exchange or port-1455 login was reached by the failed case.
- No v1.2-versus-v1.3 semantic conflict was exercised by this case; the failure is caused by the unwired CLI bootstrap. There are no exact historical conflicts to report for this package.
- Accepted evidence is component-only. `I3 OAuth CLI` remains open until the OAuth routes consume this verifier and the case is rerun after integration. The application bootstrap failure is a concrete integration gap; the current JSONL must remain preserved.
- The shared coherent v1.3 Quint/conformance cohort and frozen v1.3 case IDs are unavailable. Planned `D-*` fixtures are not v1.2 cases, so no D gate is claimed. `R(slice)` was not run; it requires the shared cohort copied to scratch, spec checking, 500 traces × 25 steps for seeds 17, 23, and 41, and full-observation conformance against a same-revision private binary.
- Linux, race/custody, optional-runtime, production replay and live provider comparison evidence are unavailable here. No release or behavior acceptance is claimed.
