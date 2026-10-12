package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"time"

	metricsServer "github.com/mewowz/vyrr/internal/metrics"
	"github.com/prometheus/client_golang/prometheus"
)

var (
	ErrInvalidWriteBufSize = errors.New("WriteBufSize must be >= 1")
	ErrInvalidWriteWindow  = errors.New("WriteWindow must be >= 1")
)

type LocalAssetServer struct {
	cache *LocalAssetCache

	writeBufSize int
	writeWindow  time.Duration

	logger  *slog.Logger
	metrics *metricsServer.Metrics
}

type localAssetServerConfig struct {
	// LocalAssetCache
	AssetDir     string        `koanf:"asset-dir"`
	AssetMaxSize int64         `koanf:"max-cacheable-size"`
	CacheMaxSize int64         `koanf:"cache-size"`
	TTL          time.Duration `koanf:"asset-ttl"`

	// LocalAssetServer
	WriteBufSize int           `koanf:"advanced.write-buffer-size"`
	WriteWindow  time.Duration `koanf:"advanced.write-window"`
}

func NewLocalAssetServer(
	cfg localAssetServerConfig,
	logger *slog.Logger,
	metrics *metricsServer.Metrics,
) (*LocalAssetServer, error) {
	cache, err := NewLocalAssetCache(
		cfg.AssetDir,
		cfg.AssetMaxSize,
		cfg.CacheMaxSize,
		cfg.TTL,
		metrics,
	)
	if err != nil {
		return nil, err
	}

	if logger == nil {
		logger = slog.Default()
	}

	if cfg.WriteBufSize <= 0 {
		return nil, ErrInvalidWriteBufSize
	}

	if cfg.WriteWindow <= 0 {
		return nil, ErrInvalidWriteWindow
	}

	if metrics == nil {
		return nil, metricsServer.ErrNilMetrics
	}

	return &LocalAssetServer{
		cache:        cache,
		logger:       logger,
		writeBufSize: cfg.WriteBufSize,
		writeWindow:  cfg.WriteWindow,
		metrics:      metrics,
	}, nil
}

func (s *LocalAssetServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// We're expecting the handler to be configured for routes like
	// /assets/{id...} or /{id...} or /file/{id...}
	// where "id" points to a filename relative to the source dir.
	// For instance, for the route "/assets/{id...}" and AssetDir = ./data/assets/:
	// http://foo.com/assets/abcdef.jpg -> ./data/assets/abcdef.jpg
	sw := &metricsServer.StatusWriter{ResponseWriter: w}

	defer func() {
		s.metrics.TotalRequests.With(
			prometheus.Labels{"method": r.Method, "status": sw.StatusString()},
		).Inc()
	}()

	timeStart := time.Now()
	defer func() {
		timeEnd := time.Now()
		dt := timeEnd.Sub(timeStart).Seconds()
		s.metrics.RequestsDuration.With(
			prometheus.Labels{"method": r.Method, "status": sw.StatusString()},
		).Observe(dt)
	}()

	assetID := r.PathValue("id")

	if r.Method == http.MethodHead {
		asset, err := s.cache.Fetch(assetID)
		if err != nil {
			s.handleCacheAndFetchErr(err, assetID, sw, r)
			return
		}
		assetReader, err := asset.Open()
		if err != nil {
			s.handleCacheAndFetchErr(err, assetID, sw, r)
			return
		}
		defer assetReader.Close()

		var prefix [512]byte
		n, err := io.ReadFull(assetReader, prefix[:])
		if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
			s.handleCacheAndFetchErr(err, assetID, sw, r)
			return
		}

		if n > 0 {
			sw.Header().Set("Content-Type", http.DetectContentType(prefix[:n]))
		}
		sw.Header().Set("Content-Length", strconv.FormatInt(asset.FileInfo.Size(), 10))
		sw.WriteHeader(http.StatusOK)
		return
	}

	asset, err := s.cacheAndFetch(assetID)
	if err != nil {
		s.handleCacheAndFetchErr(err, assetID, sw, r)
		return
	}

	assetReader, err := asset.Open()
	if err != nil {
		s.handleCacheAndFetchErr(err, assetID, sw, r)
		return
	}
	defer assetReader.Close()

	sw.Header().Set("Content-Length", strconv.FormatInt(asset.FileInfo.Size(), 10))
	err = s.writeAssetToClient(assetReader, sw, r)
	if err != nil {
		s.handleWriteAssetToClientError(assetID, r, err)
		return
	}
}

