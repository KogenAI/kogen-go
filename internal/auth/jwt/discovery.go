package jwt

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	MaxDiscoveryBytes = 64 << 10
	MaxJWKSBytes      = 1 << 20
	requestTimeout    = 20 * time.Second
)

// Discovery is the validated subset of the OpenID configuration consumed by
// the OAuth flow.
type Discovery struct {
	Issuer                string
	AuthorizationEndpoint string
	TokenEndpoint         string
	JWKSURI               string
	RevocationEndpoint    string
}

// Discover fetches the OpenID configuration beneath authBaseURL (or the
// production issuer when empty), enforces the expected issuer, validates every
// returned endpoint, and caps the response at MaxDiscoveryBytes. HTTP is
// accepted only when allowHTTP is explicitly enabled for the fake auth seam.
func Discover(ctx context.Context, client *http.Client, authBaseURL string, allowHTTP bool) (Discovery, error) {
	if ctx == nil {
		return Discovery{}, errors.New("jwt: discovery context is required")
	}
	if authBaseURL == "" {
		authBaseURL = Issuer
	}
	base, err := parseEndpoint(authBaseURL, allowHTTP)
	if err != nil || base.RawQuery != "" || base.Fragment != "" {
		return Discovery{}, errors.New("jwt: OpenID auth URL is invalid")
	}
	discoveryURL := strings.TrimRight(authBaseURL, "/") + "/.well-known/openid-configuration"
	body, err := getBounded(ctx, client, discoveryURL, allowHTTP, MaxDiscoveryBytes)
	if err != nil {
		return Discovery{}, err
	}
	object, err := parseJSONObject(body)
	if err != nil {
		return Discovery{}, errors.New("jwt: OpenID discovery response is invalid")
	}
	result, err := discoveryFromObject(object)
	if err != nil {
		return Discovery{}, err
	}
	if result.Issuer != Issuer {
		return Discovery{}, errors.New("jwt: OpenID issuer is invalid")
	}
	for _, endpoint := range []string{result.AuthorizationEndpoint, result.TokenEndpoint, result.JWKSURI} {
		if _, err := parseEndpoint(endpoint, allowHTTP); err != nil {
			return Discovery{}, errors.New("jwt: OpenID endpoint is invalid")
		}
	}
	if result.RevocationEndpoint != "" {
		if _, err := parseEndpoint(result.RevocationEndpoint, allowHTTP); err != nil {
			return Discovery{}, errors.New("jwt: OpenID endpoint is invalid")
		}
	}
	return result, nil
}

func discoveryFromObject(object map[string]any) (Discovery, error) {
	read := func(name string) (string, bool) {
		value, exists := object[name]
		if !exists {
			return "", false
		}
		text, ok := value.(string)
		return text, ok && text != ""
	}
	var result Discovery
	var valid bool
	if result.Issuer, valid = read("issuer"); !valid {
		return Discovery{}, errors.New("jwt: OpenID discovery response is incomplete")
	}
	if result.AuthorizationEndpoint, valid = read("authorization_endpoint"); !valid {
		return Discovery{}, errors.New("jwt: OpenID discovery response is incomplete")
	}
	if result.TokenEndpoint, valid = read("token_endpoint"); !valid {
		return Discovery{}, errors.New("jwt: OpenID discovery response is incomplete")
	}
	if result.JWKSURI, valid = read("jwks_uri"); !valid {
		return Discovery{}, errors.New("jwt: OpenID discovery response is incomplete")
	}
	if value, exists := object["revocation_endpoint"]; exists {
		endpoint, ok := value.(string)
		if !ok || endpoint == "" {
			return Discovery{}, errors.New("jwt: OpenID discovery response is invalid")
		}
		result.RevocationEndpoint = endpoint
	}
	return result, nil
}

// FetchJWKS retrieves the bounded signing-key set advertised by discovery.
// Its JSON structure and key count are checked before returning it to callers.
func FetchJWKS(ctx context.Context, client *http.Client, jwksURI string, allowHTTP bool) ([]byte, error) {
	if ctx == nil {
		return nil, errors.New("jwt: JWKS context is required")
	}
	body, err := getBounded(ctx, client, jwksURI, allowHTTP, MaxJWKSBytes)
	if err != nil {
		return nil, err
	}
	object, err := parseJSONObject(body)
	if err != nil {
		return nil, ErrInvalidJWKS
	}
	keys, ok := object["keys"].([]any)
	if !ok || len(keys) == 0 || len(keys) > maxJWKSKeys {
		return nil, ErrInvalidJWKS
	}
	for _, key := range keys {
		if _, ok := key.(map[string]any); !ok {
			return nil, ErrInvalidJWKS
		}
	}
	return body, nil
}

func getBounded(ctx context.Context, client *http.Client, endpoint string, allowHTTP bool, limit int64) ([]byte, error) {
	if _, err := parseEndpoint(endpoint, allowHTTP); err != nil {
		return nil, errors.New("jwt: OpenID endpoint is invalid")
	}
	requestContext, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestContext, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, errors.New("jwt: OpenID request could not be created")
	}
	request.Header.Set("Accept", "application/json")
	transport := http.DefaultClient
	if client != nil {
		transport = client
	}
	clientCopy := *transport
	// Discovery and JWKS are pinned to the advertised endpoints. Do not let a
	// redirect silently move authentication traffic to another origin.
	clientCopy.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := clientCopy.Do(request)
	if err != nil {
		return nil, errors.New("jwt: OpenID request failed")
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("jwt: OpenID request returned HTTP %d", response.StatusCode)
	}
	if response.ContentLength > limit {
		return nil, errors.New("jwt: OpenID response exceeds the size limit")
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return nil, errors.New("jwt: OpenID response could not be read")
	}
	if int64(len(body)) > limit {
		return nil, errors.New("jwt: OpenID response exceeds the size limit")
	}
	return body, nil
}

func parseEndpoint(endpoint string, allowHTTP bool) (*url.URL, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil || !parsed.IsAbs() || parsed.Host == "" || parsed.Opaque != "" ||
		parsed.User != nil || parsed.Fragment != "" || parsed.Hostname() == "" {
		return nil, errors.New("invalid endpoint")
	}
	switch parsed.Scheme {
	case "https":
	case "http":
		if !allowHTTP {
			return nil, errors.New("HTTP endpoint is disabled")
		}
	default:
		return nil, errors.New("unsupported endpoint scheme")
	}
	return parsed, nil
}
