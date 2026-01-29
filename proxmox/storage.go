package proxmox

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/oliverwhk/versa-proxmox-deployer/ssh"
)

// StorageManager handles storage operations on Proxmox
type StorageManager struct {
	client *ssh.Client
}

// NewStorageManager creates a new storage manager
func NewStorageManager(client *ssh.Client) *StorageManager {
	return &StorageManager{client: client}
}

// ISOInfo holds information about an ISO on Proxmox
type ISOInfo struct {
	Storage  string
	Filename string
	Size     int64
	Path     string
}

// ListISOs lists ISO files in a storage
func (s *StorageManager) ListISOs(storage string) ([]ISOInfo, error) {
	// Get storage path
	result, err := s.client.Run(fmt.Sprintf("pvesm path %s:iso/dummy.iso 2>/dev/null | sed 's|/dummy.iso||'", storage))
	if err != nil {
		return nil, err
	}

	basePath := strings.TrimSpace(result.Stdout)
	if basePath == "" {
		// Try common paths
		basePath = fmt.Sprintf("/var/lib/vz/template/iso")
	}

	// List ISOs
	result, err = s.client.Run(fmt.Sprintf("ls -la %s/*.iso 2>/dev/null || true", basePath))
	if err != nil {
		return nil, err
	}

	var isos []ISOInfo
	for _, line := range strings.Split(result.Stdout, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "total") {
			continue
		}

		// Parse ls output
		fields := strings.Fields(line)
		if len(fields) >= 9 {
			filename := fields[len(fields)-1]
			if strings.HasSuffix(filename, ".iso") {
				isos = append(isos, ISOInfo{
					Storage:  storage,
					Filename: filepath.Base(filename),
					Path:     filename,
				})
			}
		}
	}

	return isos, nil
}

// ISOExists checks if an ISO exists in storage
func (s *StorageManager) ISOExists(storage, filename string) (bool, error) {
	result, err := s.client.Run(fmt.Sprintf("pvesm list %s --content iso 2>/dev/null | grep -q '%s'", storage, filename))
	if err != nil {
		return false, nil // Command failed, ISO doesn't exist
	}
	return result.ExitCode == 0, nil
}

// GetISOPath returns the full path to an ISO on Proxmox
func (s *StorageManager) GetISOPath(storage, filename string) (string, error) {
	result, err := s.client.Run(fmt.Sprintf("pvesm path %s:iso/%s", storage, filename))
	if err != nil {
		return "", err
	}

	path := strings.TrimSpace(result.Stdout)
	if path == "" {
		return "", fmt.Errorf("could not determine path for %s:iso/%s", storage, filename)
	}

	return path, nil
}

// GetISOStoragePath returns the base path for ISO storage
func (s *StorageManager) GetISOStoragePath(storage string) (string, error) {
	// Get path to a dummy ISO to extract the base path
	result, err := s.client.Run(fmt.Sprintf("pvesm path %s:iso/test.iso 2>/dev/null || echo '/var/lib/vz/template/iso/test.iso'", storage))
	if err != nil {
		return "/var/lib/vz/template/iso", nil
	}

	path := strings.TrimSpace(result.Stdout)
	return filepath.Dir(path), nil
}

// UploadISO uploads an ISO file to Proxmox storage
func (s *StorageManager) UploadISO(localPath, storage string, progress func(written, total int64)) error {
	filename := filepath.Base(localPath)

	// Get storage path
	storagePath, err := s.GetISOStoragePath(storage)
	if err != nil {
		return fmt.Errorf("getting storage path: %w", err)
	}

	remotePath := filepath.Join(storagePath, filename)

	// Upload via SCP
	if err := s.client.Upload(localPath, remotePath, progress); err != nil {
		return fmt.Errorf("uploading ISO: %w", err)
	}

	return nil
}

// VerifyISOMD5 verifies the MD5 checksum of an ISO on Proxmox
func (s *StorageManager) VerifyISOMD5(storage, filename, expectedMD5 string) (bool, error) {
	path, err := s.GetISOPath(storage, filename)
	if err != nil {
		return false, err
	}

	result, err := s.client.Run(fmt.Sprintf("md5sum %s", path))
	if err != nil {
		return false, err
	}

	// Parse MD5 from output "checksum  filename"
	parts := strings.Fields(result.Stdout)
	if len(parts) < 1 {
		return false, fmt.Errorf("could not parse MD5 output")
	}

	actualMD5 := strings.ToLower(parts[0])
	expectedMD5 = strings.ToLower(strings.TrimSpace(expectedMD5))

	return actualMD5 == expectedMD5, nil
}

