// Package grok implements xAI device-code login and saved-account refresh.
package grok

import (
	"context"
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

	"kogen-go/internal/auth/refresh"
	"kogen-go/internal/auth/vault"
)

const (
	DefaultIssuer = "https://auth.x.ai"
	ClientID      = "b1a00492-073a-47ea-816f-4c329264a828"
	Scope         = "openid profile email offline_access grok-cli:access api:access"
	DeviceGrant   = "urn:ietf:params:oauth:grant-type:device_code"
	RefreshGrant  = "refresh_token"
	defaultLabel  = "default"
	maxAuthBody   = 1 << 20
)

// Options binds refreshes to one saved account. A caller may provide an
// alternate issuer and allow HTTP only for loopback OAuth fixtures.
type Options struct {
	Home           string
	Label          string
	IssuerURL      string
	AllowLocalHTTP bool
	Store          *vault.Store
	HTTPClient     *http.Client
	Sleep          func(context.Context, time.Duration) error
	TimeScale      float64
}

// Client owns the Grok OAuth protocol boundary, but not vault lifetime.
type Client struct {
	home           string
	label          string
	issuer         *url.URL
	allowLocalHTTP bool
	store          *vault.Store
	http           *http.Client
	sleep          func(context.Context, time.Duration) error
	timeScale      float64
}

// Error contains only a public, credential-free diagnostic. Auth errors do
// not implement retry.RetryClass: only an HTTP 401 can trigger a forced
// refresh, while a missing login or failed refresh must stop immediately.
type Error struct {
	Class   string
	Message string
	Cause   error
}

func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}
	return e.Message
}

func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

func (e *Error) String() string {
	if e == nil {
		return "grok auth error <nil>"
	}
	return fmt.Sprintf("grok auth error{class=%q}", e.Class)
}

// New validates the account binding and configures a bounded OAuth client.
func New(options Options) (*Client, error) {
	if options.Store == nil || options.Home == "" {
		return nil, errors.New("grok auth requires a home directory and vault")
	}
	if options.Label == "" {
		options.Label = defaultLabel
	}
	if !validLabel(options.Label) {
		return nil, &Error{Class: "login", Message: "Invalid Grok account label."}
	}
	issuerText := options.IssuerURL
	if issuerText == "" {
		if configured := os.Getenv("KOGEN_AUTH_URL"); configured != "" {
			issuerText = configured
			options.AllowLocalHTTP = true
		} else {
			issuerText = DefaultIssuer
		}
	}
	issuer, err := url.Parse(issuerText)
	if err != nil || !validEndpoint(issuer, options.AllowLocalHTTP) {
		return nil, invalidEndpoint()
	}
	client := options.HTTPClient
	if client == nil {
		client = &http.Client{}
	}
	clientCopy := *client
	clientCopy.Timeout = 20 * time.Second
	clientCopy.CheckRedirect = sameOriginRedirect
	if options.Sleep == nil {
		options.Sleep = sleepContext
	}
	if options.TimeScale <= 0 {
		options.TimeScale = timeScaleFromEnvironment()
	}
	return &Client{
		home: options.Home, label: options.Label, issuer: issuer,
		allowLocalHTTP: options.AllowLocalHTTP, store: options.Store,
		http: &clientCopy, sleep: options.Sleep, timeScale: options.TimeScale,
	}, nil
}

