//go:build !darwin && !linux

package safefs

import "errors"

func openPlatformRoot(string) (platformRoot, error) {
	return nil, errors.New("safefs: descriptor-rooted operations require Linux or macOS")
}
