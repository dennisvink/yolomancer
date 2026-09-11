package transport

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
)

func TestRemoteHelperConnect(t *testing.T) {
	var output bytes.Buffer
	called := false
	err := ServeRemoteHelper(context.Background(), ResolverFunc(func(_ context.Context, address, service string, input io.Reader, output io.Writer) error {
		called = address == "bgit://demo.git" && service == UploadPackService
		return nil
	}), []string{"origin", "bgit://demo.git"}, strings.NewReader("capabilities\nconnect git-upload-pack\n"), &output, io.Discard)
	if err != nil || !called {
		t.Fatalf("ServeRemoteHelper = %v, called=%v", err, called)
	}
	if output.String() != "connect\n\n\n" {
		t.Fatalf("output = %q", output.String())
	}
}
