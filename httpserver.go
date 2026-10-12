package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	metricsServer "github.com/mewowz/vyrr/internal/metrics"
)

type CDNServer struct {
	mux    *http.ServeMux
	server *http.Server

	logger *slog.Logger
}

type CDNServerConfig struct {
	ServeRoute  string `koanf:"serve-route"`
	UploadRoute string `koanf:"upload-route"`

	Address           string        `koanf:"address"`
	ReadTimeout       time.Duration `koanf:"advanced.read-timeout"`
	ReadHeaderTimeout time.Duration `koanf:"advanced.read-header-timeout"`
	WriteTimeout      time.Duration `koanf:"advanced.write-timeout"`
	IdleTimeout       time.Duration `koanf:"advanced.idle-timeout"`
}

func NewCDNServer(
	serveCfg localAssetServerConfig,
	uploadCfg localAssetUploaderConfig,
	cdnCfg CDNServerConfig,
	metrics *metricsServer.Metrics,
	logger *slog.Logger,
) (*CDNServer, error) {
	if logger == nil {
		logger = slog.Default()
		logger.Info("no logger specified - using default logger")
	}
	serveHandler, err := NewLocalAssetServer(serveCfg, logger, metrics)
	if err != nil {
		return nil, fmt.Errorf("initialze asset server: %w", err)
	}

	// TODO: re-register uploadHandler after auth is implemented again
	//uploadHandler, err := NewLocalAssetUploader(uploadCfg, logger, metrics)
	//if err != nil {
	//	return nil, fmt.Errorf("initialze upload server: %w", err)
	//}

	mux := http.NewServeMux()
	mux.Handle(
		fmt.Sprintf(
			"GET %s/{id...}", cdnCfg.ServeRoute,
		),
		serveHandler,
	)
	//mux.Handle(
	//	fmt.Sprintf(
	//		"POST %s", cdnCfg.UploadRoute,
	//	),
	//	uploadHandler,
	//)

	server := &http.Server{
		Addr:              cdnCfg.Address,
		Handler:           mux,
		ReadTimeout:       cdnCfg.ReadTimeout,
		ReadHeaderTimeout: cdnCfg.ReadHeaderTimeout,
		WriteTimeout:      cdnCfg.WriteTimeout,
		IdleTimeout:       cdnCfg.IdleTimeout,
	}

	return &CDNServer{
		mux:    mux,
		server: server,
		logger: logger,
	}, nil
}

func (s *CDNServer) ShutdownGracefully(ctx context.Context) error {
	err := s.server.Shutdown(ctx)
	if err != nil {
		return err
	}

	return nil
}

func (s *CDNServer) ForceCloseServer() error {
	err := s.server.Close()
	if err != nil {
		return fmt.Errorf("force close server: %w", err)
	}
	return nil
}

func (s *CDNServer) Start() error {
	err := s.server.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
