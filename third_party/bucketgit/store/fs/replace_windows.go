//go:build windows

package fs

import (
	"errors"
	"time"

	"golang.org/x/sys/windows"
)

func replaceFile(source, target string) error {
	sourcePtr, err := windows.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	targetPtr, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	const attempts = 100
	for attempt := 0; attempt < attempts; attempt++ {
		err = windows.MoveFileEx(sourcePtr, targetPtr, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
		if err == nil {
			return nil
		}
		if !errors.Is(err, windows.ERROR_ACCESS_DENIED) &&
			!errors.Is(err, windows.ERROR_SHARING_VIOLATION) &&
			!errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
			return err
		}
		delay := time.Duration(attempt+1) * time.Millisecond
		if delay > 10*time.Millisecond {
			delay = 10 * time.Millisecond
		}
		time.Sleep(delay)
	}
	return err
}
