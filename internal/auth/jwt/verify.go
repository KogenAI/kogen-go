// Package jwt verifies the narrow OpenID Connect identity token used by
// Kogen's ChatGPT login flow. It accepts RS256 only and never follows key URLs
// supplied by a token.
package jwt

import (
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"
)

const (
	Issuer        = "https://auth.openai.com"
	RequiredScope = "chatgpt.tokens.use.direct"
	maxTokenBytes = 64 << 10
	maxJWKSKeys   = 64
	maxKIDBytes   = 256
	minRSABits    = 2048
	maxRSABits    = 8192
)

var (
	ErrInvalidToken  = errors.New("jwt: invalid identity token")
	ErrInvalidJWKS   = errors.New("jwt: invalid JWKS")
	ErrInvalidClaims = errors.New("jwt: identity token claims are invalid")
	ErrMissingScope  = errors.New("jwt: token lacks chatgpt.tokens.use.direct")
	ErrSubjectChange = errors.New("jwt: account subject changed")
)

// Identity contains only the identity claims consumed by the login flow.
// String and GoString intentionally omit values because subject and account
// identifiers are sensitive profile data.
type Identity struct {
	Subject   string
	Email     *string
	ExpiresAt int64
	AccountID *string
	PlanUsage *string
}

func (Identity) String() string   { return "Identity{REDACTED}" }
func (Identity) GoString() string { return "Identity{REDACTED}" }

// VerifyOptions supplies the client ID, optional login nonce, and a clock for
// deterministic callers. A nil Now uses the current wall clock.
type VerifyOptions struct {
	ClientID      string
	ExpectedNonce *string
	Now           func() time.Time
}

// VerifyIDToken verifies a compact JWT against a JSON JWKS document. An empty
// expectedNonce skips nonce checking, which is appropriate for refreshes; the
// initial authorization-code exchange must provide the nonce returned by its
// authorization request.
func VerifyIDToken(token string, jwksJSON []byte, clientID, expectedNonce string) (Identity, error) {
	var nonce *string
	if expectedNonce != "" {
		nonce = &expectedNonce
	}
	return VerifyIDTokenWithOptions(token, jwksJSON, VerifyOptions{
		ClientID:      clientID,
		ExpectedNonce: nonce,
	})
}