// Login completes device-code OAuth and persists both the secret and its
// non-secret profile row before returning.
func (c *Client) Login(ctx context.Context, progress func(string)) (vault.GrokCredential, error) {
	if c == nil || c.store == nil {
		return vault.GrokCredential{}, errors.New("grok auth client is not configured")
	}
	discovery, err := c.discover(ctx)
	if err != nil {
		return vault.GrokCredential{}, err
	}
	deviceEndpoint := discovery.DeviceAuthorizationEndpoint
	if deviceEndpoint == "" {
		deviceEndpoint = strings.TrimRight(discovery.Issuer, "/") + "/oauth2/device/code"
	}
	if err := c.validateEndpoint(deviceEndpoint); err != nil {
		return vault.GrokCredential{}, err
	}
	if err := c.validateEndpoint(discovery.TokenEndpoint); err != nil {
		return vault.GrokCredential{}, err
	}
	form := url.Values{"client_id": {ClientID}, "scope": {Scope}}
	status, body, err := c.postForm(ctx, deviceEndpoint, form)
	if err != nil {
		return vault.GrokCredential{}, signInNetworkError(err)
	}
	if status < 200 || status >= 300 {
		return vault.GrokCredential{}, authError("login", fmt.Sprintf("Grok device sign-in failed (HTTP %d).", status), nil)
	}
	var grant deviceResponse
	if decodeErr := decodeObject(body, &grant); decodeErr != nil || grant.DeviceCode == "" || grant.UserCode == "" || grant.ExpiresIn == 0 {
		return vault.GrokCredential{}, invalidDeviceResponse()
	}
	openURL := grant.VerificationURIComplete
	if openURL == "" {
		openURL = grant.VerificationURI
	}
	if !validVerificationURI(openURL) {
		return vault.GrokCredential{}, invalidDeviceResponse()
	}
	if progress != nil {
		progress("Grok sign-in code: " + grant.UserCode + "\n")
		progress("Open: " + openURL + "\n")
	}
	tokens, err := c.pollDevice(ctx, discovery.TokenEndpoint, grant)
	if err != nil {
		return vault.GrokCredential{}, err
	}
	if tokens.AccessToken == "" || tokens.RefreshToken == "" || tokens.ExpiresIn == 0 {
		return vault.GrokCredential{}, invalidTokenResponse()
	}
	credential := vault.GrokCredential{
		AccessToken: tokens.AccessToken, RefreshToken: tokens.RefreshToken,
		ExpiresAt: unixExpiry(time.Now(), tokens.ExpiresIn), Scopes: tokenScopes(tokens.Scope),
		Email: tokenEmail(tokens), ClientID: ClientID, TokenEndpoint: discovery.TokenEndpoint,
	}
	if err := c.store.PutGrok(c.label, credential); err != nil {
		return vault.GrokCredential{}, err
	}
	if err := c.writeProfile(credential, true); err != nil {
		return vault.GrokCredential{}, err
	}
	return credential, nil
}

// ForRequest returns a valid saved account, refreshing under the shared
// provider/label lock when its expiry is within five minutes.
func (c *Client) ForRequest(ctx context.Context) (vault.GrokCredential, error) {
	if c == nil || c.store == nil {
		return vault.GrokCredential{}, errors.New("grok auth client is not configured")
	}
	credential, found, err := c.store.GetGrok(c.label)
	if errors.Is(err, vault.ErrCredentialCorrupt) {
		return vault.GrokCredential{}, missingLogin(err)
	}
	if err != nil {
		return vault.GrokCredential{}, err
	}
	if !found {
		return vault.GrokCredential{}, missingLogin(err)
	}
	if credential.AccessToken == "" || c.validateEndpoint(credential.TokenEndpoint) != nil {
		return vault.GrokCredential{}, missingLogin(nil)
	}
	updated, err := refresh.GrokForRequest(ctx, c.home, c.label, c.store, c.refreshToken)
	if err != nil {
		return vault.GrokCredential{}, refreshError(err)
	}
	if updated.AccessToken != credential.AccessToken {
		if err := c.writeProfile(updated, true); err != nil {
			return vault.GrokCredential{}, err
		}
	}
	return updated, nil
}

// AfterUnauthorized performs one forced refresh only if the saved token still
// equals the access token rejected by the provider.
func (c *Client) AfterUnauthorized(ctx context.Context, rejectedAccessToken string) (vault.GrokCredential, bool, error) {
	if c == nil || c.store == nil {
		return vault.GrokCredential{}, false, errors.New("grok auth client is not configured")
	}
	credential, refreshed, err := refresh.GrokAfterUnauthorized(ctx, c.home, c.label, rejectedAccessToken, c.store, c.refreshToken)
	if err != nil {
		return vault.GrokCredential{}, false, refreshError(err)
	}
	if refreshed {
		if err := c.writeProfile(credential, true); err != nil {
			return vault.GrokCredential{}, false, err
		}
	}
	return credential, refreshed, nil
}

