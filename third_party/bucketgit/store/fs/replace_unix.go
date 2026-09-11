//go:build !windows

package fs

import "os"

func replaceFile(source, target string) error {
	return os.Rename(source, target)
}
