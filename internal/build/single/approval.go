package single

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"regexp"
	"strings"
	"time"

	"kogen-go/internal/approval/prepare"
	"kogen-go/internal/contract"
	"kogen-go/internal/gate"
	"kogen-go/internal/gitio"
	"kogen-go/internal/intent"
	"kogen-go/internal/project"
	"kogen-go/internal/protection"
	"kogen-go/internal/safefs"
	"kogen-go/internal/yamlmini"
)

var approvalSlugPattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

// GitApprovalSource reads schema-2 approval packages from refs/kogen/intents.
// It validates the ref, package metadata, exact source digests, target branch,
// baseline rows, and the approval-bound protection manifest.
type GitApprovalSource struct {
	Git    contract.GitPort
	Policy func(string) contract.GitPolicy
	Roots  contract.RootOpener
}

type approvalPackage struct {
	Schema            int                   `json:"schema"`
	Slug              string                `json:"slug"`
	ApprovalSHA256    string                `json:"approval_sha256"`
	IntentSHA256      string                `json:"intent_sha256"`
	TargetBranch      string                `json:"target_branch"`
	BaseSHA           string                `json:"base_sha"`
	Domains           []string              `json:"domains"`
	AcceptancePaths   []string              `json:"acceptance_paths"`
	ProtectedManifest map[string]string     `json:"protected_manifest"`
	CheckBaseline     []prepare.BaselineRow `json:"check_baseline"`
	Witness           json.RawMessage       `json:"witness"`
	By                string                `json:"by"`
	At                string                `json:"at"`
}

