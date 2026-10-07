package accounts

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	"kogen-go/internal/auth/vault"
	"kogen-go/internal/safefs"
	"kogen-go/internal/yamlmini"
)

const (
	stateDir       = ".kogen"
	accountsPath   = ".kogen/accounts.yaml"
	accountsLimit  = 1 << 20
	accountsHeader = "# Kogen accounts on this machine, written by kogen provider use.\n"
)

var (
	ErrInvalidLabel       = errors.New("accounts: invalid account label")
	ErrInvalidProvider    = errors.New("accounts: invalid provider")
	ErrInvalidFile        = errors.New("accounts: invalid accounts file")
	ErrUnsafeState        = errors.New("accounts: unsafe state path")
	ErrStoreClosed        = errors.New("accounts: store is closed")
	ErrProjectNotFound    = errors.New("accounts: project checkout does not exist")
	ErrNoSavedLogin       = errors.New("accounts: selected account has no saved login")
	ErrCredentialStoreNil = errors.New("accounts: credential store is required")
)

type Provider string

const (
	ChatGPT Provider = "chatgpt"
	Grok    Provider = "grok"
)

// AccountProject maps one canonical checkout path to a provider account label.
type AccountProject struct {
	Path    string
	Account string
}

// ProviderAccounts stores a provider's machine default and project overrides.
type ProviderAccounts struct {
	Default  string
	Projects []AccountProject
}

// ProviderProject selects the provider for one canonical checkout path.
type ProviderProject struct {
	Path     string
	Provider Provider
}

// Selection stores the machine-wide and per-checkout provider choice.
type Selection struct {
	Default  Provider
	Projects []ProviderProject
}

// File is the typed representation of ~/.kogen/accounts.yaml.
type File struct {
	ChatGPT   ProviderAccounts
	Grok      ProviderAccounts
	Selection Selection
}

// RunAccount is the provider/account decision captured before a run starts.
// Callers should retain this value and pass it through the run instead of
// re-reading account state between requests.
type RunAccount struct {
	Provider         Provider
	Label            string
	CredentialSource string
	ProviderSource   string
	AccountSource    string
}

// ResolveInput contains the per-run overrides and resolved project metadata.
// Project must be the canonical checkout directory when non-empty.
type ResolveInput struct {
	Project          string
	ProviderOverride string
	AccountOverride  string
	CommittedAccount string
	InjectedAuth     bool
}

// Store accesses accounts.yaml through a descriptor-rooted home directory.
type Store struct {
	mu     sync.RWMutex
	root   *safefs.Root
	closed bool
}

// Open creates a rooted account store. It does not create state directories
// until a write is requested.
func Open(home string) (*Store, error) {
	if home == "" {
		return nil, errors.New("accounts: home directory is required")
	}
	absHome, err := filepath.Abs(home)
	if err != nil {
		return nil, fmt.Errorf("accounts: resolve home directory: %w", err)
	}
	root, err := safefs.OpenRoot(absHome)
	if err != nil {
		return nil, fmt.Errorf("accounts: open home directory: %w", err)
	}
	return &Store{root: root}, nil
}

// Close releases the home directory descriptor. It is safe to call repeatedly.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	return s.root.Close()
}

// Read reads and validates accounts.yaml. A missing state directory or file is
// an empty account file; malformed state is returned as ErrInvalidFile.
func (s *Store) Read() (File, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return File{}, ErrStoreClosed
	}
	data, exists, err := s.readBytes()
	if err != nil || !exists {
		return File{}, err
	}
	parsed, err := Parse(data)
	if err != nil {
		return File{}, err
	}
	return parsed, nil
}

// Write canonicalizes and atomically publishes accounts.yaml with mode 0600.
// Project rows whose canonical checkout directory no longer exists are
// removed before serialization.
func (s *Store) Write(file File) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrStoreClosed
	}
	canonical, err := canonicalFile(file)
	if err != nil {
		return err
	}
	if err := s.ensurePrivateStateDir(); err != nil {
		return err
	}
	data, err := Marshal(canonical)
	if err != nil {
		return err
	}
	if err := s.root.PublishPrivate(accountsPath, data, safefs.PublicationReplace); err != nil {
		return fmt.Errorf("accounts: publish accounts file: %w", err)
	}
	return nil
}

