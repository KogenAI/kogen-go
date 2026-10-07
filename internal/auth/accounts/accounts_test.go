package accounts

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"kogen-go/internal/auth/vault"
)

func TestParseAndMarshalCanonicalAccountsFile(t *testing.T) {
	root := t.TempDir()
	first := filepath.Join(root, "a checkout")
	second := filepath.Join(root, "z-checkout")
	for _, path := range []string{first, second} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	input := "# user comments are accepted\n" +
		"selection:\n  projects:\n    - path: \"" + second + "\"\n      provider: grok\n" +
		"grok:\n  projects:\n    - path: \"" + second + "\"\n      account: zz\n" +
		"chatgpt:\n  default: default\n  projects:\n    - path: \"" + second + "\"\n      account: z\n" +
		"    - path: \"" + first + "\"\n      account: a\n"
	file, err := Parse([]byte(input))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	data, err := Marshal(file)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	got := string(data)
	want := accountsHeader +
		"chatgpt:\n  default: default\n  projects:\n" +
		"    - path: \"" + first + "\"\n      account: a\n" +
		"    - path: \"" + second + "\"\n      account: z\n" +
		"grok:\n  projects:\n    - path: \"" + second + "\"\n      account: zz\n" +
		"selection:\n  projects:\n    - path: \"" + second + "\"\n      provider: grok\n"
	if got != want {
		t.Fatalf("Marshal() =\n%s\nwant:\n%s", got, want)
	}
	roundTrip, err := Parse(data)
	if err != nil || len(roundTrip.ChatGPT.Projects) != 2 || roundTrip.Selection.Projects[0].Provider != Grok {
		t.Fatalf("round trip = %#v, %v", roundTrip, err)
	}
}

func TestParseEmptyMapAndRejectMalformedOrInvalidLabels(t *testing.T) {
	file, err := Parse([]byte("chatgpt: {}\n"))
	if err != nil || file.ChatGPT.Default != "" || len(file.ChatGPT.Projects) != 0 || file.Grok.Default != "" || len(file.Grok.Projects) != 0 || file.Selection.Default != "" || len(file.Selection.Projects) != 0 {
		t.Fatalf("Parse(chatgpt: {}) = %#v, %v", file, err)
	}
	if _, err := Parse([]byte("chatgpt:\n")); !errors.Is(err, ErrInvalidFile) {
		t.Fatalf("bare chatgpt mapping error = %v", err)
	}
	for _, source := range []string{
		"chatgpt:\n  default: .bad\n",
		"chatgpt:\n  default: " + strings.Repeat("a", 65) + "\n",
		"chatgpt:\n  projects: []\n  unknown: value\n",
		"selection:\n  default: other\n",
		"chatgpt:\n  default: first\n  default: second\n",
	} {
		if _, err := Parse([]byte(source)); !errors.Is(err, ErrInvalidFile) {
			t.Errorf("Parse(%q) error = %v, want invalid file", source, err)
		}
	}
	for _, label := range []string{"", ".x", "_x", "-x", "é", "has space", "bad/label", strings.Repeat("a", 65)} {
		if ValidLabel(label) {
			t.Errorf("ValidLabel(%q) = true", label)
		}
	}
	for _, label := range []string{"a", "Z9", "a._-z", strings.Repeat("a", 64)} {
		if !ValidLabel(label) {
			t.Errorf("ValidLabel(%q) = false", label)
		}
	}
}

