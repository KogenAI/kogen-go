package oauth

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"kogen-go/internal/contract"
)

const fixtureHostID = "urn:uuid:123e4567-e89b-42d3-a456-426614174000"

type oauthFixture struct {
	t               *testing.T
	server          *httptest.Server
	key             *rsa.PrivateKey
	mu              sync.Mutex
	sequence        int
	authorized      map[string]authorizeRequest
	wrongState      bool
	denySavedClient bool
	tokenRequests   int
	revokeRequests  int
	revokedForm     url.Values
}

type authorizeRequest struct {
	clientID  string
	redirect  string
	challenge string
	nonce     string
}

func newOAuthFixture(t *testing.T) *oauthFixture {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	fixture := &oauthFixture{t: t, key: key, authorized: make(map[string]authorizeRequest)}
	fixture.server = httptest.NewServer(http.HandlerFunc(fixture.serveHTTP))
	t.Cleanup(fixture.server.Close)
	return fixture
}

func (fixture *oauthFixture) serveHTTP(writer http.ResponseWriter, request *http.Request) {
	switch request.URL.Path {
	case "/.well-known/openid-configuration":
		_ = json.NewEncoder(writer).Encode(map[string]string{
			"issuer":                 "https://auth.openai.com",
			"authorization_endpoint": fixture.server.URL + "/api/accounts/authorize",
			"token_endpoint":         fixture.server.URL + "/api/accounts/oauth/token",
			"jwks_uri":               fixture.server.URL + "/jwks",
			"revocation_endpoint":    fixture.server.URL + "/revoke",
		})
	case "/api/accounts/authorize":
		fixture.authorize(writer, request)
	case "/api/accounts/oauth/token":
		fixture.token(writer, request)
	case "/jwks":
		_ = json.NewEncoder(writer).Encode(map[string]any{"keys": []any{map[string]string{
			"kty": "RSA", "kid": "fixture-key", "alg": "RS256", "use": "sig",
			"n": base64.RawURLEncoding.EncodeToString(fixture.key.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(fixture.key.E)).Bytes()),
		}}})
	case "/revoke":
		if err := request.ParseForm(); err != nil {
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		fixture.mu.Lock()
		fixture.revokeRequests++
		fixture.revokedForm = request.Form
		fixture.mu.Unlock()
		writer.WriteHeader(http.StatusOK)
	default:
		http.NotFound(writer, request)
	}
}

func (fixture *oauthFixture) authorize(writer http.ResponseWriter, request *http.Request) {
	query := request.URL.Query()
	fixture.mu.Lock()
	fixture.sequence++
	sequence := fixture.sequence
	wrongState := fixture.wrongState
	fixture.mu.Unlock()
	clientID := query.Get("client_id")
	if query.Get("response_type") != "code" || query.Get("ext_agent_host_id") != fixtureHostID ||
		query.Get("redirect_uri") == "" || query.Get("scope") != Scope || query.Get("resource") != Resource ||
		query.Get("state") == "" || query.Get("nonce") == "" || query.Get("code_challenge_method") != "S256" ||
		query.Get("code_challenge") == "" {
		fixture.t.Errorf("authorization query is incomplete or inexact: %v", query)
		writer.WriteHeader(http.StatusBadRequest)
		return
	}
	if clientID == DynamicClientID {
		if query.Get("agent_name_hint") != AgentNameHint {
			fixture.t.Errorf("dynamic authorization registration hint = %q", query.Get("agent_name_hint"))
		}
	} else if query.Get("agent_name_hint") != "" {
		fixture.t.Errorf("saved client authorization included first-use registration hint: %v", query)
	}
	code := fmt.Sprintf("fixture-code-%d", sequence)
	denied := fixture.denySavedClient && clientID == "saved-client-id"
	if !denied {
		fixture.mu.Lock()
		fixture.authorized[code] = authorizeRequest{
			clientID: clientID, redirect: query.Get("redirect_uri"),
			challenge: query.Get("code_challenge"), nonce: query.Get("nonce"),
		}
		fixture.mu.Unlock()
	}
	callbackURL, err := url.Parse(query.Get("redirect_uri"))
	if err != nil {
		fixture.t.Errorf("invalid redirect URI: %v", err)
		writer.WriteHeader(http.StatusBadRequest)
		return
	}
	callbackQuery := callbackURL.Query()
	if denied {
		callbackQuery.Set("error", "access_denied")
	} else {
		callbackQuery.Set("code", code)
	}
	state := query.Get("state")
	if wrongState {
		state = "mismatched-state"
	}
	callbackQuery.Set("state", state)
	callbackURL.RawQuery = callbackQuery.Encode()
	http.Redirect(writer, request, callbackURL.String(), http.StatusFound)
}

