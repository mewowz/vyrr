package main

import (
	"bytes"
	"errors"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	metricsServer "github.com/mewowz/vyrr/internal/metrics"
	"github.com/prometheus/client_golang/prometheus"
)

func TestNewLocalAssetCache(t *testing.T) {
	testDirPath := t.TempDir()

	testFilePath := filepath.Join(testDirPath, "file.jpg")
	err := os.WriteFile(
		testFilePath,
		[]byte{},
		0o777,
	)
	if err != nil {
		t.Fatalf("could not make temp file %q: %v", testFilePath, err)
	}

	tests := []struct {
		name         string
		assetDir     string
		assetMaxSize int64
		cacheMaxSize int64
		ttl          time.Duration
		metrics      *metricsServer.Metrics
		expectedErr  error
	}{
		{
			"no failure",
			testDirPath,
			1,
			2,
			time.Second * 1,
			metricsServer.NewMetrics(prometheus.NewRegistry()),
			nil,
		},
		{
			name:        "assetDir is not a dir",
			assetDir:    testFilePath,
			expectedErr: ErrNotADir,
		},
		{
			name:         "assetMaxSize is negative",
			assetDir:     testDirPath,
			assetMaxSize: -1,
			expectedErr:  ErrInvalidCacheLimit,
		},
		{
			name:         "cacheMaxSize is negative",
			assetDir:     testDirPath,
			assetMaxSize: 1,
			cacheMaxSize: -1,
			expectedErr:  ErrInvalidCacheLimit,
		},
		{
			name:         "assetMaxSize > cacheMaxSize",
			assetDir:     testDirPath,
			assetMaxSize: 2,
			cacheMaxSize: 1,
			expectedErr:  ErrAssetLimitExceedsCacheLimit,
		},
		{
			name:         "ttl is negative",
			assetDir:     testDirPath,
			assetMaxSize: 1,
			cacheMaxSize: 2,
			ttl:          time.Second * -1,
			expectedErr:  ErrInvalidTTL,
		},
		{
			name:         "metrics is nil",
			assetDir:     testDirPath,
			assetMaxSize: 1,
			cacheMaxSize: 2,
			ttl:          time.Second * 1,
			metrics:      nil,
			expectedErr:  metricsServer.ErrNilMetrics,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewLocalAssetCache(
				test.assetDir,
				test.assetMaxSize,
				test.cacheMaxSize,
				test.ttl,
				test.metrics,
			)

			if !errors.Is(err, test.expectedErr) {
				t.Errorf("got %v, want %v", err, test.expectedErr)
			}
		})
	}
}

func TestNewLocalAsset(t *testing.T) {
	testDirPath := t.TempDir()

	testFilePath := filepath.Join(testDirPath, "file.jpg")
	err := os.WriteFile(
		testFilePath,
		[]byte{},
		0o777,
	)
	if err != nil {
		t.Fatalf("could not make temp file %q: %v", testFilePath, err)
	}

	tests := []struct {
		name        string
		path        string
		expectedErr error
	}{
		{
			name:        "no error",
			path:        testFilePath,
			expectedErr: nil,
		},
		{
			"path is not a file",
			testDirPath,
			ErrNotAFile,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewLocalAsset(test.path)
			if !errors.Is(err, test.expectedErr) {
				t.Errorf("got %v, want %v", err, test.expectedErr)
			}
		})
	}
}

