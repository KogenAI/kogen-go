package vault

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"kogen-go/internal/safefs"
)

const (
	stateDirName     = ".kogen"
	credentialsDir   = ".kogen/credentials"
	credentialsLimit = 1 << 20
)

var (
	ErrInvalidLabel        = errors.New("vault: invalid account label")
	ErrUnsupportedProvider = errors.New("vault: unsupported provider")
	ErrCredentialCorrupt   = errors.New("vault: credential is corrupt; sign in again")
	ErrProfilesCorrupt     = errors.New("vault: profiles file is corrupt")
	ErrUnsafeState         = errors.New("vault: state file or directory is unsafe")
	ErrVaultClosed         = errors.New("vault: store is closed")
	ErrInjectedUnavailable = errors.New("vault: injected auth is unavailable")
	ErrInjectedInvalid     = errors.New("vault: injected auth is invalid")
	ErrInjectedExpired     = errors.New("vault: injected access token has expired")
)

// Credential is a Kogen-owned ChatGPT OAuth credential. Its String methods
// intentionally redact token material to reduce accidental log disclosure.
type Credential struct {
	ClientID     string   `json:"client_id"`
	AccessToken  string   `json:"access_token"`
	RefreshToken string   `json:"refresh_token"`
	IDToken      string   `json:"id_token"`
	ExpiresAt    int64    `json:"expires_at"`
	Scopes       []string `json:"scopes"`
	Subject      string   `json:"subject"`
	Email        *string  `json:"email"`
	HostID       string   `json:"host_id"`
}

func (Credential) String() string   { return "Credential{REDACTED}" }
func (Credential) GoString() string { return "Credential{REDACTED}" }

// GrokCredential is a Kogen-owned device-code OAuth credential.
type GrokCredential struct {
	AccessToken   string   `json:"access_token"`
	RefreshToken  string   `json:"refresh_token"`
	ExpiresAt     int64    `json:"expires_at"`
	Scopes        []string `json:"scopes"`
	Email         *string  `json:"email"`
	ClientID      string   `json:"client_id"`
	TokenEndpoint string   `json:"token_endpoint"`
}

func (GrokCredential) String() string   { return "GrokCredential{REDACTED}" }
func (GrokCredential) GoString() string { return "GrokCredential{REDACTED}" }

// InjectedCredential is loaded from KOGEN_AUTH_PATH for one provider request.
// AccessToken is secret material; String and GoString redact it.
type InjectedCredential struct {
	AccessToken string
	AccountID   string
	ExpiresAt   int64
}

func (InjectedCredential) String() string   { return "InjectedCredential{REDACTED}" }
func (InjectedCredential) GoString() string { return "InjectedCredential{REDACTED}" }

// ChatGPTProfile is the non-secret account listing metadata stored in
// ~/.kogen/profiles.json.
type ChatGPTProfile struct {
	ClientID      string  `json:"client_id,omitempty"`
	Subject       string  `json:"subject,omitempty"`
	Email         *string `json:"email"`
	ExpiresAt     int64   `json:"expires_at,omitempty"`
	SignedIn      bool    `json:"signed_in"`
	PlanUsage     *string `json:"plan_usage,omitempty"`
	NoticeShown   *bool   `json:"notice_shown,omitempty"`
	RemoteRevoked bool    `json:"remote_revoked"`
}

// GrokProfile is the non-secret account listing metadata stored in
// ~/.kogen/profiles.json.
type GrokProfile struct {
	Email     *string `json:"email"`
	ExpiresAt int64   `json:"expires_at,omitempty"`
	SignedIn  bool    `json:"signed_in"`
}

// Profiles is the public shape of ~/.kogen/profiles.json.
type Profiles struct {
	ChatGPT map[string]ChatGPTProfile `json:"chatgpt,omitempty"`
	Grok    map[string]GrokProfile    `json:"grok,omitempty"`
}

// Store is anchored to the supplied home directory. All file reads and writes
// are relative to a descriptor-rooted filesystem capability.
type Store struct {
	mu     sync.RWMutex
	root   *safefs.Root
	closed bool
}

