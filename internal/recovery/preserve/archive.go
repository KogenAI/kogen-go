package preserve

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"kogen-go/internal/contract"
	"kogen-go/internal/gitio"
	"kogen-go/internal/recovery"
	"kogen-go/internal/safefs"
)

var archiveMagic = []byte("KOGEN-RECOVERY-ARCHIVE\x00v1\n")
var archiveDigestRegex = regexp.MustCompile(`^[0-9a-f]{64}$`)

type archiveManifest struct {
	Schema    int            `json:"schema"`
	Kind      string         `json:"kind"`
	RunID     string         `json:"run_id"`
	Workspace string         `json:"workspace"`
	Base      string         `json:"base"`
	Tree      string         `json:"tree"`
	Entries   []archiveEntry `json:"entries"`
}

type archiveEntry struct {
	PathBase64 string `json:"path_base64"`
	Mode       uint32 `json:"mode"`
	Size       uint64 `json:"size"`
	SHA256     string `json:"sha256"`
}

func encodeArchive(request recovery.PreservationRequest, kind, tree string, files []gitio.TreeFile) ([]byte, error) {
	if kind != "candidate_tree" && kind != "raw_workspace" {
		return nil, errors.New("preserve: unsupported archive kind")
	}
	ordered := append([]gitio.TreeFile(nil), files...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Path < ordered[j].Path })
	manifest := archiveManifest{
		Schema: 1, Kind: kind, RunID: request.RunID,
		Workspace: request.Workspace.Name, Base: string(request.Base), Tree: tree,
		Entries: make([]archiveEntry, 0, len(ordered)),
	}
	for index, file := range ordered {
		if err := validateArchivePath(file.Path); err != nil {
			return nil, fmt.Errorf("preserve: invalid archive path: %w", err)
		}
		if index > 0 && ordered[index-1].Path == file.Path {
			return nil, fmt.Errorf("preserve: duplicate archive path %q", file.Path)
		}
		if !archiveMode(file.Mode) {
			return nil, fmt.Errorf("preserve: unsupported archive mode %o for %q", file.Mode, file.Path)
		}
		digest := sha256.Sum256(file.Bytes)
		manifest.Entries = append(manifest.Entries, archiveEntry{
			PathBase64: base64.RawStdEncoding.EncodeToString([]byte(file.Path)),
			Mode:       file.Mode, Size: uint64(len(file.Bytes)), SHA256: hex.EncodeToString(digest[:]),
		})
	}
	header, err := json.Marshal(manifest)
	if err != nil {
		return nil, fmt.Errorf("preserve: encode archive manifest: %w", err)
	}
	if uint64(len(header)) > uint64(^uint32(0)) {
		return nil, errors.New("preserve: archive manifest is too large")
	}
	buffer := bytes.NewBuffer(make([]byte, 0, len(archiveMagic)+8+len(header)+len(files)*64))
	buffer.Write(archiveMagic)
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(header)))
	buffer.Write(size[:])
	buffer.Write(header)
	for _, file := range ordered {
		buffer.Write(file.Bytes)
	}
	checksum := sha256.Sum256(buffer.Bytes())
	buffer.Write(checksum[:])
	return buffer.Bytes(), nil
}

func archiveMode(mode uint32) bool {
	switch mode {
	case gitio.GitModeRegular, gitio.GitModeExecutable, gitio.GitModeSymlink, 0o040000:
		return true
	default:
		return false
	}
}

func validateArchivePath(value string) error {
	if !fs.ValidPath(value) || value == "." || strings.ContainsRune(value, '\x00') {
		return errors.New("path must be a non-empty canonical relative path")
	}
	for _, component := range strings.Split(value, "/") {
		if strings.EqualFold(component, ".git") {
			return errors.New("archive paths may not include Git metadata")
		}
	}
	return nil
}

