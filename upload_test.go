package main

import (
	"bytes"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	metricsServer "github.com/mewowz/vyrr/internal/metrics"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/gabriel-vasile/mimetype"
)

func TestNewLocalAssetUploader(t *testing.T) {
	tempDirPath := t.TempDir()
	metrics := metricsServer.NewMetrics(prometheus.NewRegistry())

	tests := []struct {
		name                string
		urlPrefix           string
		maxUploadSize       int64
		metrics             *metricsServer.Metrics
		expectedErr         error
		expectedErrFragment string
	}{
		{
			"no errors",
			"/assets",
			1,
			metrics,
			nil,
			"",
		},
		{
			"negative asset size limit",
			"/assets",
			-1,
			metrics,
			ErrInvalidAssetSizeLimit,
			"",
		},
		{
			"URL prefix not starting with /",
			".assets/",
			1,
			metrics,
			nil,
			"must start with /",
		},
		{
			"URL prefix containing query",
			"/assets?x=1",
			1,
			metrics,
			nil,
			"query or fragment",
		},
		{
			"URL prefix containing fragment",
			"/assets#x=10",
			1,
			metrics,
			nil,
			"query or fragment",
		},
		{
			"metrics pointer is nil",
			"/assets",
			1,
			nil,
			metricsServer.ErrNilMetrics,
			"",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewLocalAssetUploader(
				localAssetUploaderConfig{
					tempDirPath,
					tempDirPath,
					test.urlPrefix,
					test.maxUploadSize,
				},
				slog.New(slog.DiscardHandler),
				test.metrics,
			)
			if test.expectedErr != nil && !errors.Is(err, test.expectedErr) {
				t.Fatalf("got %v, want %v", err, test.expectedErr)
			}
			if test.expectedErrFragment != "" &&
				!strings.Contains(err.Error(), test.expectedErrFragment) {
				t.Fatalf(
					"did not find error string fragment '%q' in '%v'",
					test.expectedErrFragment,
					err,
				)
			}
		})
	}
}

func TestLocalAssetUploader_handleAssetUploadErr(t *testing.T) {
	tests := []struct {
		name           string
		err            error
		expectedStatus int
	}{
		{
			"max bytes propagates status 413",
			&http.MaxBytesError{},
			http.StatusRequestEntityTooLarge,
		},
		{
			"unknown error propagates status 500",
			errors.New("unknown error"),
			http.StatusInternalServerError,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			u := &LocalAssetUploader{
				logger: slog.New(slog.DiscardHandler),
			}
			writer := &errorResponseWriter{
				header: make(http.Header),
			}
			request := &http.Request{
				URL: &url.URL{},
			}
			u.handleAssetUploadErr(
				test.err,
				writer,
				request,
			)

			if writer.statusCode != test.expectedStatus {
				t.Fatalf(
					"got writer.statusCode=%d, want %d",
					writer.statusCode,
					test.expectedStatus,
				)
			}
		})
	}
}

type errorReader struct {
	readErr  error
	closeErr error
}

func (e *errorReader) Read(p []byte) (int, error) {
	return 0, e.readErr
}

func (e *errorReader) Close() error {
	return e.closeErr
}

