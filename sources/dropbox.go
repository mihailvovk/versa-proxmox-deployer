package sources

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// DropboxSource represents a Dropbox shared folder source for ISOs
type DropboxSource struct {
	name string
	url  string
}

// NewDropboxSource creates a new Dropbox source
func NewDropboxSource(url, name string) *DropboxSource {
	return &DropboxSource{
		name: name,
		url:  url,
	}
}

// Name returns the source name
func (s *DropboxSource) Name() string {
	return s.name
}

// Type returns the source type
func (s *DropboxSource) Type() string {
	return string(SourceTypeDropbox)
}

// URL returns the source URL
func (s *DropboxSource) URL() string {
	return s.url
}

// dropboxFolderEntry represents a file entry from the Dropbox internal API
type dropboxFolderEntry struct {
	Bytes    int64  `json:"bytes"`
	FileID   string `json:"file_id"`
	Filename string `json:"filename"`
	Href     string `json:"href"`
	IsDir    bool   `json:"is_dir"`
}

// dropboxFolderResponse represents the response from list_shared_link_folder_entries
type dropboxFolderResponse struct {
	Entries []dropboxFolderEntry `json:"entries"`
}

// parseSharedFolderURL extracts link_key, secure_hash, and rlkey from a Dropbox shared folder URL
func parseSharedFolderURL(rawURL string) (linkKey, secureHash, rlkey string, err error) {
	// URL format: https://www.dropbox.com/scl/fo/<link_key>/<secure_hash>?rlkey=<rlkey>&dl=0
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", "", "", fmt.Errorf("parsing URL: %w", err)
	}

	// Extract path components
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	// Expected: scl/fo/<link_key>/<secure_hash>
	if len(parts) < 4 || parts[0] != "scl" || parts[1] != "fo" {
		return "", "", "", fmt.Errorf("unexpected Dropbox folder URL format: %s", u.Path)
	}

	linkKey = parts[2]
	secureHash = parts[3]
	rlkey = u.Query().Get("rlkey")

	return linkKey, secureHash, rlkey, nil
}

// List returns all ISO files in the Dropbox folder using the internal API
func (s *DropboxSource) List() ([]ISOFile, error) {
	linkKey, secureHash, rlkey, err := parseSharedFolderURL(s.url)
	if err != nil {
		return nil, fmt.Errorf("parsing Dropbox URL: %w", err)
	}

	// Step 1: Fetch the shared folder page to get session cookies and CSRF token
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, fmt.Errorf("creating cookie jar: %w", err)
	}

	client := &http.Client{
		Jar:     jar,
		Timeout: 30 * time.Second,
	}

	req, err := http.NewRequest("GET", s.url, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching Dropbox page: %w", err)
	}
	// Read and discard the body (we only need cookies)
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Dropbox returned status %d", resp.StatusCode)
	}

	// Extract CSRF token from the "t" cookie
	u, _ := url.Parse("https://www.dropbox.com")
	var csrfToken string
	for _, c := range jar.Cookies(u) {
		if c.Name == "t" {
			csrfToken = c.Value
			break
		}
	}
	if csrfToken == "" {
		return nil, fmt.Errorf("could not extract CSRF token from Dropbox cookies")
	}

	// Step 2: Call the internal API to list folder entries
	apiURL := "https://www.dropbox.com/list_shared_link_folder_entries"

	formData := url.Values{}
	formData.Set("link_key", linkKey)
	formData.Set("link_type", "s")
	formData.Set("secure_hash", secureHash)
	formData.Set("sub_path", "")
	formData.Set("is_dir", "true")
	if rlkey != "" {
		formData.Set("rlkey", rlkey)
	}
	formData.Set("t", csrfToken)

	apiReq, err := http.NewRequest("POST", apiURL, strings.NewReader(formData.Encode()))
	if err != nil {
		return nil, fmt.Errorf("creating API request: %w", err)
	}
	apiReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	apiReq.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	apiReq.Header.Set("Origin", "https://www.dropbox.com")
	apiReq.Header.Set("Referer", s.url)
	apiReq.Header.Set("X-Requested-With", "XMLHttpRequest")

	apiResp, err := client.Do(apiReq)
	if err != nil {
		return nil, fmt.Errorf("calling Dropbox internal API: %w", err)
	}
	defer apiResp.Body.Close()

	if apiResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(apiResp.Body)
		return nil, fmt.Errorf("Dropbox API returned status %d: %s", apiResp.StatusCode, truncate(string(body), 200))
	}

	var folderResp dropboxFolderResponse
	if err := json.NewDecoder(apiResp.Body).Decode(&folderResp); err != nil {
		return nil, fmt.Errorf("decoding API response: %w", err)
	}

	// Step 3: Process entries into ISOFile list
	md5Files := make(map[string]dropboxFolderEntry) // ISO filename -> MD5 entry
	var isoEntries []dropboxFolderEntry

	for _, entry := range folderResp.Entries {
		if entry.IsDir {
			continue
		}
		if IsMD5File(entry.Filename) {
			isoName := GetISOForMD5(entry.Filename)
			md5Files[isoName] = entry
		} else if IsISOFile(entry.Filename) {
			isoEntries = append(isoEntries, entry)
		}
	}

	var isos []ISOFile
	for _, entry := range isoEntries {
		iso := ParseISOFilename(entry.Filename, s.name, s.Type(), "")
		iso.Size = entry.Bytes
		iso.SourceURL = buildFileDownloadURL(entry.Href)

		if md5Entry, ok := md5Files[entry.Filename]; ok {
			iso.HasMD5File = true
			iso.MD5FileURL = buildFileDownloadURL(md5Entry.Href)
		}

		isos = append(isos, iso)
	}

	return isos, nil
}

// buildFileDownloadURL converts a Dropbox file href to a direct download URL
func buildFileDownloadURL(href string) string {
	if href == "" {
		return ""
	}
	// Convert dl=0 to dl=1 for direct download
	if strings.Contains(href, "dl=0") {
		return strings.Replace(href, "dl=0", "dl=1", 1)
	}
	if strings.Contains(href, "?") {
		return href + "&dl=1"
	}
	return href + "?dl=1"
}

// Download downloads an ISO from Dropbox
func (s *DropboxSource) Download(iso ISOFile, destPath string, progress func(downloaded, total int64)) error {
	downloadURL := iso.SourceURL
	if downloadURL == "" {
		return fmt.Errorf("no download URL for %s", iso.Filename)
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
	if totalSize <= 0 {
		totalSize = iso.Size // Use size from listing
	}

	if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
		return fmt.Errorf("creating destination directory: %w", err)
	}

	dst, err := os.Create(destPath)
	if err != nil {
		return fmt.Errorf("creating destination file: %w", err)
	}
	defer dst.Close()

	buf := make([]byte, 256*1024) // 256KB buffer for better throughput
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

// DownloadMD5 downloads the MD5 file for an ISO
func (s *DropboxSource) DownloadMD5(iso ISOFile) (string, error) {
	if !iso.HasMD5File || iso.MD5FileURL == "" {
		return "", fmt.Errorf("no MD5 file available")
	}

	client := &http.Client{
		Timeout: 30 * time.Second,
	}

	resp, err := client.Get(iso.MD5FileURL)
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

	content := strings.TrimSpace(string(body))
	parts := strings.Fields(content)
	if len(parts) < 1 {
		return "", fmt.Errorf("invalid MD5 file format")
	}

	return strings.ToLower(parts[0]), nil
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
