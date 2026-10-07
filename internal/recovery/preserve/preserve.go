// Package preserve publishes create-only recovery snapshots for dead Build
// workspaces. Git trees are captured against the immutable base through the
// native ignore implementation. When the origin cannot retain that tree, a
// private fsynced archive preserves its exact paths, modes and bytes.
package preserve

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"kogen-go/internal/contract"
	"kogen-go/internal/gitio"
	"kogen-go/internal/journal"
	"kogen-go/internal/process"
	"kogen-go/internal/recovery"
	"kogen-go/internal/safefs"
)

var (
	runIDPattern     = regexp.MustCompile(`^[0-9a-f]{32}$`)
	workspacePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
)

// RefEffects is the subset of origin Git operations used to create or adopt a
// recovery ref. A nil Config.Refs binds a supervised gitio.RefPort instead.
type RefEffects interface {
	ReadRef(context.Context, string) (contract.RefObservation, error)
	ResolveTree(context.Context, string) (contract.ObjectID, error)
	IsAncestor(context.Context, contract.ObjectID, contract.ObjectID) (bool, error)
	CompareAndSwap(context.Context, contract.RefUpdate) (contract.RefUpdateResult, error)
}

// ArchiveStore is a create-only persistence effect for private archive bytes.
// Read must reject unsafe leaves; Publish must durably sync file and parent.
type ArchiveStore interface {
	Read(string) ([]byte, error)
	Publish(string, []byte) error
}

// Config binds separate supervised Git policies for mutable workspace reads
// and trusted origin object/ref publication. ArchiveStore is optional and is
// primarily useful for controlled fault injection; the default implementation
// uses safefs under StateRoot.
type Config struct {
	StateRoot            string
	WorkspaceGit         contract.GitPort
	WorkspaceEnvironment process.Environment
	OriginGit            contract.GitPort
	OriginPolicy         contract.GitPolicy
	Refs                 RefEffects
	Archives             ArchiveStore
}

// Preserver implements recovery.PreservationPort. It never mutates or removes
// the source workspace and never replaces an existing ref or archive.
type Preserver struct {
	stateRoot    string
	root         *safefs.Root
	workspace    contract.GitPort
	workspaceEnv process.Environment
	origin       contract.GitPort
	originPolicy contract.GitPolicy
	refs         RefEffects
	archives     ArchiveStore
}

var _ recovery.PreservationPort = (*Preserver)(nil)

// New opens the private state root and binds all required production effects.
func New(config Config) (*Preserver, error) {
	if strings.TrimSpace(config.StateRoot) == "" || !filepath.IsAbs(config.StateRoot) || filepath.Clean(config.StateRoot) != config.StateRoot {
		return nil, errors.New("preserve: state root must be a clean absolute path")
	}
	if config.WorkspaceGit == nil || config.OriginGit == nil || config.OriginPolicy.WorkingDirectory == "" {
		return nil, errors.New("preserve: workspace and origin Git effects are required")
	}
	resolvedRoot, err := filepath.EvalSymlinks(config.StateRoot)
	if err != nil {
		return nil, fmt.Errorf("preserve: resolve state root: %w", err)
	}
	resolvedRoot, err = filepath.Abs(resolvedRoot)
	if err != nil {
		return nil, fmt.Errorf("preserve: resolve state root: %w", err)
	}
	rootInfo, err := os.Stat(resolvedRoot)
	if err != nil {
		return nil, fmt.Errorf("preserve: state root is not a directory: %w", err)
	}
	if !rootInfo.IsDir() {
		return nil, errors.New("preserve: state root is not a directory")
	}
	if rootInfo.Mode().Perm()&0o077 != 0 {
		return nil, errors.New("preserve: state root must be private (mode 0700 or stricter)")
	}
	if !filepath.IsAbs(config.OriginPolicy.WorkingDirectory) || filepath.Clean(config.OriginPolicy.WorkingDirectory) != config.OriginPolicy.WorkingDirectory {
		return nil, errors.New("preserve: origin Git directory must be a clean absolute path")
	}
	root, err := safefs.OpenRoot(resolvedRoot)
	if err != nil {
		return nil, fmt.Errorf("preserve: open state root: %w", err)
	}
	environment := cloneEnvironment(config.WorkspaceEnvironment)
	if environment == nil {
		environment = process.HostEnvironment()
	}
	refs := config.Refs
	if refs == nil {
		refs = gitio.NewRefPort(config.OriginGit, config.OriginPolicy)
	}
	preserver := &Preserver{
		stateRoot: resolvedRoot, root: root, workspace: config.WorkspaceGit,
		workspaceEnv: environment, origin: config.OriginGit,
		originPolicy: config.OriginPolicy, refs: refs, archives: config.Archives,
	}
	if preserver.archives == nil {
		store := &rootedArchiveStore{root: root}
		preserver.archives = store
	}
	return preserver, nil
}