// Use selects a saved account as the provider default or for one checkout.
// A missing saved login is rejected before accounts.yaml is changed.
func (s *Store) Use(provider Provider, label, project string, credentials *vault.Store) (string, error) {
	if !validProvider(provider) {
		return "", ErrInvalidProvider
	}
	if !ValidLabel(label) {
		return "", ErrInvalidLabel
	}
	if credentials == nil {
		return "", ErrCredentialStoreNil
	}
	canonicalProject := ""
	if project != "" {
		var err error
		canonicalProject, err = CanonicalProject(project)
		if err != nil {
			return "", err
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return "", ErrStoreClosed
	}
	file, exists, err := s.readFileLocked()
	if err != nil {
		return "", err
	}
	if !exists {
		file = File{}
	}

	if err := ensureSavedLogin(provider, label, credentials); err != nil {
		return "", err
	}
	if canonicalProject == "" {
		if provider == ChatGPT {
			file.ChatGPT.Default = label
		} else {
			file.Grok.Default = label
		}
		file.Selection.Default = provider
	} else {
		if provider == ChatGPT {
			file.ChatGPT.Projects = putAccountProject(file.ChatGPT.Projects, canonicalProject, label)
		} else {
			file.Grok.Projects = putAccountProject(file.Grok.Projects, canonicalProject, label)
		}
		file.Selection.Projects = putProviderProject(file.Selection.Projects, canonicalProject, provider)
	}

	canonical, err := canonicalFile(file)
	if err != nil {
		return "", err
	}
	if err := s.ensurePrivateStateDir(); err != nil {
		return "", err
	}
	data, err := Marshal(canonical)
	if err != nil {
		return "", err
	}
	if err := s.root.PublishPrivate(accountsPath, data, safefs.PublicationReplace); err != nil {
		return "", fmt.Errorf("accounts: publish accounts file: %w", err)
	}
	return FormatUse(provider, label, canonicalProject), nil
}

// ResolveRun reads account state once and returns the immutable provider and
// label decision for a single run.
func (s *Store) ResolveRun(input ResolveInput) (RunAccount, error) {
	file, err := s.Read()
	if err != nil {
		return RunAccount{}, err
	}
	if input.Project != "" {
		canonical, err := CanonicalProject(input.Project)
		if err != nil {
			return RunAccount{}, err
		}
		input.Project = canonical
	}
	return Resolve(file, input)
}

func (s *Store) readFileLocked() (File, bool, error) {
	data, exists, err := s.readBytes()
	if err != nil || !exists {
		return File{}, exists, err
	}
	file, err := Parse(data)
	return file, true, err
}

func (s *Store) readBytes() ([]byte, bool, error) {
	dirInfo, err := s.root.Lstat(stateDir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("accounts: inspect state directory: %w", err)
	}
	if !dirInfo.IsDir() || dirInfo.Mode().Perm() != 0o700 {
		return nil, false, ErrUnsafeState
	}
	info, err := s.root.Lstat(accountsPath)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("accounts: inspect accounts file: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() > accountsLimit {
		return nil, false, ErrUnsafeState
	}
	reader, err := s.root.OpenRead(accountsPath)
	if err != nil {
		return nil, false, fmt.Errorf("accounts: read accounts file: %w", err)
	}
	defer reader.Close()
	data, err := io.ReadAll(io.LimitReader(reader, accountsLimit+1))
	if err != nil {
		return nil, false, fmt.Errorf("accounts: read accounts file: %w", err)
	}
	if len(data) > accountsLimit {
		return nil, false, ErrUnsafeState
	}
	return data, true, nil
}

func (s *Store) ensurePrivateStateDir() error {
	if err := s.root.MkdirAll(stateDir, 0o700); err != nil {
		return fmt.Errorf("accounts: create state directory: %w", err)
	}
	info, err := s.root.Lstat(stateDir)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		return ErrUnsafeState
	}
	return nil
}

// Parse decodes the strict YAML subset and validates the accounts schema.
func Parse(data []byte) (File, error) {
	value, err := yamlmini.Parse(data)
	if err != nil {
		return File{}, fmt.Errorf("%w: %v", ErrInvalidFile, err)
	}
	root, ok := value.(yamlmini.Mapping)
	if !ok || len(root) == 0 {
		return File{}, ErrInvalidFile
	}
	var file File
	for key, value := range root {
		switch key {
		case "chatgpt":
			file.ChatGPT, err = parseProviderAccounts(value, "account")
		case "grok":
			file.Grok, err = parseProviderAccounts(value, "account")
		case "selection":
			file.Selection, err = parseSelection(value)
		default:
			err = ErrInvalidFile
		}
		if err != nil {
			return File{}, fmt.Errorf("%w: %v", ErrInvalidFile, err)
		}
	}
	if err := validateFile(file); err != nil {
		return File{}, err
	}
	return file, nil
}

