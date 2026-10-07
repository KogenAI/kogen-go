package refresh

import (
	"context"
	"errors"
	"time"

	"kogen-go/internal/auth/vault"
)

const (
	refreshSkew        = 300 * time.Second
	refreshHTTPTimeout = 20 * time.Second
)

var (
	ErrInvalidLock        = errors.New("refresh: provider, label, home, or context is invalid")
	ErrLockUnavailable    = errors.New("refresh: credential lock is unavailable")
	ErrUnsafeLock         = errors.New("refresh: credential lock path is unsafe")
	ErrLockWaitTimeout    = errors.New("refresh: timed out waiting for credential lock")
	ErrLockOwnership      = errors.New("refresh: credential lock ownership changed")
	ErrCredentialMissing  = errors.New("refresh: saved credential is missing")
	ErrRefreshUnavailable = errors.New("refresh: token refresh is unavailable")
	ErrRejectedToken      = errors.New("refresh: rejected access token is required")
	ErrAccountChanged     = errors.New("refresh: account identity changed during token refresh")
)

// LockTimeoutError retains the provider-specific public message while
// supporting errors.Is(err, ErrLockWaitTimeout).
type LockTimeoutError struct {
	Provider string
}

func (e *LockTimeoutError) Error() string {
	if e != nil && e.Provider == "grok" {
		return "Grok session refresh timed out."
	}
	return "timed out waiting for ChatGPT token refresh"
}

func (*LockTimeoutError) Unwrap() error { return ErrLockWaitTimeout }

// ChatGPTRefresher performs one OAuth refresh for the supplied saved
// credential. It must honor ctx. The coordinator bounds ctx to 20 seconds,
// persists the returned credential while still holding the refresh lock, and
// never passes injected credentials to this function.
type ChatGPTRefresher func(context.Context, vault.Credential) (vault.Credential, error)

// GrokRefresher performs one OAuth refresh for the supplied saved credential.
// It must honor ctx. The coordinator bounds ctx to 20 seconds and persists the
// returned credential before releasing the refresh lock.
type GrokRefresher func(context.Context, vault.GrokCredential) (vault.GrokCredential, error)

// ChatGPTForRequest returns a saved ChatGPT credential with more than the
// five-minute refresh window remaining. A credential expiring within that
// window is re-read under the per-label lock and refreshed once if still
// necessary.
func ChatGPTForRequest(ctx context.Context, home, label string, store *vault.Store, refresh ChatGPTRefresher) (vault.Credential, error) {
	if ctx == nil || store == nil {
		return vault.Credential{}, ErrInvalidLock
	}
	credential, found, err := store.GetChatGPT(label)
	if err != nil {
		return vault.Credential{}, err
	}
	if !found {
		return vault.Credential{}, ErrCredentialMissing
	}
	if !expiresSoon(credential.ExpiresAt, time.Now()) {
		return credential, nil
	}
	updated, _, err := chatGPTUnderLock(ctx, home, label, store, false, "", refresh)
	return updated, err
}

// ChatGPTAfterUnauthorized handles one provider 401. It locks and re-reads the
// saved credential; if another request already replaced rejectedAccessToken it
// returns that current credential without another OAuth refresh. The bool says
// whether this call performed and persisted a refresh.
func ChatGPTAfterUnauthorized(ctx context.Context, home, label, rejectedAccessToken string, store *vault.Store, refresh ChatGPTRefresher) (vault.Credential, bool, error) {
	if rejectedAccessToken == "" {
		return vault.Credential{}, false, ErrRejectedToken
	}
	if ctx == nil || store == nil {
		return vault.Credential{}, false, ErrInvalidLock
	}
	return chatGPTUnderLock(ctx, home, label, store, true, rejectedAccessToken, refresh)
}

// GrokForRequest returns a saved Grok credential with more than the five-minute
// refresh window remaining, refreshing under the provider/label lock when
// required.
func GrokForRequest(ctx context.Context, home, label string, store *vault.Store, refresh GrokRefresher) (vault.GrokCredential, error) {
	if ctx == nil || store == nil {
		return vault.GrokCredential{}, ErrInvalidLock
	}
	credential, found, err := store.GetGrok(label)
	if err != nil {
		return vault.GrokCredential{}, err
	}
	if !found {
		return vault.GrokCredential{}, ErrCredentialMissing
	}
	if !expiresSoon(credential.ExpiresAt, time.Now()) {
		return credential, nil
	}
	updated, _, err := grokUnderLock(ctx, home, label, store, false, "", refresh)
	return updated, err
}

