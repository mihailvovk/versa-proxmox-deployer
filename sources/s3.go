package sources

import (
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// S3Source represents an S3 bucket source for ISOs.
// Works with public buckets — no credentials needed.
type S3Source struct {
	name      string
	bucketURL string // e.g. https://bucket.s3.us-west-2.amazonaws.com/prefix/
	bucket    string
	prefix    string
	region    string
	baseURL   string // base URL for direct file downloads
}

// s3ListResult represents the S3 ListObjectsV2 XML response
type s3ListResult struct {
	XMLName               xml.Name   `xml:"ListBucketResult"`
	Contents              []s3Object `xml:"Contents"`
	IsTruncated           bool       `xml:"IsTruncated"`
	NextContinuationToken string     `xml:"NextContinuationToken"`
}

type s3Object struct {
	Key  string `xml:"Key"`
	Size int64  `xml:"Size"`
}

// NewS3Source creates a new S3 source from a bucket URL.
// Accepts URLs like:
//   - https://bucket.s3.region.amazonaws.com/prefix/
//   - https://s3.region.amazonaws.com/bucket/prefix/
//   - s3://bucket/prefix
func NewS3Source(rawURL, name string) (*S3Source, error) {
	s := &S3Source{name: name}

	if strings.HasPrefix(rawURL, "s3://") {
		// s3://bucket/prefix
		trimmed := strings.TrimPrefix(rawURL, "s3://")
		parts := strings.SplitN(trimmed, "/", 2)
		s.bucket = parts[0]
		if len(parts) > 1 {
			s.prefix = strings.TrimSuffix(parts[1], "/")
		}
		s.region = "us-east-1" // default, will be resolved
		s.baseURL = fmt.Sprintf("https://%s.s3.amazonaws.com/", s.bucket)
		if s.prefix != "" {
			s.baseURL += s.prefix + "/"
		}
		s.bucketURL = s.baseURL
		return s, nil
	}

	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("invalid S3 URL: %w", err)
	}

	host := parsed.Hostname()
	path := strings.TrimPrefix(parsed.Path, "/")
	path = strings.TrimSuffix(path, "/")
	// Strip index.html if user pasted the full index URL
	path = strings.TrimSuffix(path, "/index.html")
	path = strings.TrimSuffix(path, "index.html")

	if strings.Contains(host, ".s3.") || strings.Contains(host, ".s3-") {
		// Virtual-hosted style: bucket.s3.region.amazonaws.com
		dotIdx := strings.Index(host, ".s3.")
		if dotIdx < 0 {
			dotIdx = strings.Index(host, ".s3-")
		}
		s.bucket = host[:dotIdx]
		s.prefix = path
		s.region = extractS3Region(host)
	} else if strings.HasPrefix(host, "s3.") || strings.HasPrefix(host, "s3-") {
		// Path-style: s3.region.amazonaws.com/bucket/prefix
		parts := strings.SplitN(path, "/", 2)
		s.bucket = parts[0]
		if len(parts) > 1 {
			s.prefix = parts[1]
		}
		s.region = extractS3Region(host)
	} else {
		return nil, fmt.Errorf("unrecognized S3 URL format: %s", rawURL)
	}

	// Normalize base URL for downloads (virtual-hosted style)
	if s.region != "" {
		s.baseURL = fmt.Sprintf("https://%s.s3.%s.amazonaws.com/", s.bucket, s.region)
	} else {
		s.baseURL = fmt.Sprintf("https://%s.s3.amazonaws.com/", s.bucket)
	}
	if s.prefix != "" {
		s.baseURL += s.prefix + "/"
	}
	s.bucketURL = rawURL

	return s, nil
}

func extractS3Region(host string) string {
	// s3.us-west-2.amazonaws.com → us-west-2
	// bucket.s3.us-west-2.amazonaws.com → us-west-2
	parts := strings.Split(host, ".")
	for i, p := range parts {
		if (p == "s3" || strings.HasPrefix(p, "s3-")) && i+1 < len(parts) {
			next := parts[i+1]
			if next != "amazonaws" {
				return next
			}
		}
	}
	return ""
}