// Marshal returns canonical YAML with sorted project rows and a final newline.
func Marshal(file File) ([]byte, error) {
	if err := validateFile(file); err != nil {
		return nil, err
	}
	var out strings.Builder
	out.WriteString(accountsHeader)
	writeProvider := func(name string, provider ProviderAccounts) {
		if provider.Default == "" && len(provider.Projects) == 0 {
			return
		}
		out.WriteString(name)
		out.WriteString(":\n")
		if provider.Default != "" {
			out.WriteString("  default: ")
			out.WriteString(provider.Default)
			out.WriteByte('\n')
		}
		projects := append([]AccountProject(nil), provider.Projects...)
		sort.Slice(projects, func(i, j int) bool { return projects[i].Path < projects[j].Path })
		if len(projects) > 0 {
			out.WriteString("  projects:\n")
			for _, project := range projects {
				path, _ := json.Marshal(project.Path)
				out.WriteString("    - path: ")
				out.Write(path)
				out.WriteString("\n      account: ")
				out.WriteString(project.Account)
				out.WriteByte('\n')
			}
		}
	}
	writeProvider("chatgpt", file.ChatGPT)
	writeProvider("grok", file.Grok)
	selection := file.Selection
	projects := append([]ProviderProject(nil), selection.Projects...)
	sort.Slice(projects, func(i, j int) bool { return projects[i].Path < projects[j].Path })
	if selection.Default != "" || len(projects) > 0 {
		out.WriteString("selection:\n")
		if selection.Default != "" {
			out.WriteString("  default: ")
			out.WriteString(string(selection.Default))
			out.WriteByte('\n')
		}
		if len(projects) > 0 {
			out.WriteString("  projects:\n")
			for _, project := range projects {
				path, _ := json.Marshal(project.Path)
				out.WriteString("    - path: ")
				out.Write(path)
				out.WriteString("\n      provider: ")
				out.WriteString(string(project.Provider))
				out.WriteByte('\n')
			}
		}
	}
	if out.Len() == len(accountsHeader) {
		out.WriteString("chatgpt: {}\n")
	}
	return []byte(out.String()), nil
}

// Resolve applies provider and account precedence and returns one fixed choice.
func Resolve(file File, input ResolveInput) (RunAccount, error) {
	if err := validateFile(file); err != nil {
		return RunAccount{}, err
	}
	if input.Project != "" && (!filepath.IsAbs(input.Project) || filepath.Clean(input.Project) != input.Project) {
		return RunAccount{}, ErrProjectNotFound
	}
	result := RunAccount{}
	switch {
	case input.ProviderOverride != "":
		result.Provider = Provider(input.ProviderOverride)
		result.ProviderSource = "environment"
	case input.Project != "" && projectProvider(file.Selection.Projects, input.Project) != "":
		result.Provider = projectProvider(file.Selection.Projects, input.Project)
		result.ProviderSource = "project"
	case file.Selection.Default != "":
		result.Provider = file.Selection.Default
		result.ProviderSource = "default"
	default:
		result.Provider = ChatGPT
		result.ProviderSource = "fallback"
	}
	if !validProvider(result.Provider) {
		return RunAccount{}, fmt.Errorf("%w: KOGEN_BENCH_PROVIDER must be chatgpt or grok", ErrInvalidProvider)
	}

	mapping := providerAccounts(file, result.Provider)
	switch {
	case input.AccountOverride != "":
		result.Label = input.AccountOverride
		result.AccountSource = "environment"
	case input.Project != "" && projectAccount(mapping.Projects, input.Project) != "":
		result.Label = projectAccount(mapping.Projects, input.Project)
		result.AccountSource = "project"
	case result.Provider == ChatGPT && input.CommittedAccount != "":
		result.Label = input.CommittedAccount
		result.AccountSource = "committed"
	case mapping.Default != "":
		result.Label = mapping.Default
		result.AccountSource = "default"
	default:
		result.Label = "default"
		result.AccountSource = "fallback"
	}
	if !ValidLabel(result.Label) {
		return RunAccount{}, ErrInvalidLabel
	}
	result.CredentialSource = "owned"
	if result.Provider == ChatGPT && input.InjectedAuth {
		result.CredentialSource = "injected"
	}
	return result, nil
}

// CanonicalProject resolves an existing checkout directory to its canonical
// absolute path.
func CanonicalProject(path string) (string, error) {
	if path == "" {
		return "", ErrProjectNotFound
	}
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("%w: %s", ErrProjectNotFound, path)
	}
	abs, err := filepath.Abs(canonical)
	if err != nil {
		return "", fmt.Errorf("%w: %s", ErrProjectNotFound, path)
	}
	info, err := os.Stat(abs)
	if err != nil || !info.IsDir() || !utf8.ValidString(abs) {
		return "", fmt.Errorf("%w: %s", ErrProjectNotFound, path)
	}
	return filepath.Clean(abs), nil
}