func TestWriteSortsRowsPurgesMissingDirectoriesAndUsesPrivateAtomicFiles(t *testing.T) {
	home := t.TempDir()
	store, err := Open(home)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	projectA := filepath.Join(home, "a")
	projectZ := filepath.Join(home, "z")
	if err := os.Mkdir(projectA, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(projectZ, 0o700); err != nil {
		t.Fatal(err)
	}
	file := File{
		ChatGPT: ProviderAccounts{
			Default: "default",
			Projects: []AccountProject{
				{Path: projectZ, Account: "work"},
				{Path: filepath.Join(home, "missing"), Account: "old"},
				{Path: projectA, Account: "home"},
			},
		},
		Selection: Selection{Default: ChatGPT, Projects: []ProviderProject{
			{Path: projectZ, Provider: Grok},
			{Path: filepath.Join(home, "missing"), Provider: ChatGPT},
		}},
	}
	if err := store.Write(file); err != nil {
		t.Fatal(err)
	}
	read, err := store.Read()
	if err != nil {
		t.Fatal(err)
	}
	if len(read.ChatGPT.Projects) != 2 || len(read.Selection.Projects) != 1 {
		t.Fatalf("missing directory rows were not purged: %#v", read)
	}
	if read.ChatGPT.Projects[0].Path >= read.ChatGPT.Projects[1].Path {
		t.Fatalf("project rows are not sorted: %#v", read.ChatGPT.Projects)
	}
	for name, wantMode := range map[string]os.FileMode{".kogen": 0o700, accountsPath: 0o600} {
		info, err := os.Stat(filepath.Join(home, name))
		if err != nil || info.Mode().Perm() != wantMode {
			t.Errorf("%s mode = %v, %v; want %04o", name, info, err, wantMode)
		}
	}
	entries, err := os.ReadDir(filepath.Join(home, stateDir))
	if err != nil || len(entries) != 1 || entries[0].Name() != "accounts.yaml" {
		t.Fatalf("state directory has leftover staging entries: %#v, %v", entries, err)
	}
}

func TestWriteReplacesSymlinkLeafWithoutTouchingTarget(t *testing.T) {
	home := t.TempDir()
	if err := os.Mkdir(filepath.Join(home, stateDir), 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "target.yaml")
	if err := os.WriteFile(target, []byte("outside bytes\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(home, accountsPath)); err != nil {
		t.Fatal(err)
	}
	store, err := Open(home)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Write(File{ChatGPT: ProviderAccounts{Default: "default"}}); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(target); err != nil || string(data) != "outside bytes\n" {
		t.Fatalf("symlink target changed: %q, %v", data, err)
	}
	info, err := os.Lstat(filepath.Join(home, accountsPath))
	if err != nil || !info.Mode().IsRegular() {
		t.Fatalf("published accounts leaf = %v, %v; want regular file", info, err)
	}
}

func TestReadRejectsSymlinkedStateDirectoryAndAccountsLeaf(t *testing.T) {
	t.Run("state directory", func(t *testing.T) {
		home := t.TempDir()
		target := t.TempDir()
		if err := os.Symlink(target, filepath.Join(home, stateDir)); err != nil {
			t.Fatal(err)
		}
		store, err := Open(home)
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		if _, err := store.Read(); !errors.Is(err, ErrUnsafeState) {
			t.Fatalf("Read() error = %v, want unsafe state", err)
		}
	})
	t.Run("accounts leaf", func(t *testing.T) {
		home := t.TempDir()
		if err := os.Mkdir(filepath.Join(home, stateDir), 0o700); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(t.TempDir(), "target.yaml")
		if err := os.WriteFile(target, []byte("chatgpt: {}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(home, accountsPath)); err != nil {
			t.Fatal(err)
		}
		store, err := Open(home)
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		if _, err := store.Read(); !errors.Is(err, ErrUnsafeState) {
			t.Fatalf("Read() error = %v, want unsafe state", err)
		}
	})
}

func TestUseRequiresSavedCredentialAndWritesProviderAndAccountSelection(t *testing.T) {
	home := t.TempDir()
	accountsStore, err := Open(home)
	if err != nil {
		t.Fatal(err)
	}
	defer accountsStore.Close()
	credentials, err := vault.Open(home)
	if err != nil {
		t.Fatal(err)
	}
	defer credentials.Close()
	project := filepath.Join(home, "checkout")
	if err := os.Mkdir(project, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := accountsStore.Use(ChatGPT, "work", "", credentials); !errors.Is(err, ErrNoSavedLogin) {
		t.Fatalf("Use without credential error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, accountsPath)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed use wrote accounts file: %v", err)
	}
	if err := credentials.PutChatGPT("work", chatGPTCredential()); err != nil {
		t.Fatal(err)
	}
	canonicalProject, err := CanonicalProject(project)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := accountsStore.Use(ChatGPT, "work", project, credentials); err != nil || got != "chatgpt:work is the account for "+canonicalProject+"\n" {
		t.Fatalf("Use() = %q, %v", got, err)
	}
	if err := credentials.PutGrok("default", grokCredential()); err != nil {
		t.Fatal(err)
	}
	if got, err := accountsStore.Use(Grok, "default", "", credentials); err != nil || got != "grok:default is the default account\n" {
		t.Fatalf("Use() = %q, %v", got, err)
	}
	file, err := accountsStore.Read()
	if err != nil {
		t.Fatal(err)
	}
	if file.ChatGPT.Projects[0] != (AccountProject{Path: canonicalProject, Account: "work"}) || file.Grok.Default != "default" || file.Selection.Default != Grok || file.Selection.Projects[0] != (ProviderProject{Path: canonicalProject, Provider: ChatGPT}) {
		t.Fatalf("accounts after use = %#v", file)
	}
}

func TestResolveRunUsesDocumentedProviderAndAccountPrecedence(t *testing.T) {
	project := filepath.Join(string(filepath.Separator), "work", "checkout")
	file := File{
		ChatGPT:   ProviderAccounts{Default: "personal", Projects: []AccountProject{{Path: project, Account: "project-chat"}}},
		Grok:      ProviderAccounts{Default: "grok-default", Projects: []AccountProject{{Path: project, Account: "project-grok"}}},
		Selection: Selection{Default: ChatGPT, Projects: []ProviderProject{{Path: project, Provider: Grok}}},
	}
	got, err := Resolve(file, ResolveInput{Project: project, CommittedAccount: "legacy"})
	if err != nil || got != (RunAccount{Provider: Grok, Label: "project-grok", CredentialSource: "owned", ProviderSource: "project", AccountSource: "project"}) {
		t.Fatalf("project selection = %#v, %v", got, err)
	}
	got, err = Resolve(file, ResolveInput{Project: project, ProviderOverride: "chatgpt", CommittedAccount: "legacy"})
	if err != nil || got.Provider != ChatGPT || got.Label != "project-chat" || got.AccountSource != "project" {
		t.Fatalf("machine project account should precede committed account: %#v, %v", got, err)
	}
	got, err = Resolve(file, ResolveInput{ProviderOverride: "chatgpt", CommittedAccount: "legacy"})
	if err != nil || got.Label != "legacy" || got.AccountSource != "committed" {
		t.Fatalf("committed ChatGPT account = %#v, %v", got, err)
	}
	got, err = Resolve(file, ResolveInput{ProviderOverride: "grok", CommittedAccount: "legacy"})
	if err != nil || got.Label != "grok-default" || got.AccountSource != "default" {
		t.Fatalf("Grok must ignore committed account = %#v, %v", got, err)
	}
	got, err = Resolve(file, ResolveInput{ProviderOverride: "grok", AccountOverride: "env-account"})
	if err != nil || got.Label != "env-account" || got.AccountSource != "environment" {
		t.Fatalf("environment account override = %#v, %v", got, err)
	}
	got, err = Resolve(File{}, ResolveInput{})
	if err != nil || got != (RunAccount{Provider: ChatGPT, Label: "default", CredentialSource: "owned", ProviderSource: "fallback", AccountSource: "fallback"}) {
		t.Fatalf("fallback account = %#v, %v", got, err)
	}
	got, err = Resolve(File{}, ResolveInput{ProviderOverride: "chatgpt", InjectedAuth: true})
	if err != nil || got.CredentialSource != "injected" {
		t.Fatalf("ChatGPT injected credential source = %#v, %v", got, err)
	}
	got, err = Resolve(File{}, ResolveInput{ProviderOverride: "grok", InjectedAuth: true})
	if err != nil || got.CredentialSource != "owned" {
		t.Fatalf("Grok must ignore injected auth = %#v, %v", got, err)
	}
	if _, err := Resolve(file, ResolveInput{ProviderOverride: "invalid"}); !errors.Is(err, ErrInvalidProvider) {
		t.Fatalf("invalid provider error = %v", err)
	}
	if _, err := Resolve(file, ResolveInput{AccountOverride: "../bad"}); !errors.Is(err, ErrInvalidLabel) {
		t.Fatalf("invalid account label error = %v", err)
	}
}

func TestDeterministicListLoginLogoutUseAndDeprecatedWarningOutput(t *testing.T) {
	chatEmail := "chat@example.test"
	grokEmail := "grok@example.test"
	profiles := vault.Profiles{
		ChatGPT: map[string]vault.ChatGPTProfile{
			"z":       {SignedIn: false},
			"default": {SignedIn: true, Email: &chatEmail, ExpiresAt: 123},
		},
		Grok: map[string]vault.GrokProfile{
			"b": {SignedIn: true, Email: &grokEmail},
			"a": {SignedIn: false},
		},
	}
	file := File{ChatGPT: ProviderAccounts{Default: "default"}, Selection: Selection{Default: ChatGPT}}
	want := "chatgpt:default (default) signed in chat@example.test expires=123\n" +
		"chatgpt:z signed out\n" +
		"grok:a signed out\n" +
		"grok:b signed in grok@example.test\n"
	if got := FormatList(file, profiles); got != want {
		t.Fatalf("FormatList() =\n%q\nwant:\n%q", got, want)
	}
	if got := FormatList(File{}, vault.Profiles{}); got != "chatgpt: not signed in\ngrok: not signed in\n" {
		t.Fatalf("empty FormatList() = %q", got)
	}
	if got := FormatList(File{ChatGPT: ProviderAccounts{Default: "default"}}, vault.Profiles{ChatGPT: profiles.ChatGPT}); !strings.HasPrefix(got, "chatgpt:default (default) signed in") {
		t.Fatalf("ChatGPT should be selected when selection.default is omitted: %q", got)
	}
	if got := ChatGPTPlanNotice(); got != "You're using your ChatGPT plan\n" {
		t.Fatalf("ChatGPT plan notice = %q", got)
	}
	if got := FormatChatGPTSignedIn(&chatEmail); got != "chatgpt:default signed in (chat@example.test)\n" {
		t.Fatalf("ChatGPT login output = %q", got)
	}
	if got := FormatGrokSignedIn(nil); got != "grok:default signed in\n" {
		t.Fatalf("Grok login output = %q", got)
	}
	if got := FormatChatGPTSignedOut(true); got != "chatgpt:default signed out\n" {
		t.Fatalf("ChatGPT remote logout output = %q", got)
	}
	if got := FormatChatGPTSignedOut(false); got != "chatgpt:default signed out locally; remote revocation was not confirmed. You can disconnect Kogen in ChatGPT Settings if needed.\n" {
		t.Fatalf("ChatGPT local logout output = %q", got)
	}
	if got := FormatGrokSignedOut(); got != "grok:default signed out locally\n" {
		t.Fatalf("Grok logout output = %q", got)
	}
	if got := FormatUse(Grok, "work", "/tmp/project"); got != "grok:work is the account for /tmp/project\n" {
		t.Fatalf("project use output = %q", got)
	}
	if got := DeprecatedAccountWarning(); got != "kogen: moved: account in .kogen/project.yaml; use kogen provider use chatgpt --as <label> --project <checkout>\n" {
		t.Fatalf("deprecated account warning = %q", got)
	}
}

func chatGPTCredential() vault.Credential {
	return vault.Credential{
		ClientID: "client", AccessToken: "access", RefreshToken: "refresh", IDToken: "id",
		ExpiresAt: time.Now().Add(time.Hour).Unix(), Subject: "subject",
		HostID: "urn:uuid:123e4567-e89b-42d3-a456-426614174000",
	}
}

func grokCredential() vault.GrokCredential {
	return vault.GrokCredential{
		AccessToken: "access", RefreshToken: "refresh", ExpiresAt: time.Now().Add(time.Hour).Unix(),
		ClientID: "client", TokenEndpoint: "https://auth.example.test/token",
	}
}