func TestLocalAsset_Open(t *testing.T) {
	testDirPath := t.TempDir()
	seed := int64(123)
	source := rand.NewSource(seed)
	r := rand.New(source)

	testFileData := make([]byte, 128)
	n, err := r.Read(testFileData)
	if err != nil {
		t.Fatalf("rand: %v", err)
	}
	if n != len(testFileData) {
		t.Fatalf("read bytes n=%v != len(testFileBytes)=%v", n, len(testFileData))
	}
	testFilePath := filepath.Join(testDirPath, "file.jpg")
	err = os.WriteFile(
		testFilePath,
		testFileData,
		0o777,
	)
	if err != nil {
		t.Fatalf("could not make temp file %q: %v", testFilePath, err)
	}

	tests := []struct {
		name         string
		localAsset   *LocalAsset
		expectedData []byte
		expectedSize int64
		expectedErr  error
	}{
		{
			name: "returns cached data",
			localAsset: &LocalAsset{
				cachedBytes: []byte{0x01},
			},
			expectedData: []byte{0x01},
			expectedSize: 1,
			expectedErr:  nil,
		},
		{
			name: "returns on-disk data",
			localAsset: &LocalAsset{
				Path: testFilePath,
			},
			expectedData: testFileData,
			expectedSize: int64(len(testFileData)),
			expectedErr:  nil,
		},
		{
			name: "error on non-existant file",
			localAsset: &LocalAsset{
				Path: filepath.Join(testDirPath, "nonexistant"),
			},
			expectedData: nil,
			expectedSize: 0,
			expectedErr:  os.ErrNotExist,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			r, size, err := test.localAsset.Open()
			if !errors.Is(err, test.expectedErr) {
				t.Fatalf("got %v, want %v", err, test.expectedErr)
			}
			if test.expectedErr != nil {
				return
			}
			defer r.Close()

			data, err := io.ReadAll(r)
			if err != nil {
				t.Fatalf("cannot read from reader: %v", err)
			}
			if !bytes.Equal(data, test.expectedData) {
				t.Fatalf("data=%v != expectedData=%v", data, test.expectedData)
			}
			if size != test.expectedSize {
				t.Fatalf("size=%v != expectedSize=%v", size, test.expectedSize)
			}
		})
	}
}

func TestLocalAsset_uncache(t *testing.T) {
	asset := &LocalAsset{
		cachedBytes: []byte{0x01, 0x02},
		expiresAt:   time.Now().Add(time.Second),
	}

	asset.uncache()

	t.Run("cachedBytes cleared", func(t *testing.T) {
		if asset.cachedBytes != nil {
			t.Fatalf("cachedBytes=%v, want cachedBytes=nil", asset.cachedBytes)
		}
	})

	t.Run("expiresAt cleared", func(t *testing.T) {
		zeroTime := time.Time{}
		if asset.expiresAt.Compare(zeroTime) != 0 {
			t.Fatalf(
				"asset.expiresAt=%v, want asset.expiresAt=%v",
				asset.expiresAt,
				zeroTime,
			)
		}
	})

	t.Run("IsCached() == false", func(t *testing.T) {
		if cached := asset.IsCached(); cached != false {
			t.Fatalf("asset.IsCached()=%v, want asset.IsCached()=false", cached)
		}
	})
}

func TestLocalAsset_OpenConcurrent(t *testing.T) {
	var wg sync.WaitGroup

	testDirPath := t.TempDir()

	testFileData := []byte{0x00, 0x10, 0x20}
	testFilePath := filepath.Join(testDirPath, "file.jpg")
	err := os.WriteFile(
		testFilePath,
		testFileData,
		0o777,
	)
	if err != nil {
		t.Fatalf("could not make temp file %q: %v", testFilePath, err)
	}

	asset, err := NewLocalAsset(testFilePath)
	if err != nil {
		t.Fatalf("could not make LocalAsset %v: %v", testFilePath, err)
	}

	nFlips := 1234
	cacheFlipper := func() {
		for range nFlips {
			ttl := time.Now().Add(time.Second)
			asset.setCached(testFileData, ttl)
			asset.uncache()
		}
	}

	nReaders := 1234
	cacheReader := func() {
		r, _, err := asset.Open()
		if err != nil {
			t.Errorf("asset Open(): %v", err)
			return
		}
		defer r.Close()

		data, err := io.ReadAll(r)
		if err != nil {
			t.Errorf("io.Read: %v", err)
			return
		}
		if !bytes.Equal(data, testFileData) {
			t.Errorf("data=%v != testFileData=%v", data, testFileData)
			return
		}
	}

	wg.Go(cacheFlipper)
	for range nReaders {
		wg.Go(cacheReader)
	}

	wg.Wait()
}

