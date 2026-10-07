// Package queuestatus adapts queue and status replay events to the production
// scheduler and status derivation. Inputs are snapshots of repository refs,
// durable runs, and observed process ownership; observations are complete xspec
// values rather than replay-side status decisions.
package queuestatus
