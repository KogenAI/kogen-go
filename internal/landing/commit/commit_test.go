package commit

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"kogen-go/internal/acceptance"
	acceptancecommand "kogen-go/internal/acceptance/command"
	"kogen-go/internal/contract"
	"kogen-go/internal/gate"
	"kogen-go/internal/gitio"
	"kogen-go/internal/intent"
	"kogen-go/internal/process"
	"kogen-go/internal/protection"
	"kogen-go/internal/testkit"
)

func TestCreateMakesSingleParentSquashWithExactApprovedFilesAndMessage(t *testing.T) {
	state := newLandingState(t)
	if !state.gateReport.IsLandable() {
		t.Fatalf("gate report is not landable: %s", state.gateReport.Verdict())
	}
	receipt, ok := state.gateReport.Receipt()
	if !ok {
		t.Fatal("passing gate did not issue a receipt")
	}
	if strings.Contains(string(state.fixture.RunIn(t, state.workspace, "ls-tree", "-r", "--name-only", string(mustID(t, receipt.CandidateTree)))), "test-candidates/greeting.t.sh") {
		t.Fatal("fixture did not exercise the ignored candidate-test path")
	}

	hookMarker := filepath.Join(state.fixture.Root, "hook-ran")
	hook := filepath.Join(state.workspace, ".git", "hooks", "pre-commit")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nprintf ran > "+hookMarker+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(hook, 0o700); err != nil {
		t.Fatal(err)
	}

	request := state.request()
	result, err := Create(context.Background(), request)
	if err != nil {
		t.Fatalf("create landing commit: %v", err)
	}
	wantMessage := "Landing greeting\n\nKogen-Intent: greeting\n"
	if string(result.Message) != wantMessage {
		t.Fatalf("commit message = %q, want %q", result.Message, wantMessage)
	}
	if result.Parent != state.baseCommit || result.VerifiedTree != contract.ObjectID(receipt.CandidateTree) {
		t.Fatalf("result base binding = %+v; receipt=%+v", result, receipt)
	}
	if result.Tree == result.VerifiedTree {
		t.Fatal("final tree did not force the approved ignored files into the commit")
	}

	parents := strings.Fields(string(state.fixture.RunIn(t, state.workspace, "show", "-s", "--format=%P", string(result.Commit))))
	if len(parents) != 1 || parents[0] != string(state.baseCommit) {
		t.Fatalf("landing commit parents = %q, want sole parent %s", parents, state.baseCommit)
	}
	gotTree := strings.TrimSpace(string(state.fixture.RunIn(t, state.workspace, "rev-parse", string(result.Commit)+"^{tree}")))
	if gotTree != string(result.Tree) {
		t.Fatalf("landing commit tree = %s, want %s", gotTree, result.Tree)
	}
	gotIntent := state.fixture.RunIn(t, state.workspace, "show", string(result.Commit)+":.kogen/intents/greeting/intent.md")
	if !bytes.Equal(gotIntent, state.intentBytes) {
		t.Fatalf("committed Intent bytes differ: got %q", gotIntent)
	}
	gotCandidate := state.fixture.RunIn(t, state.workspace, "show", string(result.Commit)+":test-candidates/greeting.t.sh")
	if !bytes.Equal(gotCandidate, state.candidateBytes) {
		t.Fatalf("committed candidate-test bytes differ: got %q", gotCandidate)
	}
	paths := strings.Split(strings.TrimSpace(string(state.fixture.RunIn(t, state.workspace, "ls-tree", "-r", "--name-only", string(result.Commit)))), "\n")
	if contains(paths, ".kogen/acceptance/greeting.t.sh") {
		t.Fatal("landing commit retained the source acceptance copy")
	}
	if !contains(paths, ".kogen/intents/greeting/intent.md") || !contains(paths, "test-candidates/greeting.t.sh") {
		t.Fatalf("landing tree omitted approved files: %v", paths)
	}
	if !contains(paths, "README.md") {
		t.Fatalf("landing tree omitted the builder change: %v", paths)
	}
	if got := strings.TrimSpace(string(state.fixture.RunIn(t, state.workspace, "rev-parse", "HEAD"))); got != state.builderHead {
		t.Fatalf("Create moved workspace HEAD from builder commit %s to %s", state.builderHead, got)
	}
	if _, err := os.Stat(hookMarker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pre-commit hook ran during landing: stat error = %v", err)
	}
}

