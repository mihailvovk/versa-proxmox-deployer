package sources

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// HTTPSource represents an HTTP/HTTPS directory source for ISOs
type HTTPSource struct {
	name string
	url  string
}

// NewHTTPSource creates a new HTTP source
func NewHTTPSource(urlStr, name string) *HTTPSource {
	// Ensure URL ends with /
	if !strings.HasSuffix(urlStr, "/") {
		urlStr += "/"
	}

	return &HTTPSource{
		name: name,
		url:  urlStr,
	}
}

// Name returns the source name
func (s *HTTPSource) Name() string {
	return s.name
}

// Type returns the source type
func (s *HTTPSource) Type() string {
	return string(SourceTypeHTTP)
}

// URL returns the source URL
func (s *HTTPSource) URL() string {
	return s.url
}

// List returns all ISO files in the HTTP directory (recursive)
func (s *HTTPSource) List() ([]ISOFile, error) {
	return s.listRecursive(s.url, make(map[string]bool), 3) // Max depth of 3
}

// listRecursive recursively lists ISO files from HTTP directories
func (s *HTTPSource) listRecursive(baseURL string, visited map[string]bool, maxDepth int) ([]ISOFile, error) {
	if maxDepth <= 0 {
		return nil, nil
	}

	if visited[baseURL] {
		return nil, nil
	}
	visited[baseURL] = true

	client := &http.Client{
		Timeout: 30 * time.Second,
	}

	resp, err := client.Get(baseURL)
	if err != nil {
		return nil, fmt.Errorf("fetching directory listing: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("server returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}

	isos, subdirs := s.parseDirectoryListingWithDirs(string(body), baseURL)

	// Recursively scan subdirectories
	for _, subdir := range subdirs {
		subIsos, err := s.listRecursive(subdir, visited, maxDepth-1)
		if err == nil {
			isos = append(isos, subIsos...)
		}
	}

	return isos, nil
}

// parseDirectoryListing parses an HTTP directory listing page
func (s *HTTPSource) parseDirectoryListing(html string) ([]ISOFile, error) {
	isos, _ := s.parseDirectoryListingWithDirs(html, s.url)
	return isos, nil
}

// parseDirectoryListingWithDirs parses directory listing and returns ISOs and subdirectory URLs
func (s *HTTPSource) parseDirectoryListingWithDirs(html string, baseURL string) ([]ISOFile, []string) {
	var isos []ISOFile
	var subdirs []string
	md5Files := make(map[string]bool)

	// Ensure baseURL ends with /
	if !strings.HasSuffix(baseURL, "/") {
		baseURL += "/"
	}

	// Pattern to find hrefs
	hrefPattern := regexp.MustCompile(`href="([^"]+)"`)
	matches := hrefPattern.FindAllStringSubmatch(html, -1)

	seen := make(map[string]bool)

	for _, match := range matches {
		if len(match) < 2 {
			continue
		}

		href := match[1]

		// Skip parent links, query strings, external links
		if href == "" || href == "../" || href == "./" || strings.HasPrefix(href, "?") {
			continue
		}
		if strings.HasPrefix(href, "http://") || strings.HasPrefix(href, "https://") {
			// External link, skip
			continue
		}

		// URL decode if necessary
		if decoded, err := url.QueryUnescape(href); err == nil {
			href = decoded
		}

		// Check if it's a directory (ends with /)
		if strings.HasSuffix(href, "/") {
			subdirURL := baseURL + href
			subdirs = append(subdirs, subdirURL)
			continue
		}

		// Get just the filename
		filename := filepath.Base(href)

		if IsMD5File(filename) {
			md5Files[GetISOForMD5(filename)] = true
			continue
		}

		if !IsISOFile(filename) {
			continue
		}

		if seen[filename] {
			continue
		}
		seen[filename] = true

		// Build full URL
		fileURL := baseURL + href

		iso := ParseISOFilename(filename, s.name, s.Type(), fileURL)

		isos = append(isos, iso)
	}

	// Update MD5 status for found ISOs
	for i := range isos {
		if md5Files[isos[i].Filename] {
			isos[i].HasMD5File = true
			isos[i].MD5FileURL = baseURL + isos[i].Filename + ".md5"
		}
	}

	return isos, subdirs
}

// Download downloads an ISO from HTTP
func (s *HTTPSource) Download(iso ISOFile, destPath string, progress func(downloaded, total int64)) error {
	downloadURL := iso.SourceURL
	if downloadURL == "" {
		downloadURL = s.url + iso.Filename
	}

	client := &http.Client{
		Timeout: 0, // No timeout for large downloads
	}

	resp, err := client.Get(downloadURL)
	if err != nil {
		return fmt.Errorf("starting download: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download failed with status %d", resp.StatusCode)
	}

	totalSize := resp.ContentLength

	// Ensure destination directory exists
	if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
		return fmt.Errorf("creating destination directory: %w", err)
	}

	// Create destination file
	dst, err := os.Create(destPath)
	if err != nil {
		return fmt.Errorf("creating destination file: %w", err)
	}
	defer dst.Close()

	// Copy with progress
	buf := make([]byte, 32*1024)
	var downloaded int64

	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			nw, werr := dst.Write(buf[:n])
			if werr != nil {
				return fmt.Errorf("writing: %w", werr)
			}
			if nw != n {
				return fmt.Errorf("short write")
			}
			downloaded += int64(nw)
			if progress != nil {
				progress(downloaded, totalSize)
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("reading: %w", err)
		}
	}

	return nil
}

// GetFileSize gets the size of a file via HEAD request
func (s *HTTPSource) GetFileSize(filename string) (int64, error) {
	fileURL := s.url + filename

	client := &http.Client{
		Timeout: 30 * time.Second,
	}

	resp, err := client.Head(fileURL)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("HEAD request failed with status %d", resp.StatusCode)
	}

	return resp.ContentLength, nil
}

// DownloadMD5 downloads the MD5 file for an ISO
func (s *HTTPSource) DownloadMD5(iso ISOFile) (string, error) {
	md5URL := iso.MD5FileURL
	if md5URL == "" {
		md5URL = s.url + iso.Filename + ".md5"
	}

	client := &http.Client{
		Timeout: 30 * time.Second,
	}

	resp, err := client.Get(md5URL)
	if err != nil {
		return "", fmt.Errorf("downloading MD5: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("MD5 download failed with status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("reading MD5: %w", err)
	}

	// Parse MD5 value
	content := strings.TrimSpace(string(body))
	parts := strings.Fields(content)
	if len(parts) < 1 {
		return "", fmt.Errorf("invalid MD5 file format")
	}

	return strings.ToLower(parts[0]), nil
}
