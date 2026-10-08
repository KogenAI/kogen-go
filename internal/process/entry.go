package process

import "os"

// Private guardian submodes are registered in the process package so the
// same executable can enter its supervisor before public CLI dispatch. The
// modes require both the private marker and inherited control descriptors.
func init() {
	switch os.Getenv(internalProcessMode) {
	case guardianMode:
		if guardianDescriptorsPresent() {
			os.Exit(guardianEntry())
		}
	case anchorMode:
		if anchorDescriptorsPresent() {
			os.Exit(anchorEntry())
		}
	}
}