// GetRemoteMD5 calculates MD5 of a file on Proxmox
func (s *StorageManager) GetRemoteMD5(remotePath string) (string, error) {
	result, err := s.client.Run(fmt.Sprintf("md5sum %s", remotePath))
	if err != nil {
		return "", err
	}

	parts := strings.Fields(result.Stdout)
	if len(parts) < 1 {
		return "", fmt.Errorf("could not parse MD5 output")
	}

	return strings.ToLower(parts[0]), nil
}

// CalculateLocalMD5 calculates MD5 of a local file
func CalculateLocalMD5(localPath string) (string, error) {
	f, err := os.Open(localPath)
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

// ReadMD5File reads an MD5 from a .md5 companion file
func ReadMD5File(md5FilePath string) (string, error) {
	data, err := os.ReadFile(md5FilePath)
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

// CreateMD5File creates a .md5 companion file for an ISO
func CreateMD5File(isoPath string) (string, error) {
	md5sum, err := CalculateLocalMD5(isoPath)
	if err != nil {
		return "", err
	}

	md5Path := isoPath + ".md5"
	content := fmt.Sprintf("%s  %s\n", md5sum, filepath.Base(isoPath))

	if err := os.WriteFile(md5Path, []byte(content), 0644); err != nil {
		return "", err
	}

	return md5sum, nil
}

// DeleteISO deletes an ISO from Proxmox storage
func (s *StorageManager) DeleteISO(storage, filename string) error {
	path, err := s.GetISOPath(storage, filename)
	if err != nil {
		return err
	}

	return s.client.RunQuiet(fmt.Sprintf("rm -f %s", path))
}

// GetStorageInfo returns detailed info about a storage
func (s *StorageManager) GetStorageInfo(storage string) (*StorageInfo, error) {
	// Parse text output from pvesm status (works on all Proxmox versions)
	result, err := s.client.Run("pvesm status")
	if err != nil {
		return nil, err
	}

	// Format: "Name             Type     Status           Total            Used       Available        %"
	lines := strings.Split(result.Stdout, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "Name") {
			continue
		}

		fields := strings.Fields(line)
		if len(fields) < 6 {
			continue
		}

		name := fields[0]
		if name != storage {
			continue
		}

		storageType := fields[1]
		status := fields[2]

		// Parse sizes (in KB)
		total, _ := strconv.ParseInt(fields[3], 10, 64)
		used, _ := strconv.ParseInt(fields[4], 10, 64)
		avail, _ := strconv.ParseInt(fields[5], 10, 64)

		// Get content from storage.cfg
		content := s.getStorageContent(name)

		return &StorageInfo{
			Name:        name,
			Type:        storageType,
			TotalGB:     int(total / (1024 * 1024)), // KB to GB
			UsedGB:      int(used / (1024 * 1024)),
			AvailableGB: int(avail / (1024 * 1024)),
			Content:     content,
			Active:      status == "active",
			Shared:      false,
		}, nil
	}

	return nil, fmt.Errorf("storage %s not found", storage)
}

// getStorageContent gets content types for a storage from config
func (s *StorageManager) getStorageContent(storageName string) []string {
	result, err := s.client.Run(fmt.Sprintf("grep -A 10 '^%s:' /etc/pve/storage.cfg 2>/dev/null | grep 'content' | head -1", storageName))
	if err != nil || result.ExitCode != 0 {
		return []string{"images", "rootdir"}
	}

	line := strings.TrimSpace(result.Stdout)
	if strings.HasPrefix(line, "content") {
		parts := strings.Fields(line)
		if len(parts) >= 2 {
			return strings.Split(parts[1], ",")
		}
	}

	return []string{"images"}
}

// EnsureStorageHasSpace checks if storage has enough space
func (s *StorageManager) EnsureStorageHasSpace(storage string, requiredGB int) error {
	info, err := s.GetStorageInfo(storage)
	if err != nil {
		return fmt.Errorf("getting storage info: %w", err)
	}

	if info.AvailableGB < requiredGB {
		return fmt.Errorf("storage %s has %dGB available but %dGB required", storage, info.AvailableGB, requiredGB)
	}

	return nil
}
