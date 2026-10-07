// Package run coordinates one public Shape operation. It owns the command-local
// conversation loop, deterministic validation, audits, result serialization,
// and the success-or-failure accounting publication. Approval and commit are
// intentionally outside this package.
package run
