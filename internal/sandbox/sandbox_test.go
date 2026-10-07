package sandbox

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

type processFunc func(context.Context, contract.ProcessSpec) (contract.ProcessResult, error)

func (f processFunc) Run(ctx context.Context, spec contract.ProcessSpec) (contract.ProcessResult, error) {
	return f(ctx, spec)
}

type sequenceSnapshot struct {
	values []string
	count  int
	err    error
}

func (s *sequenceSnapshot) Snapshot(context.Context) (string, error) {
	if s.err != nil {
		return "", s.err
	}
	index := s.count
	s.count++
	if index >= len(s.values) {
		return s.values[len(s.values)-1], nil
	}
	return s.values[index], nil
}

func TestBuildAndCheckoutPoliciesKeepTheCheckoutBoundaryDistinct(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	checkout := filepath.Join(root, "checkout")
	origin := filepath.Join(root, "origin")
	runDir := filepath.Join(root, "run")
	home := filepath.Join(root, "home")
	for _, path := range []string{workspace, checkout, origin, runDir, home} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{filepath.Join(home, ".kogen"), filepath.Join(home, ".ssh"), filepath.Join(home, ".gnupg"), filepath.Join(home, ".codex")} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	credential := filepath.Join(home, ".kogen", "credentials-extra.json")
	authFile := filepath.Join(root, "auth.json")
	host := map[string]string{
		"HOME":            home,
		"KOGEN_AUTH_PATH": authFile,
		"GOMODCACHE":      filepath.Join(root, "go-cache"),
	}
	if err := os.WriteFile(credential, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	build, err := NewBuildPolicy(true, workspace, checkout, origin, runDir, host)
	if err != nil {
		t.Fatal(err)
	}
	if !build.verifyIntegrity {
		t.Fatal("Build policy did not enable source integrity checks")
	}
	if !contains(build.writeDeniedPaths, checkout) || !contains(build.writeDeniedPaths, origin) {
		t.Fatalf("Build policy does not deny checkout/origin writes: %#v", build.writeDeniedPaths)
	}
	if !contains(build.writablePaths, workspace) || !contains(build.writablePaths, filepath.Join(runDir, "tmp")) || !contains(build.writablePaths, host["GOMODCACHE"]) {
		t.Fatalf("Build policy lacks workspace, temp or module cache writes: %#v", build.writablePaths)
	}
	for _, protected := range []string{filepath.Join(home, ".kogen", "credentials"), credential, filepath.Join(home, ".ssh"), filepath.Join(home, ".gnupg"), filepath.Join(home, ".codex"), authFile} {
		if !contains(build.protectedReadPaths, protected) {
			t.Errorf("Build policy does not protect %s", protected)
		}
	}
	checkoutPolicy, err := NewCheckoutPolicy(true, checkout, runDir, host)
	if err != nil {
		t.Fatal(err)
	}
	if checkoutPolicy.verifyIntegrity || !contains(checkoutPolicy.writablePaths, checkout) {
		t.Fatalf("checkout policy has wrong mode: verify=%t writable=%v", checkoutPolicy.verifyIntegrity, checkoutPolicy.writablePaths)
	}
	if len(checkoutPolicy.writeDeniedPaths) != 0 {
		t.Fatalf("checkout policy unexpectedly denies checkout writes: %v", checkoutPolicy.writeDeniedPaths)
	}
}

func TestPolicyHostModesAreDistinctAndOrdered(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	runDir := filepath.Join(root, "run")
	if err := os.Mkdir(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(runDir, 0o700); err != nil {
		t.Fatal(err)
	}
	policy, err := NewCheckoutPolicy(false, workspace, runDir, map[string]string{
		"KOGEN_SANDBOX":   "unavailable",
		"KOGEN_SANDBOXED": "1",
	})
	if err != nil {
		t.Fatal(err)
	}
	called := 0
	runner := NewRunner(processFunc(func(_ context.Context, spec contract.ProcessSpec) (contract.ProcessResult, error) {
		called++
		if spec.Executable != "fixture" {
			t.Fatalf("off mode wrapped child as %q", spec.Executable)
		}
		return contract.ProcessResult{ExitStatus: intRef(0)}, nil
	}), policy, nil)
	execution, err := runner.Run(context.Background(), contract.ProcessSpec{Executable: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	if execution.Observation.Status != StatusOff || execution.Observation.WarningReason != "" || called != 1 {
		t.Fatalf("off mode = %#v, calls=%d", execution.Observation, called)
	}

	already, err := NewCheckoutPolicy(true, workspace, runDir, map[string]string{
		"KOGEN_SANDBOX":   "unavailable",
		"KOGEN_SANDBOXED": "1",
	})
	if err != nil {
		t.Fatal(err)
	}
	alreadyRunner := NewRunner(processFunc(func(_ context.Context, spec contract.ProcessSpec) (contract.ProcessResult, error) {
		if spec.Executable != "fixture" {
			t.Fatalf("already-confined child was wrapped as %q", spec.Executable)
		}
		return contract.ProcessResult{ExitStatus: intRef(0)}, nil
	}), already, nil)
	execution, err = alreadyRunner.Run(context.Background(), contract.ProcessSpec{Executable: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	if execution.Observation.Status != StatusConfined || execution.Observation.WarningReason != "" {
		t.Fatalf("already-confined mode = %#v", execution.Observation)
	}

	forced, err := NewCheckoutPolicy(true, workspace, runDir, map[string]string{"KOGEN_SANDBOX": "unavailable"})
	if err != nil {
		t.Fatal(err)
	}
	forcedRunner := NewRunner(processFunc(func(_ context.Context, spec contract.ProcessSpec) (contract.ProcessResult, error) {
		if spec.Executable != "fixture" {
			t.Fatalf("forced-unavailable child was wrapped as %q", spec.Executable)
		}
		return contract.ProcessResult{ExitStatus: intRef(0)}, nil
	}), forced, nil)
	execution, err = forcedRunner.Run(context.Background(), contract.ProcessSpec{Executable: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	if execution.Observation.Status != StatusUnconfined || execution.Observation.WarningReason != "forced by KOGEN_SANDBOX=unavailable" {
		t.Fatalf("forced-unavailable mode = %#v", execution.Observation)
	}
}

func TestUnconfinedBuildRequiresAndComparesIntegritySnapshots(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	runDir := filepath.Join(root, "run")
	if err := os.Mkdir(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(runDir, 0o700); err != nil {
		t.Fatal(err)
	}
	policy, err := NewBuildPolicy(true, workspace, filepath.Join(root, "checkout"), filepath.Join(root, "origin"), runDir, map[string]string{"KOGEN_SANDBOX": "unavailable"})
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	inner := processFunc(func(_ context.Context, _ contract.ProcessSpec) (contract.ProcessResult, error) {
		calls++
		return contract.ProcessResult{ExitStatus: intRef(0)}, nil
	})
	spec := contract.ProcessSpec{Executable: "fixture"}
	withoutSnapshot := NewRunner(inner, policy, nil)
	if _, err := withoutSnapshot.Run(context.Background(), spec); !errors.Is(err, ErrIntegritySnapshotRequired) {
		t.Fatalf("missing snapshot error = %v", err)
	}
	if calls != 0 {
		t.Fatalf("child ran without a required snapshot: calls=%d", calls)
	}

	changing := &sequenceSnapshot{values: []string{"before", "after"}}
	guarded := NewRunner(inner, policy, changing)
	if _, err := guarded.Run(context.Background(), spec); !errors.Is(err, ErrIntegrityChanged) {
		t.Fatalf("changed source integrity error = %v", err)
	}
	if changing.count != 2 || calls != 1 {
		t.Fatalf("snapshot count=%d child calls=%d", changing.count, calls)
	}

	stable := &sequenceSnapshot{values: []string{"same", "same"}}
	guarded = NewRunner(inner, policy, stable)
	execution, err := guarded.Run(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if execution.Observation.Status != StatusUnconfined || execution.Observation.WarningReason == "" || stable.count != 2 {
		t.Fatalf("stable unconfined result=%#v snapshots=%d", execution.Observation, stable.count)
	}

	offPolicy, err := NewBuildPolicy(false, workspace, "", "", runDir, nil)
	if err != nil {
		t.Fatal(err)
	}
	offSnapshot := &sequenceSnapshot{values: []string{"off-before", "off-after"}}
	execution, err = NewRunner(inner, offPolicy, offSnapshot).Run(context.Background(), spec)
	if !errors.Is(err, ErrIntegrityChanged) {
		t.Fatalf("sandbox-off Build did not enforce integrity guard: %v", err)
	}
	if offSnapshot.count != 2 {
		t.Fatalf("sandbox-off Build snapshots=%d, want 2", offSnapshot.count)
	}
}

func TestSnapshotErrorStopsUnconfinedChild(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	runDir := filepath.Join(root, "run")
	if err := os.Mkdir(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(runDir, 0o700); err != nil {
		t.Fatal(err)
	}
	policy, err := NewBuildPolicy(true, workspace, "", "", runDir, map[string]string{"KOGEN_SANDBOX": "unavailable"})
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	inner := processFunc(func(context.Context, contract.ProcessSpec) (contract.ProcessResult, error) {
		calls++
		return contract.ProcessResult{ExitStatus: intRef(0)}, nil
	})
	guarded := NewRunner(inner, policy, &sequenceSnapshot{err: errors.New("git snapshot failed")})
	if _, err := guarded.Run(context.Background(), contract.ProcessSpec{Executable: "fixture"}); err == nil {
		t.Fatal("snapshot failure was ignored")
	}
	if calls != 0 {
		t.Fatalf("child ran after snapshot failure: calls=%d", calls)
	}
}

func TestSandboxExecMissingTargetIsNormalized(t *testing.T) {
	if !sandboxExecTargetMissing([]byte("sandbox-exec: execvp() of 'missing-check' failed: No such file or directory\n")) {
		t.Fatal("sandbox-exec missing-target diagnostic was not recognized")
	}
	if sandboxExecTargetMissing([]byte("missing-check: No such file or directory\n")) {
		t.Fatal("ordinary child missing-target diagnostic was misclassified")
	}
}

func TestMacOSBuildRunnerConfinesWorkspaceCacheAndSecrets(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("requires macOS Seatbelt")
	}
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	checkout := filepath.Join(root, "checkout")
	origin := filepath.Join(root, "origin")
	runDir := filepath.Join(root, "run")
	cache := filepath.Join(root, "cache")
	home := filepath.Join(root, "home")
	logs := filepath.Join(runDir, "logs")
	for _, path := range []string{workspace, checkout, origin, runDir, cache, home, logs} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	secret := filepath.Join(home, "credential")
	if err := os.WriteFile(secret, []byte("private-sentinel"), 0o600); err != nil {
		t.Fatal(err)
	}
	policy, err := NewBuildPolicy(true, workspace, checkout, origin, runDir, map[string]string{
		"HOME":            home,
		"KOGEN_AUTH_PATH": secret,
	})
	if err != nil {
		t.Fatal(err)
	}
	policy, err = policy.WithWritablePath(cache)
	if err != nil {
		t.Fatal(err)
	}
	allowedWorkspace := filepath.Join(workspace, "allowed.txt")
	allowedCache := filepath.Join(cache, "allowed.txt")
	allowedTemp := filepath.Join(runDir, "tmp", "allowed.txt")
	deniedCheckout := filepath.Join(checkout, "denied.txt")
	command := "printf workspace > '" + allowedWorkspace + "' && printf cache > '" + allowedCache + "' && printf temp > '" + allowedTemp + "' && " +
		"if /bin/cat '" + secret + "' >/dev/null 2>&1; then exit 43; fi && " +
		"if printf denied > '" + deniedCheckout + "' 2>/dev/null; then exit 42; fi"
	spec := contract.ProcessSpec{
		Executable:      "/bin/sh",
		Args:            []string{"-c", command},
		Dir:             workspace,
		Env:             []string{"HOME=" + home, "PATH=/bin:/usr/bin"},
		Timeout:         5 * time.Second,
		OutputLimit:     4096,
		OutputTailLimit: 4096,
		LogPath:         filepath.Join(logs, "child.log"),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	execution, err := NewRunner(process.Supervisor{}, policy, nil).Run(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	if execution.Observation.Status != StatusConfined {
		t.Fatalf("Build command was not confined: %#v", execution.Observation)
	}
	if execution.Process.ExitStatus == nil || *execution.Process.ExitStatus != 0 {
		t.Fatalf("child status=%v output=%q", execution.Process.ExitStatus, execution.Process.OutputTail)
	}
	for path, expected := range map[string]string{allowedWorkspace: "workspace", allowedCache: "cache", allowedTemp: "temp"} {
		got, err := os.ReadFile(path)
		if err != nil || string(got) != expected {
			t.Errorf("allowed file %s = %q, error = %v", path, got, err)
		}
	}
	if _, err := os.Lstat(deniedCheckout); !os.IsNotExist(err) {
		t.Errorf("child wrote into checkout: %v", err)
	}
	if strings.Contains(string(execution.Process.OutputTail), "private-sentinel") {
		t.Fatal("secret bytes appeared in child output")
	}
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func intRef(value int) *int { return &value }
