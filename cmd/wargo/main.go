// SPDX-License-Identifier: AGPL-3.0-only

// Command wargo is a single-binary homelab dashboard.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/kushalpandya/wargo/internal/auth"
	"github.com/kushalpandya/wargo/internal/config"
	"github.com/kushalpandya/wargo/internal/helper"
	"github.com/kushalpandya/wargo/internal/server"
	"github.com/kushalpandya/wargo/internal/store"
)

func usage() {
	fmt.Fprintf(os.Stderr, `Wargo %s - homelab dashboard

Usage:
  wargo [serve] [-config PATH]     run the web service
  wargo admin reset-auth           remove all passkeys, TOTP and sessions
  wargo helper <op> ...            privileged helper (invoked via sudo)
  wargo version
`, server.Version)
}

func main() {
	args := os.Args[1:]
	cmd := "serve"
	if len(args) > 0 && args[0] != "" && args[0][0] != '-' {
		cmd, args = args[0], args[1:]
	}
	switch cmd {
	case "serve":
		serve(args)
	case "helper":
		os.Exit(helper.Main(args))
	case "admin":
		admin(args)
	case "version":
		fmt.Println(server.Version)
	case "help", "-h", "--help":
		usage()
	default:
		usage()
		os.Exit(2)
	}
}

func loadConfig(args []string) config.Config {
	fs := flag.NewFlagSet("wargo", flag.ExitOnError)
	path := fs.String("config", config.DefaultPath(), "config file")
	listen := fs.String("listen", "", "override listen address")
	debug := fs.Bool("debug", false, "debug logging")
	fs.Parse(args)
	level := slog.LevelInfo
	if *debug {
		level = slog.LevelDebug
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})))
	cfg, err := config.Load(*path)
	if err != nil {
		slog.Error("config", "err", err)
		os.Exit(1)
	}
	if *listen != "" {
		cfg.Listen = *listen
	}
	slog.Info("config loaded", "path", *path, "data", cfg.DataDir)
	return cfg
}

func serve(args []string) {
	cfg := loadConfig(args)
	if os.Geteuid() == 0 {
		slog.Warn("running as root is not recommended; run wargo as the admin user")
	}
	srv, err := server.New(cfg)
	if err != nil {
		slog.Error("startup", "err", err)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := srv.Run(ctx); err != nil {
		slog.Error("server", "err", err)
		os.Exit(1)
	}
}

func admin(args []string) {
	if len(args) == 0 || args[0] != "reset-auth" {
		usage()
		os.Exit(2)
	}
	cfg := loadConfig(args[1:])
	st, err := store.Open(cfg.DataDir)
	if err != nil {
		slog.Error("open store", "err", err)
		os.Exit(1)
	}
	defer st.Close()
	if err := auth.ResetAll(st); err != nil {
		slog.Error("reset", "err", err)
		os.Exit(1)
	}
	fmt.Println("All passkeys, TOTP, recovery codes and sessions were removed.")
	fmt.Println("Sign in with your password to set up two-factor authentication again.")
}