func TestLocalAssetCache_uncacheAssetLocked(t *testing.T) {
	testDirPath := t.TempDir()

	testFileData := []byte{0x00, 0x10, 0x20}
	testFilePath := filepath.Join(testDirPath, "file.jpg")
	err := os.WriteFile(
		testFilePath,
		testFileData,
		0o777,
	)
	if err != nil {
		t.Fatalf("could not make temp file %q: %v", testFilePath, err)
	}

	tests := []struct {
		name             string
		asset            *LocalAsset
		initialCacheSize int64
		postCacheSize    int64
	}{
		{
			name: "uncache uncached asset",
			asset: &LocalAsset{
				Path: testFilePath,
			},
			initialCacheSize: 0,
			postCacheSize:    0,
		},
		{
			name: "uncache cached asset",
			asset: &LocalAsset{
				Path:        testFilePath,
				cachedBytes: testFileData,
			},
			initialCacheSize: int64(len(testFileData)),
			postCacheSize:    0,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			metrics := metricsServer.NewMetrics(prometheus.NewRegistry())
			assetCache := &LocalAssetCache{
				cacheSize: test.initialCacheSize,
				metrics:   metrics,
			}
			assetCache.uncacheAssetLocked(test.asset)
			if assetCache.cacheSize != test.postCacheSize {
				t.Errorf(
					"cacheSize=%v, want cacheSize=%v",
					assetCache.cacheSize,
					test.postCacheSize,
				)
			}
		})
	}
}

func TestLocalAssetCache_Fetch(t *testing.T) {
	testDirPath := t.TempDir()

	testFileName1 := "abcd.jpg"
	testFileName2 := "bcdf.png"

	testFileData := []byte{0x00, 0x10}

	testFilePath1 := filepath.Join(testDirPath, testFileName1)
	testFilePath2 := filepath.Join(testDirPath, testFileName2)

	err := os.WriteFile(
		testFilePath1,
		testFileData,
		0o777,
	)
	if err != nil {
		t.Fatalf("could not make temp file %q: %v", testFilePath1, err)
	}

	err = os.WriteFile(
		testFilePath2,
		testFileData,
		0o777,
	)
	if err != nil {
		t.Fatalf("could not make temp file %q: %v", testFilePath2, err)
	}

	knownAsset := &LocalAsset{
		Path: testFilePath1,
		Hash: "abcd",
	}
	unknownAsset := &LocalAsset{
		Path: testFilePath2,
		Hash: "bcdf",
	}

	tests := []struct {
		name        string
		fileName    string
		wantAsset   *LocalAsset
		wantSamePtr bool
	}{
		{
			"fetch known asset",
			testFileName1,
			knownAsset,
			true,
		},
		{
			"fetch unknown asset",
			testFileName2,
			unknownAsset,
			false,
		},
	}

	metrics := metricsServer.NewMetrics(prometheus.NewRegistry())
	cache, err := NewLocalAssetCache(
		testDirPath,
		2,
		4,
		time.Second,
		metrics,
	)
	if err != nil {
		t.Fatalf("could not create cache: %v", err)
	}
	cache.assets[testFileName1] = knownAsset

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			asset, err := cache.Fetch(test.fileName)
			if err != nil {
				t.Fatalf("got err=%v, want err=nil", err)
			}

			if test.wantSamePtr && asset != test.wantAsset {
				t.Errorf("got asset=%p, want existing asset %p", asset, test.wantAsset)
			}

			if asset.Path != test.wantAsset.Path ||
				asset.Hash != test.wantAsset.Hash {
				t.Errorf("got asset=%v, want %v", asset, test.wantAsset)
			}
		})
	}
}

