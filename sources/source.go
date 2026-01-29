package sources

import (
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/oliverwhk/versa-proxmox-deployer/config"
)

// ImageSource represents a source for Versa ISO images
type ImageSource interface {
	// Name returns a descriptive name for the source
	Name() string

	// Type returns the source type (dropbox, http, sftp, local)
	Type() string

	// URL returns the source URL or path
	URL() string

	// List returns all ISO files found in the source
	List() ([]ISOFile, error)

	// Download downloads an ISO to local path, calling progress callback
	Download(iso ISOFile, destPath string, progress func(downloaded, total int64)) error
}

// ISOFile represents an ISO file found in a source
type ISOFile struct {
	Filename    string            // e.g., "versa-director-d58d641-22.1.4-B.iso"
	Component   config.ComponentType // Detected component type
	Version     string            // Extracted version (e.g., "22.1.4-B")
	Size        int64             // File size in bytes
	MD5         string            // MD5 checksum if available
	HasMD5File  bool              // Whether .md5 companion file exists
	SourceName  string            // Name of the source
	SourceType  string            // Type of source (dropbox, http, sftp, local)
	SourceURL   string            // Full URL or path to file
	MD5FileURL  string            // URL or path to .md5 file
}

// ISOCollection holds categorized ISOs from all sources
type ISOCollection struct {
	// ISOs grouped by component type
	Director   []ISOFile
	Analytics  []ISOFile
	Controller []ISOFile
	Concerto   []ISOFile
	FlexVNF    []ISOFile

	// All sources scanned
	Sources []SourceSummary
}

// SourceSummary holds summary info about a scanned source
type SourceSummary struct {
	Name      string
	Type      string
	URL       string
	ISOCount  int
	MD5Count  int
	Error     string
}

// DetectComponent detects the component type from an ISO filename
func DetectComponent(filename string) config.ComponentType {
	lower := strings.ToLower(filename)

	switch {
	case strings.Contains(lower, "director"):
		return config.ComponentDirector
	case strings.Contains(lower, "analytics") || strings.Contains(lower, "van"):
		return config.ComponentAnalytics
	case strings.Contains(lower, "concerto"):
		return config.ComponentConcerto
	case strings.Contains(lower, "flexvnf") || strings.Contains(lower, "vos") || strings.Contains(lower, "branch"):
		// FlexVNF is also used for Controller and Router
		return config.ComponentFlexVNF
	default:
		return ""
	}
}

// ExtractVersion extracts version string from ISO filename
func ExtractVersion(filename string) string {
	// Common patterns:
	// versa-director-d58d641-22.1.4-B.iso
	// versa-analytics-262aa66-22.1.4-B-snb.iso
	// concerto-a7254df-12.2.2.iso

	// Pattern: find version like "22.1.4" or "22.1.4-B"
	patterns := []string{
		`(\d+\.\d+\.\d+(?:-[A-Za-z0-9]+)?)`,  // 22.1.4 or 22.1.4-B
		`v(\d+\.\d+\.\d+(?:-[A-Za-z0-9]+)?)`, // v22.1.4
	}

	for _, pattern := range patterns {
		re := regexp.MustCompile(pattern)
		matches := re.FindStringSubmatch(filename)
		if len(matches) >= 2 {
			return matches[1]
		}
	}

	return ""
}

// ParseISOFilename parses an ISO filename and returns file info
func ParseISOFilename(filename, sourceName, sourceType, sourceURL string) ISOFile {
	iso := ISOFile{
		Filename:   filename,
		Component:  DetectComponent(filename),
		Version:    ExtractVersion(filename),
		SourceName: sourceName,
		SourceType: sourceType,
		SourceURL:  sourceURL,
	}

	return iso
}

// ScanAllSources scans all configured sources and returns categorized ISOs
func ScanAllSources(sources []ImageSource) (*ISOCollection, error) {
	collection := &ISOCollection{}

	for _, source := range sources {
		summary := SourceSummary{
			Name: source.Name(),
			Type: source.Type(),
			URL:  source.URL(),
		}

		isos, err := source.List()
		if err != nil {
			summary.Error = err.Error()
			collection.Sources = append(collection.Sources, summary)
			continue
		}

		// Count ISOs and MD5s
		for _, iso := range isos {
			summary.ISOCount++
			if iso.HasMD5File || iso.MD5 != "" {
				summary.MD5Count++
			}

			// Categorize by component
			switch iso.Component {
			case config.ComponentDirector:
				collection.Director = append(collection.Director, iso)
			case config.ComponentAnalytics:
				collection.Analytics = append(collection.Analytics, iso)
			case config.ComponentConcerto:
				collection.Concerto = append(collection.Concerto, iso)
			case config.ComponentFlexVNF:
				// FlexVNF is used for Controller, Router, and FlexVNF
				collection.FlexVNF = append(collection.FlexVNF, iso)
			}
		}

		collection.Sources = append(collection.Sources, summary)
	}

	// Sort each category by version (newest first)
	sortByVersion := func(isos []ISOFile) {
		sort.Slice(isos, func(i, j int) bool {
			return compareVersions(isos[i].Version, isos[j].Version) > 0
		})
	}

	sortByVersion(collection.Director)
	sortByVersion(collection.Analytics)
	sortByVersion(collection.Controller)
	sortByVersion(collection.Concerto)
	sortByVersion(collection.FlexVNF)

	return collection, nil
}

