package grok

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"kogen-go/internal/auth/vault"
)

func TestDeviceGrantPollAndVaultPersistence(t *testing.T) {
	var polls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			_ = json.NewEncoder(w).Encode(map[string]string{
				"issuer": serverIssuer(r), "token_endpoint": "http://" + r.Host + "/token",
				"device_authorization_endpoint": "http://" + r.Host + "/device/code",
			})
		case "/device/code":
			if err := r.ParseForm(); err != nil {
				t.Errorf("parse device form: %v", err)
			}
			if r.Form.Get("client_id") != ClientID || r.Form.Get("scope") != Scope {
				t.Errorf("device form = %v", r.Form)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"device_code":"private-device-code","user_code":"ABCD-EFGH","verification_uri":"https://x.ai/activate","verification_uri_complete":"https://x.ai/activate?code=ABCD","expires_in":60,"interval":0}`))
		case "/token":
			if err := r.ParseForm(); err != nil {
				t.Errorf("parse token form: %v", err)
			}
			polls++
			if polls == 1 && (r.Form.Get("grant_type") != DeviceGrant || r.Form.Get("device_code") != "private-device-code") {
				t.Errorf("first poll form = %v", r.Form)
			}
			if polls < 3 {
				w.WriteHeader(http.StatusBadRequest)
				if polls == 1 {
					_, _ = w.Write([]byte(`{"error":"authorization_pending"}`))
				} else {
					_, _ = w.Write([]byte(`{"error":"slow_down"}`))
				}
				return
			}
			payload := base64.RawURLEncoding.EncodeToString([]byte(`{"email":"from-id-token@example.test"}`))
			_, _ = w.Write([]byte(`{"access_token":"access-secret","refresh_token":"refresh-secret","expires_in":3600,"scope":"openid api:access","id_token":"header.` + payload + `.signature"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	home := t.TempDir()
	store := openStore(t, home)
	var waits []time.Duration
	client, err := New(Options{
		Home: home, Label: "default", IssuerURL: server.URL, AllowLocalHTTP: true,
		Store: store, TimeScale: 1,
		Sleep: func(ctx context.Context, delay time.Duration) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			waits = append(waits, delay)
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var progress strings.Builder
	credential, err := client.Login(context.Background(), func(line string) { progress.WriteString(line) })
	if err != nil {
		t.Fatal(err)
	}
	if polls != 3 {
		t.Fatalf("device polls = %d, want 3", polls)
	}
	if !reflect.DeepEqual(waits, []time.Duration{time.Second, time.Second, 6 * time.Second}) {
		t.Fatalf("waits = %v", waits)
	}
	if got, want := progress.String(), "Grok sign-in code: ABCD-EFGH\nOpen: https://x.ai/activate?code=ABCD\n"; got != want {
		t.Fatalf("progress = %q, want %q", got, want)
	}
	if credential.AccessToken != "access-secret" || credential.RefreshToken != "refresh-secret" || credential.ClientID != ClientID || credential.TokenEndpoint != server.URL+"/token" {
		t.Fatalf("unexpected persisted credential: %#v", credential)
	}
	if credential.Email == nil || *credential.Email != "from-id-token@example.test" {
		t.Fatalf("email claim was not retained: %#v", credential.Email)
	}
	stored, found, err := store.GetGrok("default")
	if err != nil || !found || stored.AccessToken != credential.AccessToken {
		t.Fatalf("vault credential = found %t, %#v, %v", found, stored, err)
	}
	profiles, err := store.ReadProfiles()
	if err != nil {
		t.Fatal(err)
	}
	profile := profiles.Grok["default"]
	if !profile.SignedIn || profile.Email == nil || *profile.Email != *credential.Email || profile.ExpiresAt != credential.ExpiresAt {
		t.Fatalf("profile row did not follow the saved grant: %#v", profile)
	}
}

func TestRefreshRotationAndForced401Recheck(t *testing.T) {
	var tokenCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/token" {
			http.NotFound(w, r)
			return
		}
		if err := r.ParseForm(); err != nil {
			t.Errorf("parse refresh form: %v", err)
		}
		if r.Form.Get("grant_type") != RefreshGrant || r.Form.Get("client_id") != "grok-client" || r.Form.Get("refresh_token") != "old-refresh" {
			t.Errorf("refresh form = %v", r.Form)
		}
		tokenCalls++
		access := "rotated-access"
		if tokenCalls > 1 {
			access = "after-401-access"
		}
		_, _ = fmt.Fprintf(w, `{"access_token":%q,"expires_in":3600}`, access)
	}))
	defer server.Close()

	home := t.TempDir()
	store := openStore(t, home)
	oldEmail := "grok@example.test"
	if err := store.PutGrok("work", vault.GrokCredential{
		AccessToken: "old-access", RefreshToken: "old-refresh", ExpiresAt: time.Now().Add(-time.Minute).Unix(),
		Scopes: []string{"openid", "api:access"}, Email: &oldEmail, ClientID: "grok-client", TokenEndpoint: server.URL + "/token",
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutGrokProfile("work", vault.GrokProfile{Email: &oldEmail, SignedIn: true}); err != nil {
		t.Fatal(err)
	}
	client, err := New(Options{Home: home, Label: "work", IssuerURL: server.URL, AllowLocalHTTP: true, Store: store, TimeScale: 1})
	if err != nil {
		t.Fatal(err)
	}
	updated, err := client.ForRequest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if updated.AccessToken != "rotated-access" || updated.RefreshToken != "old-refresh" || updated.Email == nil || *updated.Email != oldEmail || !reflect.DeepEqual(updated.Scopes, []string{"openid", "api:access"}) {
		t.Fatalf("refresh did not preserve non-rotated fields: %#v", updated)
	}
	if tokenCalls != 1 {
		t.Fatalf("token refresh calls = %d, want one", tokenCalls)
	}
	profiles, err := store.ReadProfiles()
	if err != nil {
		t.Fatal(err)
	}
	if profiles.Grok["work"].ExpiresAt != updated.ExpiresAt || !profiles.Grok["work"].SignedIn {
		t.Fatalf("profile was not updated with rotated credentials: %#v", profiles.Grok["work"])
	}

	current, refreshed, err := client.AfterUnauthorized(context.Background(), "stale-rejected-token")
	if err != nil || refreshed || current.AccessToken != "rotated-access" {
		t.Fatalf("newer token should win a forced-refresh recheck: %#v %t %v", current, refreshed, err)
	}
	if tokenCalls != 1 {
		t.Fatalf("stale rejection performed an unnecessary refresh: calls=%d", tokenCalls)
	}
	current, refreshed, err = client.AfterUnauthorized(context.Background(), "rotated-access")
	if err != nil || !refreshed || current.AccessToken != "after-401-access" {
		t.Fatalf("forced refresh result = %#v %t %v", current, refreshed, err)
	}
	if tokenCalls != 2 {
		t.Fatalf("forced refresh calls = %d, want 2", tokenCalls)
	}
}

func TestDeviceGrantRefusalsAndLoopbackEndpointPolicy(t *testing.T) {
	badIssuer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"issuer":"https://other.example","token_endpoint":"https://auth.x.ai/token"}`))
	}))
	defer badIssuer.Close()
	home := t.TempDir()
	store := openStore(t, home)
	if _, err := New(Options{Home: home, Store: store, IssuerURL: "http://example.test", AllowLocalHTTP: true}); err == nil {
		t.Fatal("non-loopback HTTP issuer was accepted")
	}
	client, err := New(Options{Home: home, Store: store, IssuerURL: badIssuer.URL, AllowLocalHTTP: true, TimeScale: 1})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Login(context.Background(), nil)
	var authErr *Error
	if !errors.As(err, &authErr) || authErr.Message != "Grok returned an invalid sign-in discovery document." {
		t.Fatalf("discovery issuer mismatch = %v", err)
	}
	if _, err := New(Options{Home: home, Store: store, IssuerURL: "http://127.0.0.1:1455", AllowLocalHTTP: true}); err != nil {
		t.Fatalf("explicit loopback OAuth fixture was rejected: %v", err)
	}
}

func serverIssuer(request *http.Request) string {
	return "http://" + request.Host
}

func openStore(t *testing.T, home string) *vault.Store {
	t.Helper()
	store, err := vault.Open(home)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}