func TestLocalAssetCache_findEvictionCandidatesLocked(t *testing.T) {
	now := time.Now()

	oldest := &LocalAsset{
		cachedBytes: []byte{0x00},
		expiresAt:   now.Add(-30 * time.Second),
	}
	middle := &LocalAsset{
		cachedBytes: []byte{0x00, 0x01},
		expiresAt:   now.Add(-20 * time.Second),
	}
	newest := &LocalAsset{
		cachedBytes: []byte{0x00, 0x01, 0x02, 0x03},
		expiresAt:   now.Add(-10 * time.Second),
	}
	unexpired := &LocalAsset{
		cachedBytes: []byte{0x00},
		expiresAt:   now.Add(10 * time.Second),
	}
	uncached := &LocalAsset{}

	allAssets := map[string]*LocalAsset{
		"oldest":    oldest,
		"middle":    middle,
		"newest":    newest,
		"unexpired": unexpired,
		"uncached":  uncached,
	}

	tests := []struct {
		name            string
		assets          map[string]*LocalAsset
		bytesNeeded     int64
		expectedVictims []*LocalAsset
	}{
		{
			name:            "empty assets map returns nil",
			assets:          map[string]*LocalAsset{},
			bytesNeeded:     1,
			expectedVictims: nil,
		},
		{
			name: "no expired cached assets returns nil",
			assets: map[string]*LocalAsset{
				"unexpired": unexpired,
				"uncached":  uncached,
			},
			bytesNeeded:     1,
			expectedVictims: nil,
		},
		{
			name: "single expired asset satisfies required space",
			assets: map[string]*LocalAsset{
				"oldest": oldest,
			},
			bytesNeeded:     1,
			expectedVictims: []*LocalAsset{oldest},
		},
		{
			name:            "oldest asset chosen first",
			assets:          allAssets,
			bytesNeeded:     1,
			expectedVictims: []*LocalAsset{oldest},
		},
		{
			name:        "multiple victims selected oldest to newest",
			assets:      allAssets,
			bytesNeeded: 3,
			expectedVictims: []*LocalAsset{
				oldest,
				middle,
			},
		},
		{
			name:        "continues selecting until enough bytes accumulated",
			assets:      allAssets,
			bytesNeeded: 6,
			expectedVictims: []*LocalAsset{
				oldest,
				middle,
				newest,
			},
		},
		{
			name:            "insufficient expired bytes returns nil",
			assets:          allAssets,
			bytesNeeded:     8,
			expectedVictims: nil,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			metrics := metricsServer.NewMetrics(prometheus.NewRegistry())
			cache := &LocalAssetCache{
				assets:  test.assets,
				metrics: metrics,
			}

			got := cache.findEvictionCandidatesLocked(
				test.bytesNeeded,
				now,
			)

			if len(got) != len(test.expectedVictims) {
				t.Fatalf(
					"got %d victims, want %d",
					len(got),
					len(test.expectedVictims),
				)
			}

			for i := range test.expectedVictims {
				if got[i] != test.expectedVictims[i] {
					t.Errorf(
						"victim[%d]=%p, want %p",
						i,
						got[i],
						test.expectedVictims[i],
					)
				}
			}
		})
	}
}