func (fixture *oauthFixture) token(writer http.ResponseWriter, request *http.Request) {
	if err := request.ParseForm(); err != nil {
		writer.WriteHeader(http.StatusBadRequest)
		return
	}
	fixture.mu.Lock()
	fixture.tokenRequests++
	authorized, found := fixture.authorized[request.Form.Get("code")]
	fixture.mu.Unlock()
	verifier := request.Form.Get("code_verifier")
	challenge := sha256.Sum256([]byte(verifier))
	if !found || request.Form.Get("grant_type") != "authorization_code" ||
		request.Form.Get("client_id") != authorized.clientID || request.Form.Get("redirect_uri") != authorized.redirect ||
		request.Form.Get("resource") != Resource || base64.RawURLEncoding.EncodeToString(challenge[:]) != authorized.challenge {
		fixture.t.Errorf("token exchange form did not match authorization request: %v", request.Form)
		writer.WriteHeader(http.StatusBadRequest)
		return
	}
	now := time.Now()
	identityToken := fixture.signToken(map[string]any{
		"iss": "https://auth.openai.com", "aud": []string{authorized.clientID}, "exp": now.Add(time.Hour).Unix(),
		"nonce": authorized.nonce, "sub": "fixture-subject", "email": "person@example.test",
	})
	_ = json.NewEncoder(writer).Encode(map[string]any{
		"access_token": "fixture-access-token", "refresh_token": "fixture-refresh-token",
		"id_token": identityToken, "expires_in": 3600, "scope": Scope,
	})
}

func (fixture *oauthFixture) signToken(claims map[string]any) string {
	fixture.t.Helper()
	encode := func(value any) []byte {
		data, err := json.Marshal(value)
		if err != nil {
			fixture.t.Fatal(err)
		}
		return data
	}
	header := base64.RawURLEncoding.EncodeToString(encode(map[string]string{"alg": "RS256", "kid": "fixture-key", "typ": "JWT"}))
	payload := base64.RawURLEncoding.EncodeToString(encode(claims))
	unsigned := header + "." + payload
	digest := sha256.Sum256([]byte(unsigned))
	signature, err := rsa.SignPKCS1v15(rand.Reader, fixture.key, crypto.SHA256, digest[:])
	if err != nil {
		fixture.t.Fatal(err)
	}
	return unsigned + "." + base64.RawURLEncoding.EncodeToString(signature)
}

type fixtureBrowser struct {
	t      *testing.T
	client *http.Client
	bodies []string
	specs  []contract.ProcessSpec
}