// Open creates or opens a file-backed vault under home/.kogen. State and
// credential directories must be real, owner-only directories. Go always uses
// this file backend, including on macOS and in fake-provider tests.
func Open(home string) (*Store, error) {
	if home == "" {
		return nil, fmt.Errorf("vault: home directory is required")
	}
	absHome, err := filepath.Abs(home)
	if err != nil {
		return nil, fmt.Errorf("vault: cannot resolve home directory: %w", err)
	}
	root, err := safefs.OpenRoot(absHome)
	if err != nil {
		return nil, fmt.Errorf("vault: cannot open home directory: %w", err)
	}
	if err := root.MkdirAll(credentialsDir, 0o700); err != nil {
		_ = root.Close()
		return nil, fmt.Errorf("vault: cannot create private state directories: %w", err)
	}
	for _, name := range []string{stateDirName, credentialsDir} {
		info, statErr := root.Lstat(name)
		if statErr != nil {
			_ = root.Close()
			return nil, fmt.Errorf("%w: %s", ErrUnsafeState, name)
		}
		if !info.IsDir() || info.Mode().Perm() != 0o700 {
			_ = root.Close()
			return nil, fmt.Errorf("%w: %s", ErrUnsafeState, name)
		}
	}
	return &Store{root: root}, nil
}

// Close releases the rooted home directory descriptor. It is safe to call
// more than once.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	return s.root.Close()
}

// GetChatGPT reads a saved ChatGPT credential. The bool is false when no file
// exists. Malformed or incomplete credentials return ErrCredentialCorrupt.
func (s *Store) GetChatGPT(label string) (Credential, bool, error) {
	if err := validateLabel(label); err != nil {
		return Credential{}, false, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return Credential{}, false, ErrVaultClosed
	}
	var credential Credential
	found, err := s.readCredential(credentialName("chatgpt", label), &credential)
	if err != nil || !found {
		return Credential{}, found, err
	}
	if err := validateChatGPT(&credential); err != nil {
		return Credential{}, true, ErrCredentialCorrupt
	}
	return credential, true, nil
}

// GetChatGPTForLogin treats malformed JSON as an unreadable prior login so a
// successful new login can replace it. Unsafe filesystem errors remain errors.
func (s *Store) GetChatGPTForLogin(label string) (*Credential, bool, error) {
	credential, found, err := s.GetChatGPT(label)
	if errors.Is(err, ErrCredentialCorrupt) {
		return nil, true, nil
	}
	if err != nil || !found {
		return nil, false, err
	}
	return &credential, false, nil
}

// PutChatGPT durably replaces a credential, including an unreadable credential
// left by an interrupted or corrupt earlier login.
func (s *Store) PutChatGPT(label string, credential Credential) error {
	if err := validateLabel(label); err != nil {
		return err
	}
	if err := validateChatGPT(&credential); err != nil {
		return fmt.Errorf("vault: refusing incomplete ChatGPT credential")
	}
	return s.putJSON(credentialName("chatgpt", label), credential)
}

// GetGrok reads a saved Grok credential. The bool is false when no file exists.
func (s *Store) GetGrok(label string) (GrokCredential, bool, error) {
	if err := validateLabel(label); err != nil {
		return GrokCredential{}, false, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return GrokCredential{}, false, ErrVaultClosed
	}
	var credential GrokCredential
	found, err := s.readCredential(credentialName("grok", label), &credential)
	if err != nil || !found {
		return GrokCredential{}, found, err
	}
	if err := validateGrok(&credential); err != nil {
		return GrokCredential{}, true, ErrCredentialCorrupt
	}
	return credential, true, nil
}

// GetGrokForLogin mirrors GetChatGPTForLogin for recovery from corrupt files.
func (s *Store) GetGrokForLogin(label string) (*GrokCredential, bool, error) {
	credential, found, err := s.GetGrok(label)
	if errors.Is(err, ErrCredentialCorrupt) {
		return nil, true, nil
	}
	if err != nil || !found {
		return nil, false, err
	}
	return &credential, false, nil
}

// PutGrok durably replaces a saved Grok credential.
func (s *Store) PutGrok(label string, credential GrokCredential) error {
	if err := validateLabel(label); err != nil {
		return err
	}
	if err := validateGrok(&credential); err != nil {
		return fmt.Errorf("vault: refusing incomplete Grok credential")
	}
	return s.putJSON(credentialName("grok", label), credential)
}

