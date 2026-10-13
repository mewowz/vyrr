package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	metricsServer "github.com/mewowz/vyrr/internal/metrics"
)

var (
	ErrCacheFull                   = errors.New("cache full")
	ErrNotADir                     = errors.New("not a directory")
	ErrInvalidCacheLimit           = errors.New("invalid limit")
	ErrAssetLimitExceedsCacheLimit = errors.New("assetSize exceeds cacheSize")
	ErrInvalidTTL                  = errors.New("invalid setting for TTL")
	ErrInvalidAssetName            = errors.New("invalid asset name")
	ErrAssetTooLargeToCache        = errors.New("asset too large to cache")
	ErrNotAFile                    = errors.New("path does not resolve to a file")
)

type LocalAssetCache struct {
	assetDir     string
	assetMaxSize int64
	cacheMaxSize int64

	assets    map[string]*LocalAsset
	cacheSize int64

	ttl time.Duration

	mu      sync.RWMutex
	metrics *metricsServer.Metrics
}

type LocalAsset struct {
	FileInfo fs.FileInfo
	Path     string
	Hash     string

	cachedBytes []byte
	expiresAt   time.Time

	mu sync.RWMutex
}

type cacheState struct {
	Cached    bool
	ExpiresAt time.Time
}

func NewLocalAssetCache(
	assetDir string,
	assetMaxSize,
	cacheMaxSize int64,
	ttl time.Duration,
	metrics *metricsServer.Metrics,
) (*LocalAssetCache, error) {
	assetDir = filepath.Clean(assetDir)
	assetDirInfo, err := os.Stat(assetDir)
	if err != nil {
		return nil, fmt.Errorf("local asset cache %q: %w", assetDir, err)
	}

	if !assetDirInfo.IsDir() {
		return nil, fmt.Errorf("local asset cache %q: %w", assetDir, ErrNotADir)
	}

	if assetMaxSize <= 0 {
		return nil, fmt.Errorf(
			"local asset cache assetMaxSize=%d: %w",
			assetMaxSize,
			ErrInvalidCacheLimit,
		)
	}

	if cacheMaxSize <= 0 {
		return nil, fmt.Errorf(
			"local asset cache cacheMaxSize=%d: %w",
			cacheMaxSize,
			ErrInvalidCacheLimit,
		)
	}

	if assetMaxSize > cacheMaxSize {
		return nil, fmt.Errorf(
			"local asset cache assetMaxSize=%d > cacheMaxSize=%d: %w",
			assetMaxSize,
			cacheMaxSize,
			ErrAssetLimitExceedsCacheLimit,
		)
	}

	if ttl <= 0 {
		return nil, fmt.Errorf("local asset cache ttl=%s: %w", ttl, ErrInvalidTTL)
	}

	if metrics == nil {
		return nil, metricsServer.ErrNilMetrics
	}

	return &LocalAssetCache{
		assetDir:     assetDir,
		assetMaxSize: assetMaxSize,
		cacheMaxSize: cacheMaxSize,
		assets:       make(map[string]*LocalAsset),
		ttl:          ttl,
		metrics:      metrics,
	}, nil
}

func NewLocalAsset(path string) (*LocalAsset, error) {
	assetInfo, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if assetInfo.IsDir() {
		return nil, fmt.Errorf("local asset %q: %w", path, ErrNotAFile)
	}

	pathBase := filepath.Base(path)
	hashString := strings.TrimSuffix(
		pathBase,
		filepath.Ext(pathBase),
	)

	return &LocalAsset{
		FileInfo: assetInfo,
		Path:     path,
		Hash:     hashString,
	}, nil
}

func (a *LocalAsset) Open() (io.ReadCloser, error) {
	a.mu.RLock()
	cachedBytes := a.cachedBytes
	a.mu.RUnlock()

	if cachedBytes != nil {
		r := bytes.NewReader(cachedBytes)
		return io.NopCloser(r), nil
	} else {
		return os.Open(a.Path)
	}
}

func (a *LocalAsset) IsCached() bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.cachedBytes != nil
}

func (a *LocalAsset) CacheExpired(now time.Time) bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.cachedBytes != nil && !now.Before(a.expiresAt)
}

func (a *LocalAsset) cacheState() cacheState {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return cacheState{
		Cached:    a.cachedBytes != nil,
		ExpiresAt: a.expiresAt,
	}
}

func (a *LocalAsset) setCached(data []byte, expiresAt time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.cachedBytes = data
	a.expiresAt = expiresAt
}

