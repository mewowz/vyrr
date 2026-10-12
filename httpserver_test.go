package main

import (
	"log/slog"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	metricsServer "github.com/mewowz/mourncdn/internal/metrics"
	"github.com/prometheus/client_golang/prometheus"
)

func TestNewCDNServer(t *testing.T) {
	tmpDirPath := t.TempDir()
	logger := slog.New(slog.DiscardHandler)

	metrics := metricsServer.NewMetrics(prometheus.NewRegistry())

	t.Run("bad serverCfg propagates error", func(t *testing.T) {
		serverCfg := localAssetServerConfig{
			AssetMaxSize: -1,
		}
		_, err := NewCDNServer(
			serverCfg,
			localAssetUploaderConfig{},
			CDNServerConfig{},
			metrics,
			nil,
		)
		if err == nil {
			t.Errorf("got %v, want non-nil", err)
		}
	})

	t.Run("bad uploadCfg propagates error", func(t *testing.T) {
		// TODO: remove this guard after auth is re-implemented
		t.Skip("auth is not implemented, so upload is currently unregistered")
		serverCfg := localAssetServerConfig{
			AssetDir:     tmpDirPath,
			AssetMaxSize: 1024,
			CacheMaxSize: 4096,
			TTL:          time.Minute,
			WriteBufSize: 4096,
			WriteWindow:  time.Second,
		}
		uploadCfg := localAssetUploaderConfig{
			TmpDirPath:         tmpDirPath,
			OutputDirPath:      tmpDirPath,
			MaxAssetUploadSize: -1,
		}
		_, err := NewCDNServer(
			serverCfg,
			uploadCfg,
			CDNServerConfig{},
			metrics,
			nil,
		)
		if err == nil {
			t.Errorf("got %v, want non-nil", err)
		}
	})

	t.Run("assert NewCDNServer output", func(t *testing.T) {
		serverCfg := localAssetServerConfig{
			AssetDir:     tmpDirPath,
			AssetMaxSize: 1024,
			CacheMaxSize: 4096,
			TTL:          time.Minute,
			WriteBufSize: 4096,
			WriteWindow:  time.Second,
		}
		uploadCfg := localAssetUploaderConfig{
			TmpDirPath:         tmpDirPath,
			OutputDirPath:      tmpDirPath,
			URLPrefix:          "/assets",
			MaxAssetUploadSize: 1024,
		}
		cdnCfg := CDNServerConfig{
			ServeRoute:        "/assets",
			UploadRoute:       "/upload",
			Address:           "localhost:8123",
			ReadTimeout:       time.Second,
			ReadHeaderTimeout: time.Second,
			WriteTimeout:      time.Second,
			IdleTimeout:       time.Second,
		}
		got, err := NewCDNServer(
			serverCfg,
			uploadCfg,
			cdnCfg,
			metrics,
			logger,
		)
		if err != nil {
			t.Fatalf("got err=%v, want %v", err, nil)
		}

		gotCfg := struct {
			Addr              string
			ReadTimeout       time.Duration
			ReadHeaderTimeout time.Duration
			WriteTimeout      time.Duration
			IdleTimeout       time.Duration
		}{
			got.server.Addr,
			got.server.ReadTimeout,
			got.server.ReadHeaderTimeout,
			got.server.WriteTimeout,
			got.server.IdleTimeout,
		}

		wantCfg := struct {
			Addr              string
			ReadTimeout       time.Duration
			ReadHeaderTimeout time.Duration
			WriteTimeout      time.Duration
			IdleTimeout       time.Duration
		}{
			cdnCfg.Address,
			cdnCfg.ReadTimeout,
			cdnCfg.ReadHeaderTimeout,
			cdnCfg.WriteTimeout,
			cdnCfg.IdleTimeout,
		}

		if diff := cmp.Diff(wantCfg, gotCfg); diff != "" {
			t.Errorf("server config mismatch (-want, +got):\n%s", diff)
		}
	})
}
