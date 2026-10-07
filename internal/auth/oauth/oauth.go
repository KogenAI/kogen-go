// Package oauth implements ChatGPT's OpenID Connect authorization-code flow.
// It returns verified credentials to the caller; account selection and durable
// credential/profile publication remain with the auth route and vault.
package oauth

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"kogen-go/internal/auth/jwt"
	"kogen-go/internal/auth/vault"
	"kogen-go/internal/contract"
)

const (
	// CallbackPort is the stable loopback port used by the ChatGPT sign-in flow.
	CallbackPort = 1455
	CallbackPath = "/auth/callback"
	// Resource is the API resource requested during authorization and exchange.
	Resource = "https://api.openai.com/v1"
	// Scope is the exact ordered scope string used by Kogen's ChatGPT client.
	Scope = "openid profile email offline_access resource.invoke chatgpt.tokens.use.direct"
	// DynamicClientID and AgentNameHint identify the first-use client registration hint.
	DynamicClientID = "dynamic_agent_client"
	AgentNameHint   = "Kogen"

	requestTimeout       = 20 * time.Second
	callbackWait         = 300 * time.Second
	maxTokenResponseSize = 1 << 20
	maxTokenSize         = 256 << 10
)

var (
	ErrInvalidOptions        = errors.New("oauth: login options are invalid")
	ErrDiscovery             = errors.New("oauth: OpenID discovery failed")
	ErrCallbackPort          = errors.New("oauth: ChatGPT sign-in callback port is unavailable")
	ErrCallbackTimeout       = errors.New("oauth: timed out waiting for ChatGPT sign-in callback")
	ErrInvalidCallback       = errors.New("oauth: ChatGPT callback is invalid")
	ErrStateMismatch         = errors.New("oauth: ChatGPT callback state did not match")
	ErrAuthorizationDenied   = errors.New("oauth: ChatGPT sign-in was declined")
	ErrTokenExchange         = errors.New("oauth: ChatGPT token exchange failed")
	ErrInvalidTokenResponse  = errors.New("oauth: ChatGPT token response is invalid")
	ErrBrowserOpen           = errors.New("oauth: could not open a browser for ChatGPT sign-in")
	ErrRevocationUnavailable = errors.New("oauth: ChatGPT revocation is unavailable")
	ErrRevocationRejected    = errors.New("oauth: ChatGPT token revocation was not confirmed")
)

// Options supplies the account identity and the external effects needed by a
// login. AuthBaseURL and AllowHTTP are test seams; an HTTP auth base is accepted
// only on a loopback IP/localhost, and discovered HTTP endpoints must also be
// loopback. When AuthBaseURL is empty, KOGEN_AUTH_URL is used when set, then the
// production OpenID issuer. The environment override is the fake-provider seam.
//
// ProcessRunner defaults to process.Supervisor. Output defaults to os.Stdout so
// the authorization URL is written before the supervised browser command runs.
type Options struct {
	HTTPClient *http.Client

	AuthBaseURL string
	AllowHTTP   bool

	HostID             string
	PreviousClientID   string
	PreviousSubject    string
	CallbackPort       int
	CallbackTimeout    time.Duration
	Random             io.Reader
	Now                func() time.Time
	Output             io.Writer
	ProcessRunner      contract.ProcessRunner
	BrowserExecutable  string
	BrowserEnvironment []string
}

// Result contains the identity-verified credential produced by a login.
// Credential and identity String methods redact their sensitive fields.
type Result struct {
	Credential vault.Credential
	Identity   jwt.Identity
}

func (Result) String() string   { return "oauth.Result{REDACTED}" }
func (Result) GoString() string { return "oauth.Result{REDACTED}" }

// Login performs discovery, PKCE authorization, loopback callback validation,
// token exchange, scope validation, and RS256 identity-token verification.
// The caller persists Result.Credential only after Login succeeds.
func Login(ctx context.Context, options Options) (Result, error) {
	if ctx == nil || !validHostID(options.HostID) ||
		(options.PreviousClientID != "" && !validClientID(options.PreviousClientID)) ||
		(options.PreviousSubject != "" && !validOpaqueText(options.PreviousSubject, 4096)) {
		return Result{}, ErrInvalidOptions
	}
	options, err := normalizeOptions(options)
	if err != nil {
		return Result{}, err
	}

	clientID := options.PreviousClientID
	if clientID == "" {
		clientID = DynamicClientID
	}
	result, err := loginAttempt(ctx, options, clientID)
	if errors.Is(err, ErrAuthorizationDenied) && options.PreviousClientID != "" {
		return loginAttempt(ctx, options, DynamicClientID)
	}
	return result, err
}

