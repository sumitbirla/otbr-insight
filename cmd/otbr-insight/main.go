package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/otbr-insight/otbr-insight/internal/api"
	"github.com/otbr-insight/otbr-insight/internal/backup"
	"github.com/otbr-insight/otbr-insight/internal/config"
	"github.com/otbr-insight/otbr-insight/internal/names"
	"github.com/otbr-insight/otbr-insight/internal/otbr"
	"github.com/otbr-insight/otbr-insight/internal/otctl"
	"github.com/otbr-insight/otbr-insight/internal/service"
	"github.com/otbr-insight/otbr-insight/web"
)

func main() {
	cfg, err := config.Parse(os.Args[1:])
	if err != nil {
		// -h/--help is a request, not a failure: print usage on stdout and succeed.
		if errors.Is(err, flag.ErrHelp) {
			config.Usage(os.Stdout)
			return
		}
		fmt.Fprintf(os.Stderr, "otbr-insight: %v\n\n", err)
		config.Usage(os.Stderr)
		os.Exit(2)
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: logLevel(cfg.LogLevel)}))
	slog.SetDefault(logger)

	client, err := otbr.NewClient(cfg.OTBRURL, &http.Client{Timeout: 4 * time.Second})
	if err == nil {
		client.SetLogger(logger)
	}
	if err != nil {
		logger.Error("create OTBR client", "error", err)
		os.Exit(2)
	}
	// Active scanning has no REST equivalent, so it comes from the OpenThread daemon
	// socket when otbr-insight runs on the border router. Absent socket, the otbr-web
	// endpoint is still tried, and absent that the feature reports itself unavailable.
	if strings.TrimSpace(cfg.OTBRSocket) != "" {
		socket := otctl.New(cfg.OTBRSocket, 30*time.Second)
		client.SetNetworkScanner(socket)
		// Channel energy scans, answered synchronously where REST needs an action.
		client.SetEnergyScanner(socket)
		// Live device and topology data, preferred over OTBR's REST caches.
		client.SetMeshReader(socket)
		// Runtime fields otbr-web used to supply; the socket has them either way.
		client.SetStatusReader(socket)
		// Reachability testing, which only the border router can perform.
		client.SetPinger(socket)
		// OpenThread's own event recorder, for the diagnostics view.
		client.SetHistoryReader(socket)
		if socket.Available() {
			// Only a stat: whether it can actually be opened shows up on first use.
			logger.Info("OpenThread daemon socket present", "path", socket.Path())
		} else {
			logger.Info("OpenThread daemon socket not present; nearby-network scanning will fall back to otbr-web", "path", socket.Path())
		}
	}

	monitor := service.NewMonitor(client, cfg.PollInterval, logger, service.WithDiscoveryInterval(cfg.DiscoveryInterval))

	nameStore := names.New(cfg.DataDir)
	if err := nameStore.Load(); err != nil {
		logger.Warn("load device names", "error", err)
	}
	if nameStore.Enabled() {
		logger.Info("device naming enabled", "data_dir", cfg.DataDir)
	} else {
		logger.Info("device naming disabled: no data directory")
	}

	backupStore := backup.New(cfg.DataDir)
	if err := backupStore.Load(); err != nil {
		logger.Warn("load dataset backup", "error", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go monitor.Run(ctx)

	server := &http.Server{
		Addr: cfg.Listen, Handler: api.Handler(monitor, nameStore, client, backupStore, web.Handler(), logger),
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second,
		WriteTimeout: 25 * time.Second, IdleTimeout: 60 * time.Second,
	}
	go func() {
		logger.Info("OTBR Insight started", "listen", cfg.Listen, "otbr_url", cfg.OTBRURL, "poll_interval", cfg.PollInterval.String(), "discovery_interval", cfg.DiscoveryInterval.String())
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("HTTP server failed", "error", err)
			stop()
		}
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Error("HTTP server shutdown failed", "error", err)
	}
	logger.Info("OTBR Insight stopped")
}

func logLevel(value string) slog.Level {
	switch strings.ToLower(value) {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