func (c *Client) discover(ctx context.Context) (discoveryDocument, error) {
	endpoint := strings.TrimRight(c.issuer.String(), "/") + "/.well-known/openid-configuration"
	status, body, err := c.get(ctx, endpoint)
	if err != nil {
		return discoveryDocument{}, signInNetworkError(err)
	}
	if status < 200 || status >= 300 {
		return discoveryDocument{}, authError("login", fmt.Sprintf("Grok sign-in discovery failed (HTTP %d).", status), nil)
	}
	var document discoveryDocument
	if err := decodeObject(body, &document); err != nil || document.Issuer == "" || document.TokenEndpoint == "" {
		return discoveryDocument{}, invalidDiscovery()
	}
	docIssuer, parseErr := url.Parse(document.Issuer)
	if parseErr != nil || !validEndpoint(docIssuer, c.allowLocalHTTP) || trimURLSlash(docIssuer) != trimURLSlash(c.issuer) {
		return discoveryDocument{}, invalidDiscovery()
	}
	return document, nil
}

func (c *Client) pollDevice(ctx context.Context, tokenEndpoint string, grant deviceResponse) (tokenResponse, error) {
	deadline := time.Now().Add(scaleDuration(durationSeconds(grant.ExpiresIn), c.timeScale))
	interval := uint64(5)
	if grant.Interval != nil {
		interval = *grant.Interval
		if interval < 1 {
			interval = 1
		}
	}
	for {
		wait := scaleDuration(durationSeconds(interval), c.timeScale)
		if !time.Now().Add(wait).Before(deadline) {
			return tokenResponse{}, expiredCode()
		}
		if err := c.sleep(ctx, wait); err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				return tokenResponse{}, authError("login", "Grok sign-in timed out.", err)
			}
			return tokenResponse{}, err
		}
		if !time.Now().Before(deadline) {
			return tokenResponse{}, expiredCode()
		}
		form := url.Values{
			"grant_type": {DeviceGrant}, "device_code": {grant.DeviceCode}, "client_id": {ClientID},
		}
		status, body, err := c.postForm(ctx, tokenEndpoint, form)
		if err != nil {
			return tokenResponse{}, signInNetworkError(err)
		}
		if !time.Now().Before(deadline) {
			return tokenResponse{}, expiredCode()
		}
		if status >= 200 && status < 300 {
			var tokens tokenResponse
			if err := decodeObject(body, &tokens); err != nil {
				return tokenResponse{}, invalidTokenResponse()
			}
			return tokens, nil
		}
		var response oauthError
		if decodeObject(body, &response) == nil {
			switch response.Error {
			case "authorization_pending":
				continue
			case "slow_down":
				if interval > math.MaxUint64-5 {
					interval = math.MaxUint64
				} else {
					interval += 5
				}
				continue
			case "expired_token":
				return tokenResponse{}, expiredCode()
			case "access_denied":
				return tokenResponse{}, authError("login", "Grok sign-in was cancelled.", nil)
			}
		}
		return tokenResponse{}, authError("login", fmt.Sprintf("Grok sign-in polling failed (HTTP %d).", status), nil)
	}
}

func (c *Client) refreshToken(ctx context.Context, current vault.GrokCredential) (vault.GrokCredential, error) {
	if current.RefreshToken == "" || c.validateEndpoint(current.TokenEndpoint) != nil {
		return vault.GrokCredential{}, unavailableRefresh()
	}
	form := url.Values{
		"grant_type": {RefreshGrant}, "client_id": {current.ClientID}, "refresh_token": {current.RefreshToken},
	}
	status, body, err := c.postForm(ctx, current.TokenEndpoint, form)
	if err != nil {
		return vault.GrokCredential{}, refreshNetworkError(err)
	}
	if status < 200 || status >= 300 {
		return vault.GrokCredential{}, unavailableRefresh()
	}
	var tokens tokenResponse
	if err := decodeObject(body, &tokens); err != nil || tokens.AccessToken == "" || tokens.ExpiresIn == 0 {
		return vault.GrokCredential{}, unavailableRefresh()
	}
	updated := current
	updated.AccessToken = tokens.AccessToken
	if tokens.RefreshToken != "" {
		updated.RefreshToken = tokens.RefreshToken
	}
	updated.ExpiresAt = unixExpiry(time.Now(), tokens.ExpiresIn)
	if tokens.Scope != "" {
		updated.Scopes = tokenScopes(tokens.Scope)
	}
	if email := tokenEmail(tokens); email != nil {
		updated.Email = email
	}
	return updated, nil
}

