// SPDX-License-Identifier: AGPL-3.0-only

// Command wargo-privd is Wargo's optional root helper. It listens on a unix
// socket, accepts requests only from the configured user, and runs a closed
// set of privileged commands after verifying a passkey assertion.
//
// Install it as root with deploy/systemd/wargo-privd.{socket,service} and a
// config at /etc/wargo/privd.toml (see deploy/privd.toml).
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/kushalpandya/wargo/internal/privd"
)

var version = "0.1.0-alpha"

func main() {
	cfgPath := flag.String("config", privd.DefaultConfigPath, "config file")
	socket := flag.String("socket", "", "override socket path")
	check := flag.Bool("check", false, "validate the config and exit")
	showVersion := flag.Bool("version", false, "print version")
	flag.Parse()
	if *showVersion {
		fmt.Println(version)
		return
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, nil)))

	cfg, err := privd.LoadConfig(*cfgPath)
	if *socket != "" {
		cfg.Socket = *socket
	}
	if err != nil {
		slog.Error("config", "path", *cfgPath, "err", err)
		os.Exit(1)
	}
	if *check {
		fmt.Println("config OK")
		return
	}
	if os.Geteuid() != 0 {
		slog.Warn("not running as root; privileged commands will fail")
	}
	ln, err := privd.Listen(cfg)
	if err != nil {
		slog.Error("listen", "err", err)
		os.Exit(1)
	}
	slog.Info("wargo-privd listening", "socket", ln.Addr().String(), "allowed_uid", cfg.AllowedUID, "origins", cfg.Origins)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		ln.Close()
	}()
	if err := privd.New(cfg).Serve(ln); err != nil {
		slog.Error("serve", "err", err)
		os.Exit(1)
	}
}
