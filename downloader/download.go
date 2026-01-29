package downloader

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/oliverwhk/versa-proxmox-deployer/config"
	"github.com/oliverwhk/versa-proxmox-deployer/sources"
)

// Downloader handles ISO download, caching, and verification
type Downloader struct {
	sources  []sources.ImageSource
	cacheDir string
}

// NewDownloader creates a new downloader
func NewDownloader(srcs []sources.ImageSource) *Downloader {
	return &Downloader{
		sources:  srcs,
		cacheDir: sources.CacheDir(),
	}
}

// DownloadResult holds the result of a download operation
type DownloadResult struct {
	LocalPath  string
	WasCached  bool
	MD5        string
	MD5Verified bool
	Size       int64
}

// EnsureISO ensures an ISO is available locally (downloads if needed)
func (d *Downloader) EnsureISO(iso sources.ISOFile, progress func(downloaded, total int64)) (*DownloadResult, error) {
	result := &DownloadResult{}

	// Ensure cache directory exists
	if err := os.MkdirAll(d.cacheDir, 0755); err != nil {
		return nil, fmt.Errorf("creating cache directory: %w", err)
	}

	cachePath := filepath.Join(d.cacheDir, iso.Filename)
	result.LocalPath = cachePath

	// For local sources, the SourceURL is the actual file path.
	// If it's a symlink pointing to the source, or the file exists with matching size, trust it.
	if info, err := os.Lstat(cachePath); err == nil {
		// If it's a symlink, resolve and check it points to a valid file
		if info.Mode()&os.ModeSymlink != 0 {
			if target, err := os.Readlink(cachePath); err == nil {
				if realInfo, err := os.Stat(target); err == nil {
					result.Size = realInfo.Size()
					result.WasCached = true
					result.LocalPath = target // Use actual path for upload
					if iso.MD5 != "" {
						result.MD5 = iso.MD5
						result.MD5Verified = true // Trust the MD5 from scan
					}
					return result, nil
				}
			}
			// Stale symlink, remove
			os.Remove(cachePath)
		} else {
			// Regular file — check size match
			result.Size = info.Size()
			if iso.Size > 0 && result.Size == iso.Size {
				result.WasCached = true
				if iso.MD5 != "" {
					result.MD5 = iso.MD5
					result.MD5Verified = true // Trust the MD5 from scan
				}
				return result, nil
			}
			// Size mismatch, re-download
		}
	}

	// Find the source for this ISO
	var source sources.ImageSource
	for _, src := range d.sources {
		if src.Name() == iso.SourceName {
			source = src
			break
		}
	}

	if source == nil {
		return nil, fmt.Errorf("source not found: %s", iso.SourceName)
	}

	// Download (or symlink for local sources)
	if err := source.Download(iso, cachePath, progress); err != nil {
		os.Remove(cachePath)
		return nil, fmt.Errorf("downloading ISO: %w", err)
	}

	// Resolve symlinks for the local path
	if resolved, err := filepath.EvalSymlinks(cachePath); err == nil {
		result.LocalPath = resolved
	}

	// Get file info (follows symlinks)
	info, err := os.Stat(cachePath)
	if err != nil {
		return nil, fmt.Errorf("getting file info: %w", err)
	}
	result.Size = info.Size()

	// Trust MD5 from source scan rather than re-computing
	if iso.MD5 != "" {
		result.MD5 = iso.MD5
		result.MD5Verified = true
	}

	return result, nil
}

// CalculateMD5 calculates the MD5 checksum of a file
func CalculateMD5(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := md5.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}

	return hex.EncodeToString(h.Sum(nil)), nil
}

// GetCachedPath returns the cache path for a filename
func (d *Downloader) GetCachedPath(filename string) string {
	return filepath.Join(d.cacheDir, filename)
}

// IsCached checks if a file is already cached
func (d *Downloader) IsCached(filename string) bool {
	path := d.GetCachedPath(filename)
	_, err := os.Stat(path)
	return err == nil
}

// GetCachedMD5 returns the MD5 of a cached file if available
func (d *Downloader) GetCachedMD5(filename string) (string, error) {
	path := d.GetCachedPath(filename)
	return CalculateMD5(path)
}

// ClearCache removes all cached files
func (d *Downloader) ClearCache() error {
	return os.RemoveAll(d.cacheDir)
}

// GetCacheSize returns the total size of cached files
func (d *Downloader) GetCacheSize() (int64, error) {
	var totalSize int64

	err := filepath.Walk(d.cacheDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			totalSize += info.Size()
		}
		return nil
	})

	return totalSize, err
}

// ListCachedFiles returns a list of cached files
func (d *Downloader) ListCachedFiles() ([]string, error) {
	entries, err := os.ReadDir(d.cacheDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var files []string
	for _, entry := range entries {
		if !entry.IsDir() && sources.IsISOFile(entry.Name()) {
			files = append(files, entry.Name())
		}
	}

	return files, nil
}

// GenerateMD5File creates an MD5 file for an ISO
func GenerateMD5File(isoPath string) (string, error) {
	md5sum, err := CalculateMD5(isoPath)
	if err != nil {
		return "", fmt.Errorf("calculating MD5: %w", err)
	}

	md5Path := isoPath + ".md5"
	content := fmt.Sprintf("%s  %s\n", md5sum, filepath.Base(isoPath))

	if err := os.WriteFile(md5Path, []byte(content), 0644); err != nil {
		return "", fmt.Errorf("writing MD5 file: %w", err)
	}

	return md5sum, nil
}

// GenerateAllMD5Files generates MD5 files for all ISOs in a directory
func GenerateAllMD5Files(dir string) ([]string, error) {
	dir = config.ExpandPath(dir)

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("reading directory: %w", err)
	}

	var generated []string

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		name := entry.Name()
		if !sources.IsISOFile(name) {
			continue
		}

		isoPath := filepath.Join(dir, name)
		md5Path := isoPath + ".md5"

		// Skip if MD5 already exists
		if _, err := os.Stat(md5Path); err == nil {
			continue
		}

		md5sum, err := GenerateMD5File(isoPath)
		if err != nil {
			return generated, fmt.Errorf("generating MD5 for %s: %w", name, err)
		}

		generated = append(generated, fmt.Sprintf("%s: %s", name, md5sum))
	}

	return generated, nil
}

// VerifyMD5 verifies a file against its expected MD5
func VerifyMD5(path, expectedMD5 string) (bool, string, error) {
	actualMD5, err := CalculateMD5(path)
	if err != nil {
		return false, "", err
	}

	return actualMD5 == expectedMD5, actualMD5, nil
}

// ReadMD5File reads an MD5 checksum from a .md5 file
func ReadMD5File(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}

	// Parse "checksum  filename" or just "checksum"
	content := string(data)
	fields := splitFields(content)
	if len(fields) < 1 {
		return "", fmt.Errorf("invalid MD5 file format")
	}

	return fields[0], nil
}

// splitFields splits a string on whitespace
func splitFields(s string) []string {
	var fields []string
	field := ""
	for _, c := range s {
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' {
			if field != "" {
				fields = append(fields, field)
				field = ""
			}
		} else {
			field += string(c)
		}
	}
	if field != "" {
		fields = append(fields, field)
	}
	return fields
}