// VerifyIDTokenWithOptions performs strict compact-token, RS256, JWKS, issuer,
// audience, expiry, nonce and subject validation. Payload claims are not
// interpreted until the signature has verified.
func VerifyIDTokenWithOptions(token string, jwksJSON []byte, options VerifyOptions) (Identity, error) {
	if len(token) == 0 || len(token) > maxTokenBytes || options.ClientID == "" {
		return Identity{}, ErrInvalidToken
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return Identity{}, ErrInvalidToken
	}
	headerBytes, err := base64.RawURLEncoding.Strict().DecodeString(parts[0])
	if err != nil {
		return Identity{}, ErrInvalidToken
	}
	header, err := parseJSONObject(headerBytes)
	if err != nil {
		return Identity{}, ErrInvalidToken
	}
	alg, ok := header["alg"].(string)
	if !ok || alg != "RS256" {
		return Identity{}, fmt.Errorf("%w: only RS256 is accepted", ErrInvalidToken)
	}
	kid, ok := header["kid"].(string)
	if !ok || kid == "" || len(kid) > maxKIDBytes {
		return Identity{}, fmt.Errorf("%w: signing key id is missing", ErrInvalidToken)
	}
	// Extensions that alter the signed representation are not implemented.
	if _, exists := header["crit"]; exists {
		return Identity{}, fmt.Errorf("%w: critical header extensions are unsupported", ErrInvalidToken)
	}
	if b64, exists := header["b64"]; exists {
		if value, valid := b64.(bool); !valid || !value {
			return Identity{}, fmt.Errorf("%w: unencoded payloads are unsupported", ErrInvalidToken)
		}
	}
	if _, exists := header["jku"]; exists {
		return Identity{}, fmt.Errorf("%w: token-provided key URLs are unsupported", ErrInvalidToken)
	}
	if _, exists := header["x5u"]; exists {
		return Identity{}, fmt.Errorf("%w: token-provided key URLs are unsupported", ErrInvalidToken)
	}

	key, err := keyForID(jwksJSON, kid)
	if err != nil {
		return Identity{}, err
	}
	signature, err := base64.RawURLEncoding.Strict().DecodeString(parts[2])
	if err != nil || len(signature) != key.Size() {
		return Identity{}, ErrInvalidToken
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(key, crypto.SHA256, digest[:], signature); err != nil {
		return Identity{}, fmt.Errorf("%w: signature verification failed", ErrInvalidToken)
	}

	payloadBytes, err := base64.RawURLEncoding.Strict().DecodeString(parts[1])
	if err != nil {
		return Identity{}, ErrInvalidClaims
	}
	claims, err := parseJSONObject(payloadBytes)
	if err != nil {
		return Identity{}, ErrInvalidClaims
	}
	identity, err := identityFromClaims(claims, options)
	if err != nil {
		return Identity{}, err
	}
	return identity, nil
}

func identityFromClaims(claims map[string]any, options VerifyOptions) (Identity, error) {
	issuer, ok := claims["iss"].(string)
	if !ok || issuer != Issuer {
		return Identity{}, fmt.Errorf("%w: issuer is invalid", ErrInvalidClaims)
	}
	if !audienceContains(claims["aud"], options.ClientID) {
		return Identity{}, fmt.Errorf("%w: audience is invalid", ErrInvalidClaims)
	}
	issuedExpiry, ok := claims["exp"].(jsonNumber)
	if !ok {
		return Identity{}, fmt.Errorf("%w: expiration is invalid", ErrInvalidClaims)
	}
	expiresAt, err := issuedExpiry.int64()
	if err != nil {
		return Identity{}, fmt.Errorf("%w: expiration is invalid", ErrInvalidClaims)
	}
	now := time.Now()
	if options.Now != nil {
		now = options.Now()
	}
	if expiresAt <= now.Unix() {
		return Identity{}, fmt.Errorf("%w: token has expired", ErrInvalidClaims)
	}
	subject, ok := claims["sub"].(string)
	if !ok || subject == "" {
		return Identity{}, fmt.Errorf("%w: subject is missing", ErrInvalidClaims)
	}
	if options.ExpectedNonce != nil {
		nonce, ok := claims["nonce"].(string)
		if !ok || nonce != *options.ExpectedNonce {
			return Identity{}, fmt.Errorf("%w: nonce is invalid", ErrInvalidClaims)
		}
	}

	identity := Identity{Subject: subject, ExpiresAt: expiresAt}
	if email, ok := claims["email"].(string); ok {
		identity.Email = stringPointer(email)
	}
	if auth, ok := claims["https://api.openai.com/auth"].(map[string]any); ok {
		if accountID, ok := auth["chatgpt_account_id"].(string); ok {
			identity.AccountID = stringPointer(accountID)
		}
		if planUsage, ok := auth["chatgpt_plan_type"].(string); ok {
			identity.PlanUsage = stringPointer(planUsage)
		}
	}
	return identity, nil
}

func stringPointer(value string) *string { return &value }

func audienceContains(value any, clientID string) bool {
	switch audience := value.(type) {
	case string:
		return audience == clientID
	case []any:
		if len(audience) == 0 {
			return false
		}
		found := false
		for _, item := range audience {
			stringItem, ok := item.(string)
			if !ok || stringItem == "" {
				return false
			}
			if stringItem == clientID {
				found = true
			}
		}
		return found
	default:
		return false
	}
}

func keyForID(jwksJSON []byte, kid string) (*rsa.PublicKey, error) {
	if len(jwksJSON) == 0 || len(jwksJSON) > MaxJWKSBytes {
		return nil, ErrInvalidJWKS
	}
	root, err := parseJSONObject(jwksJSON)
	if err != nil {
		return nil, ErrInvalidJWKS
	}
	keys, ok := root["keys"].([]any)
	if !ok || len(keys) == 0 || len(keys) > maxJWKSKeys {
		return nil, ErrInvalidJWKS
	}
	var selected map[string]any
	matches := 0
	for _, value := range keys {
		entry, ok := value.(map[string]any)
		if !ok {
			return nil, ErrInvalidJWKS
		}
		entryID, _ := entry["kid"].(string)
		if entryID == kid {
			selected = entry
			matches++
		}
	}
	if matches != 1 {
		return nil, fmt.Errorf("%w: signing key is missing or ambiguous", ErrInvalidJWKS)
	}
	if selected["kty"] != "RSA" {
		return nil, fmt.Errorf("%w: signing key is not RSA", ErrInvalidJWKS)
	}
	if algorithm, present := selected["alg"]; present && algorithm != "RS256" {
		return nil, fmt.Errorf("%w: signing key algorithm is not RS256", ErrInvalidJWKS)
	}
	if usage, present := selected["use"]; present && usage != "sig" {
		return nil, fmt.Errorf("%w: key is not a signature key", ErrInvalidJWKS)
	}
	if operations, present := selected["key_ops"]; present {
		list, valid := operations.([]any)
		if !valid {
			return nil, ErrInvalidJWKS
		}
		canVerify := false
		for _, operation := range list {
			name, valid := operation.(string)
			if !valid {
				return nil, ErrInvalidJWKS
			}
			if name == "verify" {
				canVerify = true
			}
		}
		if !canVerify {
			return nil, fmt.Errorf("%w: key cannot verify signatures", ErrInvalidJWKS)
		}
	}
	for _, member := range []string{"d", "p", "q", "dp", "dq", "qi", "oth"} {
		if _, present := selected[member]; present {
			return nil, fmt.Errorf("%w: private key material is not accepted", ErrInvalidJWKS)
		}
	}

	modulusText, okN := selected["n"].(string)
	exponentText, okE := selected["e"].(string)
	if !okN || !okE || modulusText == "" || exponentText == "" {
		return nil, ErrInvalidJWKS
	}
	modulus, err := base64.RawURLEncoding.Strict().DecodeString(modulusText)
	if err != nil || len(modulus) == 0 || modulus[0] == 0 || len(modulus) > maxRSABits/8 {
		return nil, ErrInvalidJWKS
	}
	exponent, err := base64.RawURLEncoding.Strict().DecodeString(exponentText)
	if err != nil || len(exponent) == 0 || len(exponent) > 4 || exponent[0] == 0 {
		return nil, ErrInvalidJWKS
	}
	publicExponent := 0
	for _, value := range exponent {
		publicExponent = publicExponent<<8 | int(value)
	}
	publicModulus := new(big.Int).SetBytes(modulus)
	if publicModulus.BitLen() < minRSABits || publicModulus.BitLen() > maxRSABits ||
		publicModulus.Sign() <= 0 || publicModulus.Bit(0) != 1 || publicExponent < 3 || publicExponent%2 == 0 {
		return nil, fmt.Errorf("%w: RSA public key is invalid", ErrInvalidJWKS)
	}
	return &rsa.PublicKey{N: publicModulus, E: publicExponent}, nil
}
