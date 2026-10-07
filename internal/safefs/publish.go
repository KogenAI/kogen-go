package safefs

import "io/fs"

const privateFileMode fs.FileMode = 0o600

// PublishPrivate atomically publishes a controller-owned file with owner-only
// permissions. Publish creates an exclusive private temporary file, syncs its
// contents, replaces the leaf without following it, and syncs the parent.
func (r *Root) PublishPrivate(name string, contents []byte, mode PublicationMode) error {
	return r.Publish(name, contents, privateFileMode, mode)
}

// AppendPrivate appends to a controller-owned file using copy-on-write
// publication and owner-only permissions.
func (r *Root) AppendPrivate(name string, contents []byte) error {
	return r.Append(name, contents, privateFileMode)
}