func (c *Client) writeProfile(credential vault.GrokCredential, signedIn bool) error {
	return c.store.PutGrokProfile(c.label, vault.GrokProfile{
		Email: credential.Email, ExpiresAt: credential.ExpiresAt, SignedIn: signedIn,
	})
}

func (c *Client) validateEndpoint(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || !validEndpoint(parsed, c.allowLocalHTTP) {
		return invalidEndpoint()
	}
	return nil
}

func (c *Client) get(ctx context.Context, endpoint string) (int, []byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return 0, nil, err
	}
	return c.do(request)
}

func (c *Client) postForm(ctx context.Context, endpoint string, form url.Values) (int, []byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return 0, nil, err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Accept", "application/json")
	return c.do(request)
}

func (c *Client) do(request *http.Request) (int, []byte, error) {
	response, err := c.http.Do(request)
	if err != nil {
		return 0, nil, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxAuthBody+1))
	if err != nil {
		return response.StatusCode, nil, err
	}
	if len(body) > maxAuthBody {
		return response.StatusCode, nil, errors.New("OAuth response exceeded the size limit")
	}
	return response.StatusCode, body, nil
}

func decodeObject(data []byte, target any) error {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" || trimmed[0] != '{' {
		return errors.New("OAuth response must be an object")
	}
	decoder := json.NewDecoder(strings.NewReader(trimmed))
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("OAuth response has trailing data")
	}
	return nil
}

type discoveryDocument struct {
	Issuer                      string `json:"issuer"`
	TokenEndpoint               string `json:"token_endpoint"`
	DeviceAuthorizationEndpoint string `json:"device_authorization_endpoint"`
}

type deviceResponse struct {
	DeviceCode              string  `json:"device_code"`
	UserCode                string  `json:"user_code"`
	VerificationURI         string  `json:"verification_uri"`
	VerificationURIComplete string  `json:"verification_uri_complete"`
	ExpiresIn               uint64  `json:"expires_in"`
	Interval                *uint64 `json:"interval"`
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    uint64 `json:"expires_in"`
	Scope        string `json:"scope"`
	Email        string `json:"email"`
	IDToken      string `json:"id_token"`
}

type oauthError struct {
	Error string `json:"error"`
}

func tokenScopes(scope string) []string {
	if scope == "" {
		scope = Scope
	}
	return strings.Fields(scope)
}

func tokenEmail(tokens tokenResponse) *string {
	if tokens.Email != "" {
		return stringPointer(tokens.Email)
	}
	parts := strings.Split(tokens.IDToken, ".")
	if len(parts) < 2 {
		return nil
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil
	}
	var claims struct {
		Email string `json:"email"`
	}
	if json.Unmarshal(payload, &claims) != nil || claims.Email == "" {
		return nil
	}
	return stringPointer(claims.Email)
}

func validEndpoint(endpoint *url.URL, allowLocalHTTP bool) bool {
	if endpoint == nil || endpoint.Hostname() == "" || endpoint.User != nil || endpoint.Fragment != "" {
		return false
	}
	if endpoint.Scheme == "https" {
		return true
	}
	return allowLocalHTTP && endpoint.Scheme == "http" && isLoopbackHost(endpoint.Hostname())
}

func validVerificationURI(raw string) bool {
	endpoint, err := url.Parse(raw)
	return err == nil && endpoint.Hostname() != "" && endpoint.User == nil && endpoint.Fragment == "" && (endpoint.Scheme == "http" || endpoint.Scheme == "https")
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func trimURLSlash(endpoint *url.URL) string {
	if endpoint == nil {
		return ""
	}
	copy := *endpoint
	copy.Path = strings.TrimRight(copy.Path, "/")
	copy.RawPath = ""
	return copy.String()
}

func sameOriginRedirect(request *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return errors.New("too many OAuth redirects")
	}
	if len(via) == 0 {
		return nil
	}
	previous := via[len(via)-1].URL
	if request.URL.Scheme != previous.Scheme || !strings.EqualFold(request.URL.Host, previous.Host) {
		return http.ErrUseLastResponse
	}
	return nil
}

