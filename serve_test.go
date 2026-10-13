package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	metricsServer "github.com/mewowz/vyrr/internal/metrics"
	"github.com/prometheus/client_golang/prometheus"
)

func TestNewLocalAssetServer(t *testing.T) {
	testDirPath := t.TempDir()

	tests := []struct {
		name        string
		cfg         localAssetServerConfig
		logger      *slog.Logger
		metrics     *metricsServer.Metrics
		expectedErr error
	}{
		{
			"good config no error",
			localAssetServerConfig{
				testDirPath,
				1,
				2,
				time.Second,
				1,
				time.Second,
			},
			nil,
			metricsServer.NewMetrics(prometheus.NewRegistry()),
			nil,
		},
		{
			"good config custom logger",
			localAssetServerConfig{
				testDirPath,
				1,
				2,
				time.Second,
				1,
				time.Second,
			},
			slog.New(slog.DiscardHandler),
			metricsServer.NewMetrics(prometheus.NewRegistry()),
			nil,
		},
		{
			"NewLocalAssetCache errors propagated",
			localAssetServerConfig{
				filepath.Join(testDirPath, "doesnotexist"),
				1,
				2,
				time.Second,
				1,
				time.Second,
			},
			nil,
			metricsServer.NewMetrics(prometheus.NewRegistry()),
			os.ErrNotExist,
		},
		{
			"<= 0 WriteBufSize raises error",
			localAssetServerConfig{
				testDirPath,
				1,
				2,
				time.Second,
				-1,
				time.Second,
			},
			nil,
			metricsServer.NewMetrics(prometheus.NewRegistry()),
			ErrInvalidWriteBufSize,
		},
		{
			"<= 0 WriteWindow raises error",
			localAssetServerConfig{
				testDirPath,
				1,
				2,
				time.Second,
				1,
				-1 * time.Second,
			},
			nil,
			metricsServer.NewMetrics(prometheus.NewRegistry()),
			ErrInvalidWriteWindow,
		},
		{
			"metrics is nil",
			localAssetServerConfig{
				testDirPath,
				1,
				2,
				time.Second,
				1,
				time.Second,
			},
			nil,
			nil,
			metricsServer.ErrNilMetrics,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := NewLocalAssetServer(
				test.cfg,
				test.logger,
				test.metrics,
			)
			if !errors.Is(err, test.expectedErr) {
				t.Fatalf("got %v, want %v", err, test.expectedErr)
			}

			if test.expectedErr != nil {
				return
			}

			if test.logger != nil && got.logger != test.logger {
				t.Fatalf("got logger=%v, want logger=%v", got.logger, test.logger)
			}

			want := &LocalAssetServer{
				writeBufSize: test.cfg.WriteBufSize,
				writeWindow:  test.cfg.WriteWindow,
				logger:       slog.New(slog.DiscardHandler),
				metrics:      test.metrics,
			}

			diff := cmp.Diff(
				got,
				want,
				cmp.AllowUnexported(LocalAssetServer{}),
				cmpopts.IgnoreFields(LocalAssetServer{}, "cache", "logger", "metrics"),
			)
			if diff != "" {
				t.Fatalf("NewLocalAssetServer() mismatch (-want, +got):\n%s", diff)
			}
			if got.metrics != test.metrics || got.cache.metrics != test.metrics {
				t.Fatalf("got metrics=%v, want %v", got.metrics, test.metrics)
			}
		})
	}
}

func TestLocalAssetServer_cacheAndFetch(t *testing.T) {
	tests := []struct {
		name           string
		fileName       string
		fileData       []byte
		assetMaxSize   int64
		cacheMaxSize   int64
		preCacheName   string
		preCacheData   []byte
		expectedCached bool
		expectedErr    error
	}{
		{
			"cacheable asset is cached and fetched",
			"abcd.jpg",
			[]byte("hello"),
			8,
			16,
			"",
			nil,
			true,
			nil,
		},
		{
			"asset too large to cache is still fetched",
			"abcd.jpg",
			[]byte("hello"),
			4,
			16,
			"",
			nil,
			false,
			nil,
		},
		{
			"cache full is still fetched",
			"abcd.jpg",
			[]byte("data"),
			8,
			8,
			"resident.txt",
			[]byte("12345678"),
			false,
			nil,
		},
		{
			"fetch error is propagated",
			"doesnotexist.png",
			nil,
			8,
			16,
			"",
			nil,
			false,
			os.ErrNotExist,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			testDirPath := t.TempDir()

			if test.fileData != nil {
				err := os.WriteFile(
					filepath.Join(testDirPath, test.fileName),
					test.fileData,
					0o777,
				)
				if err != nil {
					t.Fatal(err)
				}
			}

			metrics := metricsServer.NewMetrics(prometheus.NewRegistry())
			server, err := NewLocalAssetServer(
				localAssetServerConfig{
					testDirPath,
					test.assetMaxSize,
					test.cacheMaxSize,
					time.Hour,
					1,
					time.Second,
				},
				nil,
				metrics,
			)
			if err != nil {
				t.Fatal(err)
			}

			if test.preCacheName != "" {
				err = os.WriteFile(
					filepath.Join(testDirPath, test.preCacheName),
					test.preCacheData,
					0o777,
				)
				if err != nil {
					t.Fatal(err)
				}

				if err = server.cache.Cache(test.preCacheName); err != nil {
					t.Fatal(err)
				}
			}

			got, err := server.cacheAndFetch(test.fileName)
			if !errors.Is(err, test.expectedErr) {
				t.Fatalf("got err=%v, want %v", err, test.expectedErr)
			}

			if test.expectedErr != nil {
				return
			}

			if got == nil {
				t.Fatal("got nil asset")
			}

			if got.Path != filepath.Join(testDirPath, test.fileName) {
				t.Errorf(
					"got.Path=%v, want %v",
					got.Path,
					filepath.Join(testDirPath, test.fileName),
				)
			}

			if got.IsCached() != test.expectedCached {
				t.Errorf(
					"got.IsCached()=%v, want %v",
					got.IsCached(),
					test.expectedCached,
				)
			}
		})
	}
}

