package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"docket/internal/docket"
)

func main() {
	// Go otherwise exits on a broken stdout pipe before we can report a possibly committed write.
	signal.Ignore(syscall.SIGPIPE)
	os.Exit(execute(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func serve(arguments []string, output io.Writer) (result error) {
	flags := flag.NewFlagSet("docket serve", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.Usage = func() {}
	const shutdownTimeout = 10 * time.Second
	listenAddress := flags.String("listen", "127.0.0.1:7331", "HTTP listen address; loopback by default")
	databasePath := flags.String("database", "data/docket.sqlite", "SQLite file on local persistent storage")
	publicURL := flags.String("public-url", "", "browser origin, required for non-loopback listeners (no subpath)")
	backupPath := flags.String("backup-to", "", "write a consistent backup to a new file and exit")
	if err := flags.Parse(arguments); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			var help bytes.Buffer
			help.WriteString("Usage: docket serve [options]\n\n")
			flags.SetOutput(&help)
			flags.PrintDefaults()
			_, err := output.Write(help.Bytes())
			return err
		}
		return errors.New("Invalid server flags; use docket serve --help")
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	store, err := docket.Open(*databasePath)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, store.Close()) }()
	if *backupPath != "" {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		return store.Backup(ctx, *backupPath)
	}
	host, _, err := net.SplitHostPort(*listenAddress)
	if err != nil {
		return fmt.Errorf("listen address: %w", err)
	}
	if *publicURL == "" && host != "127.0.0.1" && host != "localhost" && host != "::1" {
		return errors.New("non-loopback listeners require --public-url; v1 is unauthenticated, so restrict network access")
	}
	listener, err := net.Listen("tcp", *listenAddress)
	if err != nil {
		return err
	}
	defer func() {
		if err := listener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			result = errors.Join(result, err)
		}
	}()
	if *publicURL == "" {
		*publicURL = "http://" + listener.Addr().String()
	}
	handler, err := docket.NewServer(store, *publicURL)
	if err != nil {
		return err
	}
	server := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       30 * time.Second,
		MaxHeaderBytes:    16384,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	finished := make(chan error, 1)
	go func() { finished <- server.Serve(listener) }()
	slog.Warn("unauthenticated server; anyone who can reach it can read and write", "url", *publicURL)
	select {
	case err := <-finished:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	case <-ctx.Done():
		shutdownContext, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		if err := server.Shutdown(shutdownContext); err != nil {
			return errors.Join(err, server.Close())
		}
		err := <-finished
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	}
}
