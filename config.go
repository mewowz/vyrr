package main

import (
	"errors"
	"fmt"
	"os"
	"reflect"

	"github.com/mewowz/mourncdn/internal/metrics"

	"github.com/dustin/go-humanize"
	"github.com/go-viper/mapstructure/v2"
	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/confmap"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/v2"
)

const DefaultConfigPath = "./config.yml"

type Config struct {
	ServeCfg   localAssetServerConfig
	UploadCfg  localAssetUploaderConfig
	HTTPCfg    CDNServerConfig
	MetricsCfg metrics.MetricsServerConfig
}

func defaultKoanfConfig() (*koanf.Koanf, error) {
	defaults := map[string]any{
		"cdn.serve-route":                  "/assets",
		"cdn.upload-route":                 "/upload",
		"cdn.address":                      "0.0.0.0:9743",
		"cdn.advanced.read-timeout":        "0s",
		"cdn.advanced.read-header-timeout": "3s",
		"cdn.advanced.write-timeout":       "0s",
		"cdn.advanced.idle-timeout":        "60s",

		"serve.asset-dir":                  "./data/assets",
		"serve.max-cacheable-size":         "100MiB",
		"serve.cache-size":                 "1GiB",
		"serve.asset-ttl":                  "60s",
		"serve.advanced.write-buffer-size": 4096,
		"serve.advanced.write-window":      "10s",

		"upload.temp-dir":        "./data/tmp",
		"upload.output-dir":      "./data/assets",
		"upload.url-prefix":      "/assets",
		"upload.max-upload-size": "2GiB",

		"metrics.address":                   "127.0.0.1:9884",
		"metrics.endpoint":                  "/metrics",
		"metrics.advanced.shutdown-timeout": "3s",
	}

	k := koanf.New(".")
	if err := k.Load(confmap.Provider(defaults, "."), nil); err != nil {
		return nil, err
	}

	return k, nil
}

func LoadConfigFile(path string) (*Config, error) {
	k, err := defaultKoanfConfig()
	if err != nil {
		return nil, fmt.Errorf("default config: %w", err)
	}
	f := file.Provider(path)
	if err := k.Load(f, yaml.Parser()); err != nil {
		if path != DefaultConfigPath || !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("config file %q: %w", path, err)
		}
	}

	dc := mapstructure.DecoderConfig{
		DecodeHook: mapstructure.ComposeDecodeHookFunc(
			mapstructure.StringToTimeDurationHookFunc(),
			byteSizeHook(),
		),
	}
	uc := koanf.UnmarshalConf{
		Tag:           "koanf",
		DecoderConfig: &dc,
		FlatPaths:     true,
	}

	var serveCfg localAssetServerConfig
	if err := k.UnmarshalWithConf("serve", &serveCfg, uc); err != nil {
		return nil, fmt.Errorf("serve config: %w", err)
	}

	var uploadCfg localAssetUploaderConfig
	if err := k.UnmarshalWithConf("upload", &uploadCfg, uc); err != nil {
		return nil, fmt.Errorf("upload config: %w", err)
	}

	var cdnCfg CDNServerConfig
	if err := k.UnmarshalWithConf("cdn", &cdnCfg, uc); err != nil {
		return nil, fmt.Errorf("cdn config: %w", err)
	}

	var metricsCfg metrics.MetricsServerConfig
	if err := k.UnmarshalWithConf("metrics", &metricsCfg, uc); err != nil {
		return nil, fmt.Errorf("metrics config: %w", err)
	}

	return &Config{
		ServeCfg:   serveCfg,
		UploadCfg:  uploadCfg,
		HTTPCfg:    cdnCfg,
		MetricsCfg: metricsCfg,
	}, nil
}

func byteSizeHook() mapstructure.DecodeHookFunc {
	return byteSizeHookFunc
}

func byteSizeHookFunc(
	from reflect.Type,
	to reflect.Type,
	data any,
) (any, error) {
	if from.Kind() != reflect.String {
		return data, nil
	}

	if to.Kind() != reflect.Int && to.Kind() != reflect.Int64 {
		return data, nil
	}

	n, err := humanize.ParseBytes(data.(string))
	if err != nil {
		return nil, err
	}

	if to.Kind() == reflect.Int {
		return int(n), nil
	}

	return int64(n), nil
}