func TestLocalAssetServer_writeAssetToClient(t *testing.T) {
	errWrite := errors.New("write error")

	tests := []struct {
		name          string
		fileData      []byte
		cacheAsset    bool
		cancelRequest bool
		writeErr      error
		expectedErr   error
	}{
		{
			"uncached asset written",
			[]byte{0x01},
			false,
			false,
			nil,
			nil,
		},
		{
			"cached asset written",
			[]byte{0x01},
			true,
			false,
			nil,
			nil,
		},
		{
			"uncached file larger than write buffer written",
			bytes.Repeat([]byte{0x01}, 200),
			false,
			false,
			nil,
			nil,
		},

		{
			"cached file larger than write buffer written",
			bytes.Repeat([]byte{0x01}, 200),
			true,
			false,
			nil,
			nil,
		},
		{
			"cancelled request propagated",
			[]byte{0x01},
			false,
			true,
			nil,
			context.Canceled,
		},
		{
			"write error propagated",
			[]byte{0x01},
			false,
			false,
			errWrite,
			errWrite,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			testDirPath := t.TempDir()
			filePath := filepath.Join(testDirPath, "abcd.jpg")

			if err := os.WriteFile(filePath, test.fileData, 0o777); err != nil {
				t.Fatal(err)
			}

			asset, err := NewLocalAsset(filePath)
			if err != nil {
				t.Fatal(err)
			}

			if test.cacheAsset {
				asset.setCached(test.fileData, time.Now().Add(time.Hour))
			}
			assetReader, err := asset.Open()
			if err != nil {
				t.Fatal(err)
			}
			defer assetReader.Close()

			metrics := metricsServer.NewMetrics(prometheus.NewRegistry())
			server := &LocalAssetServer{
				writeBufSize: 3,
				writeWindow:  time.Second,
				logger:       slog.New(slog.DiscardHandler),
				metrics:      metrics,
			}

			req := httptest.NewRequest(
				http.MethodGet,
				"/abcd.jpg",
				nil,
			)

			if test.cancelRequest {
				ctx, cancel := context.WithCancel(req.Context())
				cancel()
				req = req.WithContext(ctx)
			}

			var recorder *httptest.ResponseRecorder
			var writer http.ResponseWriter

			if test.writeErr != nil {
				writer = &errorResponseWriter{
					header: make(http.Header),
					err:    test.writeErr,
				}
			} else {
				recorder = httptest.NewRecorder()
				writer = recorder
			}

			err = server.writeAssetToClient(assetReader, writer, req)
			if !errors.Is(err, test.expectedErr) {
				t.Fatalf("got err=%v, want %v", err, test.expectedErr)
			}

			if test.expectedErr != nil {
				return
			}

			if got := recorder.Body.Bytes(); !cmp.Equal(got, test.fileData) {
				t.Errorf(
					"written data mismatch (-want, +got):\n%s",
					cmp.Diff(test.fileData, got),
				)
			}
		})
	}
}

func TestLocalAssetServer_handleCachAndFetchErr(t *testing.T) {
	unknownErr := errors.New("unknown error")
	tests := []struct {
		name           string
		err            error
		expectedStatus int
	}{
		{
			"unknown error propagates status 500",
			unknownErr,
			http.StatusInternalServerError,
		},
		{
			"invalid asset name propagates status 404",
			ErrInvalidAssetName,
			http.StatusNotFound,
		},
		{
			"not a file propagates status 404",
			ErrNotAFile,
			http.StatusNotFound,
		},
		{
			"file not exist propagates status 404",
			os.ErrNotExist,
			http.StatusNotFound,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			metrics := metricsServer.NewMetrics(prometheus.NewRegistry())
			assetServer := &LocalAssetServer{
				logger:  slog.New(slog.DiscardHandler),
				metrics: metrics,
			}
			writer := &errorResponseWriter{
				header: make(http.Header),
			}
			request := &http.Request{
				URL: &url.URL{},
			}
			assetID := "abcd.jpg"
			sw := &metricsServer.StatusWriter{ResponseWriter: writer}
			assetServer.handleCacheAndFetchErr(
				test.err,
				assetID,
				sw,
				request,
			)
			if writer.statusCode != test.expectedStatus {
				t.Fatalf(
					"got writer.statusCode=%v, want %v",
					writer.statusCode,
					test.expectedStatus,
				)
			}
		})
	}
}
