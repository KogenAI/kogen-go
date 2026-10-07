// Package drain owns the public queue process lifecycle around the pure
// scheduler: state-root preparation, exclusive ownership, stop/detach, signals,
// status refresh, Build outcomes, streamed lines, and final exit counts.
package drain
