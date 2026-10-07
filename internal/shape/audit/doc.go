// Package audit runs Shape's requirement ledger and acceptance-test audit at
// the end of every eligible validation traversal. It keeps both auditor calls
// in spec order and returns simultaneous repair findings as one repair.
package audit
