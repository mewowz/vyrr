package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	metricsServer "github.com/mewowz/vyrr/internal/metrics"
	"github.com/prometheus/client_golang/prometheus"
)

const DefaultConfigFilePath = "./config.yml"

func main() {
	logger := slog.Default()
	if err := run(logger); err != nil {
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	var workersGroup sync.WaitGroup
	defer workersGroup.Wait()

	config, err := LoadConfigFile(DefaultConfigFilePath)
	if err != nil {
		logger.Error("config", "err", err)
		return err
	}

	reg := prometheus.NewRegistry()
	metrics := metricsServer.NewMetrics(reg)
	metricsCtx, metricsCancelF := context.WithCancel(context.Background())
	defer metricsCancelF()
	workersGroup.Go(func() {
		metricsErr := metricsServer.ServeMetricsWithContext(
			metricsCtx,
			reg,
			config.MetricsCfg,
		)
		if metricsErr != nil {
			logger.Error("metrics server", "err", metricsErr)
		}
	})

	server, err := NewCDNServer(
		config.ServeCfg,
		config.UploadCfg,
		config.HTTPCfg,
		metrics,
		logger,
	)
	if err != nil {
		logger.Error("cdn server", "err", err)
		return err
	}

	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)

	logger.Info("starting server", "address", config.HTTPCfg.Address)
	errCh := make(chan error, 1)
	workersGroup.Go(func() {
		errCh <- server.Start()
	})

	select {
	case err := <-errCh:
		if err != nil {
			logger.Error("server", "err", err)
		}
		return err
	case <-c:
		logger.Info("shutting down server gracefully")
	}

	metricsCancelF()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	if err := server.ShutdownGracefully(ctx); err != nil {
		logger.Error("graceful shutdown failed", "err", err)

		if err := server.ForceCloseServer(); err != nil {
			logger.Error("force close failed", "err", err)
		}
	}

	workersGroup.Wait()
	logger.Info("bye")

	return nil
}
