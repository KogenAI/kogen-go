package refresh

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"kogen-go/internal/auth/vault"
)

const testHostID = "urn:uuid:123e4567-e89b-42d3-a456-426614174000"

func TestConcurrentRejectedTokenRefreshRunsOnceAndRereadsStoredToken(t *testing.T) {
	home := t.TempDir()
	store, err := vault.Open(home)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.PutChatGPT("default", testChatGPTCredential("rejected-access")); err != nil {
		t.Fatal(err)
	}

	started := make(chan struct{}, 1)
	continueRefresh := make(chan struct{})
	var calls atomic.Int32
	refresh := func(ctx context.Context, current vault.Credential) (vault.Credential, error) {
		if _, ok := ctx.Deadline(); !ok {
			t.Error("refresh callback did not receive a bounded context")
		}
		if calls.Add(1) == 1 {
			started <- struct{}{}
		}
		select {
		case <-continueRefresh:
			updated := current
			updated.AccessToken = "rotated-access"
			updated.RefreshToken = "rotated-refresh"
			updated.ExpiresAt = time.Now().Add(time.Hour).Unix()
			return updated, nil
		case <-ctx.Done():
			return vault.Credential{}, ctx.Err()
		}
	}
	type result struct {
		credential vault.Credential
		refreshed  bool
		err        error
	}
	results := make(chan result, 2)
	call := func() {
		credential, refreshed, err := ChatGPTAfterUnauthorized(context.Background(), home, "default", "rejected-access", store, refresh)
		results <- result{credential: credential, refreshed: refreshed, err: err}
	}
	go call()
	<-started
	go call()
	// Keep the first refresh in flight long enough for the second caller to
	// observe the owned lock and wait for its post-lock credential reread.
	time.Sleep(60 * time.Millisecond)
	if got := calls.Load(); got != 1 {
		t.Fatalf("refresh callback count while the first call held the lock = %d, want 1", got)
	}
	close(continueRefresh)

	first, second := <-results, <-results
	for index, got := range []result{first, second} {
		if got.err != nil {
			t.Fatalf("call %d failed: %v", index+1, got.err)
		}
		if got.credential.AccessToken != "rotated-access" {
			t.Fatalf("call %d returned stale access token %q", index+1, got.credential.AccessToken)
		}
	}
	if first.refreshed == second.refreshed {
		t.Fatalf("exactly one call must refresh, got refreshed flags %t and %t", first.refreshed, second.refreshed)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("refresh callback count = %d, want 1", got)
	}
	stored, found, err := store.GetChatGPT("default")
	if err != nil || !found || stored.AccessToken != "rotated-access" || stored.RefreshToken != "rotated-refresh" {
		t.Fatalf("rotated credential was not durably stored before reuse: found=%t credential=%#v err=%v", found, stored, err)
	}
	if _, err := os.Stat(filepath.Join(home, ".kogen", "locks", "chatgpt-default.lock")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("refresh lock remained after both calls: %v", err)
	}
}

func TestRefreshLocksAreScopedByProviderAndLabel(t *testing.T) {
	home := t.TempDir()
	store, err := vault.Open(home)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	locks := make([]*Lock, 0, 3)
	for _, key := range [][2]string{{"chatgpt", "default"}, {"chatgpt", "work"}, {"grok", "default"}} {
		lock, err := Acquire(context.Background(), home, key[0], key[1])
		if err != nil {
			t.Fatalf("Acquire(%q, %q): %v", key[0], key[1], err)
		}
		locks = append(locks, lock)
	}
	for _, lock := range locks {
		if err := lock.Release(); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := os.ReadDir(filepath.Join(home, ".kogen", "locks"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("lock directory has leftover entries: %v", entries)
	}
}

func TestEmptyAndTimestampedOwnerStalenessAreRecovered(t *testing.T) {
	for _, test := range []struct {
		name    string
		content string
	}{
		{name: "empty owner uses directory time"},
		{name: "owner timestamp takes precedence", content: fmt.Sprintf("17 %d stale-token\n", time.Now().Add(-2*time.Minute).UnixMilli())},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("KOGEN_TIME_SCALE", "1")
			home := t.TempDir()
			store, err := vault.Open(home)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			if err := os.MkdirAll(filepath.Join(home, ".kogen", "locks"), 0o700); err != nil {
				t.Fatal(err)
			}
			lockPath := filepath.Join(home, ".kogen", "locks", "grok-work.lock")
			if err := os.Mkdir(lockPath, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(lockPath, ownerFileName), []byte(test.content), 0o600); err != nil {
				t.Fatal(err)
			}
			if test.content == "" {
				old := time.Now().Add(-2 * time.Minute)
				if err := os.Chtimes(lockPath, old, old); err != nil {
					t.Fatal(err)
				}
			} else {
				now := time.Now()
				if err := os.Chtimes(lockPath, now, now); err != nil {
					t.Fatal(err)
				}
			}

			lock, err := Acquire(context.Background(), home, "grok", "work")
			if err != nil {
				t.Fatalf("Acquire did not recover stale lock: %v", err)
			}
			if err := lock.Release(); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(lockPath); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("stale or released lock remains at %s: %v", lockPath, err)
			}
		})
	}
}