// ValidLabel implements ^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$.
func ValidLabel(label string) bool {
	if len(label) == 0 || len(label) > 64 || !isASCIIAlphaNumeric(label[0]) {
		return false
	}
	for index := 1; index < len(label); index++ {
		ch := label[index]
		if !isASCIIAlphaNumeric(ch) && ch != '.' && ch != '_' && ch != '-' {
			return false
		}
	}
	return true
}

func parseProviderAccounts(value yamlmini.Value, accountKey string) (ProviderAccounts, error) {
	var result ProviderAccounts
	fields, ok := value.(yamlmini.Mapping)
	if !ok {
		return result, ErrInvalidFile
	}
	for key, value := range fields {
		switch key {
		case "default":
			label, ok := value.(string)
			if !ok || !ValidLabel(label) {
				return ProviderAccounts{}, ErrInvalidLabel
			}
			result.Default = label
		case "projects":
			sequence, ok := value.(yamlmini.Sequence)
			if !ok {
				return ProviderAccounts{}, ErrInvalidFile
			}
			seen := make(map[string]struct{}, len(sequence))
			for _, row := range sequence {
				fields, ok := row.(yamlmini.Mapping)
				if !ok || len(fields) != 2 {
					return ProviderAccounts{}, ErrInvalidFile
				}
				path, pathOK := fields["path"].(string)
				label, labelOK := fields[accountKey].(string)
				if !pathOK || !validAccountPath(path) || !labelOK || !ValidLabel(label) {
					return ProviderAccounts{}, ErrInvalidFile
				}
				if _, exists := seen[path]; exists {
					return ProviderAccounts{}, ErrInvalidFile
				}
				seen[path] = struct{}{}
				result.Projects = append(result.Projects, AccountProject{Path: path, Account: label})
			}
		default:
			return ProviderAccounts{}, ErrInvalidFile
		}
	}
	return result, nil
}

func parseSelection(value yamlmini.Value) (Selection, error) {
	var result Selection
	fields, ok := value.(yamlmini.Mapping)
	if !ok {
		return result, ErrInvalidFile
	}
	for key, value := range fields {
		switch key {
		case "default":
			provider, ok := value.(string)
			if !ok || !validProvider(Provider(provider)) {
				return Selection{}, ErrInvalidProvider
			}
			result.Default = Provider(provider)
		case "projects":
			sequence, ok := value.(yamlmini.Sequence)
			if !ok {
				return Selection{}, ErrInvalidFile
			}
			seen := make(map[string]struct{}, len(sequence))
			for _, row := range sequence {
				fields, ok := row.(yamlmini.Mapping)
				if !ok || len(fields) != 2 {
					return Selection{}, ErrInvalidFile
				}
				path, pathOK := fields["path"].(string)
				provider, providerOK := fields["provider"].(string)
				if !pathOK || !validAccountPath(path) || !providerOK || !validProvider(Provider(provider)) {
					return Selection{}, ErrInvalidFile
				}
				if _, exists := seen[path]; exists {
					return Selection{}, ErrInvalidFile
				}
				seen[path] = struct{}{}
				result.Projects = append(result.Projects, ProviderProject{Path: path, Provider: Provider(provider)})
			}
		default:
			return Selection{}, ErrInvalidFile
		}
	}
	return result, nil
}

func canonicalFile(file File) (File, error) {
	if err := validateFile(file); err != nil {
		return File{}, err
	}
	var err error
	file.ChatGPT.Projects, err = canonicalAccountProjects(file.ChatGPT.Projects)
	if err != nil {
		return File{}, err
	}
	file.Grok.Projects, err = canonicalAccountProjects(file.Grok.Projects)
	if err != nil {
		return File{}, err
	}
	file.Selection.Projects, err = canonicalProviderProjects(file.Selection.Projects)
	if err != nil {
		return File{}, err
	}
	return file, nil
}

func canonicalAccountProjects(rows []AccountProject) ([]AccountProject, error) {
	byPath := make(map[string]string, len(rows))
	for _, row := range rows {
		path, err := canonicalExistingDirectory(row.Path)
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, ErrProjectNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if prior, exists := byPath[path]; exists && prior != row.Account {
			return nil, fmt.Errorf("%w: conflicting project account rows", ErrInvalidFile)
		}
		byPath[path] = row.Account
	}
	result := make([]AccountProject, 0, len(byPath))
	for path, label := range byPath {
		result = append(result, AccountProject{Path: path, Account: label})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Path < result[j].Path })
	return result, nil
}

