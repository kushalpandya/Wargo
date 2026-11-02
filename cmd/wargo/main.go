// SPDX-License-Identifier: AGPL-3.0-only

// Command wargo is a single-binary homelab dashboard.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/kushalpandya/wargo/internal/auth"
	"github.com/kushalpandya/wargo/internal/config"
	"github.com/kushalpandya/wargo/internal/helper"
	"github.com/kushalpandya/wargo/internal/host"
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
  wargo health [-addr HOST:PORT]   exit 0 if the local web service answers

Container mode is enabled by WARGO_HOST_ROOT (see docs/DOCKER.md).
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
	case "health":
		os.Exit(health(args))
	case "help", "-h", "--help":
		usage()
	default:
		usage()
		os.Exit(2)
	}
}

func loadConfig(args []string) config.Config { return loadConfigChecked(args, false) }

// loadConfigChecked parses flags and loads the config. With preflight set,
// container mode is verified first (so a missing /host mount is reported
// as such rather than as a config error).
func loadConfigChecked(args []string, preflight bool) config.Config {
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
	if host.Container() {
		host.SetupEnv()
		if preflight {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			err := host.Check(ctx)
			cancel()
			if err != nil {
				slog.Error("startup", "err", err)
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
		}
	}
	cfg, err := config.Load(*path)
	if err != nil {
		slog.Error("config", "err", err)
		os.Exit(1)
	}
	if *listen != "" {
		cfg.Listen = *listen
	}
	slog.Info("config loaded", "path", *path, "data", cfg.DataDir)
	if host.Container() {
		a, err := host.SetAdmin(cfg.Auth.User)
		if err != nil {
			slog.Error("container mode: the admin account is not a host user", "user", cfg.Auth.User, "err", err)
			os.Exit(1)
		}
		slog.Info("container mode", "host_root", host.Root(), "user", a.Name, "uid", a.UID, "groups", a.Groups)
	}
	return cfg
}

func serve(args []string) {
	cfg := loadConfigChecked(args, true)
	if os.Geteuid() == 0 && !host.Container() {
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

// health probes /api/auth/state on the local listener (Docker HEALTHCHECK).
func health(args []string) int {
	fs := flag.NewFlagSet("health", flag.ExitOnError)
	addr := fs.String("addr", "", "address to probe (default: the configured listen address)")
	fs.Parse(args)
	a := *addr
	if a == "" {
		a = config.ListenAddr(config.DefaultPath())
	}
	h, p, err := net.SplitHostPort(a)
	if err != nil {
		fmt.Fprintln(os.Stderr, "health:", err)
		return 1
	}
	if h == "" || h == "0.0.0.0" || h == "::" || h == "[::]" {
		h = "127.0.0.1"
	}
	c := &http.Client{Timeout: 4 * time.Second}
	resp, err := c.Get("http://" + net.JoinHostPort(h, p) + "/api/auth/state")
	if err != nil {
		fmt.Fprintln(os.Stderr, "health:", err)
		return 1
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintln(os.Stderr, "health: status", resp.Status)
		return 1
	}
	return 0
}