func readCandidateFiles(ctx context.Context, git contract.GitPort, policy contract.GitPolicy, workspacePath string, tree contract.ObjectID) ([]gitio.TreeFile, error) {
	listed, err := git.Exec(ctx, []string{"ls-tree", "-r", "-z", "--full-tree", string(tree)}, nil, policy)
	if err != nil {
		return nil, fmt.Errorf("list frozen workspace tree: %w", err)
	}
	if err := requireSuccess("list frozen workspace tree", listed); err != nil {
		return nil, err
	}
	workspaceRoot, err := safefs.OpenRoot(workspacePath)
	if err != nil {
		return nil, fmt.Errorf("open frozen workspace: %w", err)
	}
	defer workspaceRoot.Close()
	files := make([]gitio.TreeFile, 0)
	for _, row := range splitNUL(listed.Stdout) {
		metadata, pathBytes, ok := bytes.Cut(row, []byte{'\t'})
		if !ok {
			return nil, errors.New("Git returned a malformed recovery tree row")
		}
		fields := strings.Fields(string(metadata))
		if len(fields) != 3 || fields[1] != "blob" {
			return nil, errors.New("Git recovery tree contains an unsupported object")
		}
		pathName := string(pathBytes)
		if err := validateArchivePath(pathName); err != nil {
			return nil, err
		}
		object, err := gitio.ParseObjectID(fields[2])
		if err != nil {
			return nil, fmt.Errorf("Git returned an invalid recovery blob id: %w", err)
		}
		var mode uint32
		switch fields[0] {
		case "100644":
			mode = gitio.GitModeRegular
		case "100755":
			mode = gitio.GitModeExecutable
		case "120000":
			mode = gitio.GitModeSymlink
		default:
			return nil, fmt.Errorf("unsupported Git recovery mode %q", fields[0])
		}
		info, err := workspaceRoot.Lstat(filepath.ToSlash(pathName))
		if err != nil {
			return nil, fmt.Errorf("inspect frozen path %q: %w", pathName, err)
		}
		var contents []byte
		switch mode {
		case gitio.GitModeSymlink:
			if info.Mode()&fs.ModeSymlink == 0 {
				return nil, fmt.Errorf("frozen path %q changed from symlink", pathName)
			}
			link, err := workspaceRoot.Readlink(filepath.ToSlash(pathName))
			if err != nil {
				return nil, fmt.Errorf("read frozen symlink %q: %w", pathName, err)
			}
			contents = []byte(link)
		default:
			if !info.Mode().IsRegular() {
				return nil, fmt.Errorf("frozen path %q changed from regular file", pathName)
			}
			executable := info.Mode().Perm()&0o111 != 0
			if executable != (mode == gitio.GitModeExecutable) {
				return nil, fmt.Errorf("frozen path %q changed executable mode", pathName)
			}
			contents, err = workspaceRoot.ReadFile(filepath.ToSlash(pathName))
			if err != nil {
				return nil, fmt.Errorf("read frozen file %q: %w", pathName, err)
			}
		}
		actualObject, err := hashObject(ctx, git, policy, contents)
		if err != nil {
			return nil, fmt.Errorf("verify frozen path %q: %w", pathName, err)
		}
		if actualObject != string(object) {
			return nil, fmt.Errorf("frozen path %q changed after tree capture", pathName)
		}
		files = append(files, gitio.TreeFile{Path: pathName, Mode: mode, Bytes: contents})
	}
	return files, nil
}

func hashObject(ctx context.Context, git contract.GitPort, policy contract.GitPolicy, contents []byte) (string, error) {
	result, err := git.Exec(ctx, []string{"hash-object", "--no-filters", "--stdin"}, contents, policy)
	if err != nil {
		return "", err
	}
	if err := requireSuccess("hash frozen recovery blob", result); err != nil {
		return "", err
	}
	object, err := gitio.ParseObjectID(strings.TrimSpace(string(result.Stdout)))
	if err != nil {
		return "", err
	}
	return string(object), nil
}

func requireSuccess(operation string, result contract.GitResult) error {
	if result.Process.ExitStatus == nil || *result.Process.ExitStatus != 0 || result.Process.TimedOut || result.Process.Unavailable {
		status := "unknown"
		if result.Process.ExitStatus != nil {
			status = fmt.Sprint(*result.Process.ExitStatus)
		}
		return fmt.Errorf("%s failed (exit=%s timed_out=%t unavailable=%t)", operation, status, result.Process.TimedOut, result.Process.Unavailable)
	}
	return nil
}