// Close releases the descriptor-rooted state capability. Injected archive
// stores remain owned by their caller.
func (p *Preserver) Close() error {
	if p == nil || p.root == nil {
		return nil
	}
	err := p.root.Close()
	p.root = nil
	return err
}

// Preserve freezes and durably records one workspace. A successful return is
// either an adopted/new create-only origin ref or an adopted/new durable
// archive. The archive fallback also works when workspace Git cannot produce
// a tree: it captures all non-.git files, including ignored files, so a Git
// failure cannot destroy otherwise recoverable bytes.
func (p *Preserver) Preserve(ctx context.Context, request recovery.PreservationRequest) (journal.RecoveryRecord, error) {
	if p == nil || p.root == nil || ctx == nil {
		return journal.RecoveryRecord{}, errors.New("preserve: live preserver and context are required")
	}
	if err := ctx.Err(); err != nil {
		return journal.RecoveryRecord{}, err
	}
	if err := p.validateRequest(request); err != nil {
		return journal.RecoveryRecord{}, err
	}
	base, err := gitio.ParseObjectID(string(request.Base))
	if err != nil {
		return journal.RecoveryRecord{}, fmt.Errorf("preserve: invalid frozen base: %w", err)
	}
	workspacePolicy := gitio.WorkspacePolicy(request.Workspace.AbsolutePath, p.workspaceEnv)
	tree, treeErr := gitio.BuildCandidateTree(ctx, p.workspace, workspacePolicy, base)
	if treeErr != nil {
		files, rawErr := captureRawWorkspace(request.Workspace.AbsolutePath)
		if rawErr != nil {
			return journal.RecoveryRecord{}, errors.Join(fmt.Errorf("preserve: build latest candidate tree: %w", treeErr), fmt.Errorf("preserve: capture fallback workspace: %w", rawErr))
		}
		archive, archiveErr := encodeArchive(request, "raw_workspace", "", files)
		if archiveErr != nil {
			return journal.RecoveryRecord{}, errors.Join(fmt.Errorf("preserve: build latest candidate tree: %w", treeErr), archiveErr)
		}
		record, persistErr := p.publishArchive(request, archive)
		if persistErr != nil {
			return journal.RecoveryRecord{}, errors.Join(fmt.Errorf("preserve: build latest candidate tree: %w", treeErr), persistErr)
		}
		return record, nil
	}

	files, err := readCandidateFiles(ctx, p.workspace, workspacePolicy, request.Workspace.AbsolutePath, tree)
	if err != nil {
		rawFiles, rawErr := captureRawWorkspace(request.Workspace.AbsolutePath)
		if rawErr != nil {
			return journal.RecoveryRecord{}, errors.Join(fmt.Errorf("preserve: read frozen candidate tree: %w", err), fmt.Errorf("preserve: capture fallback workspace: %w", rawErr))
		}
		archive, archiveErr := encodeArchive(request, "raw_workspace", "", rawFiles)
		if archiveErr != nil {
			return journal.RecoveryRecord{}, errors.Join(fmt.Errorf("preserve: read frozen candidate tree: %w", err), archiveErr)
		}
		record, persistErr := p.publishArchive(request, archive)
		if persistErr != nil {
			return journal.RecoveryRecord{}, errors.Join(fmt.Errorf("preserve: read frozen candidate tree: %w", err), persistErr)
		}
		return record, nil
	}

	archive, err := encodeArchive(request, "candidate_tree", string(tree), files)
	if err != nil {
		return journal.RecoveryRecord{}, err
	}
	// An archive may have been published immediately before a prior process
	// died while recording run.json. Adopt it before attempting another effect.
	if record, found, err := p.adoptArchive(request, archive); err != nil {
		return journal.RecoveryRecord{}, err
	} else if found {
		return record, nil
	}

	refRecord, published, refErr := p.publishRef(ctx, request, tree, files)
	if published {
		return refRecord, nil
	}
	archiveRecord, archiveErr := p.publishArchive(request, archive)
	if archiveErr != nil {
		if refErr == nil {
			refErr = errors.New("recovery ref was already occupied by a different snapshot")
		}
		return journal.RecoveryRecord{}, errors.Join(fmt.Errorf("preserve: publish recovery ref: %w", refErr), archiveErr)
	}
	return archiveRecord, nil
}