func TestLocalAssetUploader_writeAssetToDisk(t *testing.T) {
	tempDirPath := t.TempDir()
	metrics := metricsServer.NewMetrics(prometheus.NewRegistry())

	pngSig := []byte{
		0x89, 'P', 'N', 'G',
		0x0D, 0x0A, 0x1A, 0x0A,
	}

	errRead := errors.New("read error")

	tests := []struct {
		name            string
		fileData        []byte
		expectedHashSum [sha512.Size]byte
		expectedMIME    string
		readErr         error
		expectedErr     error
	}{
		{
			"no errors; good data",
			pngSig,
			sha512.Sum512(pngSig),
			"image/png",
			nil,
			nil,
		},
		{
			"reader error propagated",
			nil,
			[sha512.Size]byte{},
			"",
			errRead,
			errRead,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			u := &LocalAssetUploader{
				tmpDirPath: tempDirPath,
				metrics:    metrics,
			}

			var reader io.ReadCloser
			if test.readErr != nil {
				reader = &errorReader{
					readErr: test.readErr,
				}
			} else {
				reader = io.NopCloser(bytes.NewReader(test.fileData))
			}

			got, err := u.writeAssetUploadToDisk(reader)
			if !errors.Is(err, test.expectedErr) {
				t.Fatalf("got err=%v, want %v", err, test.expectedErr)
			}

			if test.expectedErr != nil {
				entries, readDirErr := os.ReadDir(tempDirPath)
				if readDirErr != nil {
					t.Fatal(readDirErr)
				}

				if len(entries) != 0 {
					t.Fatalf(
						"got %d files remaining in temp dir, want 0",
						len(entries),
					)
				}

				return
			}

			if got.Path == "" {
				t.Fatal("got empty asset path")
			}

			if _, err := os.Stat(got.Path); err != nil {
				t.Fatalf("stat uploaded asset: %v", err)
			}

			gotHash := got.Hash.Sum(nil)
			if !bytes.Equal(gotHash, test.expectedHashSum[:]) {
				t.Errorf(
					"got hash=%x, want %x",
					gotHash,
					test.expectedHashSum,
				)
			}

			if got.MimeInfo.String() != test.expectedMIME {
				t.Errorf(
					"got MIME=%q, want %q",
					got.MimeInfo.String(),
					test.expectedMIME,
				)
			}

			if got.Path != filepath.Join(tempDirPath, filepath.Base(got.Path)) {
				t.Errorf(
					"got path=%q outside temp dir %q",
					got.Path,
					tempDirPath,
				)
			}

			if err := os.Remove(got.Path); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestLocalAssetUploader_moveAssetToOutputDir(t *testing.T) {
	pngSig := []byte{
		0x89, 'P', 'N', 'G',
		0x0D, 0x0A, 0x1A, 0x0A,
	}

	tests := []struct {
		name            string
		fileData        []byte
		fileDataHashsum [sha512.Size]byte
		extension       string
	}{
		{
			"png data properly hashed and named",
			pngSig,
			sha512.Sum512(pngSig),
			".png",
		},
		{
			"arbitrary bytes have no ext",
			[]byte{0x1, 0x2, 0x3, 0x4},
			sha512.Sum512([]byte{0x1, 0x2, 0x3, 0x4}),
			"",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			tmpDirPath := t.TempDir()
			outputDirPath := t.TempDir()
			uploader := &LocalAssetUploader{
				tmpDirPath:    tmpDirPath,
				outputDirPath: outputDirPath,
			}

			tempFilePath := filepath.Join(tmpDirPath, "testfile")
			f, err := os.Create(tempFilePath)
			if err != nil {
				t.Fatalf("create: %v", err)
			}

			_, err = io.Copy(f, bytes.NewReader(test.fileData))
			f.Close()
			if err != nil {
				t.Fatalf("write: %v", err)
			}

			expectedBaseName := hex.EncodeToString(test.fileDataHashsum[:]) + test.extension
			expectedOutputFilePath := filepath.Join(outputDirPath, expectedBaseName)

			hash := sha512.New()
			_, err = io.Copy(hash, bytes.NewReader(test.fileData))
			if err != nil {
				t.Fatalf("hash: %v", err)
			}

			mime := mimetype.Detect(test.fileData)
			meta := assetMeta{
				Hash:     hash,
				MimeInfo: mime,
				Path:     tempFilePath,
			}

			gotOutputPath, err := uploader.moveAssetToOutputDir(meta)
			if err != nil {
				t.Fatalf("move asset: %v", err)
			}

			if gotOutputPath != expectedOutputFilePath {
				t.Errorf(
					"got outputPath=%q, want %q",
					gotOutputPath,
					expectedOutputFilePath,
				)
			}
		})
	}
}

func TestLocalAssetUploader_ServeHTTP(t *testing.T) {
	urlPrefix := "/assets/"
	target := "localhost:8983/upload"
	metrics := metricsServer.NewMetrics(prometheus.NewRegistry())

	type statusCreatedResponse struct {
		AssetPath string `json:"asset_path"`
	}

	maxUploadSize := 10

	tests := []struct {
		name               string
		method             string
		body               []byte
		expectedStatusCode int
		expectedHeaders    map[string]string
	}{
		{
			"upload succeeds normally",
			"POST",
			[]byte{0x0, 0x01},
			http.StatusCreated,
			map[string]string{"Content-Type": "application/json"},
		},
		{
			"non-POST method returns 405",
			"GET",
			[]byte{},
			http.StatusMethodNotAllowed,
			make(map[string]string),
		},
		{
			"oversized upload returns 413",
			"POST",
			bytes.Repeat([]byte{0x0}, maxUploadSize+1),
			http.StatusRequestEntityTooLarge,
			make(map[string]string),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			outputDir := t.TempDir()

			uploader, err := NewLocalAssetUploader(
				localAssetUploaderConfig{
					tmpDir,
					outputDir,
					urlPrefix,
					int64(maxUploadSize),
				},
				slog.New(slog.DiscardHandler),
				metrics,
			)
			if err != nil {
				t.Fatalf("LocalAssetUploader: %v", err)
			}

			req := httptest.NewRequest(
				test.method,
				target,
				bytes.NewReader(test.body),
			)
			w := httptest.NewRecorder()
			uploader.ServeHTTP(w, req)

			resp := w.Result()

			if resp.StatusCode != test.expectedStatusCode {
				t.Fatalf(
					"got respStatusCode=%d, want %d",
					resp.StatusCode,
					test.expectedStatusCode,
				)
			}
			if test.method != "POST" {
				allowStr := resp.Header.Get("Allow")
				if allowStr != "POST" {
					t.Fatalf("want \"Allow: POST\", got %v", allowStr)
				}
				return // end test early
			}

			if val, ok := test.expectedHeaders["Content-Type"]; ok {
				got := resp.Header.Get("Content-Type")
				if got != val {
					t.Fatalf("want %v, got %v", val, got)
				}

				dec := json.NewDecoder(w.Body)
				dec.DisallowUnknownFields()
				var jsonResp statusCreatedResponse
				err = dec.Decode(&jsonResp)
				if err != nil {
					t.Fatalf("unmarshal: %v", err)
				}

				baseName, hasPrefix := strings.CutPrefix(
					jsonResp.AssetPath,
					urlPrefix,
				)
				if !hasPrefix {
					t.Fatalf("want prefix %q, got %q", urlPrefix, jsonResp.AssetPath)
				}
				if baseName == "" {
					t.Fatalf("want string after urlPrefix, got %q", jsonResp.AssetPath)
				}
			}
		})
	}
}
