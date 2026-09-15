package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/dennisvink/yolomancer/internal/agentapi"
	appconfig "github.com/dennisvink/yolomancer/internal/config"
	"github.com/dennisvink/yolomancer/internal/model"
	"github.com/dennisvink/yolomancer/internal/provider"
)

func serve(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	path := fs.String("config", "agents.yaml", "YAML or JSON agent configuration")
	host := fs.String("host", "", "override configured listen IP (default 127.0.0.1)")
	port := fs.Int("port", 0, "override configured port (default 8081)")
	profile := fs.String("profile", "", "AWS profile for model/tool credentials")
	check := fs.Bool("check", false, "validate configuration and tools without listening or calling a model")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected serve argument")
	}
	cfg, agents, err := agentapi.Load(*path, *host, *port)
	if err != nil {
		return err
	}
	if *check {
		fmt.Fprintf(os.Stdout, "Valid API configuration: %d agent(s).\n", len(agents))
		return nil
	}
	address := net.JoinHostPort(cfg.Server.Host, strconv.Itoa(cfg.Server.Port))
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	api := agentapi.NewServer(cfg.Server, agents, *profile, nil)
	server := &http.Server{Handler: api, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16384}
	finished := make(chan error, 1)
	go func() { finished <- server.Serve(listener) }()
	fmt.Fprintf(os.Stderr, "Yolomancer API listening on http://%s (%d agents)\n", address, len(agents))
	select {
	case err = <-finished:
		api.CancelAll()
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		api.CancelAll()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdown); err != nil {
			_ = server.Close()
			return err
		}
		return nil
	}
}

func apiWorker(ctx context.Context) error {
	return agentapi.Worker(ctx, os.Stdin, os.Stdout, func(ctx context.Context, profile string) (*model.Config, error) {
		cfg, err := appconfig.LoadForProfile(profile)
		if err != nil {
			return nil, err
		}
		if cfg.Registration != nil || cfg.AWSProfile != nil {
			err = provider.PrepareBedrock(ctx, cfg)
		}
		return cfg, err
	})
}
