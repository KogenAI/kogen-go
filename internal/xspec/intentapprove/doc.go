// Package intentapprove adapts the Quint intent and approve slices to the
// production Intent parser, approval preparation, immutable publisher, and
// removal controller. Its fixture owns a private temporary checkout, bare
// origin, cache, and state root. It does not evaluate a replay policy model or
// accept expected observations as state.
//
// Hash strings in the Quint intent slice are symbols because they cannot equal
// hashes of the fixture's real bytes. The adapter maps the three committed
// match symbols (abcd1234, bbbb2222, cccc3333) to the computed source digest
// prefix, and the committed mismatch symbols (ffff0000, deadbeef) to a
// computed nonmatching prefix. Other values must already be a real prefix or
// the adapter refuses them. Approval-slice given/sha symbols are related by
// their literal prefix relation, then translated to the real digest. The
// model's prefixOk field is deliberately never read. Output digest symbols are
// recorded only after reading the corresponding real bytes/ref/package.
//
// A late publish recheck is injected only from explicit lateIntentBytes or
// lateAcceptanceBytes. The adapter writes those bytes through the rooted
// filesystem and calls the production publisher, which rereads and hashes
// them. The current approve model's stableBeforeCas=false event carries no
// replacement bytes, so that event is refused instead of manufacturing a
// second-read source from the boolean. A migrated cohort must add those bytes
// (or another byte-derived mutation input) before this trace can be replayed.
//
// Quint's current intent generator can pair the same hash symbol with either
// prefixOk value. That pair cannot represent two different real hash
// comparisons without trusting the forbidden boolean; its v1.3 migration must
// replace this input with a claim symbol that carries the result. Similarly,
// controller-side cache/ref effects are always observed from the production
// cache and Git objects, even where the current Quint projection omits a
// dangling competing ref.
package intentapprove
