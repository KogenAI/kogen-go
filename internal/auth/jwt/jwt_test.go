package jwt

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

var (
	testKeyOnce sync.Once
	testKey     *rsa.PrivateKey
	testKeyErr  error
)

func signingKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	testKeyOnce.Do(func() { testKey, testKeyErr = rsa.GenerateKey(rand.Reader, 2048) })
	if testKeyErr != nil {
		t.Fatal(testKeyErr)
	}
	return testKey
}

func testJWKS(t *testing.T, key *rsa.PublicKey, kid string) []byte {
	t.Helper()
	modulus := base64.RawURLEncoding.EncodeToString(key.N.Bytes())
	exponent := base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes())
	data, err := json.Marshal(map[string]any{"keys": []any{map[string]any{
		"kty": "RSA", "kid": kid, "alg": "RS256", "use": "sig", "n": modulus, "e": exponent,
	}}})
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func signedToken(t *testing.T, key *rsa.PrivateKey, header, claims map[string]any) string {
	t.Helper()
	headerJSON, err := json.Marshal(header)
	if err != nil {
		t.Fatal(err)
	}
	claimsJSON, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	return signedRawToken(t, key, headerJSON, claimsJSON)
}

func signedRawToken(t *testing.T, key *rsa.PrivateKey, headerJSON, claimsJSON []byte) string {
	t.Helper()
	encodedHeader := base64.RawURLEncoding.EncodeToString(headerJSON)
	encodedClaims := base64.RawURLEncoding.EncodeToString(claimsJSON)
	unsigned := encodedHeader + "." + encodedClaims
	digest := sha256.Sum256([]byte(unsigned))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return unsigned + "." + base64.RawURLEncoding.EncodeToString(signature)
}

func validClaims(now time.Time) map[string]any {
	return map[string]any{
		"iss": Issuer, "aud": []string{"another-client", "client-1"},
		"exp": now.Add(time.Hour).Unix(), "nonce": "nonce-1", "sub": "subject-1",
		"email": "person@example.test", "https://api.openai.com/auth": map[string]any{
			"chatgpt_account_id": "account-1", "chatgpt_plan_type": "plus",
		},
	}
}

func validToken(t *testing.T, now time.Time) (string, []byte) {
	t.Helper()
	key := signingKey(t)
	token := signedToken(t, key, map[string]any{"alg": "RS256", "kid": "key-1", "typ": "JWT"}, validClaims(now))
	return token, testJWKS(t, &key.PublicKey, "key-1")
}

func fixedVerifyOptions(now time.Time, nonce *string) VerifyOptions {
	return VerifyOptions{ClientID: "client-1", ExpectedNonce: nonce, Now: func() time.Time { return now }}
}

func TestVerifyIDTokenRS256AndIdentityClaims(t *testing.T) {
	now := time.Unix(1_900_000_000, 0)
	token, jwks := validToken(t, now)
	nonce := "nonce-1"
	identity, err := VerifyIDTokenWithOptions(token, jwks, fixedVerifyOptions(now, &nonce))
	if err != nil {
		t.Fatal(err)
	}
	if identity.Subject != "subject-1" || identity.Email == nil || *identity.Email != "person@example.test" ||
		identity.ExpiresAt != now.Add(time.Hour).Unix() || identity.AccountID == nil || *identity.AccountID != "account-1" || identity.PlanUsage == nil || *identity.PlanUsage != "plus" {
		t.Fatalf("unexpected verified identity: %#v", identity)
	}
	if got := fmt.Sprint(identity); got != "Identity{REDACTED}" {
		t.Fatalf("identity diagnostic exposed data: %s", got)
	}
}

func TestVerifyIDTokenRejectsSignatureAndHeaderFailures(t *testing.T) {
	now := time.Unix(1_900_000_000, 0)
	key := signingKey(t)
	claims := validClaims(now)
	valid := signedToken(t, key, map[string]any{"alg": "RS256", "kid": "key-1"}, claims)
	jwks := testJWKS(t, &key.PublicKey, "key-1")
	parts := strings.Split(valid, ".")
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatal(err)
	}
	signature[0] ^= 0xff
	parts[2] = base64.RawURLEncoding.EncodeToString(signature)
	badSignature := strings.Join(parts, ".")

	missingKID := signedToken(t, key, map[string]any{"alg": "RS256"}, claims)
	wrongAlgorithm := signedToken(t, key, map[string]any{"alg": "HS256", "kid": "key-1"}, claims)
	for name, token := range map[string]string{
		"signature": badSignature, "missing kid": missingKID, "wrong algorithm": wrongAlgorithm,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := VerifyIDTokenWithOptions(token, jwks, fixedVerifyOptions(now, nil)); !errors.Is(err, ErrInvalidToken) {
				t.Fatalf("VerifyIDTokenWithOptions() error = %v, want ErrInvalidToken", err)
			}
		})
	}
}