// compareVersions compares two version strings
// Returns: -1 if a < b, 0 if a == b, 1 if a > b
func compareVersions(a, b string) int {
	// Parse versions like "22.1.4" or "22.1.4-B"
	parseVersion := func(v string) (major, minor, patch int, suffix string) {
		// Remove leading 'v' if present
		v = strings.TrimPrefix(v, "v")

		// Split on dash for suffix
		parts := strings.SplitN(v, "-", 2)
		numPart := parts[0]
		if len(parts) > 1 {
			suffix = parts[1]
		}

		// Parse numbers
		nums := strings.Split(numPart, ".")
		if len(nums) >= 1 {
			fmt.Sscanf(nums[0], "%d", &major)
		}
		if len(nums) >= 2 {
			fmt.Sscanf(nums[1], "%d", &minor)
		}
		if len(nums) >= 3 {
			fmt.Sscanf(nums[2], "%d", &patch)
		}
		return
	}

	aMajor, aMinor, aPatch, aSuffix := parseVersion(a)
	bMajor, bMinor, bPatch, bSuffix := parseVersion(b)

	if aMajor != bMajor {
		if aMajor > bMajor {
			return 1
		}
		return -1
	}
	if aMinor != bMinor {
		if aMinor > bMinor {
			return 1
		}
		return -1
	}
	if aPatch != bPatch {
		if aPatch > bPatch {
			return 1
		}
		return -1
	}

	// Compare suffixes (B > A > empty)
	return strings.Compare(aSuffix, bSuffix)
}

// GetLatestISO returns the latest version ISO for a component
func (c *ISOCollection) GetLatestISO(component config.ComponentType) *ISOFile {
	var isos []ISOFile

	switch component {
	case config.ComponentDirector:
		isos = c.Director
	case config.ComponentAnalytics:
		isos = c.Analytics
	case config.ComponentController, config.ComponentRouter, config.ComponentFlexVNF:
		isos = c.FlexVNF
	case config.ComponentConcerto:
		isos = c.Concerto
	}

	if len(isos) == 0 {
		return nil
	}

	// Already sorted newest first
	return &isos[0]
}

// GetISOsForComponent returns all ISOs for a component
func (c *ISOCollection) GetISOsForComponent(component config.ComponentType) []ISOFile {
	switch component {
	case config.ComponentDirector:
		return c.Director
	case config.ComponentAnalytics:
		return c.Analytics
	case config.ComponentController, config.ComponentRouter, config.ComponentFlexVNF:
		return c.FlexVNF
	case config.ComponentConcerto:
		return c.Concerto
	default:
		return nil
	}
}

// FindISOByVersion finds an ISO with a specific version
func (c *ISOCollection) FindISOByVersion(component config.ComponentType, version string) *ISOFile {
	isos := c.GetISOsForComponent(component)
	for _, iso := range isos {
		if iso.Version == version {
			return &iso
		}
	}
	return nil
}

// GetMD5FilePath returns the expected .md5 file path for an ISO
func GetMD5FilePath(isoPath string) string {
	return isoPath + ".md5"
}

// IsISOFile checks if a filename is an ISO file
func IsISOFile(filename string) bool {
	return strings.HasSuffix(strings.ToLower(filename), ".iso")
}

// IsMD5File checks if a filename is an MD5 file
func IsMD5File(filename string) bool {
	return strings.HasSuffix(strings.ToLower(filename), ".md5")
}

// GetISOForMD5 returns the ISO filename for an MD5 file
func GetISOForMD5(md5Filename string) string {
	return strings.TrimSuffix(md5Filename, ".md5")
}

// FormatFileSize formats a file size in human-readable form
func FormatFileSize(bytes int64) string {
	const (
		KB = 1024
		MB = KB * 1024
		GB = MB * 1024
	)

	switch {
	case bytes >= GB:
		return fmt.Sprintf("%.1fGB", float64(bytes)/GB)
	case bytes >= MB:
		return fmt.Sprintf("%.1fMB", float64(bytes)/MB)
	case bytes >= KB:
		return fmt.Sprintf("%.1fKB", float64(bytes)/KB)
	default:
		return fmt.Sprintf("%dB", bytes)
	}
}

// CacheDir returns the local cache directory for downloaded ISOs
func CacheDir() string {
	return filepath.Join(config.ConfigDir(), "images")
}
