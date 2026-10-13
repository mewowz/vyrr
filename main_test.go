package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

func TestRun(t *testing.T) {
	t.Run("test alternative config loads properly", func(t *testing.T) {
		configPath := filepath.Join(t.TempDir(), "config.yml")
		err := os.WriteFile(
			configPath,
			[]byte(
				fmt.Sprintf("cdn:\n  address: 127.0.0.1:0\nmetrics:\n  address: 127.0.0.1:0\nserve:\n  asset-dir: %q", t.TempDir()),
			),
			0o600,
		)
		if err != nil {
			t.Fatalf("could not write test config, file=%v, err=%v", configPath, err)
		}

		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		err = run(ctx, configPath, slog.New(slog.DiscardHandler))
		if err != nil {
			t.Fatalf("want err=nil, got err=%v", err)
		}
	})

	t.Run("invalid config path fails", func(t *testing.T) {
		configPath := filepath.Join(t.TempDir(), "config.yml")
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		err := run(ctx, configPath, slog.New(slog.DiscardHandler))
		if !errors.Is(err, os.ErrNotExist) {
			t.Errorf("want err=%v, got err=%v", os.ErrNotExist, err)
		}
	})
}