func TestLocalAssetCache_Cache(t *testing.T) {
	testDirPath := t.TempDir()
	metrics := metricsServer.NewMetrics(prometheus.NewRegistry())

	testFileName1 := "abcd.jpg"
	testFileName2 := "bcdf.png"
	testFileData := []byte{0x00, 0x10}

	testFilePath1 := filepath.Join(testDirPath, testFileName1)
	testFilePath2 := filepath.Join(testDirPath, testFileName2)

	err := os.WriteFile(
		testFilePath1,
		testFileData,
		0o777,
	)
	if err != nil {
		t.Fatalf("could not make temp file %q: %v", testFilePath1, err)
	}

	err = os.WriteFile(
		testFilePath2,
		testFileData,
		0o777,
	)
	if err != nil {
		t.Fatalf("could not make temp file %q: %v", testFilePath2, err)
	}

	tests := []struct {
		name                  string
		fileName              string
		assetMaxSize          int64
		cacheMaxSize          int64
		initialAsset          *LocalAsset
		initialCacheSize      int64
		expectedCacheSize     int64
		expectedCached        bool
		expectedInitialCached bool
		expectedErr           error
	}{
		{
			name:              "cache asset",
			fileName:          testFileName2,
			assetMaxSize:      2,
			cacheMaxSize:      4,
			expectedCacheSize: 2,
			expectedCached:    true,
			expectedErr:       nil,
		},
		{
			name:              "asset too large to cache",
			fileName:          testFileName2,
			assetMaxSize:      1,
			cacheMaxSize:      4,
			expectedCacheSize: 0,
			expectedCached:    false,
			expectedErr:       ErrAssetTooLargeToCache,
		},
		{
			name:         "cache full with no eviction candidate",
			fileName:     testFileName2,
			assetMaxSize: 2,
			cacheMaxSize: 2,
			initialAsset: &LocalAsset{
				Path:        testFilePath1,
				cachedBytes: testFileData,
				expiresAt:   time.Now().Add(time.Second),
			},
			initialCacheSize:      2,
			expectedCacheSize:     2,
			expectedCached:        false,
			expectedInitialCached: true,
			expectedErr:           ErrCacheFull,
		},
		{
			name:         "expired asset evicted",
			fileName:     testFileName2,
			assetMaxSize: 2,
			cacheMaxSize: 2,
			initialAsset: &LocalAsset{
				Path:        testFilePath1,
				cachedBytes: testFileData,
				expiresAt:   time.Now().Add(-time.Second),
			},
			initialCacheSize:      2,
			expectedCacheSize:     2,
			expectedCached:        true,
			expectedInitialCached: false,
			expectedErr:           nil,
		},
		{
			name:         "already cached asset",
			fileName:     testFileName1,
			assetMaxSize: 2,
			cacheMaxSize: 4,
			initialAsset: &LocalAsset{
				Path:        testFilePath1,
				cachedBytes: testFileData,
				expiresAt:   time.Now().Add(time.Second),
			},
			initialCacheSize:      2,
			expectedCacheSize:     2,
			expectedCached:        true,
			expectedInitialCached: true,
			expectedErr:           nil,
		},
		{
			name:              "invalid asset name",
			fileName:          "../abcd.jpg",
			assetMaxSize:      2,
			cacheMaxSize:      4,
			expectedCacheSize: 0,
			expectedCached:    false,
			expectedErr:       ErrInvalidAssetName,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cache, err := NewLocalAssetCache(
				testDirPath,
				test.assetMaxSize,
				test.cacheMaxSize,
				time.Second,
				metrics,
			)
			if err != nil {
				t.Fatalf("could not create cache: %v", err)
			}

			if test.initialAsset != nil {
				cache.assets[testFileName1] = test.initialAsset
				cache.cacheSize = test.initialCacheSize
			}

			err = cache.Cache(test.fileName)
			if !errors.Is(err, test.expectedErr) {
				t.Errorf("got err=%v, want err=%v", err, test.expectedErr)
			}

			if cache.cacheSize != test.expectedCacheSize {
				t.Errorf(
					"cacheSize=%v, want cacheSize=%v",
					cache.cacheSize,
					test.expectedCacheSize,
				)
			}

			cached := false
			asset := cache.assets[test.fileName]
			if asset != nil {
				cached = asset.IsCached()
			}

			if cached != test.expectedCached {
				t.Errorf(
					"asset.IsCached()=%v, want asset.IsCached()=%v",
					cached,
					test.expectedCached,
				)
			}

			if test.expectedCached && !bytes.Equal(asset.cachedBytes, testFileData) {
				t.Errorf(
					"asset.cachedBytes=%v != testFileData=%v",
					asset.cachedBytes,
					testFileData,
				)
			}

			if test.initialAsset != nil {
				cached := test.initialAsset.IsCached()
				if cached != test.expectedInitialCached {
					t.Errorf(
						"initialAsset.IsCached()=%v, want initialAsset.IsCached()=%v",
						cached,
						test.expectedInitialCached,
					)
				}
			}
		})
	}
}

func TestValidateAssetName(t *testing.T) {
	tests := []struct {
		fileName    string
		expectedErr error
	}{
		{
			"abcdefg",
			nil,
		},
		{
			"abcdefg.jpg",
			nil,
		},
		{
			"abcdefg.jpg.png",
			nil,
		},
		{
			"",
			ErrInvalidAssetName,
		},
		{
			".",
			ErrInvalidAssetName,
		},
		{
			"..",
			ErrInvalidAssetName,
		},
		{
			"./.././..///...",
			ErrInvalidAssetName,
		},
		{
			"/etc/passwd",
			ErrInvalidAssetName,
		},
	}

	for _, test := range tests {
		t.Run(test.fileName, func(t *testing.T) {
			err := validateAssetName(test.fileName)
			if !errors.Is(err, test.expectedErr) {
				t.Errorf("got %v, want %v", err, test.expectedErr)
			}
		})
	}
}

