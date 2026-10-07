package audit

import (
	"context"
	"errors"
	"io/fs"
	"path"
	"strings"

	"kogen-go/internal/contract"
	"kogen-go/internal/intent"
)

// DependencyResolver reports whether a named dependency has landed on the
// branch being built and, when it has, the landed commit to record.
type DependencyResolver interface {
	LandedOnBranch(context.Context, string, string) (commit string, landed bool, err error)
}

// RecheckOptions binds approval predicates and declared dependencies to the
// exact build base and branch. Root must already be opened at that base.
type RecheckOptions struct {
	Base       string
	Branch     string
	Root       contract.RootedFS
	Predicates []intent.Contract
	BlocksOn   []string
	Deps       DependencyResolver
}

// PredicateObservation records the content check made against the build base.
type PredicateObservation struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	Matched bool   `json:"matched"`
}

// DependencyObservation records a required landed dependency commit.
type DependencyObservation struct {
	Slug   string `json:"slug"`
	Commit string `json:"commit"`
}

// StaleReason identifies the first failed predicate or dependency while the
// result also retains every observation completed during this recheck.
type StaleReason struct {
	Name   string `json:"name"`
	Path   string `json:"path"`
	Detail string `json:"detail"`
}

// RecheckResult is suitable for the shaping_rechecked or shaping_stale
// observation. Any stale reason means the Build must not start.
type RecheckResult struct {
	Base         string                  `json:"base"`
	Predicates   []PredicateObservation  `json:"predicates"`
	Dependencies []DependencyObservation `json:"dependency_commits"`
	Stale        []StaleReason           `json:"-"`
}

// CanBuild reports whether the current approval predicates and dependencies
// still match the exact base and target branch.
func (result RecheckResult) CanBuild() bool { return len(result.Stale) == 0 }

// Recheck verifies every assumption/shared-contract predicate on the checked
// base and every blocks_on dependency on the target branch. It reads paths only
// through the supplied rooted filesystem.
func Recheck(ctx context.Context, options RecheckOptions) (RecheckResult, error) {
	if ctx == nil {
		return RecheckResult{}, errors.New("shape audit: recheck context is required")
	}
	result := RecheckResult{
		Base:         options.Base,
		Predicates:   make([]PredicateObservation, 0, len(options.Predicates)),
		Dependencies: make([]DependencyObservation, 0, len(options.BlocksOn)),
	}
	if len(options.Predicates) != 0 && options.Root == nil {
		return RecheckResult{}, errors.New("shape audit: rooted base filesystem is required for predicates")
	}
	if (len(options.Predicates) != 0 || len(options.BlocksOn) != 0) && strings.TrimSpace(options.Base) == "" {
		return RecheckResult{}, errors.New("shape audit: exact build base is required for recheck")
	}
	for _, predicate := range options.Predicates {
		observation := PredicateObservation{Name: predicate.Name, Path: predicate.Path}
		if strings.TrimSpace(predicate.Name) == "" || strings.TrimSpace(predicate.Contains) == "" || !safePredicatePath(predicate.Path) {
			result.Stale = append(result.Stale, StaleReason{Name: predicate.Name, Path: predicate.Path, Detail: "predicate is incomplete or has an unsafe path"})
			result.Predicates = append(result.Predicates, observation)
			continue
		}
		data, err := options.Root.ReadFile(predicate.Path)
		if errors.Is(err, fs.ErrNotExist) {
			result.Stale = append(result.Stale, StaleReason{Name: predicate.Name, Path: predicate.Path, Detail: "predicate path is missing on the checked base"})
			result.Predicates = append(result.Predicates, observation)
			continue
		}
		if err != nil {
			return result, err
		}
		observation.Matched = strings.Contains(string(data), predicate.Contains)
		if !observation.Matched {
			result.Stale = append(result.Stale, StaleReason{Name: predicate.Name, Path: predicate.Path, Detail: "predicate text is absent from the checked base"})
		}
		result.Predicates = append(result.Predicates, observation)
	}
	if len(options.BlocksOn) != 0 && (options.Deps == nil || options.Branch == "") {
		return result, errors.New("shape audit: dependency resolver and target branch are required")
	}
	seen := make(map[string]struct{}, len(options.BlocksOn))
	for _, slug := range options.BlocksOn {
		if _, ok := seen[slug]; ok {
			continue
		}
		seen[slug] = struct{}{}
		if slug == "" {
			result.Stale = append(result.Stale, StaleReason{Name: slug, Path: "blocks_on", Detail: "dependency slug is empty"})
			continue
		}
		commit, landed, err := options.Deps.LandedOnBranch(ctx, options.Branch, slug)
		if err != nil {
			return result, err
		}
		if !landed || commit == "" {
			result.Stale = append(result.Stale, StaleReason{Name: slug, Path: "blocks_on", Detail: "dependency has not landed on the target branch"})
			continue
		}
		result.Dependencies = append(result.Dependencies, DependencyObservation{Slug: slug, Commit: commit})
	}
	return result, nil
}

func safePredicatePath(value string) bool {
	if value == "" || strings.ContainsRune(value, '\\') || strings.ContainsRune(value, '\x00') || path.IsAbs(value) || path.Clean(value) != value || !fs.ValidPath(value) {
		return false
	}
	for _, part := range strings.Split(value, "/") {
		if part == ".git" || part == "." || part == ".." {
			return false
		}
	}
	return true
}
