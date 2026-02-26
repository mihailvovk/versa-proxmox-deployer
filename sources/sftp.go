package sources

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/mihailvovk/versa-proxmox-deployer/config"
	"github.com/mihailvovk/versa-proxmox-deployer/ssh"
	"github.com/pkg/sftp"
	gossh "golang.org/x/crypto/ssh"
)

// SFTPSource represents an SFTP source for ISOs
type SFTPSource struct {
	name      string
	url       string
	sftpCfg   *SFTPConfig
	sshClient *ssh.Client
	path      string
}

// NewSFTPSource creates a new SFTP source
func NewSFTPSource(urlStr, name string) (*SFTPSource, error) {
	cfg, err := parseSFTPURL(urlStr)
	if err != nil {
		return nil, err
	}

	return &SFTPSource{
		name:    name,
		url:     urlStr,
		sftpCfg: cfg,
		path:    cfg.Path,
	}, nil
}

// Name returns the source name
func (s *SFTPSource) Name() string {
	return s.name
}

// Type returns the source type
func (s *SFTPSource) Type() string {
	return string(SourceTypeSFTP)
}

// URL returns the source URL
func (s *SFTPSource) URL() string {
	return s.url
}

// SetSSHKey sets the SSH key path for authentication
func (s *SFTPSource) SetSSHKey(keyPath string) {
	s.sftpCfg.KeyPath = config.ExpandPath(keyPath)
}

// SetPassword sets the password for authentication
func (s *SFTPSource) SetPassword(password string) {
	s.sftpCfg.Password = password
}

// connect establishes an SFTP connection
func (s *SFTPSource) connect() (*sftp.Client, func(), error) {
	// If we have an existing SSH client, use it
	if s.sshClient != nil {
		// Get the underlying connection
		client, err := s.sshClient.Run("echo connected")
		if err != nil {
			return nil, nil, fmt.Errorf("SSH connection test failed: %w", err)
		}
		_ = client // Just testing connection
	}

	// Build SSH config
	var authMethods []gossh.AuthMethod

	if s.sftpCfg.KeyPath != "" {
		keyAuth, err := ssh.KeyAuth(s.sftpCfg.KeyPath, "")
		if err != nil {
			return nil, nil, fmt.Errorf("loading SSH key: %w", err)
		}
		authMethods = append(authMethods, keyAuth)
	}

	if s.sftpCfg.Password != "" {
		authMethods = append(authMethods, gossh.Password(s.sftpCfg.Password))
	}

	if len(authMethods) == 0 {
		// Try default key
		if defaultKey := ssh.FindDefaultKey(); defaultKey != "" {
			keyAuth, err := ssh.KeyAuth(defaultKey, "")
			if err == nil {
				authMethods = append(authMethods, keyAuth)
			}
		}
	}

	if len(authMethods) == 0 {
		return nil, nil, fmt.Errorf("no authentication method available")
	}

	sshConfig := &gossh.ClientConfig{
		User:            s.sftpCfg.User,
		Auth:            authMethods,
		HostKeyCallback: gossh.InsecureIgnoreHostKey(),
	}

	// Connect SSH
	addr := fmt.Sprintf("%s:%s", s.sftpCfg.Host, s.sftpCfg.Port)
	sshConn, err := gossh.Dial("tcp", addr, sshConfig)
	if err != nil {
		return nil, nil, fmt.Errorf("SSH connection failed: %w", err)
	}

	// Create SFTP client
	sftpClient, err := sftp.NewClient(sshConn)
	if err != nil {
		sshConn.Close()
		return nil, nil, fmt.Errorf("SFTP client creation failed: %w", err)
	}

	cleanup := func() {
		sftpClient.Close()
		sshConn.Close()
	}

	return sftpClient, cleanup, nil
}

// List returns all ISO files in the SFTP directory (recursive)
func (s *SFTPSource) List() ([]ISOFile, error) {
	client, cleanup, err := s.connect()
	if err != nil {
		return nil, err
	}
	defer cleanup()

	// Collect MD5 files and ISOs recursively
	md5Files := make(map[string]string) // ISO filename -> MD5 file full path
	var isos []ISOFile

	// Walk the directory tree
	err = s.walkDir(client, s.path, func(path string, info os.FileInfo) {
		name := info.Name()
		if IsMD5File(name) {
			isoName := GetISOForMD5(name)
			md5Files[isoName] = path
		} else if IsISOFile(name) {
			iso := ParseISOFilename(name, s.name, s.Type(), path)
			iso.Size = info.Size()
			isos = append(isos, iso)
		}
	})
	if err != nil {
		return nil, fmt.Errorf("walking directory: %w", err)
	}

	// Match MD5 files with ISOs
	for i := range isos {
		if md5Path, ok := md5Files[isos[i].Filename]; ok {
			isos[i].HasMD5File = true
			isos[i].MD5FileURL = md5Path

			// Try to read MD5 value
			if md5, err := s.readRemoteMD5(client, md5Path); err == nil {
				isos[i].MD5 = md5
			}
		}
	}

	return isos, nil
}

// walkDir recursively walks a directory via SFTP
func (s *SFTPSource) walkDir(client *sftp.Client, path string, fn func(path string, info os.FileInfo)) error {
	entries, err := client.ReadDir(path)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		fullPath := path + "/" + entry.Name()
		if entry.IsDir() {
			// Recurse into subdirectory
			s.walkDir(client, fullPath, fn)
		} else {
			fn(fullPath, entry)
		}
	}

	return nil
}

// readRemoteMD5 reads an MD5 file from the SFTP server
func (s *SFTPSource) readRemoteMD5(client *sftp.Client, path string) (string, error) {
	f, err := client.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	data, err := io.ReadAll(f)
	if err != nil {
		return "", err
	}

	content := strings.TrimSpace(string(data))
	parts := strings.Fields(content)
	if len(parts) < 1 {
		return "", fmt.Errorf("invalid MD5 file format")
	}

	return strings.ToLower(parts[0]), nil
}

// Download downloads an ISO from SFTP
func (s *SFTPSource) Download(iso ISOFile, destPath string, progress func(downloaded, total int64)) error {
	client, cleanup, err := s.connect()
	if err != nil {
		return err
	}
	defer cleanup()

	remotePath := iso.SourceURL
	if remotePath == "" {
		remotePath = s.path + "/" + iso.Filename
	}

	// Open remote file
	srcFile, err := client.Open(remotePath)
	if err != nil {
		return fmt.Errorf("opening remote file: %w", err)
	}
	defer srcFile.Close()

	// Get file size
	info, err := srcFile.Stat()
	if err != nil {
		return fmt.Errorf("getting file info: %w", err)
	}
	totalSize := info.Size()

	// Ensure destination directory exists
	if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
		return fmt.Errorf("creating destination directory: %w", err)
	}

	// Create destination file
	dstFile, err := os.Create(destPath)
	if err != nil {
		return fmt.Errorf("creating destination file: %w", err)
	}
	defer dstFile.Close()

	// Copy with progress
	buf := make([]byte, 32*1024)
	var downloaded int64

	for {
		n, err := srcFile.Read(buf)
		if n > 0 {
			nw, werr := dstFile.Write(buf[:n])
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

// GetMD5 reads the MD5 for an ISO from the SFTP server
func (s *SFTPSource) GetMD5(isoFilename string) (string, error) {
	client, cleanup, err := s.connect()
	if err != nil {
		return "", err
	}
	defer cleanup()

	md5Path := s.path + "/" + isoFilename + ".md5"
	return s.readRemoteMD5(client, md5Path)
}
