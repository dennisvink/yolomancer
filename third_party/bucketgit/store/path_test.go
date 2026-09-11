package store

import (
	"errors"
	"testing"
)

func TestValidatePath(t *testing.T) {
	for _, valid := range []string{"objects/ab/cdef", "refs/heads/main", "HEAD"} {
		if got, err := ValidatePath(valid, false); err != nil || got != valid {
			t.Fatalf("ValidatePath(%q) = %q, %v", valid, got, err)
		}
	}
	if got, err := ValidatePath("", true); err != nil || got != "" {
		t.Fatalf("empty prefix = %q, %v", got, err)
	}
	if got, err := ValidatePath("refs/", true); err != nil || got != "refs/" {
		t.Fatalf("prefix separator = %q, %v", got, err)
	}
	for _, invalid := range []string{"", "/etc/passwd", "../secret", "objects/../../secret", "objects\\secret", "objects//secret", "./HEAD"} {
		if _, err := ValidatePath(invalid, false); !errors.Is(err, ErrInvalidPath) {
			t.Fatalf("ValidatePath(%q) error = %v", invalid, err)
		}
	}
}