// Delete removes a credential without parsing it. Missing files and corrupt
// JSON are both safe to remove, which lets local logout recover an unreadable
// login. The final symlink, if any, is unlinked without following its target.
func (s *Store) Delete(provider, label string) error {
	name, err := credentialNameFor(provider, label)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrVaultClosed
	}
	err = s.root.Remove(name)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("vault: cannot remove credential: %w", err)
	}
	return nil
}

// LogoutChatGPT deletes the local credential even when its JSON is corrupt,
// then records the local sign-out and whether remote revocation was confirmed.
func (s *Store) LogoutChatGPT(label string, remoteRevoked bool) error {
	if err := validateLabel(label); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrVaultClosed
	}
	if err := s.removeCredential("chatgpt", label); err != nil {
		return err
	}
	profiles, err := s.readProfiles()
	if err != nil {
		return err
	}
	if profiles.ChatGPT == nil {
		profiles.ChatGPT = make(map[string]ChatGPTProfile)
	}
	profile := profiles.ChatGPT[label]
	profile.SignedIn = false
	profile.RemoteRevoked = remoteRevoked
	profiles.ChatGPT[label] = profile
	return s.writeProfiles(profiles)
}

// LogoutGrok deletes the local credential and leaves an explicit signed-out
// profile row. Grok logout does not revoke the remote token.
func (s *Store) LogoutGrok(label string) error {
	if err := validateLabel(label); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrVaultClosed
	}
	if err := s.removeCredential("grok", label); err != nil {
		return err
	}
	profiles, err := s.readProfiles()
	if err != nil {
		return err
	}
	if profiles.Grok == nil {
		profiles.Grok = make(map[string]GrokProfile)
	}
	profile := profiles.Grok[label]
	profile.SignedIn = false
	profiles.Grok[label] = profile
	return s.writeProfiles(profiles)
}

// PutChatGPTProfile persists a non-secret profile row.
func (s *Store) PutChatGPTProfile(label string, profile ChatGPTProfile) error {
	if err := validateLabel(label); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrVaultClosed
	}
	profiles, err := s.readProfiles()
	if err != nil {
		return err
	}
	if profiles.ChatGPT == nil {
		profiles.ChatGPT = make(map[string]ChatGPTProfile)
	}
	profiles.ChatGPT[label] = profile
	return s.writeProfiles(profiles)
}

// PutGrokProfile persists a non-secret profile row.
func (s *Store) PutGrokProfile(label string, profile GrokProfile) error {
	if err := validateLabel(label); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrVaultClosed
	}
	profiles, err := s.readProfiles()
	if err != nil {
		return err
	}
	if profiles.Grok == nil {
		profiles.Grok = make(map[string]GrokProfile)
	}
	profiles.Grok[label] = profile
	return s.writeProfiles(profiles)
}

// ReadProfiles returns the persisted non-secret account listing metadata. A
// missing file is an empty profile set; malformed data is not silently erased.
func (s *Store) ReadProfiles() (Profiles, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return Profiles{}, ErrVaultClosed
	}
	return s.readProfiles()
}

// HostID returns the durable machine UUID, creating it atomically on first use.
// An invalid existing value is repaired with a newly generated UUID.
func (s *Store) HostID() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return "", ErrVaultClosed
	}
	const name = ".kogen/host.json"
	data, err := s.readPrivateFile(name)
	if err == nil {
		var doc struct {
			ID string `json:"ext_agent_host_id"`
		}
		if isJSONObject(data) && json.Unmarshal(data, &doc) == nil && validHostID(doc.ID) {
			return doc.ID, nil
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	id, err := newHostID()
	if err != nil {
		return "", fmt.Errorf("vault: cannot generate host identity: %w", err)
	}
	encoded, err := json.Marshal(struct {
		ID string `json:"ext_agent_host_id"`
	}{ID: id})
	if err != nil {
		return "", fmt.Errorf("vault: cannot encode host identity: %w", err)
	}
	if err := s.root.PublishPrivate(name, encoded, safefs.PublicationReplace); err != nil {
		return "", fmt.Errorf("vault: cannot persist host identity: %w", err)
	}
	return id, nil
}

func (s *Store) putJSON(name string, value any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrVaultClosed
	}
	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("vault: cannot encode private record: %w", err)
	}
	if err := s.root.PublishPrivate(name, data, safefs.PublicationReplace); err != nil {
		return fmt.Errorf("vault: cannot persist private record: %w", err)
	}
	return nil
}

