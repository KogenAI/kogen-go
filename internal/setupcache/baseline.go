package setupcache

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"strings"

	"kogen-go/internal/approval/prepare"
	"kogen-go/internal/contract"
	"kogen-go/internal/process"
)

type baselineManifest struct {
	Version int                   `json:"v"`
	Digest  string                `json:"digest"`
	Rows    []baselineRowManifest `json:"rows"`
}

type baselineRowManifest struct {
	Name       string                    `json:"name"`
	Status     contract.CheckStatus      `json:"status"`
	ExitStatus *int                      `json:"exit_status"`
	Findings   []baselineFindingManifest `json:"findings"`
}

type baselineFindingManifest struct {
	Path    string  `json:"path"`
	Rule    string  `json:"rule"`
	Symbol  string  `json:"symbol"`
	Message string  `json:"message"`
	Line    *uint32 `json:"line,omitempty"`
}

// GetOrCompute reads and writes only v3 exact-base baseline entries. Unknown
// identities and legacy versions always execute compute and never persist.
func (c *Cache) GetOrCompute(ctx context.Context, key prepare.BaselineKey, compute func(context.Context) ([]prepare.BaselineRow, error)) (prepare.BaselineResult, error) {
	if err := checkContext(ctx); err != nil {
		return prepare.BaselineResult{}, err
	}
	if c == nil || c.root == nil || compute == nil {
		return prepare.BaselineResult{}, errors.New("setupcache: cache and baseline operation are required")
	}
	if key.Version == baselineVersion && key.Cacheable {
		if err := validateBaselineIdentity(key); err != nil {
			return prepare.BaselineResult{}, err
		}
	}

	eligible := key.Version == baselineVersion && key.Cacheable && sha256Identity.MatchString(key.Digest)
	c.mu.Lock()
	if err := checkContext(ctx); err != nil {
		c.mu.Unlock()
		return prepare.BaselineResult{}, err
	}
	entry := ""
	if eligible {
		entry = path.Join("baseline/v3", key.Digest+".json")
		var manifest baselineManifest
		if err := readJSON(c.root, entry, &manifest); err == nil && validBaselineManifest(manifest, key) {
			c.mu.Unlock()
			return prepare.BaselineResult{Rows: fromManifest(manifest.Rows), Reused: true}, nil
		} else if err == nil || !errors.Is(err, fs.ErrNotExist) {
			if removeErr := c.root.Remove(entry); removeErr != nil && !errors.Is(removeErr, fs.ErrNotExist) {
				c.mu.Unlock()
				return prepare.BaselineResult{}, fmt.Errorf("setupcache: remove invalid baseline entry: %w", removeErr)
			}
		}
	}
	c.mu.Unlock()

	rows, err := compute(ctx)
	if err != nil {
		return prepare.BaselineResult{}, err
	}
	if err := checkContext(ctx); err != nil {
		return prepare.BaselineResult{}, err
	}
	result := prepare.BaselineResult{Rows: cloneBaselineRows(rows)}
	if !eligible {
		return result, nil
	}
	if !validBaselineRows(rows, key.Checks) {
		return result, nil
	}
	manifest := baselineManifest{Version: baselineVersion, Digest: key.Digest, Rows: toManifest(rows)}
	c.mu.Lock()
	defer c.mu.Unlock()
	// A concurrent approval may have completed the same immutable baseline
	// while this caller was running its checks. Keep the first durable result.
	var existing baselineManifest
	if err := readJSON(c.root, entry, &existing); err == nil && validBaselineManifest(existing, key) {
		return result, nil
	}
	if err := writeJSON(c.root, entry, manifest); err != nil {
		// Baseline caching is an optimization. Fresh observations remain valid
		// even when durable cache publication is unavailable.
		return result, nil
	}
	return result, nil
}

func validateBaselineIdentity(key prepare.BaselineKey) error {
	if key.Version != baselineVersion || !sha256Identity.MatchString(key.Digest) || !sha256Identity.MatchString(key.SetupKey) {
		return errors.New("setupcache: invalid v3 baseline identity")
	}
	environment := make(process.Environment, len(key.ChildEnvironment))
	previous := ""
	for _, entry := range key.ChildEnvironment {
		name, value, ok := strings.Cut(entry, "=")
		if !ok || name == "" || strings.ContainsRune(entry, '\x00') || previous != "" && name <= previous {
			return errors.New("setupcache: noncanonical v3 baseline environment")
		}
		if _, exists := environment[name]; exists {
			return errors.New("setupcache: duplicate v3 baseline environment key")
		}
		environment[name] = value
		previous = name
	}
	expected, err := prepare.NewBaselineKey(
		key.CheckedBaseTree, key.SetupKey, key.Checks, environment,
		key.Toolchain, true, key.OS, key.Arch, key.AdapterVersion,
	)
	if err != nil {
		return fmt.Errorf("setupcache: validate v3 baseline identity: %w", err)
	}
	if expected.Digest != key.Digest || key.Cacheable && !expected.Cacheable {
		return errors.New("setupcache: v3 baseline digest does not match its full context")
	}
	return nil
}

func validBaselineManifest(manifest baselineManifest, key prepare.BaselineKey) bool {
	return manifest.Version == baselineVersion && manifest.Digest == key.Digest && validBaselineRows(fromManifest(manifest.Rows), key.Checks)
}

func validBaselineRows(rows []prepare.BaselineRow, checks []contract.CheckSpec) bool {
	if len(rows) != len(checks) {
		return false
	}
	for index, row := range rows {
		if row.Name != checks[index].Name {
			return false
		}
		switch row.Status {
		case contract.CheckGreen, contract.CheckRed, contract.CheckUnavailable, contract.CheckTimeout, contract.CheckMutating:
		default:
			return false
		}
	}
	return true
}

func toManifest(rows []prepare.BaselineRow) []baselineRowManifest {
	result := make([]baselineRowManifest, len(rows))
	for index, row := range rows {
		result[index] = baselineRowManifest{
			Name: row.Name, Status: row.Status, ExitStatus: cloneInt(row.ExitStatus),
			Findings: make([]baselineFindingManifest, len(row.Findings)),
		}
		for findingIndex, finding := range row.Findings {
			result[index].Findings[findingIndex] = baselineFindingManifest{
				Path: finding.Path, Rule: finding.Rule, Symbol: finding.Symbol,
				Message: finding.Message, Line: cloneUint32(finding.Line),
			}
		}
	}
	return result
}

func fromManifest(rows []baselineRowManifest) []prepare.BaselineRow {
	result := make([]prepare.BaselineRow, len(rows))
	for index, row := range rows {
		result[index] = prepare.BaselineRow{
			Name: row.Name, Status: row.Status, ExitStatus: cloneInt(row.ExitStatus),
			Findings: make([]prepare.BaselineFinding, len(row.Findings)),
		}
		for findingIndex, finding := range row.Findings {
			result[index].Findings[findingIndex] = prepare.BaselineFinding{
				Path: finding.Path, Rule: finding.Rule, Symbol: finding.Symbol,
				Message: finding.Message, Line: cloneUint32(finding.Line),
			}
		}
	}
	return result
}

func cloneBaselineRows(rows []prepare.BaselineRow) []prepare.BaselineRow {
	return fromManifest(toManifest(rows))
}

func cloneInt(value *int) *int {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneUint32(value *uint32) *uint32 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