// Revoke asks the discovered revocation endpoint to revoke the stored refresh
// token. A false result or error means local logout may proceed, but remote
// revocation was not confirmed.
func Revoke(ctx context.Context, options Options, credential vault.Credential) (bool, error) {
	if ctx == nil || !validClientID(credential.ClientID) || len(credential.RefreshToken) > maxTokenSize {
		return false, ErrInvalidOptions
	}
	if credential.RefreshToken == "" {
		return false, ErrRevocationUnavailable
	}
	options, err := normalizeOptions(options)
	if err != nil {
		return false, err
	}
	client := newHTTPClient(options.HTTPClient)
	discovery, err := jwt.Discover(ctx, client, options.AuthBaseURL, options.AllowHTTP)
	if err != nil {
		return false, fmt.Errorf("%w: %v", ErrDiscovery, err)
	}
	if err := validateDiscoveredEndpoints(discovery, options.AllowHTTP, options.AuthBaseURL); err != nil {
		return false, err
	}
	if discovery.RevocationEndpoint == "" {
		return false, ErrRevocationUnavailable
	}

	requestContext, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	form := url.Values{
		"token":           {credential.RefreshToken},
		"token_type_hint": {"refresh_token"},
		"client_id":       {credential.ClientID},
	}
	request, err := http.NewRequestWithContext(requestContext, http.MethodPost, discovery.RevocationEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return false, ErrRevocationRejected
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := client.Do(request)
	if err != nil {
		return false, ErrRevocationRejected
	}
	defer response.Body.Close()
	_, _ = io.CopyN(io.Discard, response.Body, 64<<10)
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return false, fmt.Errorf("%w (HTTP %d)", ErrRevocationRejected, response.StatusCode)
	}
	return true, nil
}

func loginAttempt(ctx context.Context, options Options, clientID string) (Result, error) {
	client := newHTTPClient(options.HTTPClient)
	discovery, err := jwt.Discover(ctx, client, options.AuthBaseURL, options.AllowHTTP)
	if err != nil {
		return Result{}, fmt.Errorf("%w: %v", ErrDiscovery, err)
	}
	if err := validateDiscoveredEndpoints(discovery, options.AllowHTTP, options.AuthBaseURL); err != nil {
		return Result{}, err
	}

	randomSource := options.Random
	if randomSource == nil {
		randomSource = rand.Reader
	}
	verifier, err := randomURLToken(randomSource)
	if err != nil {
		return Result{}, fmt.Errorf("%w: could not generate PKCE verifier", ErrInvalidOptions)
	}
	state, err := randomURLToken(randomSource)
	if err != nil {
		return Result{}, fmt.Errorf("%w: could not generate callback state", ErrInvalidOptions)
	}
	nonce, err := randomURLToken(randomSource)
	if err != nil {
		return Result{}, fmt.Errorf("%w: could not generate identity nonce", ErrInvalidOptions)
	}
	challengeBytes := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(challengeBytes[:])

	listener, redirectURI, err := bindCallback(ctx, options.CallbackPort)
	if err != nil {
		if options.CallbackPort == CallbackPort {
			return Result{}, fmt.Errorf("%w: %v", ErrCallbackPort, err)
		}
		return Result{}, fmt.Errorf("oauth: could not bind ChatGPT sign-in callback: %w", err)
	}
	callback, stopCallback := startCallback(ctx, listener, state, options.CallbackTimeout)
	defer stopCallback()

	authorizeURL, err := authorizationURL(discovery.AuthorizationEndpoint, clientID, options.HostID, redirectURI, state, nonce, challenge)
	if err != nil {
		return Result{}, err
	}
	output := options.Output
	if output == nil {
		output = os.Stdout
	}
	if _, err := io.WriteString(output, "Continue with ChatGPT\n"+authorizeURL+"\n"); err != nil {
		return Result{}, errors.New("oauth: could not write ChatGPT authorization URL")
	}
	if flushable, ok := output.(interface{ Flush() error }); ok {
		if err := flushable.Flush(); err != nil {
			return Result{}, errors.New("oauth: could not write ChatGPT authorization URL")
		}
	}
	if err := openBrowser(ctx, options, authorizeURL); err != nil {
		return Result{}, err
	}
	returned, err := callback()
	if err != nil {
		return Result{}, err
	}
	if returned.Denied {
		return Result{}, ErrAuthorizationDenied
	}
	if returned.Code == "" {
		return Result{}, ErrInvalidCallback
	}
	returnedClientID := clientID
	if returned.ClientID != "" {
		returnedClientID = returned.ClientID
	}
	if !validClientID(returnedClientID) {
		return Result{}, ErrInvalidCallback
	}

	tokens, err := exchangeCode(ctx, client, discovery.TokenEndpoint, returned.Code, redirectURI, returnedClientID, verifier)
	if err != nil {
		return Result{}, err
	}
	jwks, err := jwt.FetchJWKS(ctx, client, discovery.JWKSURI, options.AllowHTTP)
	if err != nil {
		return Result{}, fmt.Errorf("oauth: identity signing keys could not be fetched: %v", err)
	}
	now := time.Now
	if options.Now != nil {
		now = options.Now
	}
	identity, err := jwt.VerifyIDTokenWithOptions(tokens.IDToken, jwks, jwt.VerifyOptions{
		ClientID:      returnedClientID,
		ExpectedNonce: &nonce,
		Now:           now,
	})
	if err != nil {
		return Result{}, fmt.Errorf("oauth: ChatGPT identity token was not accepted: %w", err)
	}
	if options.PreviousSubject != "" {
		if err := jwt.CheckSubjectConsistency(options.PreviousSubject, identity.Subject); err != nil {
			return Result{}, fmt.Errorf("oauth: ChatGPT account subject changed: %w", err)
		}
	}
	scopes, err := jwt.RequireScope(tokens.Scope)
	if err != nil {
		return Result{}, err
	}
	nowValue := time.Now()
	if options.Now != nil {
		nowValue = options.Now()
	}
	nowUnix := nowValue.Unix()
	expiresIn := int64(tokens.ExpiresIn)
	if nowUnix > math.MaxInt64-expiresIn {
		return Result{}, ErrInvalidTokenResponse
	}
	expiresAt := nowUnix + expiresIn
	credential := vault.Credential{
		ClientID:     returnedClientID,
		AccessToken:  tokens.AccessToken,
		RefreshToken: tokens.RefreshToken,
		IDToken:      tokens.IDToken,
		ExpiresAt:    expiresAt,
		Scopes:       scopes,
		Subject:      identity.Subject,
		Email:        identity.Email,
		HostID:       options.HostID,
	}
	return Result{Credential: credential, Identity: identity}, nil
}