func TestVerifyIDTokenRejectsInvalidClaims(t *testing.T) {
	now := time.Unix(1_900_000_000, 0)
	key := signingKey(t)
	base := validClaims(now)
	nonce := "nonce-1"
	cases := []struct {
		name   string
		change func(map[string]any)
	}{
		{name: "issuer", change: func(claims map[string]any) { claims["iss"] = "https://issuer.attacker.test" }},
		{name: "audience", change: func(claims map[string]any) { claims["aud"] = []string{"other-client"} }},
		{name: "audience type", change: func(claims map[string]any) { claims["aud"] = []any{"client-1", 7} }},
		{name: "expired", change: func(claims map[string]any) { claims["exp"] = now.Unix() }},
		{name: "fractional expiration", change: func(claims map[string]any) { claims["exp"] = 1.5 }},
		{name: "nonce", change: func(claims map[string]any) { claims["nonce"] = "wrong" }},
		{name: "missing subject", change: func(claims map[string]any) { delete(claims, "sub") }},
		{name: "empty subject", change: func(claims map[string]any) { claims["sub"] = "" }},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			claims := make(map[string]any, len(base))
			for key, value := range base {
				claims[key] = value
			}
			test.change(claims)
			token := signedToken(t, key, map[string]any{"alg": "RS256", "kid": "key-1"}, claims)
			jwks := testJWKS(t, &key.PublicKey, "key-1")
			if _, err := VerifyIDTokenWithOptions(token, jwks, fixedVerifyOptions(now, &nonce)); !errors.Is(err, ErrInvalidClaims) {
				t.Fatalf("VerifyIDTokenWithOptions() error = %v, want ErrInvalidClaims", err)
			}
		})
	}
}

func TestVerifyIDTokenRejectsBadAndAmbiguousKeys(t *testing.T) {
	now := time.Unix(1_900_000_000, 0)
	key := signingKey(t)
	token := signedToken(t, key, map[string]any{"alg": "RS256", "kid": "key-1"}, validClaims(now))
	good := json.RawMessage(testJWKS(t, &key.PublicKey, "key-1"))
	var jwksObject map[string]any
	if err := json.Unmarshal(good, &jwksObject); err != nil {
		t.Fatal(err)
	}
	duplicateKeys := append([]any(nil), jwksObject["keys"].([]any)...)
	duplicateKeys = append(duplicateKeys, duplicateKeys[0])
	ambiguous, _ := json.Marshal(map[string]any{"keys": duplicateKeys})
	wrongKID := testJWKS(t, &key.PublicKey, "another-key")
	wrongAlgorithm, _ := json.Marshal(map[string]any{"keys": []any{map[string]any{
		"kty": "RSA", "kid": "key-1", "alg": "RS512", "n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
		"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
	}}})
	weak, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	weakJWKS := testJWKS(t, &weak.PublicKey, "key-1")
	for name, jwks := range map[string][]byte{
		"unknown kid": wrongKID, "duplicate kid": ambiguous, "wrong key algorithm": wrongAlgorithm, "weak RSA key": weakJWKS,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := VerifyIDTokenWithOptions(token, jwks, fixedVerifyOptions(now, nil)); !errors.Is(err, ErrInvalidJWKS) {
				t.Fatalf("VerifyIDTokenWithOptions() error = %v, want ErrInvalidJWKS", err)
			}
		})
	}
}

func TestVerifyIDTokenRejectsDuplicateJSONAndUntrustedKeyURL(t *testing.T) {
	now := time.Unix(1_900_000_000, 0)
	key := signingKey(t)
	claims, err := json.Marshal(validClaims(now))
	if err != nil {
		t.Fatal(err)
	}
	for name, header := range map[string][]byte{
		"duplicate algorithm": []byte(`{"alg":"RS256","alg":"none","kid":"key-1"}`),
		"token key URL":       []byte(`{"alg":"RS256","kid":"key-1","jku":"https://attacker.test/jwks"}`),
	} {
		t.Run(name, func(t *testing.T) {
			token := signedRawToken(t, key, header, claims)
			if _, err := VerifyIDTokenWithOptions(token, testJWKS(t, &key.PublicKey, "key-1"), fixedVerifyOptions(now, nil)); !errors.Is(err, ErrInvalidToken) {
				t.Fatalf("VerifyIDTokenWithOptions() error = %v, want ErrInvalidToken", err)
			}
		})
	}
}