func (s *S3Source) Name() string { return s.name }
func (s *S3Source) Type() string { return string(SourceTypeS3) }
func (s *S3Source) URL() string  { return s.bucketURL }

// List lists all ISO files in the S3 bucket/prefix
func (s *S3Source) List() ([]ISOFile, error) {
	var isos []ISOFile
	md5Keys := make(map[string]bool)

	objects, err := s.listObjects()
	if err != nil {
		return nil, err
	}

	// First pass: find MD5 files
	for _, obj := range objects {
		filename := filepath.Base(obj.Key)
		if IsMD5File(filename) {
			md5Keys[GetISOForMD5(filename)] = true
		}
	}

	// Second pass: build ISO list
	for _, obj := range objects {
		filename := filepath.Base(obj.Key)
		if !IsISOFile(filename) {
			continue
		}

		fileURL := s.baseURL + filename
		iso := ParseISOFilename(filename, s.name, s.Type(), fileURL)
		iso.Size = obj.Size

		if md5Keys[filename] {
			iso.HasMD5File = true
			iso.MD5FileURL = s.baseURL + filename + ".md5"
		}

		isos = append(isos, iso)
	}

	return isos, nil
}

// listObjects fetches all objects from the S3 bucket with the configured prefix
func (s *S3Source) listObjects() ([]s3Object, error) {
	var all []s3Object
	continuationToken := ""

	client := &http.Client{Timeout: 30 * time.Second}

	for {
		listURL := fmt.Sprintf("https://%s.s3.%s.amazonaws.com/?list-type=2&prefix=%s",
			s.bucket, s.region, url.QueryEscape(s.prefix+"/"))
		if continuationToken != "" {
			listURL += "&continuation-token=" + url.QueryEscape(continuationToken)
		}

		resp, err := client.Get(listURL)
		if err != nil {
			return nil, fmt.Errorf("listing S3 objects: %w", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			return nil, fmt.Errorf("S3 list failed (status %d): %s", resp.StatusCode, string(body))
		}

		var result s3ListResult
		if err := xml.NewDecoder(resp.Body).Decode(&result); err != nil {
			return nil, fmt.Errorf("parsing S3 response: %w", err)
		}

		all = append(all, result.Contents...)

		if !result.IsTruncated {
			break
		}
		continuationToken = result.NextContinuationToken
	}

	return all, nil
}

// Download downloads an ISO from S3
func (s *S3Source) Download(iso ISOFile, destPath string, progress func(downloaded, total int64)) error {
	downloadURL := iso.SourceURL
	if downloadURL == "" {
		downloadURL = s.baseURL + iso.Filename
	}

	client := &http.Client{Timeout: 0}
	resp, err := client.Get(downloadURL)
	if err != nil {
		return fmt.Errorf("starting download: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download failed with status %d", resp.StatusCode)
	}

	totalSize := resp.ContentLength
	if totalSize <= 0 && iso.Size > 0 {
		totalSize = iso.Size
	}

	if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
		return fmt.Errorf("creating destination directory: %w", err)
	}

	dst, err := os.Create(destPath)
	if err != nil {
		return fmt.Errorf("creating destination file: %w", err)
	}
	defer dst.Close()

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

// DownloadMD5 downloads the MD5 file for an ISO from S3
func (s *S3Source) DownloadMD5(iso ISOFile) (string, error) {
	md5URL := iso.MD5FileURL
	if md5URL == "" {
		md5URL = s.baseURL + iso.Filename + ".md5"
	}

	client := &http.Client{Timeout: 30 * time.Second}
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

	content := strings.TrimSpace(string(body))
	parts := strings.Fields(content)
	if len(parts) < 1 {
		return "", fmt.Errorf("invalid MD5 file format")
	}

	return strings.ToLower(parts[0]), nil
}