func (s *Store) readCredential(name string, value any) (bool, error) {
	data, err := s.readPrivateFile(name)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !isJSONObject(data) || json.Unmarshal(data, value) != nil {
		return true, ErrCredentialCorrupt
	}
	return true, nil
}

func (s *Store) readPrivateFile(name string) ([]byte, error) {
	info, err := s.root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		return nil, ErrUnsafeState
	}
	file, err := s.root.OpenRead(name)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, credentialsLimit+1))
	if err != nil {
		return nil, err
	}
	if len(data) > credentialsLimit {
		return nil, ErrUnsafeState
	}
	return data, nil
}

func (s *Store) readProfiles() (Profiles, error) {
	data, err := s.readPrivateFile(".kogen/profiles.json")
	if errors.Is(err, fs.ErrNotExist) {
		return Profiles{ChatGPT: map[string]ChatGPTProfile{}, Grok: map[string]GrokProfile{}}, nil
	}
	if err != nil {
		return Profiles{}, fmt.Errorf("vault: cannot read profiles: %w", err)
	}
	var profiles Profiles
	if !isJSONObject(data) || json.Unmarshal(data, &profiles) != nil {
		return Profiles{}, ErrProfilesCorrupt
	}
	if profiles.ChatGPT == nil {
		profiles.ChatGPT = map[string]ChatGPTProfile{}
	}
	if profiles.Grok == nil {
		profiles.Grok = map[string]GrokProfile{}
	}
	for label := range profiles.ChatGPT {
		if validateLabel(label) != nil {
			return Profiles{}, ErrProfilesCorrupt
		}
	}
	for label := range profiles.Grok {
		if validateLabel(label) != nil {
			return Profiles{}, ErrProfilesCorrupt
		}
	}
	return profiles, nil
}

func (s *Store) writeProfiles(profiles Profiles) error {
	data, err := json.Marshal(profiles)
	if err != nil {
		return fmt.Errorf("vault: cannot encode profiles: %w", err)
	}
	if err := s.root.PublishPrivate(".kogen/profiles.json", data, safefs.PublicationReplace); err != nil {
		return fmt.Errorf("vault: cannot persist profiles: %w", err)
	}
	return nil
}

func (s *Store) removeCredential(provider, label string) error {
	err := s.root.Remove(credentialName(provider, label))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("vault: cannot remove credential: %w", err)
	}
	return nil
}

func credentialName(provider, label string) string {
	return credentialsDir + "/" + provider + "-" + label + ".json"
}

func credentialNameFor(provider, label string) (string, error) {
	if provider != "chatgpt" && provider != "grok" {
		return "", ErrUnsupportedProvider
	}
	if err := validateLabel(label); err != nil {
		return "", err
	}
	return credentialName(provider, label), nil
}

func validateLabel(label string) error {
	if len(label) < 1 || len(label) > 64 || !isASCIIAlphaNum(label[0]) {
		return ErrInvalidLabel
	}
	for i := 1; i < len(label); i++ {
		b := label[i]
		if !isASCIIAlphaNum(b) && b != '.' && b != '_' && b != '-' {
			return ErrInvalidLabel
		}
	}
	return nil
}

func isASCIIAlphaNum(b byte) bool {
	return b >= 'A' && b <= 'Z' || b >= 'a' && b <= 'z' || b >= '0' && b <= '9'
}

func validateChatGPT(credential *Credential) error {
	if credential.ClientID == "" || credential.AccessToken == "" || credential.RefreshToken == "" ||
		credential.IDToken == "" || credential.ExpiresAt <= 0 || credential.Subject == "" ||
		!validHostID(credential.HostID) {
		return ErrCredentialCorrupt
	}
	return nil
}

func validateGrok(credential *GrokCredential) error {
	if credential.AccessToken == "" || credential.RefreshToken == "" || credential.ExpiresAt <= 0 ||
		credential.ClientID == "" || credential.TokenEndpoint == "" {
		return ErrCredentialCorrupt
	}
	return nil
}

