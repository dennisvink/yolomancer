package store

import (
	"fmt"
	"net/url"
	"path"
	"strings"
)

func ValidatePath(value string, allowEmpty bool) (string, error) {
	if strings.ContainsRune(value, '\\') {
		return "", fmt.Errorf("%w: backslashes are not allowed", ErrInvalidPath)
	}
	if decoded, err := url.PathUnescape(value); err != nil {
		return "", fmt.Errorf("%w: invalid percent encoding", ErrInvalidPath)
	} else if decoded != value {
		decoded = strings.ReplaceAll(decoded, "\\", "/")
		decodedClean := path.Clean(decoded)
		if strings.HasPrefix(decoded, "/") || decodedClean != decoded || decodedClean == ".." || strings.HasPrefix(decodedClean, "../") || strings.Contains(decoded, "//") {
			return "", fmt.Errorf("%w: encoded path escapes store root", ErrInvalidPath)
		}
	}
	value = strings.TrimSpace(value)
	if value == "" {
		if allowEmpty {
			return "", nil
		}
		return "", fmt.Errorf("%w: path is empty", ErrInvalidPath)
	}
	if strings.HasPrefix(value, "/") {
		return "", fmt.Errorf("%w: path must be relative", ErrInvalidPath)
	}
	hasPrefixSeparator := allowEmpty && strings.HasSuffix(value, "/")
	clean := path.Clean(value)
	if clean == "." {
		if allowEmpty {
			return "", nil
		}
		return "", fmt.Errorf("%w: path is empty", ErrInvalidPath)
	}
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("%w: path escapes store root", ErrInvalidPath)
	}
	if clean != value && (!hasPrefixSeparator || clean+"/" != value) {
		return "", fmt.Errorf("%w: path is not canonical", ErrInvalidPath)
	}
	if hasPrefixSeparator {
		return clean + "/", nil
	}
	return clean, nil
}
