// Package edge runs optional, generated edge-test suites as an additional
// landing requirement. It preserves the approved acceptance result, bounds
// generation and execution, and runs peer suites for green parallel rungs in
// deterministic order.
//
// Generator and Runner adapters own provider and sandbox details. They must
// honor the supplied contexts; Runner must stage generated source without
// changing the candidate tree and report the observed tree identities.
package edge
