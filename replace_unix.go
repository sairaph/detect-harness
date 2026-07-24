//go:build !windows

package detectharness

import "os"

func replaceFile(source, destination string, _ bool) error {
	return os.Rename(source, destination)
}