func TestDiscoveryBoundsAndValidatesEndpoints(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/.well-known/openid-configuration" {
			http.NotFound(w, r)
			return
		}
		_, _ = fmt.Fprintf(w, `{"issuer":%q,"authorization_endpoint":%q,"token_endpoint":%q,"jwks_uri":%q}`,
			Issuer, server.URL+"/authorize", server.URL+"/token", server.URL+"/jwks")
	}))
	defer server.Close()
	discovery, err := Discover(context.Background(), server.Client(), server.URL, true)
	if err != nil {
		t.Fatal(err)
	}
	if discovery.Issuer != Issuer || discovery.JWKSURI != server.URL+"/jwks" {
		t.Fatalf("unexpected discovery: %#v", discovery)
	}
	if _, err := Discover(context.Background(), server.Client(), server.URL, false); err == nil {
		t.Fatal("Discover accepted HTTP when allowHTTP was false")
	}
}

func TestDiscoveryRejectsIssuerRedirectAndOversizedResponse(t *testing.T) {
	t.Run("wrong issuer", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = fmt.Fprintf(w, `{"issuer":"https://issuer.attacker.test","authorization_endpoint":"https://auth.openai.com/a","token_endpoint":"https://auth.openai.com/t","jwks_uri":"https://auth.openai.com/j"}`)
		}))
		defer server.Close()
		if _, err := Discover(context.Background(), server.Client(), server.URL, true); err == nil {
			t.Fatal("Discover accepted an unexpected issuer")
		}
	})
	t.Run("redirect", func(t *testing.T) {
		target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{}`)) }))
		defer target.Close()
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, target.URL, http.StatusFound)
		}))
		defer server.Close()
		if _, err := Discover(context.Background(), server.Client(), server.URL, true); err == nil {
			t.Fatal("Discover followed an HTTP redirect")
		}
	})
	t.Run("oversized", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Length", fmt.Sprint(MaxDiscoveryBytes+1))
			_, _ = w.Write([]byte(strings.Repeat("x", MaxDiscoveryBytes+1)))
		}))
		defer server.Close()
		if _, err := Discover(context.Background(), server.Client(), server.URL, true); err == nil {
			t.Fatal("Discover accepted an oversized response")
		}
	})
}

func TestFetchJWKSBoundsAndChecksStructure(t *testing.T) {
	key := signingKey(t)
	good := testJWKS(t, &key.PublicKey, "key-1")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/jwks":
			_, _ = w.Write(good)
		case "/bad":
			_, _ = w.Write([]byte(`{"keys":[null]}`))
		case "/large":
			_, _ = w.Write([]byte(strings.Repeat("x", MaxJWKSBytes+1)))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	if _, err := FetchJWKS(context.Background(), server.Client(), server.URL+"/jwks", true); err != nil {
		t.Fatal(err)
	}
	for path, wantInvalidJWKS := range map[string]bool{"/bad": true, "/large": false} {
		_, err := FetchJWKS(context.Background(), server.Client(), server.URL+path, true)
		if err == nil || wantInvalidJWKS && !errors.Is(err, ErrInvalidJWKS) {
			t.Fatalf("FetchJWKS(%s) error = %v", path, err)
		}
	}
}

func TestScopesAndSubjectConsistency(t *testing.T) {
	scopes, err := RequireScope("openid profile chatgpt.tokens.use.direct")
	if err != nil || len(scopes) != 3 || scopes[2] != RequiredScope {
		t.Fatalf("RequireScope() = %#v, %v", scopes, err)
	}
	if _, err := RequireScope("openid profile"); !errors.Is(err, ErrMissingScope) {
		t.Fatalf("RequireScope() error = %v, want ErrMissingScope", err)
	}
	if _, err := ParseScopes("openid\nchatgpt.tokens.use.direct"); err == nil {
		t.Fatal("ParseScopes accepted a line break")
	}
	if err := CheckSubjectConsistency("subject-1", "subject-1"); err != nil {
		t.Fatal(err)
	}
	for _, values := range [][2]string{{"subject-1", "subject-2"}, {"", "subject-1"}, {"subject-1", ""}} {
		if err := CheckSubjectConsistency(values[0], values[1]); !errors.Is(err, ErrSubjectChange) {
			t.Fatalf("CheckSubjectConsistency(%q, %q) error = %v", values[0], values[1], err)
		}
	}
}
