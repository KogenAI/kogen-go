// Package workspace creates isolated Build workspaces, seeds cached setup
// products without inode sharing, and installs immutable approved bytes.
//
// Workspace lifecycle controllers own when a workspace may be removed. In
// particular, crashed work must pass the recovery preservation effect before
// cleanup; this package's clone-failure cleanup only removes a workspace that
// has not yet been returned to a Build controller.
package workspace