// Not needed right now
//func (a *LocalAsset) setExpiry(expiresAt time.Time) {
//	a.mu.Lock()
//	defer a.mu.Unlock()
//	a.expiresAt = expiresAt
//}

func (a *LocalAsset) uncache() int {
	a.mu.Lock()
	defer a.mu.Unlock()

	size := len(a.cachedBytes)
	a.cachedBytes = nil
	a.expiresAt = time.Time{}
	return size
}

func (c *LocalAssetCache) Fetch(fileName string) (*LocalAsset, error) {
	if err := validateAssetName(fileName); err != nil {
		return nil, err
	}

	c.mu.RLock()
	asset := c.assets[fileName]
	c.mu.RUnlock()

	if asset != nil {
		return asset, nil
	}

	path := filepath.Join(c.assetDir, fileName)
	newAsset, err := NewLocalAsset(path)
	if err != nil {
		return nil, fmt.Errorf("fetch %q: %w", fileName, err)
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if asset := c.assets[fileName]; asset != nil {
		return asset, nil
	}

	c.assets[fileName] = newAsset

	return newAsset, nil
}

func (c *LocalAssetCache) Cache(fileName string) error {
	asset, err := c.Fetch(fileName)
	if err != nil {
		return fmt.Errorf("cache %q: %w", fileName, err)
	}

	if asset.IsCached() {
		c.metrics.CacheHits.Inc()
		return nil
	}

	file, err := os.Open(asset.Path)
	if err != nil {
		return fmt.Errorf("cache %q: %w", fileName, err)
	}
	defer file.Close()

	fileInfo, err := file.Stat()
	if err != nil {
		return fmt.Errorf("cache %q: %w", fileName, err)
	}

	assetSize := fileInfo.Size()
	if assetSize > c.assetMaxSize {
		c.metrics.CacheMisses.Inc()
		return fmt.Errorf(
			"cache %q (assetSize=%d > assetMaxSize=%d): %w",
			fileName,
			assetSize,
			c.assetMaxSize,
			ErrAssetTooLargeToCache,
		)
	}

	data := make([]byte, fileInfo.Size())
	_, err = io.ReadFull(file, data)
	if err != nil {
		return fmt.Errorf("cache %q: %w", fileName, err)
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if asset.IsCached() {
		return nil
	}
	c.metrics.CacheMisses.Inc()

	freeSpace := c.cacheMaxSize - c.cacheSize
	if assetSize > freeSpace {
		bytesNeeded := assetSize - freeSpace
		victims := c.findEvictionCandidatesLocked(bytesNeeded, time.Now())

		if victims == nil {
			return fmt.Errorf(
				"cache %q (assetSize+cacheSize=%d > cacheMaxSize=%d): %w",
				fileName,
				assetSize+c.cacheSize,
				c.cacheMaxSize,
				ErrCacheFull,
			)
		}

		for _, victim := range victims {
			c.uncacheAssetLocked(victim)
		}
	}
	asset.setCached(data, time.Now().Add(c.ttl))
	c.cacheSize += assetSize

	c.metrics.CacheSize.Set(float64(c.cacheSize))

	return nil
}

func (c *LocalAssetCache) uncacheAssetLocked(asset *LocalAsset) {
	if !asset.IsCached() {
		return
	}

	assetSize := asset.uncache()
	c.cacheSize -= int64(assetSize)
}

func (c *LocalAssetCache) findEvictionCandidatesLocked(
	bytesNeeded int64,
	now time.Time,
) []*LocalAsset {
	var candidates []*LocalAsset
	for _, candidate := range c.assets {
		state := candidate.cacheState()
		if !state.Cached {
			continue
		}

		if now.Before(state.ExpiresAt) {
			continue
		}

		candidates = append(candidates, candidate)
	}
	if candidates == nil {
		return nil
	}

	slices.SortFunc(candidates, func(a, b *LocalAsset) int {
		aState := a.cacheState()
		bState := b.cacheState()
		return aState.ExpiresAt.Compare(bState.ExpiresAt)
	})

	var victims []*LocalAsset
	var totalVictimBytes int64
	for _, victim := range candidates {
		totalVictimBytes += victim.FileInfo.Size()
		victims = append(victims, victim)
		if totalVictimBytes >= bytesNeeded {
			return victims
		}
	}

	return nil
}

func validateAssetName(name string) error {
	if name == "" ||
		name == "." ||
		name == ".." ||
		filepath.Base(name) != name {
		return fmt.Errorf("asset name %q: %w", name, ErrInvalidAssetName)
	}

	return nil
}