func TestCreateRejectsProtectedByteChangesAndPostGateCandidateChanges(t *testing.T) {
	t.Run("protected approved bytes", func(t *testing.T) {
		state := newLandingState(t)
		intentPath := filepath.Join(state.workspace, ".kogen", "intents", "greeting", "intent.md")
		if err := os.WriteFile(intentPath, []byte("changed after approval\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := Create(context.Background(), state.request())
		if !errors.Is(err, ErrProtectedBytes) {
			t.Fatalf("Create error = %v, want ErrProtectedBytes", err)
		}
	})

	t.Run("candidate tree", func(t *testing.T) {
		state := newLandingState(t)
		if err := os.WriteFile(filepath.Join(state.workspace, "README.md"), []byte("changed after gate\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := Create(context.Background(), state.request())
		if !errors.Is(err, ErrVerificationMismatch) {
			t.Fatalf("Create error = %v, want ErrVerificationMismatch", err)
		}
	})
}

func TestCreateUsesOriginSigningConfigAndIgnoresWorkspaceSigningHelper(t *testing.T) {
	state := newLandingState(t)
	root := state.fixture.Root
	globalMarker := filepath.Join(root, "global-gpg-ran")
	localMarker := filepath.Join(root, "local-gpg-ran")
	globalGPG := writeFakeGPG(t, root, "global-gpg", globalMarker)
	localGPG := writeFakeGPG(t, root, "local-gpg", localMarker)
	globalConfig := filepath.Join(root, "global.gitconfig")
	config := "[user]\n\tname = Origin Signing User\n\temail = origin-signing@example.invalid\n\tsigningkey = landing-test-key\n[commit]\n\tgpgsign = true\n[gpg]\n\tprogram = " + globalGPG + "\n"
	if err := os.WriteFile(globalConfig, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	state.fixture.RunIn(t, state.workspace, "config", "--local", "gpg.program", localGPG)
	state.fixture.RunIn(t, state.workspace, "config", "--local", "commit.gpgsign", "false")

	environment := make(process.Environment, len(state.environment)+1)
	for key, value := range state.environment {
		environment[key] = value
	}
	for _, key := range []string{"GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL", "GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL"} {
		delete(environment, key)
	}
	environment["GIT_CONFIG_GLOBAL"] = globalConfig
	request := state.request()
	request.Environment = environment
	result, err := Create(context.Background(), request)
	if err != nil {
		t.Fatalf("create signed landing commit: %v", err)
	}
	author := strings.TrimSpace(string(state.fixture.RunIn(t, state.workspace, "show", "-s", "--format=%an <%ae>", string(result.Commit))))
	if author != "Origin Signing User <origin-signing@example.invalid>" {
		t.Fatalf("landing author = %q, want origin global identity", author)
	}
	commitBytes := state.fixture.RunIn(t, state.workspace, "cat-file", "commit", string(result.Commit))
	if !bytes.Contains(commitBytes, []byte("gpgsig ")) {
		t.Fatalf("landing commit has no signature header: %q", commitBytes)
	}
	if _, err := os.Stat(globalMarker); err != nil {
		t.Fatalf("configured global signing helper did not run: %v", err)
	}
	if _, err := os.Stat(localMarker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("workspace-selected signing helper ran: stat error = %v", err)
	}
}

type landingState struct {
	fixture        *testkit.GitFixture
	workspace      string
	baseCommit     contract.ObjectID
	builderHead    string
	intentBytes    []byte
	candidateBytes []byte
	protector      *protection.Protector
	gateReport     *gate.GateReport
	environment    process.Environment
}

func newLandingState(t *testing.T) *landingState {
	t.Helper()
	fixture := testkit.NewGitFixture(t)
	ctx := context.Background()
	baseCommit := contract.ObjectID(strings.TrimSpace(string(fixture.Run(t, "rev-parse", "HEAD"))))
	workspace := filepath.Join(fixture.Root, "candidate")
	fixture.RunIn(t, fixture.Root, "clone", "--quiet", "--local", "--no-hardlinks", "--no-checkout", fixture.Checkout, workspace)
	fixture.RunIn(t, workspace, "checkout", "--quiet", "--detach", string(baseCommit))

	intentBytes := []byte("---\ntitle: Landing greeting\nsize: small\ndomains: [app]\n---\n\nUpdate the greeting.\n\n## Acceptance\n- A1: the greeting works\n\n## Verify\n- A1: test\n")
	candidateBytes := []byte("#!/bin/sh\nprintf 'approved greeting\\n'\n")
	if _, err := intent.Parse("greeting", intentBytes); err != nil {
		t.Fatalf("parse test Intent: %v", err)
	}
	intentPath := filepath.Join(workspace, ".kogen", "intents", "greeting", "intent.md")
	sourcePath := filepath.Join(workspace, ".kogen", "acceptance", "greeting.t.sh")
	if err := os.MkdirAll(filepath.Dir(intentPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(sourcePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(intentPath, intentBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sourcePath, []byte("source copy before build\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, ".gitignore"), []byte(".kogen/intents/\ntest-candidates/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "README.md"), []byte("builder change\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fixture.RunIn(t, workspace, "add", "-A")
	fixture.RunIn(t, workspace, "commit", "--message", "builder partial commit")
	builderHead := strings.TrimSpace(string(fixture.RunIn(t, workspace, "rev-parse", "HEAD")))

	manifest := protection.Manifest{
		".kogen/intents/greeting/intent.md": manifestEntry(intentBytes),
		"test-candidates/greeting.t.sh":     manifestEntry(candidateBytes),
	}
	protector, err := protection.NewProtector(manifest, []string{".kogen/acceptance/greeting.t.sh"})
	if err != nil {
		t.Fatalf("create protector: %v", err)
	}
	environment := environmentMap(fixture.Environment())
	workspaceGit := gitio.NewWorkspace(process.Supervisor{})
	workspacePolicy := gitio.WorkspacePolicy(workspace, environment)
	baseMetadata, err := gitio.LoadBaseMetadata(ctx, workspaceGit, workspacePolicy, baseCommit)
	if err != nil {
		t.Fatalf("load base metadata: %v", err)
	}
	runDir := filepath.Join(fixture.Root, "gate-run")
	if err := os.Mkdir(runDir, 0o700); err != nil {
		t.Fatal(err)
	}
	gateReport, err := gate.Run(ctx, gate.Request{
		Processes:          process.Supervisor{},
		AcceptanceRunner:   passingAcceptance{},
		Trees:              acceptance.CandidateTree{Git: workspaceGit, Policy: workspacePolicy, Base: baseMetadata},
		BaseWorkspace:      fixture.Checkout,
		CandidateWorkspace: workspace,
		RunDir:             runDir,
		ExpectedBaseTree:   string(baseMetadata.Tree()),
		ApprovalSHA256:     intent.ApprovalSHA256(intentBytes, candidateBytes),
		Acceptance: gate.AcceptancePlan{
			Request: acceptancecommand.Request{
				Slug: "greeting",
				Config: acceptancecommand.Config{
					Extension:    ".t.sh",
					CandidateDir: "test-candidates",
					Run:          []string{"true"},
					Timeout:      time.Second,
				},
				ExpectedItems: []string{"A1"},
			},
			ApprovedBytes: candidateBytes,
			ChangeItems:   []string{"A1"},
		},
		Protection: protector,
	})
	if err != nil {
		t.Fatalf("run gate: %v", err)
	}
	return &landingState{
		fixture: fixture, workspace: workspace, baseCommit: baseCommit, builderHead: builderHead,
		intentBytes: intentBytes, candidateBytes: candidateBytes, protector: protector,
		gateReport: gateReport, environment: environment,
	}
}

func (s *landingState) request() Request {
	return Request{
		Processes:            process.Supervisor{},
		Environment:          s.environment,
		Workspace:            s.workspace,
		BaseCommit:           s.baseCommit,
		Slug:                 "greeting",
		IntentBytes:          bytes.Clone(s.intentBytes),
		AcceptanceSourcePath: ".kogen/acceptance/greeting.t.sh",
		CandidatePath:        "test-candidates/greeting.t.sh",
		CandidateBytes:       bytes.Clone(s.candidateBytes),
		Protection:           s.protector,
		Gate:                 s.gateReport,
	}
}

type passingAcceptance struct{}

func (passingAcceptance) Run(context.Context, gate.AcceptanceExecution) (acceptance.Result, error) {
	status := 0
	return acceptance.Result{Process: contract.ProcessResult{ExitStatus: &status}, ItemPass: map[string]bool{"A1": true}}, nil
}

func manifestEntry(contents []byte) protection.Entry {
	digest := sha256.Sum256(contents)
	return protection.Entry{SHA256: hex.EncodeToString(digest[:]), Bytes: bytes.Clone(contents), Mode: gitio.GitModeRegular, Present: true}
}

func writeFakeGPG(t *testing.T, directory, name, marker string) string {
	t.Helper()
	path := filepath.Join(directory, name)
	script := "#!/bin/sh\nprintf ran > '" + marker + "'\nout=\nwhile [ \"$#\" -gt 0 ]; do\n  case \"$1\" in\n    --output) shift; out=$1 ;;\n    --output=*) out=${1#--output=} ;;\n  esac\n  shift\ndone\ncat >/dev/null\nif [ -n \"$out\" ]; then\n  printf '%s\\n' '-----BEGIN PGP SIGNATURE-----' 'fake signature bytes' '-----END PGP SIGNATURE-----' > \"$out\"\nelse\n  printf '%s\\n' '-----BEGIN PGP SIGNATURE-----' 'fake signature bytes' '-----END PGP SIGNATURE-----'\nfi\nprintf '%s\\n' '[GNUPG:] SIG_CREATED D 1 8 00 0 0123456789ABCDEF0123456789ABCDEF01234567' >&2\n"
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func environmentMap(entries []string) process.Environment {
	result := make(process.Environment, len(entries))
	for _, entry := range entries {
		key, value, _ := strings.Cut(entry, "=")
		result[key] = value
	}
	return result
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func mustID(t *testing.T, value string) contract.ObjectID {
	t.Helper()
	id, err := gitio.ParseObjectID(value)
	if err != nil {
		t.Fatal(err)
	}
	return id
}
