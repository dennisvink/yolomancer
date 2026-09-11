package store

import (
	"context"
	"errors"
	"io/fs"
	"strings"
)

func ReadRefs(ctx context.Context, reader Reader) (map[string]string, error) {
	refs := map[string]string{}
	for _, prefix := range []string{"refs/heads/", "refs/tags/"} {
		paths, err := reader.List(ctx, prefix)
		if err != nil {
			return nil, err
		}
		for _, path := range paths {
			data, err := reader.Read(ctx, path)
			if err != nil {
				return nil, err
			}
			if oid := strings.TrimSpace(string(data)); ValidSHA1(oid) {
				refs[path] = oid
			}
		}
	}
	packed, err := reader.Read(ctx, "packed-refs")
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	if err == nil {
		for _, line := range strings.Split(string(packed), "\n") {
			fields := strings.Fields(line)
			if len(fields) == 2 && ValidSHA1(fields[0]) && strings.HasPrefix(fields[1], "refs/") {
				refs[fields[1]] = fields[0]
			}
		}
	}
	return refs, nil
}

func ValidSHA1(value string) bool {
	value = strings.TrimSpace(value)
	if len(value) != 40 {
		return false
	}
	for _, char := range value {
		if !((char >= '0' && char <= '9') || (char >= 'a' && char <= 'f') || (char >= 'A' && char <= 'F')) {
			return false
		}
	}
	return true
}

func ValidateRefUpdate(ref, oldOID, newOID string) error {
	if _, err := ValidatePath(ref, false); err != nil || !strings.HasPrefix(ref, "refs/") {
		return ErrInvalidPath
	}
	for _, oid := range []string{oldOID, newOID} {
		if oid != "" && oid != strings.Repeat("0", 40) && !ValidSHA1(oid) {
			return ErrInvalidPath
		}
	}
	return nil
}

func IsZeroOID(value string) bool { return value == "" || value == strings.Repeat("0", 40) }
