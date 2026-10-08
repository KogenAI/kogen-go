package diagnostic

import "kogen-go/internal/approval/prepare"

// SetupCacheObservation keeps setup-product reuse and verification-baseline
// reuse as separate production observations. It records only cache identities
// and baseline rows; setup environments and local paths are not serialized.
type SetupCacheObservation struct {
	SetupKey          string                `json:"setup_key"`
	SetupReused       bool                  `json:"setup_reused"`
	BaselineVersion   int                   `json:"baseline_version"`
	CheckedBaseTree   string                `json:"checked_base_tree"`
	BaselineSetupKey  string                `json:"baseline_setup_key"`
	BaselineKey       string                `json:"baseline_key"`
	BaselineCacheable bool                  `json:"baseline_cacheable"`
	BaselineReused    bool                  `json:"baseline_reused"`
	BaselineRows      []prepare.BaselineRow `json:"baseline_rows"`
}

// ObserveSetupCache maps actual results returned by the production setup v2
// and independent baseline v3 cache APIs. It does not infer one hit from the
// other or recompute either identity.
func ObserveSetupCache(setup prepare.SetupResult, key prepare.BaselineKey, baseline prepare.BaselineResult) SetupCacheObservation {
	rows := append([]prepare.BaselineRow(nil), baseline.Rows...)
	if rows == nil {
		rows = []prepare.BaselineRow{}
	}
	return SetupCacheObservation{
		SetupKey: setup.Key, SetupReused: setup.Reused,
		BaselineVersion: key.Version, CheckedBaseTree: key.CheckedBaseTree,
		BaselineSetupKey: key.SetupKey, BaselineKey: key.Digest,
		BaselineCacheable: key.Cacheable, BaselineReused: baseline.Reused,
		BaselineRows: rows,
	}
}