func TestLocalAsset_IsCached(t *testing.T) {
	t.Run("is cached and returns true", func(t *testing.T) {
		asset := &LocalAsset{
			cachedBytes: []byte{0x01},
		}
		if cached := asset.IsCached(); cached != true {
			t.Errorf("asset.IsCached=%v, want true", cached)
		}
	})
	t.Run("is not cached and returns false", func(t *testing.T) {
		asset := &LocalAsset{}
		if cached := asset.IsCached(); cached != false {
			t.Errorf("asset.IsCached=%v, want true", cached)
		}
	})
}

func TestCacheInsertEvictConcurrent(t *testing.T) {
	var wg sync.WaitGroup
	tmpDir := t.TempDir()
	startCh := make(chan struct{})

	assetMaxSize := int64(8)
	cacheMaxSize := int64(9)

	// Files of sizes: 1, 2, 4, 8, 64 = 79 total
	// The last one is to ensure that asset size polices
	// are enforced
	fileNames := []string{}
	for _, size := range []int{1, 2, 4, 8, 64} {
		fileName := strconv.Itoa(size) + ".bin"
		outPath := filepath.Join(tmpDir, fileName)
		err := os.WriteFile(
			outPath,
			bytes.Repeat([]byte{0x0}, size),
			0o777,
		)
		if err != nil {
			t.Fatal(err)
		}
		fileNames = append(fileNames, fileName)
	}

	metrics := metricsServer.NewMetrics(prometheus.NewRegistry())
	cache, err := NewLocalAssetCache(
		tmpDir,
		assetMaxSize,
		cacheMaxSize,
		time.Minute,
		metrics,
	)
	if err != nil {
		t.Fatal(err)
	}
	// preload
	err = os.WriteFile(
		filepath.Join(tmpDir, "preload.bin"),
		bytes.Repeat([]byte{1}, 8),
		0o777,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err = cache.Cache("preload.bin"); err != nil {
		t.Fatal(err)
	}

	preloadedAsset, err := cache.Fetch("preload.bin")
	if err != nil {
		t.Fatal(err)
	}
	preloadedAsset.expiresAt = time.Now().Add(-time.Hour)

	successfulInserts := atomic.Int32{}
	for _, fileName := range fileNames {
		for range 3 {
			wg.Go(func() {
				<-startCh
				err := cache.Cache(fileName)

				if fileName == "64.bin" {
					if !errors.Is(err, ErrAssetTooLargeToCache) {
						t.Errorf("got err=%v, want ErrAssetTooLarge", err)
					}
					return
				}
				if err != nil && !errors.Is(err, ErrCacheFull) {
					t.Errorf("got unexpected err=%v", err)
				}
				if err == nil {
					successfulInserts.Add(1)
				}
			})
		}
	}

	close(startCh)
	wg.Wait()

	if cache.cacheSize > cacheMaxSize {
		t.Fatalf("got cache.cacheSize=%v > %v, want <=", cache.cacheSize, cacheMaxSize)
	}

	if successfulInserts.Load() == 0 {
		t.Fatal("no cache inserts performed")
	}

	asset, err := cache.Fetch("preload.bin")
	if err != nil {
		t.Fatal(err)
	}
	if asset.cacheState().Cached {
		t.Fatal("preloaded, expired asset was not uncached")
	}

	cachedAssetSum := 0
	for _, asset := range cache.assets {
		if asset.cacheState().Cached {
			cachedAssetSum += len(asset.cachedBytes)
		}
	}
	if int64(cachedAssetSum) != cache.cacheSize {
		t.Fatalf(
			"got cachedAssetSum=%v != cache.cacheSize=%v, want ==",
			cachedAssetSum, cache.cacheSize,
		)
	}
}
