package darwin

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"kogen-go/internal/contract"
	"kogen-go/internal/process"
)

func TestProfileEscapesPathsAndKeepsDenialsAfterGrants(t *testing.T) {
	configuration := Configuration{
		RunDir:             "/tmp/kogen run",
		Workspace:          "/tmp/kogen run/workspace",
		WritablePaths:      []string{"/tmp/kogen run/workspace", "/tmp/cache"},
		ProtectedReadPaths: []string{"/tmp/private/has\"quote"},
		WriteDeniedPaths:   []string{"/tmp/kogen run/checkout"},
	}
	profile, err := Profile(configuration)
	if err != nil {
		t.Fatal(err)
	}
	text := string(profile)
	for _, expected := range []string{
		`(allow file-write* (subpath "/tmp/kogen run/workspace"))`,
		`(deny file-read* (subpath "/tmp/private/has\"quote"))`,
		`(deny file-write* (subpath "/tmp/kogen run/checkout"))`,
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("profile does not contain %q:\n%s", expected, text)
		}
	}
	if strings.LastIndex(text, "(deny file-write*") < strings.LastIndex(text, "(allow file-write*") {
		t.Fatalf("write denial precedes a broad grant:\n%s", text)
	}
}

func TestProfileRejectsFilesystemRootWriteGrant(t *testing.T) {
	_, err := Profile(Configuration{
		RunDir:        "/tmp/run",
		Workspace:     "/tmp/workspace",
		WritablePaths: []string{"/"},
	})
	if err == nil {
		t.Fatal("Profile accepted a filesystem-root write grant")
	}
}

func TestPreparePublishesPrivateProfileAndRemovesIt(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	runDir := filepath.Join(root, "run")
	if err := os.Mkdir(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(runDir, 0o700); err != nil {
		t.Fatal(err)
	}
	configuration := Configuration{RunDir: runDir, Workspace: workspace, WritablePaths: []string{workspace}}
	spec := contract.ProcessSpec{Executable: "check", Args: []string{"--hello"}}
	wrapped, cleanup, err := Prepare(spec, configuration)
	if err != nil {
		t.Fatal(err)
	}
	if wrapped.Executable != sandboxExecPath || len(wrapped.Args) != 4 || wrapped.Args[0] != "-f" || wrapped.Args[2] != "check" || wrapped.Args[3] != "--hello" {
		t.Fatalf("prepared process = %#v", wrapped)
	}
	profilePath := wrapped.Args[1]
	info, err := os.Stat(profilePath)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("profile mode = %04o, want 0600", got)
	}
	if got, err := os.ReadFile(profilePath); err != nil || !strings.Contains(string(got), "(deny default)") {
		t.Fatalf("profile content = %q, error = %v", got, err)
	}
	if err := cleanup(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(profilePath); !os.IsNotExist(err) {
		t.Fatalf("profile remains after cleanup: %v", err)
	}
}

func TestPrepareRejectsPublicRunTempDirectory(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	runDir := filepath.Join(root, "run")
	if err := os.Mkdir(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(runDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(runDir, "tmp"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, _, err := Prepare(contract.ProcessSpec{Executable: "check"}, Configuration{RunDir: runDir, Workspace: workspace})
	if err == nil {
		t.Fatal("Prepare accepted a non-private temp directory")
	}
}

func TestRealMacOSSandboxProbeEnforcesAllowAndDenyRules(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("requires macOS Seatbelt")
	}
	if !ToolAvailable() {
		t.Skip("sandbox-exec is not installed on this macOS host")
	}
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	checkout := filepath.Join(root, "checkout")
	runDir := filepath.Join(root, "run")
	cache := filepath.Join(root, "cache")
	logs := filepath.Join(runDir, "logs")
	for _, path := range []string{workspace, checkout, runDir, cache, logs} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	configuration := Configuration{
		RunDir:             runDir,
		Workspace:          workspace,
		WritablePaths:      []string{workspace, filepath.Join(runDir, "tmp"), "/tmp", cache},
		WriteDeniedPaths:   []string{checkout},
		ProtectedReadPaths: []string{filepath.Join(root, "credential")},
	}
	spec := contract.ProcessSpec{
		Executable: "ignored by Probe",
		Dir:        workspace,
		LogPath:    filepath.Join(logs, "probe-template.log"),
		Timeout:    5 * time.Second,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	result, err := Probe(ctx, process.Supervisor{}, spec, configuration)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Available {
		t.Fatalf("real macOS sandbox probe was unavailable: %s", result.Reason)
	}
	for _, expected := range []string{"workspace-write", "run-temp-write", "system-temp-write", "declared-cache-write", "denied-write", "denied-secret-read"} {
		if !containsString(result.Checks, expected) {
			t.Errorf("probe checks %v omit %q", result.Checks, expected)
		}
	}
	for _, path := range []string{workspace, checkout, cache, filepath.Join(runDir, "tmp")} {
		entries, err := os.ReadDir(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), "kogen-sandbox-probe-") {
				t.Errorf("probe artifact remains in %s: %s", path, entry.Name())
			}
		}
	}
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
