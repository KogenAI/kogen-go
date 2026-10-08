// Package diagnostic maps production component results into complete,
// diagnostic-only observations. It does not implement a controller or add
// D-slice results to the G conformance totals; I7 owns replay routing and
// acceptance classification.
//
// Adapters in this package consume results from gate, build selection and
// orchestration, accounts, and setup/baseline caches. They do not reconstruct
// histories from model events or invent values absent from those components.
package diagnostic
