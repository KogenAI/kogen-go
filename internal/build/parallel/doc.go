// Package parallel runs the first two recipe rungs together for an eligible
// hard Build plan. It owns per-rung workspaces and conversations, cancels the
// remaining rung after a landable result, and returns observations in recipe
// order for deterministic journaling and continuation.
package parallel