func (browser *fixtureBrowser) Run(ctx context.Context, spec contract.ProcessSpec) (contract.ProcessResult, error) {
	browser.t.Helper()
	if spec.Executable != "fixture-browser" || len(spec.Args) != 1 || spec.Timeout != 20*time.Second {
		browser.t.Errorf("browser did not use expected supervised process spec: %#v", spec)
	}
	if !filepath.IsAbs(spec.Dir) || filepath.Dir(spec.LogPath) != spec.Dir {
		browser.t.Errorf("browser supervisor paths are not private and absolute: %#v", spec)
	}
	info, err := os.Stat(spec.Dir)
	if err != nil || info.Mode().Perm() != 0o700 {
		browser.t.Errorf("browser supervisor directory mode = %v, %v", info, err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, spec.Args[0], nil)
	if err != nil {
		browser.t.Fatal(err)
	}
	response, err := browser.client.Do(request)
	if err != nil {
		browser.t.Fatalf("browser authorization navigation failed: %v", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		browser.t.Fatal(err)
	}
	browser.bodies = append(browser.bodies, string(body))
	browser.specs = append(browser.specs, spec)
	status := 0
	return contract.ProcessResult{ExitStatus: &status}, nil
}

func optionsForFixture(fixture *oauthFixture, browser *fixtureBrowser, output io.Writer) Options {
	return Options{
		HTTPClient:         fixture.server.Client(),
		AuthBaseURL:        fixture.server.URL,
		AllowHTTP:          true,
		HostID:             fixtureHostID,
		CallbackPort:       CallbackPort,
		CallbackTimeout:    5 * time.Second,
		Output:             output,
		ProcessRunner:      browser,
		BrowserExecutable:  "fixture-browser",
		BrowserEnvironment: []string{"PATH=/usr/bin:/bin"},
	}
}

func TestSequentialLoginReusesCallbackPortAndRevokes(t *testing.T) {
	fixture := newOAuthFixture(t)
	browser := &fixtureBrowser{t: t, client: fixture.server.Client()}
	var firstOutput, secondOutput bytes.Buffer
	first, err := Login(context.Background(), optionsForFixture(fixture, browser, &firstOutput))
	if err != nil {
		t.Fatalf("first Login() error = %v", err)
	}
	if first.Credential.ClientID != DynamicClientID || first.Identity.Subject != "fixture-subject" ||
		first.Credential.HostID != fixtureHostID || first.Credential.ExpiresAt <= time.Now().Unix() {
		t.Fatalf("first login result is incomplete: %#v", first)
	}
	if !strings.HasPrefix(firstOutput.String(), "Continue with ChatGPT\n") || !strings.Contains(firstOutput.String(), fixture.server.URL+"/api/accounts/authorize?") {
		t.Fatalf("authorization URL was not printed immediately: %q", firstOutput.String())
	}
	if len(browser.bodies) != 1 || !strings.Contains(browser.bodies[0], "Kogen sign-in complete. You can close this tab.") {
		t.Fatalf("first callback page = %q", browser.bodies)
	}

	secondOptions := optionsForFixture(fixture, browser, &secondOutput)
	secondOptions.PreviousClientID = "saved-client-id"
	secondOptions.PreviousSubject = first.Identity.Subject
	second, err := Login(context.Background(), secondOptions)
	if err != nil {
		t.Fatalf("sequential Login() error = %v", err)
	}
	if second.Credential.ClientID != "saved-client-id" || second.Identity.Subject != first.Identity.Subject {
		t.Fatalf("sequential login result = %#v", second)
	}
	if len(browser.bodies) != 2 || !strings.Contains(browser.bodies[1], "Kogen sign-in complete. You can close this tab.") {
		t.Fatalf("second callback page = %q", browser.bodies)
	}
	if !strings.Contains(secondOutput.String(), "Continue with ChatGPT\n") {
		t.Fatalf("second authorization URL was not printed: %q", secondOutput.String())
	}

	revoked, err := Revoke(context.Background(), secondOptions, second.Credential)
	if err != nil || !revoked {
		t.Fatalf("Revoke() = %t, %v; want confirmed", revoked, err)
	}
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if fixture.sequence != 2 || fixture.tokenRequests != 2 || fixture.revokeRequests != 1 ||
		fixture.revokedForm.Get("token") != "fixture-refresh-token" ||
		fixture.revokedForm.Get("token_type_hint") != "refresh_token" ||
		fixture.revokedForm.Get("client_id") != "saved-client-id" {
		t.Fatalf("fake OAuth request counts/forms: sequence=%d token=%d revoke=%d form=%v", fixture.sequence, fixture.tokenRequests, fixture.revokeRequests, fixture.revokedForm)
	}
}

func TestLoginRefusesCallbackStateMismatch(t *testing.T) {
	fixture := newOAuthFixture(t)
	fixture.wrongState = true
	browser := &fixtureBrowser{t: t, client: fixture.server.Client()}
	var output bytes.Buffer
	_, err := Login(context.Background(), optionsForFixture(fixture, browser, &output))
	if !errors.Is(err, ErrStateMismatch) {
		t.Fatalf("Login() error = %v, want ErrStateMismatch", err)
	}
	if len(browser.bodies) != 1 || !strings.Contains(browser.bodies[0], "Kogen could not verify this sign-in callback.") {
		t.Fatalf("refused callback response = %q", browser.bodies)
	}
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if fixture.tokenRequests != 0 {
		t.Fatalf("state mismatch still exchanged a token (%d requests)", fixture.tokenRequests)
	}
}

func TestRejectedSavedClientRetriesWithDynamicRegistrationHint(t *testing.T) {
	fixture := newOAuthFixture(t)
	fixture.denySavedClient = true
	browser := &fixtureBrowser{t: t, client: fixture.server.Client()}
	var output bytes.Buffer
	options := optionsForFixture(fixture, browser, &output)
	options.PreviousClientID = "saved-client-id"
	options.PreviousSubject = "fixture-subject"
	result, err := Login(context.Background(), options)
	if err != nil {
		t.Fatalf("Login() after saved-client refusal = %v", err)
	}
	if result.Credential.ClientID != DynamicClientID || len(browser.bodies) != 2 ||
		!strings.Contains(browser.bodies[0], "Kogen could not verify this sign-in callback.") ||
		!strings.Contains(browser.bodies[1], "Kogen sign-in complete. You can close this tab.") {
		t.Fatalf("saved-client fallback result/pages = %#v / %q", result, browser.bodies)
	}
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if fixture.sequence != 2 || fixture.tokenRequests != 1 || !strings.Contains(output.String(), "agent_name_hint=Kogen") {
		t.Fatalf("fallback registrations/tokens/output = %d/%d/%q", fixture.sequence, fixture.tokenRequests, output.String())
	}
}

func TestLoginRefusesSubjectChangeForExistingAccount(t *testing.T) {
	fixture := newOAuthFixture(t)
	browser := &fixtureBrowser{t: t, client: fixture.server.Client()}
	options := optionsForFixture(fixture, browser, io.Discard)
	options.PreviousClientID = "saved-client-id"
	options.PreviousSubject = "different-subject"
	_, err := Login(context.Background(), options)
	if err == nil || !strings.Contains(err.Error(), "account subject changed") {
		t.Fatalf("Login() error = %v, want subject-change refusal", err)
	}
}