func isJSONObject(data []byte) bool {
	trimmed := bytes.TrimSpace(data)
	return len(trimmed) > 0 && trimmed[0] == '{'
}

func newHostID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	raw[6] = raw[6]&0x0f | 0x40
	raw[8] = raw[8]&0x3f | 0x80
	encoded := hex.EncodeToString(raw[:])
	return "urn:uuid:" + encoded[:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:], nil
}

func validHostID(value string) bool {
	const prefix = "urn:uuid:"
	if !strings.HasPrefix(value, prefix) {
		return false
	}
	uuid := strings.TrimPrefix(value, prefix)
	if len(uuid) != 36 || uuid[14] != '4' || !strings.ContainsRune("89ab", rune(uuid[19])) {
		return false
	}
	for i, b := range uuid {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if b != '-' {
				return false
			}
		} else if !(b >= '0' && b <= '9' || b >= 'a' && b <= 'f') {
			return false
		}
	}
	return true
}

// InjectedReader rereads its path on each Read call. It keeps no token cache
// and exposes no refresh operation, matching the injected-auth contract.
type InjectedReader struct {
	path string
	now  func() time.Time
}

// NewInjectedReader creates a rereading source for an injected auth JSON file.
func NewInjectedReader(path string) *InjectedReader {
	return &InjectedReader{path: path, now: time.Now}
}

// Read opens and validates the current file contents. Reusing the reader still
// performs a new filesystem read each time.
func (r *InjectedReader) Read() (InjectedCredential, error) {
	if r == nil || r.path == "" {
		return InjectedCredential{}, ErrInjectedUnavailable
	}
	now := time.Now
	if r.now != nil {
		now = r.now
	}
	return readInjectedAt(r.path, now())
}

// ReadInjected reads one injected credential without retaining it. Call this
// once for each provider request; it never refreshes or writes the file.
func ReadInjected(path string) (InjectedCredential, error) {
	return readInjectedAt(path, time.Now())
}

func readInjectedAt(path string, now time.Time) (InjectedCredential, error) {
	data, err := readExternalRegular(path, credentialsLimit)
	if err != nil {
		return InjectedCredential{}, ErrInjectedUnavailable
	}
	var doc struct {
		Tokens struct {
			AccessToken string `json:"access_token"`
			AccountID   string `json:"account_id"`
		} `json:"tokens"`
	}
	if !isJSONObject(data) || json.Unmarshal(data, &doc) != nil || doc.Tokens.AccessToken == "" || doc.Tokens.AccountID == "" {
		return InjectedCredential{}, ErrInjectedInvalid
	}
	parts := strings.Split(doc.Tokens.AccessToken, ".")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return InjectedCredential{}, ErrInjectedInvalid
	}
	claimsJSON, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || !isJSONObject(claimsJSON) {
		return InjectedCredential{}, ErrInjectedInvalid
	}
	var claims struct {
		Expires json.Number `json:"exp"`
	}
	decoder := json.NewDecoder(bytes.NewReader(claimsJSON))
	decoder.UseNumber()
	if decoder.Decode(&claims) != nil || claims.Expires == "" {
		return InjectedCredential{}, ErrInjectedInvalid
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return InjectedCredential{}, ErrInjectedInvalid
	}
	expiresAt, err := strconv.ParseInt(claims.Expires.String(), 10, 64)
	if err != nil {
		return InjectedCredential{}, ErrInjectedInvalid
	}
	if expiresAt <= now.Unix() {
		return InjectedCredential{}, ErrInjectedExpired
	}
	return InjectedCredential{AccessToken: doc.Tokens.AccessToken, AccountID: doc.Tokens.AccountID, ExpiresAt: expiresAt}, nil
}

func readExternalRegular(path string, limit int64) ([]byte, error) {
	if path == "" {
		return nil, os.ErrNotExist
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	name := filepath.Base(abs)
	if name == "." || name == string(filepath.Separator) || name == "" {
		return nil, safefs.ErrUnsafePath
	}
	root, err := safefs.OpenRoot(filepath.Dir(abs))
	if err != nil {
		return nil, err
	}
	defer root.Close()
	info, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, safefs.ErrUnsafeFile
	}
	file, err := root.OpenRead(name)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, safefs.ErrUnsafeFile
	}
	return data, nil
}
