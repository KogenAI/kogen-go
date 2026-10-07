package vault

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const testHostID = "urn:uuid:123e4567-e89b-42d3-a456-426614174000"

func openTestStore(t *testing.T) (*Store, string) {
	t.Helper()
	home := t.TempDir()
	store, err := Open(home)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store, home
}

func chatGPTFixture(accessToken string) Credential {
	email := "person@example.test"
	return Credential{
		ClientID:     "client-id",
		AccessToken:  accessToken,
		RefreshToken: "refresh-secret",
		IDToken:      "id-token-secret",
		ExpiresAt:    time.Now().Add(time.Hour).Unix(),
		Scopes:       []string{"openid", "offline_access"},
		Subject:      "subject-id",
		Email:        &email,
		HostID:       testHostID,
	}
}

func grokFixture(accessToken string) GrokCredential {
	email := "grok@example.test"
	return GrokCredential{
		AccessToken:   accessToken,
		RefreshToken:  "grok-refresh-secret",
		ExpiresAt:     time.Now().Add(time.Hour).Unix(),
		Scopes:        []string{"openid", "offline_access"},
		Email:         &email,
		ClientID:      "grok-client",
		TokenEndpoint: "https://auth.example.test/token",
	}
}

func TestPrivateStoreAtomicFilesProfilesAndHostIDDurability(t *testing.T) {
	store, home := openTestStore(t)
	if err := store.PutChatGPT("default", chatGPTFixture("chatgpt-access-secret")); err != nil {
		t.Fatal(err)
	}
	if err := store.PutGrok("work", grokFixture("grok-access-secret")); err != nil {
		t.Fatal(err)
	}
	shown := true
	if err := store.PutChatGPTProfile("default", ChatGPTProfile{
		ClientID: "client-id", Subject: "subject-id", SignedIn: true, NoticeShown: &shown,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutGrokProfile("work", GrokProfile{SignedIn: true}); err != nil {
		t.Fatal(err)
	}
	firstID, err := store.HostID()
	if err != nil || !validHostID(firstID) {
		t.Fatalf("HostID() = %q, %v", firstID, err)
	}

	for name, wantMode := range map[string]os.FileMode{
		".kogen":             0o700,
		".kogen/credentials": 0o700,
		".kogen/credentials/chatgpt-default.json": 0o600,
		".kogen/credentials/grok-work.json":       0o600,
		".kogen/profiles.json":                    0o600,
		".kogen/host.json":                        0o600,
	} {
		info, err := os.Stat(filepath.Join(home, name))
		if err != nil {
			t.Fatalf("stat %s: %v", name, err)
		}
		if got := info.Mode().Perm(); got != wantMode {
			t.Errorf("%s mode = %04o, want %04o", name, got, wantMode)
		}
	}

	credentialPath := filepath.Join(home, credentialsDir, "chatgpt-default.json")
	oldBytes, err := os.ReadFile(credentialPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutChatGPT("default", chatGPTFixture("replacement-access-secret")); err != nil {
		t.Fatal(err)
	}
	newBytes, err := os.ReadFile(credentialPath)
	if err != nil || string(newBytes) == string(oldBytes) {
		t.Fatalf("credential replacement did not publish new bytes: err=%v", err)
	}
	entries, err := os.ReadDir(filepath.Dir(credentialPath))
	if err != nil || len(entries) != 2 {
		t.Fatalf("credential directory entries = %d, %v; want only the two published records", len(entries), err)
	}

	got, found, err := store.GetChatGPT("default")
	if err != nil || !found || got.AccessToken != "replacement-access-secret" {
		t.Fatalf("GetChatGPT() found=%t err=%v credential=%#v", found, err, got)
	}
	profiles, err := store.ReadProfiles()
	if err != nil || !profiles.ChatGPT["default"].SignedIn || !profiles.Grok["work"].SignedIn {
		t.Fatalf("ReadProfiles() = %#v, %v", profiles, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(home)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	secondID, err := reopened.HostID()
	if err != nil || secondID != firstID {
		t.Fatalf("host identity changed after reopen: %q => %q, %v", firstID, secondID, err)
	}
}

func TestCorruptCredentialCanBeReloggedAndLoggedOutWithoutParsing(t *testing.T) {
	store, home := openTestStore(t)
	path := filepath.Join(home, credentialsDir, "chatgpt-default.json")
	if err := os.WriteFile(path, []byte(`{"access_token":`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.GetChatGPT("default"); !found || !errors.Is(err, ErrCredentialCorrupt) {
		t.Fatalf("corrupt GetChatGPT = found %t, err %v", found, err)
	}
	if credential, unreadable, err := store.GetChatGPTForLogin("default"); err != nil || credential != nil || !unreadable {
		t.Fatalf("GetChatGPTForLogin = %#v, unreadable %t, err %v", credential, unreadable, err)
	}
	if err := store.PutChatGPT("default", chatGPTFixture("new-login-secret")); err != nil {
		t.Fatalf("relogin did not replace corrupt data: %v", err)
	}
	if _, found, err := store.GetChatGPT("default"); err != nil || !found {
		t.Fatalf("relogin credential unavailable: found=%t err=%v", found, err)
	}

	if err := os.WriteFile(path, []byte("broken again"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.LogoutChatGPT("default", false); err != nil {
		t.Fatalf("logout of corrupt credential failed: %v", err)
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("corrupt credential remains after logout: %v", err)
	}
	profiles, err := store.ReadProfiles()
	if err != nil || profiles.ChatGPT["default"].SignedIn || profiles.ChatGPT["default"].RemoteRevoked {
		t.Fatalf("local logout profile = %#v, %v", profiles.ChatGPT["default"], err)
	}

	grokPath := filepath.Join(home, credentialsDir, "grok-bad.json")
	if err := os.WriteFile(grokPath, []byte("not-json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.LogoutGrok("bad"); err != nil {
		t.Fatalf("Grok logout of corrupt credential failed: %v", err)
	}
	if _, err := os.Lstat(grokPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Grok corrupt credential remains after logout: %v", err)
	}
}

func TestStoreRejectsUnsafeParentsAndNeverFollowsCredentialLeafOnWrite(t *testing.T) {
	t.Run("overly permissive state directory", func(t *testing.T) {
		home := t.TempDir()
		if err := os.Mkdir(filepath.Join(home, stateDirName), 0o755); err != nil {
			t.Fatal(err)
		}
		if store, err := Open(home); err == nil {
			_ = store.Close()
			t.Fatal("Open accepted a group/world-accessible state directory")
		}
	})

	t.Run("state symlink", func(t *testing.T) {
		home := t.TempDir()
		outside := t.TempDir()
		if err := os.Symlink(outside, filepath.Join(home, stateDirName)); err != nil {
			t.Fatal(err)
		}
		if store, err := Open(home); err == nil {
			_ = store.Close()
			t.Fatal("Open accepted a symlinked state directory")
		}
		if _, err := os.Stat(filepath.Join(outside, "credentials")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("outside directory was modified through state symlink: %v", err)
		}
	})

	t.Run("credential parent symlink", func(t *testing.T) {
		home := t.TempDir()
		outside := t.TempDir()
		if err := os.Mkdir(filepath.Join(home, stateDirName), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(home, credentialsDir)); err != nil {
			t.Fatal(err)
		}
		if store, err := Open(home); err == nil {
			_ = store.Close()
			t.Fatal("Open accepted a symlinked credential directory")
		}
		if _, err := os.Stat(filepath.Join(outside, "chatgpt-default.json")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("outside directory received a credential: %v", err)
		}
	})

	t.Run("symlink leaf replacement", func(t *testing.T) {
		store, home := openTestStore(t)
		outside := t.TempDir()
		target := filepath.Join(outside, "target")
		if err := os.WriteFile(target, []byte("untouched-outside-secret"), 0o600); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(home, credentialsDir, "chatgpt-default.json")
		if err := os.Symlink(target, path); err != nil {
			t.Fatal(err)
		}
		if err := store.PutChatGPT("default", chatGPTFixture("new-secret")); err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(target)
		if err != nil || string(got) != "untouched-outside-secret" {
			t.Fatalf("symlink target changed: %q, %v", got, err)
		}
		info, err := os.Lstat(path)
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			t.Fatalf("credential leaf was not replaced as a regular file: %v, %v", info, err)
		}
	})

	t.Run("hardlink alias refused", func(t *testing.T) {
		store, home := openTestStore(t)
		path := filepath.Join(home, credentialsDir, "chatgpt-default.json")
		if err := os.WriteFile(path, []byte("original-private-bytes"), 0o600); err != nil {
			t.Fatal(err)
		}
		alias := filepath.Join(home, "outside-alias")
		if err := os.Link(path, alias); err != nil {
			t.Fatal(err)
		}
		if err := store.PutChatGPT("default", chatGPTFixture("new-secret")); err == nil {
			t.Fatal("PutChatGPT replaced a multiply linked credential")
		}
		for _, name := range []string{path, alias} {
			got, err := os.ReadFile(name)
			if err != nil || string(got) != "original-private-bytes" {
				t.Fatalf("hardlink alias %s changed: %q, %v", name, got, err)
			}
		}
	})
}

func TestInjectedReaderRereadsFileAndDoesNotRefresh(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	now := time.Now().Truncate(time.Second)
	reader := NewInjectedReader(path)
	reader.now = func() time.Time { return now }
	firstBody := injectedJSON(t, jwtWithExpiration(now.Add(time.Hour).Unix()), "account-one")
	if err := os.WriteFile(path, firstBody, 0o600); err != nil {
		t.Fatal(err)
	}
	first, err := reader.Read()
	if err != nil || first.AccountID != "account-one" || first.AccessToken != jwtWithExpiration(now.Add(time.Hour).Unix()) {
		t.Fatalf("first injected read = %#v, %v", first, err)
	}
	secondToken := jwtWithExpiration(now.Add(2 * time.Hour).Unix())
	secondBody := injectedJSON(t, secondToken, "account-two")
	if err := os.WriteFile(path, secondBody, 0o600); err != nil {
		t.Fatal(err)
	}
	second, err := reader.Read()
	if err != nil || second.AccountID != "account-two" || second.AccessToken != secondToken {
		t.Fatalf("second injected read did not observe file update: %#v, %v", second, err)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(secondBody) {
		t.Fatalf("injected read modified file: %v", err)
	}
	if got := fmt.Sprintf("%#v", second); strings.Contains(got, secondToken) || strings.Contains(got, "account-two") {
		t.Fatalf("injected credential formatting leaked identity or secret: %s", got)
	}
}

func TestInjectedAuthRejectsMissingMalformedAndExpiredWithoutLeakingToken(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	for _, test := range []struct {
		name string
		body []byte
		want error
	}{
		{name: "bad document", body: []byte("not-json"), want: ErrInjectedInvalid},
		{name: "missing access token", body: []byte(`{"tokens":{"account_id":"acct"}}`), want: ErrInjectedInvalid},
		{name: "malformed jwt", body: injectedJSON(t, "sensitive-bad-token", "acct"), want: ErrInjectedInvalid},
		{name: "expired", body: injectedJSON(t, jwtWithExpiration(now.Add(-time.Second).Unix()), "acct"), want: ErrInjectedExpired},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "auth.json")
			if err := os.WriteFile(path, test.body, 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := readInjectedAt(path, now)
			if !errors.Is(err, test.want) {
				t.Fatalf("readInjectedAt error = %v, want %v", err, test.want)
			}
			if strings.Contains(fmt.Sprint(err), "sensitive-bad-token") {
				t.Fatalf("error leaked auth bytes: %v", err)
			}
		})
	}
}

func TestInjectedReaderRejectsNonRegularFilesBeforeReading(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadInjected(path); !errors.Is(err, ErrInjectedUnavailable) {
		t.Fatalf("directory injected-auth source error = %v", err)
	}
	if err := os.Symlink(filepath.Join(t.TempDir(), "missing-target"), path+"-link"); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadInjected(path + "-link"); !errors.Is(err, ErrInjectedUnavailable) {
		t.Fatalf("symlink injected-auth source error = %v", err)
	}
}

func TestCredentialsRedactStringFormattingAndVaultUsesNoSubprocesses(t *testing.T) {
	secret := "argv-must-not-contain-this-secret"
	formatted := fmt.Sprintf("%v %#v %v %#v %v %#v", chatGPTFixture(secret), chatGPTFixture(secret), grokFixture(secret), grokFixture(secret), InjectedCredential{AccessToken: secret}, InjectedCredential{AccessToken: secret})
	if strings.Contains(formatted, secret) {
		t.Fatalf("credential formatting leaked a token: %s", formatted)
	}

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), name, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imported := range file.Imports {
			var importPath string
			if err := json.Unmarshal([]byte(imported.Path.Value), &importPath); err != nil {
				t.Fatal(err)
			}
			if importPath == "os/exec" {
				t.Fatalf("vault production file %s imports os/exec; credential values must never become argv", name)
			}
		}
	}
}

func TestLabelValidationAndLogoutMissingAreSafe(t *testing.T) {
	store, _ := openTestStore(t)
	for _, label := range []string{"", "-bad", "bad/name", strings.Repeat("a", 65)} {
		if err := store.Delete("chatgpt", label); !errors.Is(err, ErrInvalidLabel) {
			t.Errorf("Delete(%q) error = %v, want invalid label", label, err)
		}
	}
	if err := store.Delete("other", "default"); !errors.Is(err, ErrUnsupportedProvider) {
		t.Fatalf("unsupported provider error = %v", err)
	}
	if err := store.LogoutChatGPT("default", false); err != nil {
		t.Fatalf("logout with no stored credential should be idempotent: %v", err)
	}
}

func injectedJSON(t *testing.T, token, accountID string) []byte {
	t.Helper()
	data, err := json.Marshal(map[string]any{"tokens": map[string]string{"access_token": token, "account_id": accountID}})
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func jwtWithExpiration(expiresAt int64) string {
	payload, _ := json.Marshal(map[string]int64{"exp": expiresAt})
	return "eyJhbGciOiJub25lIn0." + base64.RawURLEncoding.EncodeToString(payload) + ".unsigned"
}
