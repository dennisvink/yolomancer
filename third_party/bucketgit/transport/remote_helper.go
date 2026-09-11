package transport

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
)

type Resolver interface {
	Serve(ctx context.Context, address, service string, input io.Reader, output io.Writer) error
}

type ResolverFunc func(context.Context, string, string, io.Reader, io.Writer) error

func (f ResolverFunc) Serve(ctx context.Context, address, service string, input io.Reader, output io.Writer) error {
	return f(ctx, address, service, input, output)
}

func ServeRemoteHelper(ctx context.Context, resolver Resolver, args []string, input io.Reader, output, errorOutput io.Writer) error {
	address := RemoteHelperAddress(args)
	if address == "" {
		return errors.New("usage: git-remote-bgit <repository> [<url>]")
	}
	if resolver == nil {
		return errors.New("git remote helper resolver is required")
	}
	reader, writer := bufio.NewReader(input), bufio.NewWriter(output)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			if errors.Is(err, io.EOF) && strings.TrimSpace(line) == "" {
				return nil
			}
			return err
		}
		line = strings.TrimRight(line, "\r\n")
		switch {
		case line == "":
			return nil
		case line == "capabilities":
			fmt.Fprintln(writer, "connect")
			fmt.Fprintln(writer)
			if err := writer.Flush(); err != nil {
				return err
			}
		case strings.HasPrefix(line, "connect "):
			service := strings.TrimSpace(strings.TrimPrefix(line, "connect "))
			if service != UploadPackService && service != ReceivePackService {
				return fmt.Errorf("unsupported git remote helper service %q", service)
			}
			fmt.Fprintln(writer)
			if err := writer.Flush(); err != nil {
				return err
			}
			return resolver.Serve(ctx, address, service, reader, output)
		case strings.HasPrefix(line, "option "):
			fmt.Fprintln(writer, "unsupported")
			if err := writer.Flush(); err != nil {
				return err
			}
		default:
			fmt.Fprintf(errorOutput, "unsupported git remote helper command %q\n", line)
			return fmt.Errorf("unsupported git remote helper command %q", line)
		}
	}
}

func RemoteHelperAddress(args []string) string {
	if len(args) >= 2 {
		return strings.TrimSpace(args[1])
	}
	if len(args) == 1 {
		return strings.TrimSpace(args[0])
	}
	return ""
}