func (s *LocalAssetServer) handleWriteAssetToClientError(
	assetID string,
	r *http.Request,
	err error,
) {
	writeErrorLogger := s.logger.With(
		"err", err.Error(),
		"assetID", assetID,
		"method", r.Method,
		"remote", r.RemoteAddr,
		"url", r.URL.String(),
	)

	var pe *os.PathError
	if errors.As(err, &pe) {
		writeErrorLogger.Error("path error")
		return
	}

	if errors.Is(err, context.DeadlineExceeded) {
		writeErrorLogger.Warn(
			"deadline exceeded",
			"note", "request deadline exceeded",
		)
		return
	}

	if errors.Is(err, context.Canceled) {
		writeErrorLogger.Debug("request context cancelled")
		return
	}

	writeErrorLogger.Error("failed to serve asset")
}

func (s *LocalAssetServer) handleCacheAndFetchErr(
	err error,
	assetID string,
	w http.ResponseWriter,
	r *http.Request,
) {
	writeErrorLogger := s.logger.With(
		"err", err.Error(),
		"assetID", assetID,
		"method", r.Method,
		"remote", r.RemoteAddr,
		"url", r.URL.String(),
	)

	var statusCode int
	var statusString string

	w.Header().Set("Cache-Control", "no-store")
	switch {
	case errors.Is(err, ErrInvalidAssetName):
		statusCode = http.StatusNotFound
		statusString = "not found"
	case errors.Is(err, ErrNotAFile):
		statusCode = http.StatusNotFound
		statusString = "not found"
	case errors.Is(err, os.ErrNotExist):
		statusCode = http.StatusNotFound
		statusString = "not found"
	default:
		statusCode = http.StatusInternalServerError
		statusString = "server error"
		writeErrorLogger.Error("failed to fetch asset")
	}
	http.Error(w, statusString, statusCode)
}

func (s *LocalAssetServer) cacheAndFetch(
	fileName string,
) (*LocalAsset, error) {
	recoverableErrors := []error{
		ErrCacheFull,
		ErrAssetTooLargeToCache,
	}

	err := s.cache.Cache(fileName)
	if err != nil {
		for _, rErr := range recoverableErrors {
			if errors.Is(err, rErr) {
				err = nil
				break
			}
		}
		if err != nil {
			return nil, err
		}
	}

	asset, err := s.cache.Fetch(fileName)
	if err != nil {
		return nil, err
	}

	return asset, nil
}

func (s *LocalAssetServer) writeAssetToClient(
	assetReader io.Reader,
	w http.ResponseWriter,
	r *http.Request,
) error {
	bytesCounter := s.metrics.BytesTransferred.With(
		prometheus.Labels{"direction": "egress"},
	)
	bytesWritten := 0
	defer func() { bytesCounter.Add(float64(bytesWritten)) }()

	rc := http.NewResponseController(w)

	deadlineUnsupportedLogged := false
	buf := make([]byte, s.writeBufSize)
	for {
		select {
		case <-r.Context().Done():
			return r.Context().Err()
		default:
		}
		n, err := assetReader.Read(buf)

		deadlineErr := rc.SetWriteDeadline(time.Now().Add(s.writeWindow))
		if errors.Is(deadlineErr, http.ErrNotSupported) && deadlineUnsupportedLogged == false {
			deadlineUnsupportedLogged = true
			s.logger.Debug("deadline", "err", deadlineErr)
		}
		if n > 0 {
			n, writeErr := w.Write(buf[:n])
			if writeErr != nil {
				return writeErr
			}
			bytesWritten += n
		}

		if errors.Is(err, io.EOF) {
			return nil
		}

		if err != nil {
			return err
		}

	}
}
