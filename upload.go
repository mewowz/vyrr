package main

import (
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	metricsServer "github.com/mewowz/vyrr/internal/metrics"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/gabriel-vasile/mimetype"
)

var ErrInvalidAssetSizeLimit = errors.New("maxAssetSize cannot be negative")

type LocalAssetUploader struct {
	tmpDirPath    string
	outputDirPath string

	urlPrefix string

	maxAssetUploadSize int64

	logger *slog.Logger

	metrics *metricsServer.Metrics
}

type localAssetUploaderConfig struct {
	TmpDirPath         string `koanf:"temp-dir"`
	OutputDirPath      string `koanf:"output-dir"`
	URLPrefix          string `koanf:"url-prefix"`
	MaxAssetUploadSize int64  `koanf:"max-upload-size"`
}

type assetMeta struct {
	Hash     hash.Hash
	MimeInfo *mimetype.MIME

	Path string
}

func NewLocalAssetUploader(
	cfg localAssetUploaderConfig,
	logger *slog.Logger,
	metrics *metricsServer.Metrics,
) (*LocalAssetUploader, error) {
	if logger == nil {
		logger = slog.Default()
	}
	if err := checkDirRW(cfg.TmpDirPath); err != nil {
		return nil, fmt.Errorf("check RW on %q: %w", cfg.TmpDirPath, err)
	}
	if err := checkDirRW(cfg.OutputDirPath); err != nil {
		return nil, fmt.Errorf("check RW on %q: %w", cfg.OutputDirPath, err)
	}

	// maxAssetSize = 0 essentially disables uploading
	if cfg.MaxAssetUploadSize < 0 {
		return nil, ErrInvalidAssetSizeLimit
	}

	if !strings.HasPrefix(cfg.URLPrefix, "/") {
		return nil, fmt.Errorf("URL prefix %q must start with /", cfg.URLPrefix)
	}
	if strings.ContainsAny(cfg.URLPrefix, "?#") {
		return nil, fmt.Errorf(
			"URL prefix %q must not contain any query or fragment",
			cfg.URLPrefix,
		)
	}

	if metrics == nil {
		return nil, metricsServer.ErrNilMetrics
	}

	return &LocalAssetUploader{
		tmpDirPath:         cfg.TmpDirPath,
		outputDirPath:      cfg.OutputDirPath,
		urlPrefix:          cfg.URLPrefix,
		maxAssetUploadSize: cfg.MaxAssetUploadSize,
		logger:             logger,
		metrics:            metrics,
	}, nil
}

func (u *LocalAssetUploader) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	sw := &metricsServer.StatusWriter{ResponseWriter: w}

	defer func() {
		u.metrics.TotalRequests.With(
			prometheus.Labels{"method": r.Method, "status": sw.StatusString()},
		).Inc()
	}()

	timeStart := time.Now()
	defer func() {
		timeEnd := time.Now()
		dt := timeEnd.Sub(timeStart).Seconds()
		u.metrics.RequestsDuration.With(
			prometheus.Labels{"method": r.Method, "status": sw.StatusString()},
		).Observe(dt)
	}()

	if r.Method != "POST" {
		w.Header().Set("Allow", "POST")
		http.Error(sw, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	outFilePath, err := u.handleAssetUpload(sw, r)
	if err != nil {
		u.handleAssetUploadErr(err, sw, r)
		return
	}

	sw.Header().Set("Content-Type", "application/json")
	sw.WriteHeader(http.StatusCreated)

	err = json.NewEncoder(w).Encode(struct {
		AssetPath string `json:"asset_path"`
	}{
		AssetPath: path.Join(
			u.urlPrefix, path.Base(outFilePath),
		),
	})
	if err != nil {
		u.logger.Error(
			"failed to write upload response",
			"err", err,
			"remote", r.RemoteAddr,
			"assetpath", outFilePath,
		)
		return
	}
}

func (u *LocalAssetUploader) handleAssetUploadErr(
	err error,
	w http.ResponseWriter,
	r *http.Request,
) {
	writeErrorLogger := u.logger.With(
		"err", err.Error(),
		"method", r.Method,
		"remote", r.RemoteAddr,
		"url", r.URL.String(),
	)
	var maxErr *http.MaxBytesError
	switch {
	case errors.As(err, &maxErr):
		http.Error(w, "file too large", http.StatusRequestEntityTooLarge)
	default:
		http.Error(w, "server error", http.StatusInternalServerError)
		writeErrorLogger.Error("failed to upload asset")
	}
}

func (u *LocalAssetUploader) handleAssetUpload(
	w http.ResponseWriter,
	r *http.Request,
) (string, error) {
	assetUploadReader := http.MaxBytesReader(w, r.Body, u.maxAssetUploadSize)
	defer assetUploadReader.Close()

	meta, err := u.writeAssetUploadToDisk(assetUploadReader)
	if err != nil {
		return "", err
	}

	outputPath, err := u.moveAssetToOutputDir(meta)
	if err != nil {
		os.Remove(meta.Path)
		return "", err
	}

	return outputPath, nil
}

func (u *LocalAssetUploader) writeAssetUploadToDisk(
	r io.ReadCloser,
) (assetMeta, error) {
	var written int64
	defer func() {
		u.metrics.BytesTransferred.With(
			prometheus.Labels{"direction": "ingress"},
		).Add(float64(written))
	}()

	var err error
	outFile, err := os.CreateTemp(
		u.tmpDirPath,
		"ul-*",
	)
	if err != nil {
		return assetMeta{}, err
	}
	outFilePath := outFile.Name()

	defer func() {
		if err != nil {
			os.Remove(outFilePath)
		}
	}()

	h := sha512.New()
	written, err = io.Copy(
		io.MultiWriter(outFile, h),
		r,
	)
	if err != nil {
		_ = outFile.Close()
		return assetMeta{}, fmt.Errorf("copy upload to tempfile: %w", err)
	}

	if err = outFile.Close(); err != nil {
		return assetMeta{}, err
	}

	mime, err := mimetype.DetectFile(outFilePath)
	if err != nil {
		return assetMeta{}, fmt.Errorf("detect uploaded file MIME type: %w", err)
	}

	meta := assetMeta{
		Hash:     h,
		MimeInfo: mime,
		Path:     outFilePath,
	}

	return meta, nil
}

func (u *LocalAssetUploader) moveAssetToOutputDir(
	m assetMeta,
) (string, error) {
	baseName := hex.EncodeToString(m.Hash.Sum(nil))
	outputName := baseName + m.MimeInfo.Extension()
	outputPath := filepath.Join(
		u.outputDirPath,
		outputName,
	)

	err := os.Rename(m.Path, outputPath)
	if err != nil {
		return "", err
	}

	return outputPath, nil
}