func (p *Preserver) validateRequest(request recovery.PreservationRequest) error {
	workspace := request.Workspace
	if !runIDPattern.MatchString(request.RunID) || !workspacePattern.MatchString(workspace.Name) {
		return errors.New("preserve: run and workspace identities are not safe for deterministic publication")
	}
	if workspace.RelativePath != request.RunID+"-"+workspace.Name || !fs.ValidPath(workspace.RelativePath) || strings.Contains(workspace.RelativePath, "/") {
		return errors.New("preserve: workspace path does not match its run identity")
	}
	expectedPath := filepath.Join(p.stateRoot, filepath.FromSlash(workspace.RelativePath))
	if workspace.AbsolutePath != expectedPath || filepath.Clean(workspace.AbsolutePath) != workspace.AbsolutePath {
		return errors.New("preserve: workspace path is outside the configured state root")
	}
	info, err := p.root.Lstat(workspace.RelativePath)
	if err != nil {
		return fmt.Errorf("preserve: inspect workspace: %w", err)
	}
	if !info.IsDir() || info.Mode()&fs.ModeSymlink != 0 {
		return errors.New("preserve: workspace is not a real directory")
	}
	return nil
}

func (p *Preserver) publishRef(ctx context.Context, request recovery.PreservationRequest, tree contract.ObjectID, files []gitio.TreeFile) (journal.RecoveryRecord, bool, error) {
	ref := "refs/kogen/candidates/" + request.RunID + "/recovery-" + request.Workspace.Name
	observed, err := p.refs.ReadRef(ctx, ref)
	if err != nil {
		return journal.RecoveryRecord{}, false, err
	}
	if observed.Exists {
		return p.adoptRef(ctx, request, tree, ref, observed.Target)
	}

	// Workspaces are independent local clones. Rebuild their exact frozen tree
	// in the origin object database without reading the workspace index or HEAD.
	originTree, err := gitio.WriteExactTree(ctx, p.origin, p.originPolicy, files)
	if err != nil {
		return journal.RecoveryRecord{}, false, err
	}
	if originTree != tree {
		return journal.RecoveryRecord{}, false, errors.New("origin object format produced a different recovery tree")
	}
	updated, updateErr := p.refs.CompareAndSwap(ctx, contract.RefUpdate{Name: ref, Next: tree})
	if updateErr == nil && updated.Updated {
		return recoveryRefRecord(request, tree, ref), true, nil
	}
	// A timed out update-ref may have taken effect. Reread and adopt only the
	// exact snapshot; a different target is never replaced.
	observed, readErr := p.refs.ReadRef(ctx, ref)
	if readErr == nil && observed.Exists {
		record, found, adoptErr := p.adoptRef(ctx, request, tree, ref, observed.Target)
		if adoptErr != nil {
			return journal.RecoveryRecord{}, false, errors.Join(updateErr, adoptErr)
		}
		if found {
			return record, true, nil
		}
	}
	return journal.RecoveryRecord{}, false, errors.Join(updateErr, readErr)
}