func (s GitApprovalSource) Load(ctx context.Context, resolved *project.Resolution, slug string) (*Approval, error) {
	if ctx == nil || resolved == nil || s.Git == nil || s.Policy == nil || resolved.Origin == "" {
		return nil, errors.New("approval loader requires context, project, Git and origin policy")
	}
	if !approvalSlugPattern.MatchString(slug) || len(slug) < 3 || len(slug) > 48 {
		return nil, errors.New("approval slug is invalid")
	}
	policy := s.Policy(resolved.Origin)
	if policy.WorkingDirectory != resolved.Origin {
		return nil, errors.New("approval Git policy is not rooted at the resolved origin")
	}
	ref := "refs/kogen/intents/" + slug
	if err := requireDirectRef(ctx, s.Git, policy, ref); err != nil {
		return nil, err
	}
	refs := gitio.NewRefPort(s.Git, policy)
	observation, err := refs.ReadRef(ctx, ref)
	if err != nil {
		return nil, fmt.Errorf("read approval ref: %w", err)
	}
	if !observation.Exists {
		return nil, fs.ErrNotExist
	}
	approvalCommit, err := refs.ResolveCommit(ctx, string(observation.Target))
	if err != nil || approvalCommit != observation.Target {
		return nil, errors.New("approval ref does not point directly to a commit")
	}
	intentPath := ".kogen/intents/" + slug + "/intent.md"
	approvalPath := ".kogen/intents/" + slug + "/approval.json"
	intentBytes, err := readBlob(ctx, s.Git, policy, observation.Target, intentPath)
	if err != nil {
		return nil, fmt.Errorf("read approved Intent bytes: %w", err)
	}
	acceptancePath, candidatePath, err := acceptancePaths(resolved, slug)
	if err != nil {
		return nil, err
	}
	acceptanceBytes, err := readBlob(ctx, s.Git, policy, observation.Target, acceptancePath)
	if err != nil {
		return nil, fmt.Errorf("read approved acceptance bytes: %w", err)
	}
	packageBytes, err := readBlob(ctx, s.Git, policy, observation.Target, approvalPath)
	if err != nil {
		return nil, fmt.Errorf("read approval metadata: %w", err)
	}
	var document approvalPackage
	decoder := json.NewDecoder(bytes.NewReader(packageBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil {
		return nil, fmt.Errorf("decode approval metadata: %w", err)
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return nil, errors.New("approval metadata has trailing JSON")
	}
	if document.Schema != 2 || document.Slug != slug || document.TargetBranch != resolved.Base {
		return nil, errors.New("approval metadata schema, slug, or target branch does not match")
	}
	if len(document.AcceptancePaths) != 1 || document.AcceptancePaths[0] != acceptancePath {
		return nil, errors.New("approval metadata does not identify the expected acceptance source")
	}
	parsed, err := intent.Parse(slug, intentBytes)
	if err != nil {
		return nil, fmt.Errorf("parse approved Intent: %w", err)
	}
	if !sameStrings(document.Domains, parsed.Frontmatter.Domains) {
		return nil, errors.New("approval domains do not match the approved Intent")
	}
	if document.ApprovalSHA256 != intent.ApprovalSHA256(intentBytes, acceptanceBytes) || document.IntentSHA256 != intent.IntentSHA256(intentBytes) {
		return nil, errors.New("approval metadata digest does not match the exact approved bytes")
	}
	if strings.TrimSpace(document.By) == "" || strings.ContainsAny(document.By, "\r\n\x00") {
		return nil, errors.New("approval metadata has an invalid approver")
	}
	if _, err := time.Parse(time.RFC3339, document.At); err != nil {
		return nil, errors.New("approval metadata has an invalid timestamp")
	}
	baseCommit, err := gitio.ParseObjectID(document.BaseSHA)
	if err != nil {
		return nil, fmt.Errorf("approval base commit: %w", err)
	}
	baseTree, err := refs.ResolveTree(ctx, string(baseCommit))
	if err != nil {
		return nil, fmt.Errorf("resolve approval base tree: %w", err)
	}
	checks, err := checkSpecs(resolved.Config, "checks")
	if err != nil {
		return nil, err
	}
	fixes, err := checkSpecs(resolved.Config, "fix")
	if err != nil {
		return nil, err
	}
	baseline, err := makeBaseline(document.CheckBaseline, checks, string(baseTree))
	if err != nil {
		return nil, err
	}
	manifest, err := protection.BuildManifest(ctx, s.Git, policy, protection.BuildOptions{
		BaseCommit: baseCommit, CheckoutRoot: resolved.Checkout, Config: resolved.Config,
		Intent: parsed, CandidatePath: candidatePath, CandidateBytes: acceptanceBytes,
	})
	if err != nil {
		return nil, fmt.Errorf("rebuild protected manifest: %w", err)
	}
	if manifest == nil || !sameDigestMap(manifest.Manifest.Hashes(), document.ProtectedManifest) {
		return nil, errors.New("approval protected manifest does not match the resolved base")
	}
	protector, err := protection.NewProtector(manifest.Manifest, []string{acceptancePath})
	if err != nil {
		return nil, fmt.Errorf("construct protected workspace policy: %w", err)
	}
	return &Approval{
		Slug: slug, Commit: observation.Target, ApprovalSHA256: document.ApprovalSHA256,
		IntentSHA256: document.IntentSHA256, TargetBranch: document.TargetBranch,
		BaseCommit: baseCommit, BaseTree: baseTree, IntentBytes: intentBytes,
		AcceptanceBytes: acceptanceBytes, AcceptanceSourcePath: acceptancePath,
		CandidatePath: candidatePath, Intent: parsed, Protection: protector,
		Baseline: baseline, Checks: checks, Fixes: fixes,
	}, nil
}

func requireDirectRef(ctx context.Context, git contract.GitPort, policy contract.GitPolicy, ref string) error {
	result, err := git.Exec(ctx, []string{"symbolic-ref", "-q", "--no-recurse", ref}, nil, policy)
	if err != nil {
		return fmt.Errorf("inspect approval ref: %w", err)
	}
	if result.Process.ExitStatus == nil || result.Process.TimedOut || result.Process.Unavailable {
		return errors.New("inspect approval ref: Git returned no usable exit status")
	}
	if *result.Process.ExitStatus == 0 {
		return errors.New("approval ref is symbolic")
	}
	if *result.Process.ExitStatus != 1 {
		return errors.New("inspect approval ref: Git failed")
	}
	return nil
}

func readBlob(ctx context.Context, git contract.GitPort, policy contract.GitPolicy, commit contract.ObjectID, name string) ([]byte, error) {
	if !safeApprovalPath(name) || strings.ContainsRune(string(commit), ':') {
		return nil, errors.New("approval tree path or commit is invalid")
	}
	result, err := git.Exec(ctx, []string{"cat-file", "blob", string(commit) + ":" + name}, nil, policy)
	if err != nil {
		return nil, err
	}
	if result.Process.ExitStatus == nil || result.Process.TimedOut || result.Process.Unavailable || *result.Process.ExitStatus != 0 {
		return nil, errors.New("Git did not return the requested approval blob")
	}
	return append([]byte(nil), result.Stdout...), nil
}

func acceptancePaths(resolved *project.Resolution, slug string) (string, string, error) {
	var config *project.Config
	if resolved != nil {
		config = resolved.Config
	}
	adapter, extension, candidateDir := "", "", "test/acceptance"
	if config != nil {
		if settings, ok := config.Raw["acceptance"].(yamlmini.Mapping); ok {
			adapter, _ = settings["adapter"].(string)
			extension, _ = settings["ext"].(string)
			if value, ok := settings["candidate_dir"].(string); ok {
				candidateDir = value
			}
		}
	}
	if extension == "" {
		switch adapter {
		case "rails":
			extension = "_test.rb"
		case "exunit":
			extension = "_test.exs"
		case "command":
			return "", "", errors.New("approval command acceptance requires an extension")
		default:
			if resolved != nil && resolved.Checkout != "" {
				root, err := safefs.OpenRoot(resolved.Checkout)
				if err == nil {
					if exists(root, "Gemfile") && exists(root, "config/application.rb") {
						extension = "_test.rb"
					} else if exists(root, "mix.exs") {
						extension = "_test.exs"
					} else {
						extension = ".t.sh"
					}
					_ = root.Close()
				}
			}
			if extension == "" {
				extension = ".t.sh"
			}
		}
	}
	if strings.ContainsAny(extension, "/\\\x00\r\n") || extension == "." || extension == ".." {
		return "", "", errors.New("approval acceptance extension is invalid")
	}
	if candidateDir == "" || !safeApprovalPath(candidateDir) {
		return "", "", errors.New("approval candidate directory is invalid")
	}
	source := path.Join(".kogen", "acceptance", slug+extension)
	candidate := path.Join(candidateDir, slug+extension)
	return source, candidate, nil
}

func exists(root *safefs.Root, name string) bool {
	_, err := root.Lstat(name)
	return err == nil
}

func checkSpecs(config *project.Config, field string) ([]contract.CheckSpec, error) {
	if config == nil {
		return nil, nil
	}
	rows, ok := config.Raw[field].(yamlmini.Sequence)
	if !ok {
		return nil, nil
	}
	checks := make([]contract.CheckSpec, 0, len(rows))
	for index, row := range rows {
		mapping, ok := row.(yamlmini.Mapping)
		if !ok {
			return nil, fmt.Errorf("project %s row %d is invalid", field, index+1)
		}
		name, _ := mapping["name"].(string)
		argv, ok := mapping["argv"].(yamlmini.Sequence)
		if !ok || len(argv) == 0 {
			return nil, fmt.Errorf("project %s row %d has no argv", field, index+1)
		}
		args := make([]string, len(argv))
		for argument, item := range argv {
			value, ok := item.(string)
			if !ok || value == "" {
				return nil, fmt.Errorf("project %s row %d has invalid argv", field, index+1)
			}
			args[argument] = value
		}
		timeoutMS, ok := mapping["timeout_ms"].(int)
		if !ok || timeoutMS <= 0 {
			return nil, fmt.Errorf("project %s row %d has invalid timeout", field, index+1)
		}
		checks = append(checks, contract.CheckSpec{Name: name, Program: args[0], Args: args[1:], Timeout: time.Duration(timeoutMS) * time.Millisecond})
	}
	return checks, nil
}

func makeBaseline(rows []prepare.BaselineRow, checks []contract.CheckSpec, tree string) (*gate.CheckBaseline, error) {
	if len(rows) != len(checks) {
		return nil, errors.New("approval baseline row count differs from configured checks")
	}
	baseline := &gate.CheckBaseline{Tree: tree, Checks: make([]gate.BaselineCheck, 0, len(rows))}
	for index, row := range rows {
		if row.Name != checks[index].Name {
			return nil, errors.New("approval baseline order differs from configured checks")
		}
		switch row.Status {
		case contract.CheckGreen, contract.CheckRed, contract.CheckUnavailable, contract.CheckTimeout, contract.CheckMutating:
		default:
			return nil, fmt.Errorf("approval baseline row %d has an invalid status", index+1)
		}
		if row.Findings == nil {
			return nil, fmt.Errorf("approval baseline row %d has no findings array", index+1)
		}
		findings := make([]contract.FindingIdentity, len(row.Findings))
		for item, finding := range row.Findings {
			if finding.Path == "" || finding.Rule == "" || !safeApprovalPath(finding.Path) {
				return nil, fmt.Errorf("approval baseline row %d finding %d has an invalid identity", index+1, item+1)
			}
			findings[item] = contract.FindingIdentity{Path: finding.Path, Rule: finding.Rule, Symbol: finding.Symbol}
		}
		baseline.Checks = append(baseline.Checks, gate.BaselineCheck{Name: row.Name, Status: row.Status, ExitStatus: row.ExitStatus, Findings: findings})
	}
	return baseline, nil
}

func sameStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func sameDigestMap(left, right map[string]string) bool {
	if len(left) != len(right) {
		return false
	}
	for name, digest := range left {
		if right[name] != digest {
			return false
		}
	}
	return true
}

var _ ApprovalSource = GitApprovalSource{}