func splitNUL(input []byte) [][]byte {
	if len(input) == 0 {
		return nil
	}
	if input[len(input)-1] != 0 {
		return [][]byte{input}
	}
	return bytes.Split(input[:len(input)-1], []byte{0})
}

func captureRawWorkspace(absolutePath string) ([]gitio.TreeFile, error) {
	workspaceRoot, err := safefs.OpenRoot(absolutePath)
	if err != nil {
		return nil, err
	}
	defer workspaceRoot.Close()
	files := make([]gitio.TreeFile, 0)
	var visit func(string) error
	visit = func(directory string) error {
		entries, err := workspaceRoot.ReadDir(directory)
		if err != nil {
			return err
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
		for _, entry := range entries {
			name := entry.Name()
			if strings.EqualFold(name, ".git") {
				continue
			}
			filePath := name
			if directory != "." {
				filePath = path.Join(directory, name)
			}
			info, err := workspaceRoot.Lstat(filePath)
			if err != nil {
				return err
			}
			if info.IsDir() {
				if directory != "." {
					files = append(files, gitio.TreeFile{Path: filePath, Mode: 0o040000})
				}
				if err := visit(filePath); err != nil {
					return err
				}
				continue
			}
			switch {
			case info.Mode()&fs.ModeSymlink != 0:
				target, err := workspaceRoot.Readlink(filePath)
				if err != nil {
					return err
				}
				files = append(files, gitio.TreeFile{Path: filePath, Mode: gitio.GitModeSymlink, Bytes: []byte(target)})
			case info.Mode().IsRegular():
				contents, err := workspaceRoot.ReadFile(filePath)
				if err != nil {
					return err
				}
				mode := gitio.GitModeRegular
				if info.Mode().Perm()&0o111 != 0 {
					mode = gitio.GitModeExecutable
				}
				files = append(files, gitio.TreeFile{Path: filePath, Mode: mode, Bytes: contents})
			default:
				return fmt.Errorf("unsupported workspace entry %q", filePath)
			}
		}
		return nil
	}
	if err := visit("."); err != nil {
		return nil, err
	}
	return files, nil
}

type rootedArchiveStore struct {
	root *safefs.Root
}

func (s *rootedArchiveStore) Read(identity string) ([]byte, error) {
	if err := validateArchiveIdentity(identity); err != nil {
		return nil, err
	}
	info, err := s.root.Lstat(identity)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&fs.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
		return nil, safefs.ErrUnsafeFile
	}
	return s.root.ReadFile(identity)
}

func (s *rootedArchiveStore) Publish(identity string, contents []byte) error {
	if err := validateArchiveIdentity(identity); err != nil {
		return err
	}
	parent := path.Dir(identity)
	if err := ensurePrivateDirectory(s.root, parent); err != nil {
		return err
	}
	return s.root.Publish(identity, contents, 0o600, safefs.PublicationCreateOnly)
}

func ensurePrivateDirectory(root *safefs.Root, directory string) error {
	current := ""
	for _, component := range strings.Split(directory, "/") {
		if component == "" || component == "." || component == ".." {
			return safefs.ErrUnsafePath
		}
		if current == "" {
			current = component
		} else {
			current += "/" + component
		}
		info, err := root.Lstat(current)
		if errors.Is(err, fs.ErrNotExist) {
			if err := root.MkdirAll(current, 0o700); err != nil {
				return err
			}
			info, err = root.Lstat(current)
		}
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&fs.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
			return errors.New("preserve: archive parent is not a private real directory")
		}
	}
	return nil
}

func validateArchiveIdentity(identity string) error {
	if !fs.ValidPath(identity) || path.Clean(identity) != identity {
		return safefs.ErrUnsafePath
	}
	parts := strings.Split(identity, "/")
	if len(parts) != 4 || parts[0] != "recovery-archives" || !runIDPattern.MatchString(parts[1]) || !workspacePattern.MatchString(parts[2]) {
		return safefs.ErrUnsafePath
	}
	leaf := strings.TrimSuffix(parts[3], ".krec")
	if !archiveDigestRegex.MatchString(leaf) || parts[3] != leaf+".krec" {
		return safefs.ErrUnsafePath
	}
	return nil
}