func (p *Preserver) adoptRef(ctx context.Context, request recovery.PreservationRequest, tree contract.ObjectID, ref string, target contract.ObjectID) (journal.RecoveryRecord, bool, error) {
	observedTree, err := p.refs.ResolveTree(ctx, string(target))
	if err != nil {
		return journal.RecoveryRecord{}, false, err
	}
	if observedTree != tree {
		return journal.RecoveryRecord{}, false, errors.New("recovery ref already names a different preserved tree")
	}
	return recoveryRefRecord(request, tree, ref), true, nil
}

func recoveryRefRecord(request recovery.PreservationRequest, tree contract.ObjectID, ref string) journal.RecoveryRecord {
	treeText, refText := string(tree), ref
	return journal.RecoveryRecord{
		Workspace: request.Workspace.Name, Base: string(request.Base),
		Tree: &treeText, Ref: &refText, Verification: "unverified",
	}
}

func (p *Preserver) publishArchive(request recovery.PreservationRequest, archive []byte) (journal.RecoveryRecord, error) {
	identity := archiveIdentity(request, archive)
	if record, found, err := p.adoptArchiveAt(request, identity, archive); err != nil {
		return journal.RecoveryRecord{}, err
	} else if found {
		return record, nil
	}
	if err := p.archives.Publish(identity, archive); err != nil {
		// Create-only publication may have succeeded despite a lost response, or
		// another recovery may have published the same deterministic archive.
		if record, found, readErr := p.adoptArchiveAt(request, identity, archive); readErr == nil && found {
			return record, nil
		} else if readErr != nil && !errors.Is(readErr, fs.ErrNotExist) {
			return journal.RecoveryRecord{}, errors.Join(fmt.Errorf("publish private recovery archive: %w", err), readErr)
		}
		return journal.RecoveryRecord{}, fmt.Errorf("publish private recovery archive: %w", err)
	}
	if record, found, err := p.adoptArchiveAt(request, identity, archive); err != nil {
		return journal.RecoveryRecord{}, fmt.Errorf("verify private recovery archive: %w", err)
	} else if found {
		return record, nil
	}
	return journal.RecoveryRecord{}, errors.New("preserve: archive publication returned without a readable archive")
}

func (p *Preserver) adoptArchive(request recovery.PreservationRequest, archive []byte) (journal.RecoveryRecord, bool, error) {
	return p.adoptArchiveAt(request, archiveIdentity(request, archive), archive)
}

func (p *Preserver) adoptArchiveAt(request recovery.PreservationRequest, identity string, expected []byte) (journal.RecoveryRecord, bool, error) {
	actual, err := p.archives.Read(identity)
	if errors.Is(err, fs.ErrNotExist) {
		return journal.RecoveryRecord{}, false, nil
	}
	if err != nil {
		return journal.RecoveryRecord{}, false, fmt.Errorf("read private recovery archive: %w", err)
	}
	if !bytes.Equal(actual, expected) {
		return journal.RecoveryRecord{}, false, errors.New("preserve: deterministic archive identity contains different or corrupt bytes")
	}
	archiveID := identity
	return journal.RecoveryRecord{
		Workspace: request.Workspace.Name, Base: string(request.Base),
		Archive: &archiveID, Verification: "unverified",
	}, true, nil
}

func archiveIdentity(request recovery.PreservationRequest, archive []byte) string {
	digest := sha256Hex(archive)
	return "recovery-archives/" + request.RunID + "/" + request.Workspace.Name + "/" + digest + ".krec"
}

func sha256Hex(input []byte) string {
	digest := sha256.Sum256(input)
	return hex.EncodeToString(digest[:])
}

func cloneEnvironment(environment process.Environment) process.Environment {
	if environment == nil {
		return nil
	}
	cloned := make(process.Environment, len(environment))
	for key, value := range environment {
		cloned[key] = value
	}
	return cloned
}