func TestLockWaitIsBoundedAndContextCancelable(t *testing.T) {
	t.Setenv("KOGEN_TIME_SCALE", "0.00001") // Scaled 90 seconds is floored to 1 ms.
	home := t.TempDir()
	store, err := vault.Open(home)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	owner, err := Acquire(context.Background(), home, "chatgpt", "default")
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Release()
	started := time.Now()
	_, err = Acquire(context.Background(), home, "chatgpt", "default")
	var timeout *LockTimeoutError
	if !errors.As(err, &timeout) || timeout.Provider != "chatgpt" {
		t.Fatalf("second Acquire error = %v, want ChatGPT lock timeout", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("scaled lock wait took %s, expected bounded wait under one second", elapsed)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	t.Setenv("KOGEN_TIME_SCALE", "1")
	_, err = Acquire(ctx, home, "chatgpt", "default")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("canceled Acquire error = %v, want context deadline", err)
	}
}

func TestCorruptCredentialStoreNeverInvokesRefresh(t *testing.T) {
	home := t.TempDir()
	store, err := vault.Open(home)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	path := filepath.Join(home, ".kogen", "credentials", "chatgpt-default.json")
	if err := os.WriteFile(path, []byte(`{"access_token":`), 0o600); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	_, err = ChatGPTForRequest(context.Background(), home, "default", store, func(context.Context, vault.Credential) (vault.Credential, error) {
		calls.Add(1)
		return vault.Credential{}, nil
	})
	if !errors.Is(err, vault.ErrCredentialCorrupt) {
		t.Fatalf("ChatGPTForRequest error = %v, want corrupt store", err)
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("refresh callback ran %d times for a corrupt credential", got)
	}
}

func TestExpiryRefreshRereadsAfterAnotherCallerRotatesToken(t *testing.T) {
	home := t.TempDir()
	store, err := vault.Open(home)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.PutGrok("default", testGrokCredential("old-grok-access", time.Now().Add(30*time.Second).Unix())); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{}, 1)
	continueRefresh := make(chan struct{})
	var calls atomic.Int32
	refresh := func(ctx context.Context, current vault.GrokCredential) (vault.GrokCredential, error) {
		calls.Add(1)
		started <- struct{}{}
		select {
		case <-continueRefresh:
			current.AccessToken = "new-grok-access"
			current.ExpiresAt = time.Now().Add(time.Hour).Unix()
			return current, nil
		case <-ctx.Done():
			return vault.GrokCredential{}, ctx.Err()
		}
	}
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() {
			credential, err := GrokForRequest(context.Background(), home, "default", store, refresh)
			if err == nil && credential.AccessToken != "new-grok-access" {
				err = fmt.Errorf("Grok request received stale access token %q", credential.AccessToken)
			}
			results <- err
		}()
		if i == 0 {
			<-started
			time.Sleep(40 * time.Millisecond)
		}
	}
	time.Sleep(40 * time.Millisecond)
	if got := calls.Load(); got != 1 {
		t.Fatalf("Grok refresh callback count while first refresh is blocked = %d, want 1", got)
	}
	close(continueRefresh)
	for range 2 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("Grok refresh callback count = %d, want 1", got)
	}
}

func TestRefresherCannotChangeStoredAccountIdentity(t *testing.T) {
	home := t.TempDir()
	store, err := vault.Open(home)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.PutChatGPT("default", testChatGPTCredential("access")); err != nil {
		t.Fatal(err)
	}
	_, _, err = ChatGPTAfterUnauthorized(context.Background(), home, "default", "access", store,
		func(_ context.Context, current vault.Credential) (vault.Credential, error) {
			current.AccessToken = "replacement"
			current.Subject = "different-account"
			return current, nil
		})
	if !errors.Is(err, ErrAccountChanged) {
		t.Fatalf("identity-changing refresh error = %v, want ErrAccountChanged", err)
	}
	stored, found, err := store.GetChatGPT("default")
	if err != nil || !found || stored.AccessToken != "access" {
		t.Fatalf("identity-changing credential was persisted: found=%t credential=%#v err=%v", found, stored, err)
	}
}

func TestLockRejectsUnsafeProviderAndLabel(t *testing.T) {
	home := t.TempDir()
	for _, test := range [][2]string{{"other", "default"}, {"chatgpt", "../default"}, {"grok", ""}} {
		if _, err := Acquire(context.Background(), home, test[0], test[1]); !errors.Is(err, ErrInvalidLock) {
			t.Errorf("Acquire(%q, %q) error = %v, want ErrInvalidLock", test[0], test[1], err)
		}
	}
	if !strings.Contains((&LockTimeoutError{Provider: "grok"}).Error(), "Grok session") {
		t.Fatal("Grok timeout message lost provider identity")
	}
}

func testChatGPTCredential(accessToken string) vault.Credential {
	email := "person@example.test"
	return vault.Credential{
		ClientID: "test-client", AccessToken: accessToken, RefreshToken: "test-refresh",
		IDToken: "test-id-token", ExpiresAt: time.Now().Add(time.Hour).Unix(),
		Scopes: []string{"openid", "offline_access"}, Subject: "test-subject", Email: &email,
		HostID: testHostID,
	}
}

func testGrokCredential(accessToken string, expiresAt int64) vault.GrokCredential {
	return vault.GrokCredential{
		AccessToken: accessToken, RefreshToken: "test-grok-refresh", ExpiresAt: expiresAt,
		Scopes: []string{"openid", "offline_access"}, ClientID: "grok-client",
		TokenEndpoint: "https://auth.example.test/token",
	}
}
