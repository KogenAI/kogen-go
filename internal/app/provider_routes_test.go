package app

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"kogen-go/internal/auth/accounts"
	"kogen-go/internal/auth/oauth"
	"kogen-go/internal/auth/vault"
	"kogen-go/internal/cli/parse"
	"kogen-go/internal/contract"
	"kogen-go/internal/process"
)

func TestProviderListUseLoginAndLogoutThroughFakeOIDC(t *testing.T) {
	home := t.TempDir()
	fixture := newProviderOIDCFixture(t)
	browser := providerBrowser{t: t, client: fixture.server.Client()}
	route := providerRoute{
		home: home,
		env: process.Environment{
			"HOME": home, "KOGEN_AUTH_URL": fixture.server.URL,
			"KOGEN_CREDENTIAL_STORE": "file", "KOGEN_TIME_SCALE": "0.01",
		},
		out: &strings.Builder{}, errOut: &strings.Builder{}, processes: browser,
	}

	if status := route.run(context.Background(), parse.Command{Route: parse.RouteProviderList}); status != 0 {
		t.Fatalf("empty provider list status = %d; output %q", status, route.out)
	}
	if got := route.out.(*strings.Builder).String(); got != "chatgpt: not signed in\ngrok: not signed in\n" {
		t.Fatalf("empty provider list = %q", got)
	}
	if _, err := os.Lstat(filepath.Join(home, ".kogen")); !os.IsNotExist(err) {
		t.Fatalf("provider list created local account state: %v", err)
	}

	route.out = &strings.Builder{}
	login := parse.Command{Route: parse.RouteProviderLogin, Provider: string(accounts.ChatGPT)}
	if status := route.run(context.Background(), login); status != 0 {
		t.Fatalf("fake OIDC login status = %d; output %q", status, route.out)
	}
	if got := route.out.(*strings.Builder).String(); !strings.Contains(got, "Continue with ChatGPT\n") ||
		!strings.Contains(got, accounts.ChatGPTPlanNotice()) ||
		!strings.Contains(got, "chatgpt:default signed in (provider@example.test)\n") {
		t.Fatalf("login output did not include authorization and account result: %q", got)
	}
	if fixture.tokenRequests != 1 {
		t.Fatalf("OIDC token exchange requests = %d, want one", fixture.tokenRequests)
	}
	credentialPath := filepath.Join(home, ".kogen", "credentials", "chatgpt-default.json")
	info, err := os.Stat(credentialPath)
	if err != nil {
		t.Fatalf("saved file credential: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("credential file mode = %o, want 0600", info.Mode().Perm())
	}

	route.out = &strings.Builder{}
	label := providerDefaultLabel
	use := parse.Command{Route: parse.RouteProviderUse, Provider: string(accounts.ChatGPT), Label: &label}
	if status := route.run(context.Background(), use); status != 0 {
		t.Fatalf("provider use status = %d; output %q", status, route.out)
	}
	if got := route.out.(*strings.Builder).String(); got != "chatgpt:default is the default account\n" {
		t.Fatalf("provider use output = %q", got)
	}

	route.out = &strings.Builder{}
	if status := route.run(context.Background(), parse.Command{Route: parse.RouteProviderList}); status != 0 {
		t.Fatalf("provider list status = %d; output %q", status, route.out)
	}
	if got := route.out.(*strings.Builder).String(); !strings.Contains(got, "chatgpt:default (default) signed in provider@example.test expires=") {
		t.Fatalf("provider list did not reflect saved login and selection: %q", got)
	}

	route.out = &strings.Builder{}
	logout := parse.Command{Route: parse.RouteProviderLogout, Provider: string(accounts.ChatGPT)}
	if status := route.run(context.Background(), logout); status != 0 {
		t.Fatalf("provider logout status = %d; output %q", status, route.out)
	}
	if got := route.out.(*strings.Builder).String(); got != "chatgpt:default signed out\n" {
		t.Fatalf("provider logout output = %q", got)
	}
	if fixture.revokeRequests != 1 {
		t.Fatalf("OIDC revocation requests = %d, want one", fixture.revokeRequests)
	}
	credentials, err := vault.Open(home)
	if err != nil {
		t.Fatal(err)
	}
	defer credentials.Close()
	if _, found, err := credentials.GetChatGPT(providerDefaultLabel); err != nil || found {
		t.Fatalf("credential after logout: found=%t err=%v", found, err)
	}
	profiles, err := credentials.ReadProfiles()
	if err != nil {
		t.Fatal(err)
	}
	profile := profiles.ChatGPT[providerDefaultLabel]
	if profile.SignedIn || !profile.RemoteRevoked {
		t.Fatalf("logout profile = %#v", profile)
	}

	route.out = &strings.Builder{}
	if status := route.run(context.Background(), parse.Command{Route: parse.RouteProviderList}); status != 0 {
		t.Fatalf("signed-out list status = %d; output %q", status, route.out)
	}
	if got := route.out.(*strings.Builder).String(); !strings.Contains(got, "chatgpt:default (default) signed out provider@example.test") {
		t.Fatalf("provider list did not reflect logout: %q", got)
	}
}

type providerOIDCFixture struct {
	t              *testing.T
	server         *httptest.Server
	key            *rsa.PrivateKey
	code           string
	challenge      string
	clientID       string
	redirect       string
	nonce          string
	tokenRequests  int
	revokeRequests int
}

func newProviderOIDCFixture(t *testing.T) *providerOIDCFixture {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	fixture := &providerOIDCFixture{t: t, key: key}
	fixture.server = httptest.NewServer(http.HandlerFunc(fixture.serveHTTP))
	t.Cleanup(fixture.server.Close)
	return fixture
}

func (fixture *providerOIDCFixture) serveHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/.well-known/openid-configuration":
		_ = json.NewEncoder(w).Encode(map[string]string{
			"issuer":                 "https://auth.openai.com",
			"authorization_endpoint": fixture.server.URL + "/api/accounts/authorize",
			"token_endpoint":         fixture.server.URL + "/api/accounts/oauth/token",
			"jwks_uri":               fixture.server.URL + "/jwks",
			"revocation_endpoint":    fixture.server.URL + "/revoke",
		})
	case "/api/accounts/authorize":
		query := r.URL.Query()
		fixture.clientID = query.Get("client_id")
		fixture.redirect = query.Get("redirect_uri")
		fixture.challenge = query.Get("code_challenge")
		fixture.nonce = query.Get("nonce")
		fixture.code = "fake-code"
		callback, err := url.Parse(fixture.redirect)
		if err != nil || query.Get("state") == "" || fixture.challenge == "" || fixture.nonce == "" ||
			query.Get("scope") != oauth.Scope || query.Get("resource") != oauth.Resource {
			fixture.t.Errorf("incomplete authorization request: %v", query)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		values := callback.Query()
		values.Set("code", fixture.code)
		values.Set("state", query.Get("state"))
		callback.RawQuery = values.Encode()
		http.Redirect(w, r, callback.String(), http.StatusFound)
	case "/api/accounts/oauth/token":
		if err := r.ParseForm(); err != nil {
			fixture.t.Errorf("parse token exchange: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		fixture.tokenRequests++
		verifierHash := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
		if r.Form.Get("grant_type") != "authorization_code" || r.Form.Get("code") != fixture.code ||
			r.Form.Get("client_id") != fixture.clientID || r.Form.Get("redirect_uri") != fixture.redirect ||
			r.Form.Get("resource") != oauth.Resource ||
			base64.RawURLEncoding.EncodeToString(verifierHash[:]) != fixture.challenge {
			fixture.t.Errorf("token exchange did not match PKCE request: %v", r.Form)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		idToken := fixture.signedIDToken()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "fake-access-token", "refresh_token": "fake-refresh-token",
			"id_token": idToken, "expires_in": 3600, "scope": oauth.Scope,
		})
	case "/jwks":
		exponent := big.NewInt(int64(fixture.key.E)).Bytes()
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []any{map[string]string{
			"kty": "RSA", "kid": "provider-route-key", "alg": "RS256", "use": "sig",
			"n": base64.RawURLEncoding.EncodeToString(fixture.key.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(exponent),
		}}})
	case "/revoke":
		if err := r.ParseForm(); err != nil || r.Form.Get("token") != "fake-refresh-token" ||
			r.Form.Get("token_type_hint") != "refresh_token" || r.Form.Get("client_id") != fixture.clientID {
			fixture.t.Errorf("unexpected token revocation request: %v", r.Form)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		fixture.revokeRequests++
		w.WriteHeader(http.StatusOK)
	default:
		http.NotFound(w, r)
	}
}

func (fixture *providerOIDCFixture) signedIDToken() string {
	header, _ := json.Marshal(map[string]string{"alg": "RS256", "kid": "provider-route-key", "typ": "JWT"})
	claims, _ := json.Marshal(map[string]any{
		"iss": "https://auth.openai.com", "aud": []string{fixture.clientID},
		"exp": time.Now().Add(time.Hour).Unix(), "nonce": fixture.nonce,
		"sub": "provider-route-subject", "email": "provider@example.test",
	})
	encoded := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(claims)
	digest := sha256.Sum256([]byte(encoded))
	signature, err := rsa.SignPKCS1v15(rand.Reader, fixture.key, crypto.SHA256, digest[:])
	if err != nil {
		fixture.t.Fatal(err)
	}
	return encoded + "." + base64.RawURLEncoding.EncodeToString(signature)
}

type providerBrowser struct {
	t      *testing.T
	client *http.Client
}

func (browser providerBrowser) Run(ctx context.Context, spec contract.ProcessSpec) (contract.ProcessResult, error) {
	if len(spec.Args) != 1 {
		return contract.ProcessResult{}, fmt.Errorf("fake browser expected one URL argument, got %d", len(spec.Args))
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, spec.Args[0], nil)
	if err != nil {
		return contract.ProcessResult{}, err
	}
	response, err := browser.client.Do(request)
	if err != nil {
		return contract.ProcessResult{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		browser.t.Errorf("fake browser callback status = %d", response.StatusCode)
	}
	status := 0
	return contract.ProcessResult{ExitStatus: &status}, nil
}

var _ contract.ProcessRunner = providerBrowser{}
