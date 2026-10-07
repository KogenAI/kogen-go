package linux

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"kogen-go/internal/contract"
	"kogen-go/internal/process"
)

func TestPreparePinsBubblewrapAndAppliesOrderedPolicy(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	runDir := filepath.Join(root, "run")
	checkout := filepath.Join(root, "checkout")
	origin := filepath.Join(root, "origin")
	home := filepath.Join(root, "home")
	cache := filepath.Join(root, "cache")
	for _, path := range []string{workspace, checkout, origin, home, cache, filepath.Join(home, ".ssh"), filepath.Join(home, ".local/share/keyrings")} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(runDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(runDir, "logs"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".ssh/id_ed25519"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "auth.json"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}

	configuration := Configuration{
		RunDir: runDir, Workspace: workspace, Home: home,
		WritablePaths:      []string{workspace, filepath.Join(runDir, "logs"), cache},
		WriteDeniedPaths:   []string{checkout, origin},
		ProtectedReadPaths: []string{filepath.Join(home, ".ssh"), filepath.Join(home, "auth.json")},
	}
	validated, err := configuration.validate()
	if err != nil {
		t.Fatal(err)
	}
	spec := contract.ProcessSpec{Executable: "/bin/sh", Args: []string{"-c", "exit 0"}, Dir: workspace, Env: []string{"PATH=/bin"}}
	prepared, err := prepareWithTool(spec, validated, BubblewrapPath)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Executable != BubblewrapPath {
		t.Fatalf("executable = %q, want pinned %q", prepared.Executable, BubblewrapPath)
	}
	args := prepared.Args
	canonical := func(path string) string {
		t.Helper()
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil {
			t.Fatal(err)
		}
		return resolved
	}
	for _, want := range []string{"--unshare-all", "--share-net", "--die-with-parent", "--new-session", "--cap-drop", "ALL", "--ro-bind", "/", "--dev", "/dev", "--proc", "/proc", "--chdir", workspace, "--", "/bin/sh"} {
		if !containsArg(args, want) {
			t.Errorf("bubblewrap argv lacks %q: %#v", want, args)
		}
	}
	for _, pair := range [][3]string{
		{"--bind", canonical(workspace), canonical(workspace)},
		{"--bind", canonical(cache), canonical(cache)},
		{"--ro-bind", canonical(checkout), canonical(checkout)},
		{"--ro-bind", canonical(origin), canonical(origin)},
		{"--tmpfs", canonical(filepath.Join(home, ".ssh")), ""},
		{"--tmpfs", canonical(filepath.Join(home, ".local/share/keyrings")), ""},
		{"--ro-bind", "/dev/null", canonical(filepath.Join(home, "auth.json"))},
	} {
		if !containsMount(args, pair[0], pair[1], pair[2]) {
			t.Errorf("missing mount %q %q %q in %#v", pair[0], pair[1], pair[2], args)
		}
	}
	if !mountPrecedes(args, "--bind", canonical(workspace), "--ro-bind", canonical(checkout)) {
		t.Errorf("write-denied bind must follow writable mounts: %#v", args)
	}
	for _, arg := range args {
		if len(arg) > 4<<10 {
			t.Errorf("argv element exceeded the 4 KiB contract: %d bytes", len(arg))
		}
	}
}

func TestPathAndToolValidationFailClosed(t *testing.T) {
	root := t.TempDir()
	runDir := filepath.Join(root, "run")
	workspace := filepath.Join(root, "workspace")
	if err := os.Mkdir(runDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, configuration := range []Configuration{
		{RunDir: runDir, Workspace: "relative"},
		{RunDir: runDir, Workspace: string(filepath.Separator)},
		{RunDir: runDir, Workspace: workspace, WritablePaths: []string{"/tmp/../etc"}},
		{RunDir: runDir, Workspace: workspace, ProtectedReadPaths: []string{"/tmp/bad\npath"}},
	} {
		if _, err := configuration.validate(); err == nil {
			t.Errorf("configuration should be refused: %#v", configuration)
		}
	}

	tool := filepath.Join(root, "tool")
	if err := os.WriteFile(tool, []byte("not a bwrap"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := inspectTool(tool); err != nil {
		t.Fatalf("private executable should satisfy file policy: %v", err)
	}
	if matchesPinnedVersion([]byte("bubblewrap 0.13.0\n")) != true || matchesPinnedVersion([]byte("bubblewrap 0.12.0\n")) || matchesPinnedVersion([]byte("bubblewrap 0.13.0-custom\n")) {
		t.Fatal("bubblewrap version check did not require the exact pinned release")
	}
	if err := os.Chmod(tool, 0o770); err != nil {
		t.Fatal(err)
	}
	if err := inspectTool(tool); err == nil {
		t.Fatal("group/world writable tool must be refused")
	}
	if err := os.Remove(tool); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "missing-target"), tool); err != nil {
		t.Fatal(err)
	}
	if err := inspectTool(tool); err == nil {
		t.Fatal("symlinked tool must be refused")
	}
}

func TestProtectedCredentialAliasIsResolvedBeforeMounting(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	runDir := filepath.Join(root, "run")
	home := filepath.Join(root, "home")
	secretDir := filepath.Join(home, ".ssh")
	for _, path := range []string{workspace, secretDir} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(runDir, 0o700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(home, "ssh-alias")
	if err := os.Symlink(secretDir, alias); err != nil {
		t.Fatal(err)
	}
	validated, err := (Configuration{RunDir: runDir, Workspace: workspace, ProtectedReadPaths: []string{alias}}).validate()
	if err != nil {
		t.Fatal(err)
	}
	args, err := bubblewrapArgs(contract.ProcessSpec{Executable: "/bin/true", Dir: workspace}, validated)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(secretDir)
	if err != nil {
		t.Fatal(err)
	}
	if !containsMount(args, "--tmpfs", resolved, "") {
		t.Fatalf("protected symlink target was not masked: %#v", args)
	}
	missingBehindAlias, exists, err := resolveExistingPath(filepath.Join(alias, "credentials-not-created"))
	if err != nil || exists || missingBehindAlias != filepath.Join(resolved, "credentials-not-created") {
		t.Fatalf("missing path behind a valid alias resolved to %q, exists=%t, err=%v", missingBehindAlias, exists, err)
	}
}

func TestProbeUsesExactUnavailableFallbackOffLinux(t *testing.T) {
	if runtime.GOOS == "linux" {
		t.Skip("Linux executes the real pinned-tool probe")
	}
	root := t.TempDir()
	runDir := filepath.Join(root, "run")
	workspace := filepath.Join(root, "workspace")
	if err := os.Mkdir(runDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	result, err := Probe(context.Background(), process.Supervisor{}, contract.ProcessSpec{}, Configuration{RunDir: runDir, Workspace: workspace})
	if err != nil {
		t.Fatal(err)
	}
	if UnavailableReason != "no supported confinement tool is available on this host" {
		t.Fatalf("unavailable fallback constant = %q", UnavailableReason)
	}
	if result.Available || result.Reason != UnavailableReason {
		t.Fatalf("unsupported-host probe = %#v", result)
	}
}

func TestBubblewrapProbeAndSupervisorProcessCustody(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("requires a real Linux host")
	}
	if !ToolAvailable() {
		t.Skip("pinned non-setuid /usr/bin/bwrap is not provisioned")
	}
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	runDir := filepath.Join(root, "run")
	checkout := filepath.Join(root, "checkout")
	origin := filepath.Join(root, "origin")
	home := filepath.Join(root, "home")
	for _, path := range []string{workspace, checkout, origin, home, filepath.Join(home, ".ssh"), filepath.Join(home, ".local/share/keyrings"), runDir, filepath.Join(runDir, "logs")} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	secret := filepath.Join(home, ".ssh/id_ed25519")
	if err := os.WriteFile(secret, []byte("real-fixture-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	configuration := Configuration{
		RunDir: runDir, Workspace: workspace, Home: home,
		WriteDeniedPaths:   []string{checkout, origin},
		ProtectedReadPaths: []string{filepath.Join(home, ".ssh"), secret},
	}
	supervisor := process.Supervisor{}
	template := contract.ProcessSpec{Dir: workspace, LogPath: filepath.Join(runDir, "logs/template.log"), Env: []string{"PATH=/usr/bin:/bin"}}
	availability, err := Probe(context.Background(), supervisor, template, configuration)
	if err != nil {
		t.Fatal(err)
	}
	if !availability.Available {
		t.Skipf("pinned bwrap cannot create the required host namespaces: %s", availability.Reason)
	}
	for _, check := range []string{"workspace-write", "run-temp-write", "system-temp-write", "denied-write", "denied-secret-read", "mount-namespace", "pid-namespace", "ipc-namespace", "uts-namespace", "network-shared", "capabilities-dropped"} {
		if !containsArg(availability.Checks, check) {
			t.Errorf("probe omitted %q: %#v", check, availability.Checks)
		}
	}

	allowed := filepath.Join(workspace, "allowed.txt")
	checkoutWrite := filepath.Join(checkout, "must-not-exist")
	originWrite := filepath.Join(origin, "must-not-exist")
	script := "printf allowed > " + shellLiteral(allowed) + " && if printf denied > " + shellLiteral(checkoutWrite) + " 2>/dev/null; then exit 42; fi && if printf denied > " + shellLiteral(originWrite) + " 2>/dev/null; then exit 43; fi && if IFS= read -r value < " + shellLiteral(secret) + "; then [ \"$value\" != real-fixture-secret ] || exit 44; fi"
	spec := contract.ProcessSpec{Executable: "/bin/sh", Args: []string{"-c", script}, Dir: workspace, Env: []string{"PATH=/usr/bin:/bin"}, LogPath: filepath.Join(runDir, "logs/actual.log"), Timeout: 3 * time.Second}
	prepared, err := Prepare(spec, configuration)
	if err != nil {
		t.Fatal(err)
	}
	result, err := supervisor.Run(context.Background(), prepared)
	if err != nil {
		t.Fatal(err)
	}
	if !successful(result) {
		t.Fatalf("confined child failed: status=%v tail=%q", result.ExitStatus, result.OutputTail)
	}
	if contents, err := os.ReadFile(allowed); err != nil || string(contents) != "allowed" {
		t.Fatalf("workspace write = %q, %v", contents, err)
	}
	if _, err := os.Stat(checkoutWrite); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("checkout write escaped sandbox: %v", err)
	}
	if _, err := os.Stat(originWrite); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("origin write escaped sandbox: %v", err)
	}
	if strings.Contains(string(result.OutputTail), "real-fixture-secret") {
		t.Fatal("protected credential bytes appeared in child output")
	}

	marker := filepath.Join(workspace, "grandchild-survived")
	longScript := "( sleep 0.6; printf survived > " + shellLiteral(marker) + " ) & wait"
	longSpec := contract.ProcessSpec{Executable: "/bin/sh", Args: []string{"-c", longScript}, Dir: workspace, Env: []string{"PATH=/usr/bin:/bin"}, LogPath: filepath.Join(runDir, "logs/timeout.log"), Timeout: 250 * time.Millisecond}
	prepared, err = Prepare(longSpec, configuration)
	if err != nil {
		t.Fatal(err)
	}
	result, err = supervisor.Run(context.Background(), prepared)
	if err != nil {
		t.Fatal(err)
	}
	if !result.TimedOut || result.Duration > 1250*time.Millisecond {
		t.Fatalf("supervisor deadline result: timeout=%t duration=%s", result.TimedOut, result.Duration)
	}
	time.Sleep(700 * time.Millisecond)
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("supervised sandbox descendant wrote after timeout: %v", err)
	}
}

func containsArg(args []string, value string) bool {
	for _, arg := range args {
		if arg == value {
			return true
		}
	}
	return false
}

func containsMount(args []string, option, source, destination string) bool {
	for index := 0; index+2 < len(args); index++ {
		if args[index] == option && args[index+1] == source && args[index+2] == destination {
			return true
		}
	}
	if destination == "" {
		for index := 0; index+1 < len(args); index++ {
			if args[index] == option && args[index+1] == source {
				return true
			}
		}
	}
	return false
}

func mountPrecedes(args []string, firstOption, firstPath, secondOption, secondPath string) bool {
	first, second := -1, -1
	for index := 0; index+2 < len(args); index++ {
		if args[index] == firstOption && args[index+1] == firstPath {
			first = index
		}
		if args[index] == secondOption && args[index+1] == secondPath {
			second = index
		}
	}
	return first >= 0 && second > first
}