// GrokAfterUnauthorized handles one provider 401 and refreshes at most once,
// only if the current stored access token is still the rejected token.
func GrokAfterUnauthorized(ctx context.Context, home, label, rejectedAccessToken string, store *vault.Store, refresh GrokRefresher) (vault.GrokCredential, bool, error) {
	if rejectedAccessToken == "" {
		return vault.GrokCredential{}, false, ErrRejectedToken
	}
	if ctx == nil || store == nil {
		return vault.GrokCredential{}, false, ErrInvalidLock
	}
	return grokUnderLock(ctx, home, label, store, true, rejectedAccessToken, refresh)
}

type credentialOps[T any] struct {
	load      func(string) (T, bool, error)
	save      func(string, T) error
	access    func(T) string
	expiresAt func(T) int64
	refreshOK func(T, T) (T, error)
}

func chatGPTUnderLock(ctx context.Context, home, label string, store *vault.Store, forced bool, rejected string, refresh ChatGPTRefresher) (vault.Credential, bool, error) {
	ops := credentialOps[vault.Credential]{
		load:      store.GetChatGPT,
		save:      store.PutChatGPT,
		access:    func(credential vault.Credential) string { return credential.AccessToken },
		expiresAt: func(credential vault.Credential) int64 { return credential.ExpiresAt },
		refreshOK: func(current, updated vault.Credential) (vault.Credential, error) {
			if current.ClientID != updated.ClientID || current.Subject != updated.Subject || current.HostID != updated.HostID {
				return vault.Credential{}, ErrAccountChanged
			}
			if updated.RefreshToken == "" {
				updated.RefreshToken = current.RefreshToken
			}
			return updated, nil
		},
	}
	return coordinate(ctx, home, "chatgpt", label, forced, rejected, ops,
		func(ctx context.Context, current vault.Credential) (vault.Credential, error) {
			if refresh == nil {
				return vault.Credential{}, ErrRefreshUnavailable
			}
			return refresh(ctx, current)
		})
}

func grokUnderLock(ctx context.Context, home, label string, store *vault.Store, forced bool, rejected string, refresh GrokRefresher) (vault.GrokCredential, bool, error) {
	ops := credentialOps[vault.GrokCredential]{
		load:      store.GetGrok,
		save:      store.PutGrok,
		access:    func(credential vault.GrokCredential) string { return credential.AccessToken },
		expiresAt: func(credential vault.GrokCredential) int64 { return credential.ExpiresAt },
		refreshOK: func(current, updated vault.GrokCredential) (vault.GrokCredential, error) {
			if current.ClientID != updated.ClientID || current.TokenEndpoint != updated.TokenEndpoint {
				return vault.GrokCredential{}, ErrAccountChanged
			}
			if updated.RefreshToken == "" {
				updated.RefreshToken = current.RefreshToken
			}
			return updated, nil
		},
	}
	return coordinate(ctx, home, "grok", label, forced, rejected, ops,
		func(ctx context.Context, current vault.GrokCredential) (vault.GrokCredential, error) {
			if refresh == nil {
				return vault.GrokCredential{}, ErrRefreshUnavailable
			}
			return refresh(ctx, current)
		})
}

func coordinate[T any](ctx context.Context, home, provider, label string, forced bool, rejected string, ops credentialOps[T], refresh func(context.Context, T) (T, error)) (result T, refreshed bool, resultErr error) {
	lock, err := Acquire(ctx, home, provider, label)
	if err != nil {
		return result, false, err
	}
	defer func() {
		if err := lock.Release(); err != nil && resultErr == nil {
			var zero T
			result = zero
			refreshed = false
			resultErr = err
		}
	}()

	current, found, err := ops.load(label)
	if err != nil {
		return result, false, err
	}
	if !found {
		return result, false, ErrCredentialMissing
	}
	if forced {
		if ops.access(current) != rejected {
			return current, false, nil
		}
	} else if !expiresSoon(ops.expiresAt(current), time.Now()) {
		return current, false, nil
	}

	refreshCtx, cancel := context.WithTimeout(ctx, refreshHTTPTimeout)
	defer cancel()
	updated, err := refresh(refreshCtx, current)
	if err != nil {
		return result, false, err
	}
	updated, err = ops.refreshOK(current, updated)
	if err != nil {
		return result, false, err
	}
	if ops.access(updated) == "" {
		return result, false, vault.ErrCredentialCorrupt
	}
	if err := ops.save(label, updated); err != nil {
		return result, false, err
	}
	return updated, true, nil
}

func expiresSoon(expiresAt int64, now time.Time) bool {
	return expiresAt <= now.Add(refreshSkew).Unix()
}