type tokenResponse struct {
	AccessToken  string      `json:"access_token"`
	RefreshToken string      `json:"refresh_token"`
	IDToken      string      `json:"id_token"`
	ExpiresIn    uint64      `json:"-"`
	ExpiresInRaw json.Number `json:"expires_in"`
	Scope        string      `json:"scope"`
}

func exchangeCode(ctx context.Context, client *http.Client, endpoint, code, redirectURI, clientID, verifier string) (tokenResponse, error) {
	requestContext, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"client_id":     {clientID},
		"code_verifier": {verifier},
		"resource":      {Resource},
	}
	request, err := http.NewRequestWithContext(requestContext, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return tokenResponse{}, ErrTokenExchange
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := client.Do(request)
	if err != nil {
		return tokenResponse{}, ErrTokenExchange
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		_, _ = io.CopyN(io.Discard, response.Body, 4096)
		return tokenResponse{}, fmt.Errorf("%w (HTTP %d)", ErrTokenExchange, response.StatusCode)
	}
	if response.ContentLength > maxTokenResponseSize {
		return tokenResponse{}, ErrInvalidTokenResponse
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxTokenResponseSize+1))
	if err != nil || len(body) > maxTokenResponseSize {
		return tokenResponse{}, ErrInvalidTokenResponse
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var tokens tokenResponse
	if err := decoder.Decode(&tokens); err != nil {
		return tokenResponse{}, ErrInvalidTokenResponse
	}
	if err := requireJSONEOF(decoder); err != nil {
		return tokenResponse{}, ErrInvalidTokenResponse
	}
	expiresIn, err := strconv.ParseUint(tokens.ExpiresInRaw.String(), 10, 63)
	if err != nil || expiresIn == 0 ||
		tokens.AccessToken == "" || len(tokens.AccessToken) > maxTokenSize ||
		tokens.IDToken == "" || len(tokens.IDToken) > maxTokenSize || tokens.RefreshToken == "" || len(tokens.RefreshToken) > maxTokenSize ||
		tokens.Scope == "" {
		return tokenResponse{}, ErrInvalidTokenResponse
	}
	tokens.ExpiresIn = expiresIn
	return tokens, nil
}