func canonicalProviderProjects(rows []ProviderProject) ([]ProviderProject, error) {
	byPath := make(map[string]Provider, len(rows))
	for _, row := range rows {
		path, err := canonicalExistingDirectory(row.Path)
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, ErrProjectNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if prior, exists := byPath[path]; exists && prior != row.Provider {
			return nil, fmt.Errorf("%w: conflicting project provider rows", ErrInvalidFile)
		}
		byPath[path] = row.Provider
	}
	result := make([]ProviderProject, 0, len(byPath))
	for path, provider := range byPath {
		result = append(result, ProviderProject{Path: path, Provider: provider})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Path < result[j].Path })
	return result, nil
}

func canonicalExistingDirectory(path string) (string, error) {
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	canonical, err = filepath.Abs(canonical)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(canonical)
	if err != nil {
		return "", err
	}
	if !info.IsDir() || !utf8.ValidString(canonical) {
		return "", ErrProjectNotFound
	}
	return filepath.Clean(canonical), nil
}

func ensureSavedLogin(provider Provider, label string, credentials *vault.Store) error {
	var found bool
	var err error
	switch provider {
	case ChatGPT:
		_, found, err = credentials.GetChatGPT(label)
	case Grok:
		_, found, err = credentials.GetGrok(label)
	default:
		return ErrInvalidProvider
	}
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("%w: Selected account %s has no saved login; run kogen provider login %s to sign in", ErrNoSavedLogin, label, provider)
	}
	return nil
}

func validateFile(file File) error {
	if file.ChatGPT.Default != "" && !ValidLabel(file.ChatGPT.Default) ||
		file.Grok.Default != "" && !ValidLabel(file.Grok.Default) {
		return ErrInvalidLabel
	}
	if file.Selection.Default != "" && !validProvider(file.Selection.Default) {
		return ErrInvalidProvider
	}
	if err := validateAccountProjects(file.ChatGPT.Projects); err != nil {
		return err
	}
	if err := validateAccountProjects(file.Grok.Projects); err != nil {
		return err
	}
	seen := make(map[string]struct{}, len(file.Selection.Projects))
	for _, row := range file.Selection.Projects {
		if !validAccountPath(row.Path) || !validProvider(row.Provider) {
			return ErrInvalidFile
		}
		if _, exists := seen[row.Path]; exists {
			return ErrInvalidFile
		}
		seen[row.Path] = struct{}{}
	}
	return nil
}

func validateAccountProjects(rows []AccountProject) error {
	seen := make(map[string]struct{}, len(rows))
	for _, row := range rows {
		if !validAccountPath(row.Path) || !ValidLabel(row.Account) {
			return ErrInvalidFile
		}
		if _, exists := seen[row.Path]; exists {
			return ErrInvalidFile
		}
		seen[row.Path] = struct{}{}
	}
	return nil
}

func validAccountPath(path string) bool {
	return path != "" && filepath.IsAbs(path) && filepath.Clean(path) == path && !strings.ContainsRune(path, '\x00') && utf8.ValidString(path)
}

func putAccountProject(rows []AccountProject, path, label string) []AccountProject {
	result := append([]AccountProject(nil), rows...)
	for index := range result {
		if result[index].Path == path {
			result[index].Account = label
			return result
		}
	}
	return append(result, AccountProject{Path: path, Account: label})
}

func putProviderProject(rows []ProviderProject, path string, provider Provider) []ProviderProject {
	result := append([]ProviderProject(nil), rows...)
	for index := range result {
		if result[index].Path == path {
			result[index].Provider = provider
			return result
		}
	}
	return append(result, ProviderProject{Path: path, Provider: provider})
}

func providerAccounts(file File, provider Provider) ProviderAccounts {
	if provider == Grok {
		return file.Grok
	}
	return file.ChatGPT
}

func projectProvider(rows []ProviderProject, path string) Provider {
	for _, row := range rows {
		if row.Path == path {
			return row.Provider
		}
	}
	return ""
}

func projectAccount(rows []AccountProject, path string) string {
	for _, row := range rows {
		if row.Path == path {
			return row.Account
		}
	}
	return ""
}

func isASCIIAlphaNumeric(ch byte) bool {
	return ch >= 'A' && ch <= 'Z' || ch >= 'a' && ch <= 'z' || ch >= '0' && ch <= '9'
}

func validProvider(provider Provider) bool { return provider == ChatGPT || provider == Grok }
