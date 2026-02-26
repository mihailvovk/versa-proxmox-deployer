package proxmox

import (
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/mihailvovk/versa-proxmox-deployer/ssh"
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
	result, err := s.client.Run("pvesm path " + ssh.ShellEscape(storage+":iso/dummy.iso") + " 2>/dev/null | sed 's|/dummy.iso||'")
	if err != nil {
		return nil, err
	}

	basePath := strings.TrimSpace(result.Stdout)
	if basePath == "" {
		// Try common paths
		basePath = fmt.Sprintf("/var/lib/vz/template/iso")
	}

	// List ISOs
	result, err = s.client.Run("find " + ssh.ShellEscape(basePath) + " -maxdepth 1 -name '*.iso' -exec ls -la {} + 2>/dev/null || true")
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

// ISOExists checks if an ISO exists in storage by looking for the file on disk.
// Falls back to pvesm list if the storage path can't be resolved.
func (s *StorageManager) ISOExists(storage, filename string) (bool, error) {
	// First try: check file directly on the filesystem (most reliable)
	storagePath, err := s.GetISOStoragePath(storage)
	if err == nil && storagePath != "" {
		remotePath := storagePath + "/" + filename
		result, err := s.client.Run("test -f " + ssh.ShellEscape(remotePath))
		if err != nil {
			return false, fmt.Errorf("checking ISO existence: %w", err)
		}
		if result.ExitCode == 0 {
			return true, nil
		}
	}

	// Fallback: pvesm list (may not work on all storage types)
	result, err := s.client.Run("pvesm list " + ssh.ShellEscape(storage) + " --content iso 2>/dev/null | grep -qF " + ssh.ShellEscape(filename))
	if err != nil {
		return false, fmt.Errorf("checking ISO existence: %w", err)
	}
	return result.ExitCode == 0, nil
}

// ISOExistsOnAny checks if an ISO exists on any ISO-capable storage.
// Returns the storage name where it was found, or empty string if not found.
func (s *StorageManager) ISOExistsOnAny(storages []StorageInfo, filename string) (string, error) {
	for _, stor := range storages {
		found, err := s.ISOExists(stor.Name, filename)
		if err != nil {
			// SSH error — try next storage
			continue
		}
		if found {
			return stor.Name, nil
		}
	}

	// Last resort: check the common default ISO path directly
	result, err := s.client.Run("test -f " + ssh.ShellEscape("/var/lib/vz/template/iso/"+filename))
	if err == nil && result.ExitCode == 0 {
		return "local", nil
	}

	return "", nil
}

// FindISOByMD5 searches all ISO-capable storages for an ISO matching the given
// MD5 checksum. Returns the storage name and filename if found.
// This is used to detect when the same image exists under a different filename.
func (s *StorageManager) FindISOByMD5(storages []StorageInfo, expectedMD5 string) (storage, filename string, err error) {
	if expectedMD5 == "" {
		return "", "", fmt.Errorf("no MD5 provided")
	}
	expectedMD5 = strings.ToLower(strings.TrimSpace(expectedMD5))

	for _, stor := range storages {
		isos, err := s.ListISOs(stor.Name)
		if err != nil {
			continue
		}
		if len(isos) == 0 {
			continue
		}

		// Build a single md5sum command for all ISOs on this storage to avoid N round-trips
		var paths []string
		for _, iso := range isos {
			paths = append(paths, ssh.ShellEscape(iso.Path))
		}
		cmd := "md5sum " + strings.Join(paths, " ") + " 2>/dev/null"
		result, err := s.client.RunWithTimeout(cmd, 10*time.Minute)
		if err != nil || result.ExitCode != 0 {
			continue
		}

		for _, line := range strings.Split(result.Stdout, "\n") {
			parts := strings.Fields(line)
			if len(parts) >= 2 {
				md5 := strings.ToLower(parts[0])
				if md5 == expectedMD5 {
					return stor.Name, filepath.Base(parts[1]), nil
				}
			}
		}
	}

	return "", "", fmt.Errorf("no ISO with MD5 %s found", expectedMD5)
}

// GetISOPath returns the full path to an ISO on Proxmox
func (s *StorageManager) GetISOPath(storage, filename string) (string, error) {
	result, err := s.client.Run("pvesm path " + ssh.ShellEscape(storage+":iso/"+filename))
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
	result, err := s.client.Run("pvesm path " + ssh.ShellEscape(storage+":iso/test.iso") + " 2>/dev/null || echo '/var/lib/vz/template/iso/test.iso'")
	if err != nil {
		return "/var/lib/vz/template/iso", nil
	}

	remotePath := strings.TrimSpace(result.Stdout)
	return path.Dir(remotePath), nil
}

// UploadISO uploads an ISO file to Proxmox storage
func (s *StorageManager) UploadISO(localPath, storage string, progress func(written, total int64)) error {
	filename := filepath.Base(localPath)

	// Get storage path
	storagePath, err := s.GetISOStoragePath(storage)
	if err != nil {
		return fmt.Errorf("getting storage path: %w", err)
	}

	remotePath := storagePath + "/" + filename

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

	result, err := s.client.Run("md5sum " + ssh.ShellEscape(path))
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
	result, err := s.client.Run("md5sum " + ssh.ShellEscape(remotePath))
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

// DownloadISOFromURL downloads an ISO directly on Proxmox using the native
// pvesh download-url API (PVE 7.0+). pvesh blocks until the download finishes,
// so we run it in the background via nohup and poll the Proxmox task list.
// The optional log callback receives progress messages.
func (s *StorageManager) DownloadISOFromURL(node, storage, filename, downloadURL string, log func(string)) error {
	if log == nil {
		log = func(string) {}
	}

	// Start pvesh in the background (it blocks until download completes,
	// and we don't want our SSH timeout to kill it via broken pipe).
	// --verify-certificates 0 skips SSL verification for enterprise SSL decryption.
	cmd := fmt.Sprintf(
		"nohup pvesh create /nodes/%s/storage/%s/download-url --content iso --filename %s --url %s --verify-certificates 0 >/dev/null 2>&1 & echo started",
		ssh.ShellEscape(node),
		ssh.ShellEscape(storage),
		ssh.ShellEscape(filename),
		ssh.ShellEscape(downloadURL),
	)
	result, err := s.client.RunWithTimeout(cmd, 30*time.Second)
	if err != nil {
		return fmt.Errorf("starting pvesh download-url: %w", err)
	}
	if !strings.Contains(result.Stdout, "started") {
		return fmt.Errorf("failed to start pvesh background task: %s", result.Stdout)
	}

	log("Proxmox download task submitted, waiting for it to register...")

	// Wait for the task to register in Proxmox
	time.Sleep(5 * time.Second)

	// Find the active download task UPID from the task list
	upid, err := s.findDownloadTask(node, filename)
	if err != nil {
		return fmt.Errorf("download task did not start: %w", err)
	}
	log(fmt.Sprintf("Download task started (UPID: %s)", upid))

	// Poll task status until completion, reading task log for progress
	deadline := time.Now().Add(2 * time.Hour)
	lastLogLine := 0
	for {
		if time.Now().After(deadline) {
			return fmt.Errorf("download timed out after 2 hours (UPID: %s)", upid)
		}

		time.Sleep(10 * time.Second)

		// Check task status
		statusCmd := fmt.Sprintf("pvesh get /nodes/%s/tasks/%s/status --output-format json",
			ssh.ShellEscape(node),
			ssh.ShellEscape(upid),
		)
		statusResult, err := s.client.Run(statusCmd)
		if err != nil {
			continue
		}
		if statusResult.ExitCode != 0 {
			continue
		}

		var status struct {
			Status     string `json:"status"`
			ExitStatus string `json:"exitstatus"`
		}
		if err := json.Unmarshal([]byte(statusResult.Stdout), &status); err != nil {
			continue
		}

		// Read task log for progress info (wget output shows % and speed)
		logCmd := fmt.Sprintf("pvesh get /nodes/%s/tasks/%s/log --output-format json --start %d --limit 50 2>/dev/null",
			ssh.ShellEscape(node),
			ssh.ShellEscape(upid),
			lastLogLine,
		)
		logResult, err := s.client.Run(logCmd)
		if err == nil && logResult.ExitCode == 0 {
			var logEntries []struct {
				N int    `json:"n"`
				T string `json:"t"`
			}
			if json.Unmarshal([]byte(logResult.Stdout), &logEntries) == nil {
				for _, entry := range logEntries {
					if entry.N > lastLogLine {
						lastLogLine = entry.N
					}
					line := strings.TrimSpace(entry.T)
					if line == "" {
						continue
					}
					// Log wget progress lines (contain %) and key status lines
					if strings.Contains(line, "%") || strings.Contains(line, "downloading") ||
						strings.Contains(line, "Saving to") || strings.Contains(line, "Length:") ||
						strings.Contains(line, "ERROR") || strings.Contains(line, "error") {
						log(fmt.Sprintf("Proxmox: %s", line))
					}
				}
			}
		}

		if status.Status == "stopped" {
			if status.ExitStatus == "OK" {
				return nil
			}
			return fmt.Errorf("download task failed: %s", status.ExitStatus)
		}
		// status is "running" — keep polling
	}
}

// findDownloadTask searches active and recent Proxmox tasks for a download
// task matching the given filename. Retries a few times since the task may
// take a moment to appear.
func (s *StorageManager) findDownloadTask(node, filename string) (string, error) {
	for attempt := 0; attempt < 5; attempt++ {
		if attempt > 0 {
			time.Sleep(3 * time.Second)
		}

		// Check active tasks first, then recent tasks
		cmd := fmt.Sprintf("pvesh get /nodes/%s/tasks --output-format json --limit 20 2>/dev/null",
			ssh.ShellEscape(node))
		result, err := s.client.Run(cmd)
		if err != nil || result.ExitCode != 0 {
			continue
		}

		var tasks []struct {
			UPID   string `json:"upid"`
			Type   string `json:"type"`
			Status string `json:"status"`
		}
		if err := json.Unmarshal([]byte(result.Stdout), &tasks); err != nil {
			continue
		}

		// Look for a running download task (prefer running over recently stopped)
		for _, t := range tasks {
			if t.Type == "download" || t.Type == "imgdownload" {
				// Verify this task is for our file by checking the task log
				logCmd := fmt.Sprintf("pvesh get /nodes/%s/tasks/%s/log --output-format json --limit 5 2>/dev/null",
					ssh.ShellEscape(node), ssh.ShellEscape(t.UPID))
				logResult, err := s.client.Run(logCmd)
				if err != nil {
					// Can't verify, but if it's the only download task, use it
					return t.UPID, nil
				}
				if strings.Contains(logResult.Stdout, filename) {
					return t.UPID, nil
				}
			}
		}
	}

	return "", fmt.Errorf("no download task found for %s", filename)
}

// DownloadISODirect downloads an ISO directly on Proxmox using wget or curl
// as a fallback when the pvesh download-url API is unavailable or fails.
func (s *StorageManager) DownloadISODirect(storage, filename, downloadURL string, expectedSize int64) error {
	// Resolve the storage path
	storagePath, err := s.GetISOStoragePath(storage)
	if err != nil {
		return fmt.Errorf("resolving storage path: %w", err)
	}
	destPath := storagePath + "/" + filename

	// Ensure the target directory exists
	s.client.Run("mkdir -p " + ssh.ShellEscape(storagePath))

	// Detect available download tool
	tool, err := s.detectDownloadTool()
	if err != nil {
		return err
	}

	// Build download command (skip SSL verification for enterprise SSL decryption)
	var cmd string
	switch tool {
	case "wget":
		cmd = fmt.Sprintf("wget -q --no-check-certificate -O %s %s", ssh.ShellEscape(destPath), ssh.ShellEscape(downloadURL))
	case "curl":
		cmd = fmt.Sprintf("curl -ksfL -o %s %s", ssh.ShellEscape(destPath), ssh.ShellEscape(downloadURL))
	}

	// Run with a generous timeout (2 hours for large ISOs)
	result, err := s.client.RunWithTimeout(cmd, 2*time.Hour)
	if err != nil {
		// Clean up partial file
		s.client.Run("rm -f " + ssh.ShellEscape(destPath))
		return fmt.Errorf("%s download failed: %w", tool, err)
	}
	if result.ExitCode != 0 {
		s.client.Run("rm -f " + ssh.ShellEscape(destPath))
		output := strings.TrimSpace(result.Stderr)
		if output == "" {
			output = strings.TrimSpace(result.Stdout)
		}
		return fmt.Errorf("%s download failed (exit %d): %s", tool, result.ExitCode, output)
	}

	// Verify the file exists and is not suspiciously small
	checkResult, err := s.client.Run("stat -c '%s' " + ssh.ShellEscape(destPath) + " 2>/dev/null || stat -f '%z' " + ssh.ShellEscape(destPath))
	if err != nil {
		s.client.Run("rm -f " + ssh.ShellEscape(destPath))
		return fmt.Errorf("verifying downloaded file: %w", err)
	}
	sizeStr := strings.TrimSpace(checkResult.Stdout)
	fileSize, _ := strconv.ParseInt(sizeStr, 10, 64)
	if fileSize < 1024*1024 {
		s.client.Run("rm -f " + ssh.ShellEscape(destPath))
		return fmt.Errorf("downloaded file too small (%d bytes), likely failed", fileSize)
	}

	return nil
}

// detectDownloadTool checks whether wget or curl is available on the Proxmox host.
func (s *StorageManager) detectDownloadTool() (string, error) {
	for _, tool := range []string{"wget", "curl"} {
		result, err := s.client.Run("command -v " + tool)
		if err == nil && result.ExitCode == 0 {
			return tool, nil
		}
	}
	return "", fmt.Errorf("neither wget nor curl found on Proxmox host")
}

// DeleteISO deletes an ISO from Proxmox storage
func (s *StorageManager) DeleteISO(storage, filename string) error {
	path, err := s.GetISOPath(storage, filename)
	if err != nil {
		return err
	}

	return s.client.RunQuiet("rm -f " + ssh.ShellEscape(path))
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
	if !validStorageName.MatchString(storageName) {
		return []string{"images"}
	}
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