func signInNetworkError(err error) error {
	if isTimeout(err) {
		return authError("login", "Grok sign-in timed out.", err)
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return authError("login", "Grok sign-in could not connect to xAI.", err)
	}
	return invalidDiscoveryWithCause(err)
}

func refreshNetworkError(err error) error {
	if isTimeout(err) {
		return authError("login", "Grok session refresh timed out.", err)
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return authError("login", "Grok session could not refresh. Check the network and sign in again.", err)
	}
	return unavailableRefreshWithCause(err)
}

func refreshError(err error) error {
	if errors.Is(err, refresh.ErrCredentialMissing) || errors.Is(err, vault.ErrCredentialCorrupt) {
		return missingLogin(err)
	}
	if errors.Is(err, refresh.ErrLockWaitTimeout) {
		return authError("login", "Grok session refresh timed out.", err)
	}
	var authErr *Error
	if errors.As(err, &authErr) {
		return authErr
	}
	return unavailableRefreshWithCause(err)
}

func missingLogin(cause error) error {
	return authError("login", "Grok login is missing or invalid; run `kogen provider login grok`.", cause)
}

func unavailableRefresh() error { return unavailableRefreshWithCause(nil) }

func unavailableRefreshWithCause(cause error) error {
	return authError("login", "Grok login is unavailable; run `kogen provider login grok`.", cause)
}

func invalidEndpoint() error {
	return authError("login", "Grok returned an invalid sign-in endpoint.", nil)
}

func invalidDiscovery() error {
	return authError("login", "Grok returned an invalid sign-in discovery document.", nil)
}

func invalidDiscoveryWithCause(cause error) error {
	return authError("login", "Grok returned an invalid sign-in discovery document.", cause)
}

func invalidDeviceResponse() error {
	return authError("login", "Grok returned an invalid device sign-in response.", nil)
}

func invalidTokenResponse() error {
	return authError("login", "Grok returned an invalid sign-in token response.", nil)
}

func expiredCode() error {
	return authError("login", "Grok sign-in code expired; run `kogen provider login grok` again.", nil)
}

func authError(class, message string, cause error) *Error {
	return &Error{Class: class, Message: message, Cause: cause}
}

func isTimeout(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

func validLabel(label string) bool {
	if len(label) == 0 || len(label) > 64 || !asciiAlnum(label[0]) {
		return false
	}
	for i := 1; i < len(label); i++ {
		b := label[i]
		if !asciiAlnum(b) && b != '.' && b != '_' && b != '-' {
			return false
		}
	}
	return true
}

func asciiAlnum(value byte) bool {
	return value >= 'A' && value <= 'Z' || value >= 'a' && value <= 'z' || value >= '0' && value <= '9'
}

func unixExpiry(now time.Time, expiresIn uint64) int64 {
	seconds := int64(expiresIn)
	if expiresIn > uint64(^uint64(0)>>1) {
		seconds = int64(^uint64(0) >> 1)
	}
	if seconds > 0 && now.Unix() > int64(^uint64(0)>>1)-seconds {
		return int64(^uint64(0) >> 1)
	}
	return now.Unix() + seconds
}

func stringPointer(value string) *string { return &value }

func scaleDuration(duration time.Duration, scale float64) time.Duration {
	if scale <= 0 || math.IsNaN(scale) || math.IsInf(scale, 0) {
		return time.Millisecond
	}
	scaledFloat := float64(duration) * scale
	if math.IsInf(scaledFloat, 0) || scaledFloat >= float64(math.MaxInt64) {
		return time.Duration(math.MaxInt64)
	}
	scaled := time.Duration(scaledFloat)
	if scaled < time.Millisecond {
		return time.Millisecond
	}
	return scaled
}

func timeScaleFromEnvironment() float64 {
	value := strings.TrimSpace(os.Getenv("KOGEN_TIME_SCALE"))
	if value == "" {
		return 1
	}
	scale, err := strconv.ParseFloat(value, 64)
	if err != nil || scale <= 0 || math.IsNaN(scale) || math.IsInf(scale, 0) {
		return 1
	}
	return scale
}

func durationSeconds(seconds uint64) time.Duration {
	const maximum = uint64(math.MaxInt64 / int64(time.Second))
	if seconds > maximum {
		return time.Duration(math.MaxInt64)
	}
	return time.Duration(seconds) * time.Second
}

func sleepContext(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
