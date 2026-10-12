package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/mewowz/mourncdn/internal/metrics"
)

func TestLoadConfigFile(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yml")

	configData := []byte(`
cdn:
  serve-route: /assets
  upload-route: /upload
  address: localhost:9743
  advanced:
    read-timeout: 15s
    read-header-timeout: 3s
    write-timeout: 15s
    idle-timeout: 60s

serve:
  asset-dir: ./data/assets
  max-cacheable-size: 100MiB
  cache-size: 1GiB
  asset-ttl: 60s
  advanced:
    write-buffer-size: 4096
    write-window: 10s

upload:
  temp-dir: ./data/tmp
  output-dir: ./data/assets
  url-prefix: /assets
  max-upload-size: 2GiB

metrics:
  address: localhost:9885
  endpoint: /custom-metrics
  advanced:
    shutdown-timeout: 750ms
`)

	if err := os.WriteFile(configPath, configData, 0o777); err != nil {
		t.Fatal(err)
	}

	got, err := LoadConfigFile(configPath)
	if err != nil {
		t.Fatalf("LoadConfigFile() error = %v", err)
	}

	want := &Config{
		ServeCfg: localAssetServerConfig{
			AssetDir:     "./data/assets",
			AssetMaxSize: 100 * 1024 * 1024,
			CacheMaxSize: 1024 * 1024 * 1024,
			TTL:          60 * time.Second,
			WriteBufSize: 4096,
			WriteWindow:  10 * time.Second,
		},
		UploadCfg: localAssetUploaderConfig{
			TmpDirPath:         "./data/tmp",
			OutputDirPath:      "./data/assets",
			URLPrefix:          "/assets",
			MaxAssetUploadSize: 2 * 1024 * 1024 * 1024,
		},
		HTTPCfg: CDNServerConfig{
			ServeRoute:        "/assets",
			UploadRoute:       "/upload",
			Address:           "localhost:9743",
			ReadTimeout:       15 * time.Second,
			ReadHeaderTimeout: 3 * time.Second,
			WriteTimeout:      15 * time.Second,
			IdleTimeout:       60 * time.Second,
		},
		MetricsCfg: metrics.MetricsServerConfig{
			Addr:                   "localhost:9885",
			Route:                  "/custom-metrics",
			ShutdownTimeoutSeconds: 750 * time.Millisecond,
		},
	}

	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("LoadConfigFile() mismatch (-want +got):\n%s", diff)
	}
}

func TestLoadConfigFileExampleDefaults(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yml")
	if err := os.WriteFile(configPath, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	want, err := LoadConfigFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	got, err := LoadConfigFile("config_test.yml")
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("example config differs from defaults (-want +got):\n%s", diff)
	}
}

func TestLoadConfigFileMetrics(t *testing.T) {
	tests := []struct {
		name       string
		configData string
		want       metrics.MetricsServerConfig
		wantErr    string
	}{
		{
			name:       "missing metrics section uses defaults",
			configData: "{}\n",
			want: metrics.MetricsServerConfig{
				Addr:                   "127.0.0.1:9884",
				Route:                  "/metrics",
				ShutdownTimeoutSeconds: 3 * time.Second,
			},
		},
		{
			name:       "partial metrics config retains remaining defaults",
			configData: "metrics:\n  endpoint: /custom-metrics\n",
			want: metrics.MetricsServerConfig{
				Addr:                   "127.0.0.1:9884",
				Route:                  "/custom-metrics",
				ShutdownTimeoutSeconds: 3 * time.Second,
			},
		},
		{
			name:       "explicit zero timeout overrides default",
			configData: "metrics:\n  advanced:\n    shutdown-timeout: 0s\n",
			want: metrics.MetricsServerConfig{
				Addr:  "127.0.0.1:9884",
				Route: "/metrics",
			},
		},
		{
			name:       "invalid metrics timeout returns metrics config error",
			configData: "metrics:\n  advanced:\n    shutdown-timeout: invalid\n",
			wantErr:    "metrics config:",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			configPath := filepath.Join(t.TempDir(), "config.yml")
			if err := os.WriteFile(configPath, []byte(test.configData), 0o600); err != nil {
				t.Fatal(err)
			}
			got, err := LoadConfigFile(configPath)
			if test.wantErr != "" {
				if err == nil || !strings.HasPrefix(err.Error(), test.wantErr) {
					t.Fatalf("got err=%v, want prefix %q", err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if diff := cmp.Diff(test.want, got.MetricsCfg); diff != "" {
				t.Errorf("metrics config mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestByteSizeHook(t *testing.T) {
	tests := []struct {
		name         string
		from         reflect.Type
		to           reflect.Type
		data         any
		expectedType reflect.Kind
		expectedVal  any
	}{
		{
			name:         "string to int64",
			from:         reflect.TypeOf(""),
			to:           reflect.TypeOf(int64(0)),
			data:         "0",
			expectedType: reflect.Int64,
			expectedVal:  int64(0),
		},
		{
			name:         "string to int",
			from:         reflect.TypeOf(""),
			to:           reflect.TypeOf(int(3)),
			expectedType: reflect.Int,
			data:         "3",
			expectedVal:  int(3),
		},
		{
			name:         "float64 to int64",
			from:         reflect.TypeOf(float64(3.14159)),
			to:           reflect.TypeOf(int64(0)),
			data:         float64(3.14159),
			expectedType: reflect.Float64,
			expectedVal:  float64(3.14159),
		},
		{
			name:         "string to string",
			from:         reflect.TypeOf(""),
			to:           reflect.TypeOf(""),
			data:         "44",
			expectedType: reflect.String,
			expectedVal:  "44",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			v, err := byteSizeHookFunc(
				test.from,
				test.to,
				test.data,
			)
			if err != nil {
				t.Fatalf("error = %v, want nil", err)
			}
			if kind := reflect.TypeOf(v).Kind(); kind != test.expectedType {
				t.Fatalf(
					"got TypeOf(v).Kind() = %v, want %v",
					kind,
					test.expectedType,
				)
			}

			if v != test.expectedVal {
				t.Fatalf("got v=%v, want %v", v, test.expectedVal)
			}
		})
	}
}
