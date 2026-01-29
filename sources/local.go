package sources

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/oliverwhk/versa-proxmox-deployer/config"
)

// LocalSource represents a local filesystem source for ISOs
type LocalSource struct {
	name string
	path string
}

// NewLocalSource creates a new local filesystem source
func NewLocalSource(path, name string) *LocalSource {
	return &LocalSource{
		name: name,
		path: config.ExpandPath(path),
	}
}

// Name returns the source name
func (s *LocalSource) Name() string {
	return s.name
}

// Type returns the source type
func (s *LocalSource) Type() string {
	return string(SourceTypeLocal)
}

// URL returns the source path
func (s *LocalSource) URL() string {
	return s.path
}

// List returns all ISO files in the local directory (recursive)
func (s *LocalSource) List() ([]ISOFile, error) {
	// First pass: collect all MD5 files recursively
	md5Files := make(map[string]string) // ISO name -> MD5 file path

	err := filepath.WalkDir(s.path, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // Skip errors, continue walking
		}
		if d.IsDir() {
			return nil
		}
		if IsMD5File(d.Name()) {
			isoName := GetISOForMD5(d.Name())
			md5Files[isoName] = path
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walking directory for MD5 files: %w", err)
	}

	// Second pass: collect ISOs recursively
	var isos []ISOFile

	err = filepath.WalkDir(s.path, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // Skip errors, continue walking
		}
		if d.IsDir() {
			return nil
		}

		name := d.Name()
		if !IsISOFile(name) {
			return nil
		}

		info, err := d.Info()
		if err != nil {
			return nil
		}

		iso := ParseISOFilename(name, s.name, s.Type(), path)
		iso.Size = info.Size()

		// Check for MD5 file (can be in same directory or matched by full filename)
		if md5Path, ok := md5Files[name]; ok {
			iso.HasMD5File = true
			iso.MD5FileURL = md5Path

			// Read MD5 value
			if md5, err := readMD5File(md5Path); err == nil {
				iso.MD5 = md5
			}
		}

		isos = append(isos, iso)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walking directory for ISOs: %w", err)
	}

	return isos, nil
}

// Download copies a local ISO to destination (essentially a copy)
func (s *LocalSource) Download(iso ISOFile, destPath string, progress func(downloaded, total int64)) error {
	srcPath := iso.SourceURL

	// Verify source exists
	info, err := os.Stat(srcPath)
	if err != nil {
		return fmt.Errorf("source file not found: %w", err)
	}

	// Ensure destination directory exists
	if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
		return fmt.Errorf("creating destination directory: %w", err)
	}

	// Remove existing dest if any (could be stale symlink)
	os.Remove(destPath)

	// Symlink instead of copying — local files don't need to be duplicated
	if err := os.Symlink(srcPath, destPath); err != nil {
		return fmt.Errorf("creating symlink: %w", err)
	}

	if progress != nil {
		progress(info.Size(), info.Size())
	}

	return nil
}

// readMD5File reads an MD5 checksum from a .md5 file
func readMD5File(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}

	// Format: "checksum  filename" or just "checksum"
	content := strings.TrimSpace(string(data))
	parts := strings.Fields(content)
	if len(parts) < 1 {
		return "", fmt.Errorf("invalid MD5 file format")
	}

	return strings.ToLower(parts[0]), nil
}

// GetISOPath returns the full path to an ISO
func (s *LocalSource) GetISOPath(filename string) string {
	return filepath.Join(s.path, filename)
}

// Exists checks if a file exists in the source
func (s *LocalSource) Exists(filename string) bool {
	path := filepath.Join(s.path, filename)
	_, err := os.Stat(path)
	return err == nil
}

// GetMD5 returns the MD5 for an ISO if available
func (s *LocalSource) GetMD5(isoFilename string) (string, error) {
	md5Path := filepath.Join(s.path, isoFilename+".md5")
	return readMD5File(md5Path)
}
