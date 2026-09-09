package config

import (
	"fmt"
	"os"
	"path/filepath"
)

// WritePrivateFile commits a complete owner-only file. Exclusive creation is
// used for signing identities so simultaneous first runs cannot replace keys.
func WritePrivateFile(path string, data []byte, exclusive bool) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("private directory must not be a symlink")
	}
	if err := os.Chmod(dir, 0700); err != nil {
		return err
	}
	if info, err := os.Lstat(path); err == nil {
		if exclusive {
			return os.ErrExist
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("private file must be a regular file")
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	f, err := os.CreateTemp(dir, ".private-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if _, err = f.Write(data); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if exclusive {
		err = os.Link(f.Name(), path)
	} else {
		err = os.Rename(f.Name(), path)
	}
	if err != nil {
		return err
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
