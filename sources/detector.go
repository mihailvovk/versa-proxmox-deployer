package sources

import (
	"fmt"
	"os"
	"strings"

	"github.com/oliverwhk/versa-proxmox-deployer/config"
	"github.com/oliverwhk/versa-proxmox-deployer/ssh"
)

// SourceType represents the type of image source
type SourceType string

const (
	SourceTypeDropbox SourceType = "dropbox"
	SourceTypeHTTP    SourceType = "http"
	SourceTypeSFTP    SourceType = "sftp"
	SourceTypeLocal   SourceType = "local"
)

// DetectSourceType detects the source type from a URL or path
func DetectSourceType(url string) SourceType {
	lower := strings.ToLower(url)

	switch {
	case strings.Contains(lower, "dropbox.com"):
		return SourceTypeDropbox
	case strings.HasPrefix(lower, "sftp://"):
		return SourceTypeSFTP
	case strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://"):
		return SourceTypeHTTP
	case strings.HasPrefix(url, "/") || strings.HasPrefix(url, "~"):
		return SourceTypeLocal
	default:
		// Check if it's a local path that exists
		expanded := config.ExpandPath(url)
		if _, err := os.Stat(expanded); err == nil {
			return SourceTypeLocal
		}
		// Default to HTTP
		return SourceTypeHTTP
	}
}

// CreateSource creates an ImageSource from configuration
func CreateSource(src config.ImageSource) (ImageSource, error) {
	sourceType := SourceType(src.Type)
	if sourceType == "" {
		sourceType = DetectSourceType(src.URL)
	}

	name := src.Name
	if name == "" {
		name = src.URL
		// Truncate long URLs
		if len(name) > 40 {
			name = name[:37] + "..."
		}
	}

	switch sourceType {
	case SourceTypeDropbox:
		return NewDropboxSource(src.URL, name), nil

	case SourceTypeHTTP:
		return NewHTTPSource(src.URL, name), nil

	case SourceTypeSFTP:
		sftpSrc, err := NewSFTPSource(src.URL, name)
		if err != nil {
			return nil, err
		}
		if src.SSHKey != "" {
			sftpSrc.SetSSHKey(src.SSHKey)
		}
		if src.Password != "" {
			sftpSrc.SetPassword(src.Password)
		}
		return sftpSrc, nil

	case SourceTypeLocal:
		return NewLocalSource(src.URL, name), nil

	default:
		return nil, fmt.Errorf("unknown source type: %s", sourceType)
	}
}

// CreateSourcesFromConfig creates ImageSources from config
func CreateSourcesFromConfig(cfg *config.Config) ([]ImageSource, error) {
	var sources []ImageSource

	// If no sources configured, return empty list — user must add sources
	if len(cfg.ImageSources) == 0 {
		return sources, nil
	}

	for _, src := range cfg.ImageSources {
		source, err := CreateSource(src)
		if err != nil {
			// Log error but continue with other sources
			continue
		}
		sources = append(sources, source)
	}

	return sources, nil
}

// TestSourceConnection tests if a source is accessible
func TestSourceConnection(source ImageSource) error {
	_, err := source.List()
	return err
}

// ValidateSourceURL validates a source URL
func ValidateSourceURL(url string) error {
	sourceType := DetectSourceType(url)

	switch sourceType {
	case SourceTypeDropbox:
		if !strings.Contains(url, "dropbox.com") {
			return fmt.Errorf("invalid Dropbox URL")
		}
		return nil

	case SourceTypeHTTP:
		if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
			return fmt.Errorf("invalid HTTP URL: must start with http:// or https://")
		}
		return nil

	case SourceTypeSFTP:
		if !strings.HasPrefix(url, "sftp://") {
			return fmt.Errorf("invalid SFTP URL: must start with sftp://")
		}
		// Parse to validate format
		_, err := parseSFTPURL(url)
		return err

	case SourceTypeLocal:
		expanded := config.ExpandPath(url)
		info, err := os.Stat(expanded)
		if err != nil {
			return fmt.Errorf("path does not exist: %s", expanded)
		}
		if !info.IsDir() {
			return fmt.Errorf("path is not a directory: %s", expanded)
		}
		return nil

	default:
		return fmt.Errorf("unknown source type")
	}
}

// SFTPConfig holds parsed SFTP connection config
type SFTPConfig struct {
	User     string
	Host     string
	Port     string
	Path     string
	Password string
	KeyPath  string
}

// parseSFTPURL parses an SFTP URL into components
func parseSFTPURL(url string) (*SFTPConfig, error) {
	// Format: sftp://user@host:port/path or sftp://user@host/path
	url = strings.TrimPrefix(url, "sftp://")

	cfg := &SFTPConfig{
		Port: "22",
	}

	// Split user@host/path
	atIdx := strings.Index(url, "@")
	if atIdx > 0 {
		cfg.User = url[:atIdx]
		url = url[atIdx+1:]
	}

	// Split host:port/path or host/path
	slashIdx := strings.Index(url, "/")
	if slashIdx > 0 {
		hostPort := url[:slashIdx]
		cfg.Path = url[slashIdx:]

		colonIdx := strings.Index(hostPort, ":")
		if colonIdx > 0 {
			cfg.Host = hostPort[:colonIdx]
			cfg.Port = hostPort[colonIdx+1:]
		} else {
			cfg.Host = hostPort
		}
	} else {
		cfg.Host = url
		cfg.Path = "/"
	}

	if cfg.Host == "" {
		return nil, fmt.Errorf("host is required in SFTP URL")
	}

	return cfg, nil
}

// NewSFTPSourceFromSSHClient creates an SFTP source using an existing SSH client
func NewSFTPSourceFromSSHClient(client *ssh.Client, path, name string) *SFTPSource {
	return &SFTPSource{
		name:      name,
		url:       fmt.Sprintf("sftp://%s@%s%s", client.User(), client.Host(), path),
		sshClient: client,
		path:      path,
	}
}
