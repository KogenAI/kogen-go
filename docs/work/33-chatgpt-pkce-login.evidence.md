# 33-chatgpt-pkce-login evidence

## Revisions and scope

- Worker branch: `kgo/33-chatgpt-pkce-login`.
- OAuth component source commit: `73ad4958433c9a0626c027f147e2c0b4cc156ba2` (`Implement ChatGPT OAuth PKCE login`).
- The conformance `bin/kogen` was built from the tested worktree whose OAuth sources match commit `73ad4958433c9a0626c027f147e2c0b4cc156ba2`. Its unchanged `cmd/kogen` entrypoint is from base `b9e79d2e9ffd27d3550c08fc64c6f750d9dfb7f2` and remains the implementation bootstrap with no provider routes. OAuth adapter revision: not present; route wiring belongs to I2/I3.
- Target spec: `kogen-spec` v1.3-draft `e19dd1c21c19c5be1201c3b6a42c59c28b5c2887`. Relevant clauses read: §1.7.6, §4.6 and §4.8.1. `CHANGES-v1.3.md` has no OAuth-specific delta. Rust references read: `provider/auth/oauth.rs`, `provider/auth/callback.rs`, and `provider/auth/local.rs`.
- Dependencies consumed: `internal/auth/jwt` discovery, JWKS, scopes and identity verification; `internal/auth/vault` credential type; `contract.ProcessRunner` with `process.Supervisor` as the browser-launch default.
- The working tree contained only the owned OAuth package and this package's evidence/gate deliverables. No live provider or account was used.

## Implemented component behavior

- Discovers the configured OpenID issuer and validates the issuer and advertised endpoints. The fake-auth environment seam is `KOGEN_AUTH_URL`; insecure HTTP is restricted to loopback.
- Starts with `dynamic_agent_client` and `agent_name_hint=Kogen`, or uses the saved client ID. A denied saved-client callback retries once with the dynamic client.
- Requests the exact ordered scope `openid profile email offline_access resource.invoke chatgpt.tokens.use.direct` and resource `https://api.openai.com/v1`.
- Generates independent random state, nonce and PKCE verifier; sends the S256 challenge; validates callback state before exchanging the code.
- Binds only `127.0.0.1:1455` by default, sets `SO_REUSEADDR` before bind on Darwin/Linux, and responds with the specified success/refusal pages. Tests perform two successful logins sequentially on port 1455.
- Writes and flushes `Continue with ChatGPT` and the authorization URL before invoking the browser. Browser open defaults to `open` on macOS and `xdg-open` on Linux through `process.Supervisor`, with a private temporary working/log directory and an explicit allowlisted environment.
- Bounds token responses, exchanges the authorization code with the same redirect URI, verifier, client ID and resource, requires the direct API scope, verifies RS256/issuer/audience/expiration/nonce/subject through `internal/auth/jwt`, and rejects a changed existing subject. It returns a verified `vault.Credential`; account/profile persistence remains for the route.
- `Revoke` posts the refresh token, `token_type_hint=refresh_token` and client ID to the discovered revocation endpoint. It returns `true` only for a successful HTTP response; unavailable/rejected revocation is reported to the caller.

## Commands and results

All Go/Git/Python commands used the pinned worker PATH. Fake OAuth tests, `make check` and the v1.2 command were serialized with the owned `mkdir` lock `$HOME/cx/kgo/gates.lock/port1455`; that lock was released after each command.

1. Initial package test compile attempt, retained here as a failed run:

   ```sh
   GOMAXPROCS=2 go test -count=1 -parallel=2 ./internal/auth/oauth
   ```

   Failed before tests ran: unused `internal/process` import and callback setup called without its context argument. Both compile issues were corrected in commit `73ad495`; the failure was not an oracle run.

2. Package fixtures after correction:

   ```sh
   GOMAXPROCS=2 go test -count=1 -parallel=2 ./internal/auth/oauth
   ```

   **Pass.** Includes callback-state refusal with no token request, denied saved-client fallback to the dynamic registration hint, two sequential successful logins on port 1455, exact resource/scope/PKCE exchange, subject consistency and confirmed revoke.

3. Required repository check:

   ```sh
   GIT_CONFIG_GLOBAL=/dev/null GOMAXPROCS=2 make check
   ```

   **Pass.** Format, vendor fingerprints, `go vet`, all Go tests, and both command builds passed. This is component evidence; it does not wire or accept the public login route.

4. Required frozen v1.2 command (the failed result is retained and was not rerun):

   ```sh
   make build
   SUITE="$HOME/cx/kgo/inputs/conformance-v1.2"
   EVIDENCE="$HOME/cx/kgo/evidence/33-chatgpt-pkce-login"
   mkdir -p "$EVIDENCE"
   PYTHONDONTWRITEBYTECODE=1 PATH="$HOME/.local/share/mise/installs/python/3.14.7/bin:$PATH" \
     "$SUITE/bin/kogen-conformance" run --kogen "$PWD/bin/kogen" \
     --profile cli,state,approval,shape,build,ladder,provider,custody,format,v1.2 --case 'v1.2-32-provider-21-login-flow' \
     --jobs 2 --time-scale 0.02 --workdir "$EVIDENCE/work" \
     --out "$EVIDENCE/results.jsonl"
   ```

   **Fail, 0/1 instances passed.** Literal resolved case: `v1.2-32-provider-21-login-flow`, one expanded instance, replacement for `provider-21`. The failure occurs at step 1 (`kogen provider list`) before login: exit 2 instead of 0; expected stdout `chatgpt: not signed in\ngrok: not signed in\n`, got empty stdout; actual stderr was `kogen: implementation bootstrap; command routes are not wired\n`. Full runner record: `/Users/almirsarajcic/cx/kgo/evidence/33-chatgpt-pkce-login/results.jsonl`.

## Acceptance and closure

- Compatible component evidence: OAuth fixtures and `make check` pass on macOS 26.7.1 arm64. No v1.2 behavior pass is claimed; the sole assigned case failed because the public CLI route is not wired.
- I2 closure: wire provider list/login/logout through the real account route, use the fake OIDC flow and vault, and retain a new selected-case results file. I3 closure: exercise the public provider login/logout command path as part of the full CLI gate. Do not count this component receipt as behavior acceptance.
- Linux callback reuse was not run on this macOS worker. No live provider test was run. Race verification was not requested/run.
- No OAuth Quint `R(slice)` was assigned or run. I7 still requires a scratch copy of the coherent migrated cohort, `spec`, then 500 traces ×25 steps for seeds 17, 23 and 41, and conforming the same-revision private binary with full observations. The coherent migrated v1.3 suite and shared frozen D-* IDs are not available here; no planned D-* fixture is claimed as a conformance case.
- Package 00 remains an active foundation task. This worker did not change the source oracle, goldens, replay harness, CLI entrypoint, module files or other worktrees.

## Exact conflict record

The only observed case conflict is the bootstrap/route failure quoted above for `v1.2-32-provider-21-login-flow`. It is an unwired-handler gap, not an OAuth protocol incompatibility with the v1.2 assertion. No OAuth-specific v1.3 draft conflict was identified in `CHANGES-v1.3.md`.