func requireJSONEOF(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func authorizationURL(endpoint, clientID, hostID, redirectURI, state, nonce, challenge string) (string, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil || !parsed.IsAbs() || parsed.Host == "" {
		return "", errors.New("oauth: ChatGPT authorization endpoint is invalid")
	}
	query := parsed.Query()
	for _, key := range []string{
		"client_id", "ext_agent_host_id", "response_type", "redirect_uri", "scope", "resource",
		"state", "nonce", "code_challenge_method", "code_challenge", "agent_name_hint",
	} {
		query.Del(key)
	}
	query.Set("client_id", clientID)
	query.Set("ext_agent_host_id", hostID)
	query.Set("response_type", "code")
	query.Set("redirect_uri", redirectURI)
	query.Set("scope", Scope)
	query.Set("resource", Resource)
	query.Set("state", state)
	query.Set("nonce", nonce)
	query.Set("code_challenge_method", "S256")
	query.Set("code_challenge", challenge)
	if clientID == DynamicClientID {
		query.Set("agent_name_hint", AgentNameHint)
	}
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

func randomURLToken(source io.Reader) (string, error) {
	data := make([]byte, 32)
	if _, err := io.ReadFull(source, data); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(data), nil
}

func normalizeOptions(options Options) (Options, error) {
	base := strings.TrimSpace(options.AuthBaseURL)
	if base == "" {
		if envBase := strings.TrimSpace(os.Getenv("KOGEN_AUTH_URL")); envBase != "" {
			base = envBase
			options.AllowHTTP = true
		} else {
			base = jwt.Issuer
		}
	}
	parsed, err := url.Parse(base)
	if err != nil || !parsed.IsAbs() || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" || parsed.RawQuery != "" {
		return Options{}, ErrInvalidOptions
	}
	if parsed.Scheme != "https" && parsed.Scheme != "http" {
		return Options{}, ErrInvalidOptions
	}
	if parsed.Scheme == "http" && (!options.AllowHTTP || !isLoopbackHost(parsed.Hostname())) {
		return Options{}, ErrInvalidOptions
	}
	if options.CallbackPort == 0 {
		options.CallbackPort = CallbackPort
	}
	if options.CallbackPort < 1 || options.CallbackPort > 65535 {
		return Options{}, ErrInvalidOptions
	}
	if options.CallbackTimeout == 0 {
		options.CallbackTimeout = callbackTimeoutFromEnvironment()
	}
	if options.CallbackTimeout < 0 {
		return Options{}, ErrInvalidOptions
	}
	options.AuthBaseURL = strings.TrimRight(base, "/")
	return options, nil
}

func callbackTimeoutFromEnvironment() time.Duration {
	scale, err := strconv.ParseFloat(os.Getenv("KOGEN_TIME_SCALE"), 64)
	if err == nil && !math.IsNaN(scale) && !math.IsInf(scale, 0) && scale >= 0 {
		milliseconds := math.Max(1, math.Floor(float64(callbackWait/time.Millisecond)*scale))
		if milliseconds <= float64(math.MaxInt64/int64(time.Millisecond)) {
			return time.Duration(milliseconds) * time.Millisecond
		}
	}
	return callbackWait
}

func validateDiscoveredEndpoints(discovery jwt.Discovery, allowHTTP bool, authBaseURL string) error {
	loopbackHTTP := allowHTTP && isLoopbackURL(authBaseURL)
	endpoints := []string{discovery.AuthorizationEndpoint, discovery.TokenEndpoint, discovery.JWKSURI}
	if discovery.RevocationEndpoint != "" {
		endpoints = append(endpoints, discovery.RevocationEndpoint)
	}
	for _, endpoint := range endpoints {
		parsed, err := url.Parse(endpoint)
		if err != nil || !parsed.IsAbs() || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
			return errors.New("oauth: OpenID endpoint is invalid")
		}
		if parsed.Scheme == "http" && (!loopbackHTTP || !isLoopbackHost(parsed.Hostname())) {
			return errors.New("oauth: OpenID endpoints must use HTTPS")
		}
	}
	return nil
}

func isLoopbackURL(value string) bool {
	parsed, err := url.Parse(value)
	return err == nil && parsed.Scheme == "http" && isLoopbackHost(parsed.Hostname())
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	address := net.ParseIP(host)
	return address != nil && address.IsLoopback()
}

func newHTTPClient(client *http.Client) *http.Client {
	if client == nil {
		client = &http.Client{}
	}
	copy := *client
	if copy.Timeout == 0 || copy.Timeout > requestTimeout {
		copy.Timeout = requestTimeout
	}
	copy.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &copy
}

func validHostID(value string) bool {
	if !strings.HasPrefix(value, "urn:uuid:") {
		return false
	}
	value = strings.TrimPrefix(value, "urn:uuid:")
	if len(value) != 36 {
		return false
	}
	for index, character := range []byte(value) {
		if index == 8 || index == 13 || index == 18 || index == 23 {
			if character != '-' {
				return false
			}
		} else if !((character >= '0' && character <= '9') || (character >= 'a' && character <= 'f') || (character >= 'A' && character <= 'F')) {
			return false
		}
	}
	return true
}

func validClientID(value string) bool {
	if value == "" || len(value) > 256 {
		return false
	}
	for _, character := range []byte(value) {
		if character < 0x21 || character > 0x7e || character == '\\' || character == '"' {
			return false
		}
	}
	return true
}

func validOpaqueText(value string, limit int) bool {
	if value == "" || len(value) > limit {
		return false
	}
	for _, character := range []byte(value) {
		if character < 0x20 || character == 0x7f {
			return false
		}
	}
	return true
}
