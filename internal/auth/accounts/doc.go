// Package accounts reads and writes machine-local provider account selection.
//
// Account selections are resolved once into a RunAccount and then retained by
// the caller for the life of that run. The package never falls back between
// accounts after resolution.
package accounts
