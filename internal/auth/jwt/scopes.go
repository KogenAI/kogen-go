package jwt

import (
	"errors"
	"strings"
)

const maxScopeBytes = 16 << 10

// ParseScopes validates and splits an OAuth scope response. OAuth scope tokens
// are visible ASCII excluding quote and backslash, separated by spaces.
func ParseScopes(value string) ([]string, error) {
	if len(value) == 0 || len(value) > maxScopeBytes {
		return nil, errors.New("jwt: token scopes are invalid")
	}
	for _, character := range []byte(value) {
		if character == ' ' {
			continue
		}
		if character < 0x21 || character > 0x7e || character == '"' || character == '\\' {
			return nil, errors.New("jwt: token scopes are invalid")
		}
	}
	fields := strings.Fields(value)
	if len(fields) == 0 {
		return nil, errors.New("jwt: token scopes are invalid")
	}
	return fields, nil
}

// RequireScope validates the token's scope string and requires the direct
// ChatGPT API scope used by Kogen.
func RequireScope(value string) ([]string, error) {
	scopes, err := ParseScopes(value)
	if err != nil {
		return nil, err
	}
	for _, scope := range scopes {
		if scope == RequiredScope {
			return scopes, nil
		}
	}
	return nil, ErrMissingScope
}

// CheckSubjectConsistency refuses a refresh when the account's stable OIDC
// subject is missing or differs from the credential already stored for its
// label. Use this only for an existing credential; initial login has no prior
// subject to compare.
func CheckSubjectConsistency(existing, current string) error {
	if existing == "" || current == "" || existing != current {
		return ErrSubjectChange
	}
	return nil
}
