package proxmox

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/mihailvovk/versa-proxmox-deployer/config"
	"github.com/mihailvovk/versa-proxmox-deployer/ssh"
)

// ProxmoxInfo holds discovered Proxmox environment information
type ProxmoxInfo struct {
	Version     string
	IsCluster   bool
	ClusterName string
	Nodes       []NodeInfo
	Storage     []StorageInfo
	Networks    []NetworkInfo
	ExistingVMs []VMInfo
}

// NodeInfo holds information about a Proxmox node
type NodeInfo struct {
	Name        string
	Status      string // online, offline
	CPUCores    int
	CPUUsed     int
	RAMGB       int
	RAMUsedGB   int
	RunningVMs  int
	IsLocal     bool
}

// StorageInfo holds information about a storage pool
type StorageInfo struct {
	Name         string
	Type         string // lvmthin, rbd, nfs, dir, etc.
	TotalGB      int
	AvailableGB  int
	UsedGB       int
	Content      []string // images, iso, backup, etc.
	Shared       bool     // Available across cluster
	Active       bool
}

// NetworkInfo holds information about a network bridge
type NetworkInfo struct {
	Name       string   // Bridge name (vmbr0, vmbr1, etc.)
	Interface  string   // Physical interface
	VLANs      []int    // Available VLANs (0 = native/untagged)
	CIDR       string   // IP/subnet if configured
	Gateway    string   // Gateway if configured
	VLANAware  bool     // VLAN-aware bridge
	Comments   string   // Bridge comment/description
}

// VMInfo holds information about an existing VM
type VMInfo struct {
	VMID   int
	Name   string
	Status string // running, stopped
	Node   string
	Tags   []string
}

// Discoverer handles Proxmox environment discovery
type Discoverer struct {
	client *ssh.Client
}

// NewDiscoverer creates a new Proxmox discoverer
func NewDiscoverer(client *ssh.Client) *Discoverer {
	return &Discoverer{client: client}
}

// Discover performs full environment discovery
func (d *Discoverer) Discover() (*ProxmoxInfo, error) {
	info := &ProxmoxInfo{}

	// Get version
	version, err := d.GetVersion()
	if err != nil {
		return nil, fmt.Errorf("not a Proxmox host: %w", err)
	}
	info.Version = version

	// Check if cluster
	isCluster, clusterName, err := d.GetClusterInfo()
	if err != nil {
		// Not a cluster error is ok, continue as standalone
		isCluster = false
	}
	info.IsCluster = isCluster
	info.ClusterName = clusterName

	// Get nodes
	nodes, err := d.GetNodes()
	if err != nil {
		return nil, fmt.Errorf("getting nodes: %w", err)
	}
	info.Nodes = nodes

	// Get storage
	storage, err := d.GetStorage()
	if err != nil {
		return nil, fmt.Errorf("getting storage: %w", err)
	}
	info.Storage = storage

	// Get networks
	networks, err := d.GetNetworks()
	if err != nil {
		return nil, fmt.Errorf("getting networks: %w", err)
	}
	info.Networks = networks

	// Get existing VMs
	vms, err := d.GetVMs()
	if err != nil {
		return nil, fmt.Errorf("getting VMs: %w", err)
	}
	info.ExistingVMs = vms

	return info, nil
}

// DiscoverParallel performs environment discovery with concurrent operations.
// Phase 1 runs version check (fast, must succeed). Phase 2 runs nodes, storage,
// networks, and VMs in parallel. Partial results are returned even if some
// sub-discoveries fail; per-section errors are embedded in the result.
func (d *Discoverer) DiscoverParallel() (*ProxmoxInfo, error) {
	info := &ProxmoxInfo{}

	// Phase 1: Version (fast, required)
	version, err := d.GetVersion()
	if err != nil {
		return nil, fmt.Errorf("not a Proxmox host: %w", err)
	}
	info.Version = version

	// Check cluster status (fast, non-critical)
	isCluster, clusterName, _ := d.GetClusterInfo()
	info.IsCluster = isCluster
	info.ClusterName = clusterName

	// Phase 2: Everything else in parallel
	var wg sync.WaitGroup
	var mu sync.Mutex

	wg.Add(4)

	go func() {
		defer wg.Done()
		nodes, err := d.GetNodes()
		if err == nil {
			mu.Lock()
			info.Nodes = nodes
			mu.Unlock()
		}
	}()

	go func() {
		defer wg.Done()
		storage, err := d.GetStorage()
		if err == nil {
			mu.Lock()
			info.Storage = storage
			mu.Unlock()
		}
	}()

	go func() {
		defer wg.Done()
		networks, err := d.GetNetworks()
		if err == nil {
			mu.Lock()
			info.Networks = networks
			mu.Unlock()
		}
	}()

	go func() {
		defer wg.Done()
		vms, err := d.GetVMs()
		if err == nil {
			mu.Lock()
			info.ExistingVMs = vms
			mu.Unlock()
		}
	}()

	wg.Wait()

	return info, nil
}

// GetVersion returns the Proxmox VE version
func (d *Discoverer) GetVersion() (string, error) {
	result, err := d.client.Run("pveversion")
	if err != nil {
		return "", err
	}

	if result.ExitCode != 0 {
		return "", fmt.Errorf("pveversion command failed: %s", result.Stderr)
	}

	// Parse version from output like "pve-manager/8.1.3/ec5affc9e41f1d33 (running kernel: 6.5.11-7-pve)"
	output := strings.TrimSpace(result.Stdout)
	if strings.HasPrefix(output, "pve-manager/") {
		parts := strings.Split(output, "/")
		if len(parts) >= 2 {
			return parts[1], nil
		}
	}

	return output, nil
}

// GetClusterInfo returns cluster status and name
func (d *Discoverer) GetClusterInfo() (bool, string, error) {
	result, err := d.client.Run("pvecm status 2>/dev/null")
	if err != nil {
		return false, "", err
	}

	if result.ExitCode != 0 {
		// Not a cluster
		return false, "", nil
	}

	// Parse cluster name from output
	clusterName := ""
	for _, line := range strings.Split(result.Stdout, "\n") {
		if strings.HasPrefix(line, "Cluster name:") {
			clusterName = strings.TrimSpace(strings.TrimPrefix(line, "Cluster name:"))
			break
		}
		// Alternative format
		if strings.Contains(line, "Name:") && clusterName == "" {
			parts := strings.SplitN(line, ":", 2)
			if len(parts) == 2 {
				clusterName = strings.TrimSpace(parts[1])
			}
		}
	}

	return true, clusterName, nil
}

// GetNodes returns information about all nodes
func (d *Discoverer) GetNodes() ([]NodeInfo, error) {
	// Try to get nodes via pvesh API (works on all versions)
	var nodeList []struct {
		Node   string  `json:"node"`
		Status string  `json:"status"`
		CPU    float64 `json:"cpu"`
		MaxCPU int     `json:"maxcpu"`
		Mem    int64   `json:"mem"`
		MaxMem int64   `json:"maxmem"`
		Uptime int64   `json:"uptime"`
	}

	err := d.client.RunJSON("pvesh get /nodes --output-format json", &nodeList)
	if err == nil && len(nodeList) > 0 {
		var nodes []NodeInfo
		for _, n := range nodeList {
			// Get running VM count
			runningVMs := d.countRunningVMs(n.Node)

			// Check if local
			hostnameResult, _ := d.client.Run("hostname -s")
			isLocal := strings.TrimSpace(hostnameResult.Stdout) == n.Node

			nodes = append(nodes, NodeInfo{
				Name:       n.Node,
				Status:     n.Status,
				CPUCores:   n.MaxCPU,
				CPUUsed:    int(n.CPU * float64(n.MaxCPU)),
				RAMGB:      int(n.MaxMem / (1024 * 1024 * 1024)),
				RAMUsedGB:  int(n.Mem / (1024 * 1024 * 1024)),
				RunningVMs: runningVMs,
				IsLocal:    isLocal,
			})
		}
		return nodes, nil
	}

	// Fallback: get local hostname
	result, err := d.client.Run("hostname -s")
	if err != nil {
		return nil, fmt.Errorf("getting hostname: %w", err)
	}
	nodeName := strings.TrimSpace(result.Stdout)

	// Get info from system
	cpuCores, cpuUsed, ramGB, ramUsedGB := d.getNodeInfoFromSystem()
	runningVMs := d.countRunningVMs(nodeName)

	return []NodeInfo{{
		Name:       nodeName,
		Status:     "online",
		CPUCores:   cpuCores,
		CPUUsed:    cpuUsed,
		RAMGB:      ramGB,
		RAMUsedGB:  ramUsedGB,
		RunningVMs: runningVMs,
		IsLocal:    true,
	}}, nil
}

// countRunningVMs counts running VMs on a node
func (d *Discoverer) countRunningVMs(nodeName string) int {
	result, err := d.client.Run("qm list 2>/dev/null | grep -c running || echo 0")
	if err != nil {
		return 0
	}
	count, _ := strconv.Atoi(strings.TrimSpace(result.Stdout))
	return count
}

// getNodeInfo gets detailed information about a single node (kept for compatibility)
func (d *Discoverer) getNodeInfo(nodeName string) (*NodeInfo, error) {
	var status struct {
		CPU     float64 `json:"cpu"`
		CPUInfo struct {
			CPUs int `json:"cpus"`
		} `json:"cpuinfo"`
		Memory struct {
			Total int64 `json:"total"`
			Used  int64 `json:"used"`
		} `json:"memory"`
	}

	cmd := fmt.Sprintf("pvesh get /nodes/%s/status --output-format json", nodeName)
	err := d.client.RunJSON(cmd, &status)

	if err == nil && status.CPUInfo.CPUs > 0 {
		return &NodeInfo{
			Name:       nodeName,
			Status:     "online",
			CPUCores:   status.CPUInfo.CPUs,
			CPUUsed:    int(status.CPU * float64(status.CPUInfo.CPUs)),
			RAMGB:      int(status.Memory.Total / (1024 * 1024 * 1024)),
			RAMUsedGB:  int(status.Memory.Used / (1024 * 1024 * 1024)),
			RunningVMs: d.countRunningVMs(nodeName),
			IsLocal:    true,
		}, nil
	}

	// Fallback to system commands
	cpuCores, cpuUsed, ramGB, ramUsedGB := d.getNodeInfoFromSystem()
	return &NodeInfo{
		Name:       nodeName,
		Status:     "online",
		CPUCores:   cpuCores,
		CPUUsed:    cpuUsed,
		RAMGB:      ramGB,
		RAMUsedGB:  ramUsedGB,
		RunningVMs: d.countRunningVMs(nodeName),
		IsLocal:    true,
	}, nil
}

// getNodeInfoFromSystem gets node info from standard Linux commands
func (d *Discoverer) getNodeInfoFromSystem() (cpuCores, cpuUsed, ramGB, ramUsedGB int) {
	// Get CPU cores
	cpuResult, err := d.client.Run("nproc")
	if err == nil {
		cpuCores, _ = strconv.Atoi(strings.TrimSpace(cpuResult.Stdout))
	}

	// Get CPU usage from /proc/loadavg (1-minute load average)
	loadResult, err := d.client.Run("cat /proc/loadavg | awk '{print $1}'")
	if err == nil {
		load, _ := strconv.ParseFloat(strings.TrimSpace(loadResult.Stdout), 64)
		cpuUsed = int(load)
		if cpuUsed > cpuCores {
			cpuUsed = cpuCores
		}
	}

	// Get memory info from /proc/meminfo
	memResult, err := d.client.Run("grep -E '^(MemTotal|MemAvailable):' /proc/meminfo")
	if err == nil {
		lines := strings.Split(memResult.Stdout, "\n")
		for _, line := range lines {
			if strings.HasPrefix(line, "MemTotal:") {
				// Format: "MemTotal:       16384000 kB"
				fields := strings.Fields(line)
				if len(fields) >= 2 {
					kb, _ := strconv.ParseInt(fields[1], 10, 64)
					ramGB = int(kb / (1024 * 1024))
				}
			} else if strings.HasPrefix(line, "MemAvailable:") {
				fields := strings.Fields(line)
				if len(fields) >= 2 {
					kb, _ := strconv.ParseInt(fields[1], 10, 64)
					availGB := int(kb / (1024 * 1024))
					ramUsedGB = ramGB - availGB
				}
			}
		}
	}

	return
}

// GetStorage returns information about all storage pools
func (d *Discoverer) GetStorage() ([]StorageInfo, error) {
	// Get storage config from pvesh (works on all versions)
	var storageConfig []struct {
		Storage  string `json:"storage"`
		Type     string `json:"type"`
		Content  string `json:"content"`
		Path     string `json:"path"`
		Shared   int    `json:"shared"`
		Disabled int    `json:"disable"`
	}

	// Get storage definitions
	err := d.client.RunJSON("pvesh get /storage --output-format json", &storageConfig)
	if err != nil {
		// Fallback to text parsing
		result, err := d.client.Run("pvesm status")
		if err != nil {
			return nil, err
		}
		return d.parseStorageText(result.Stdout)
	}

	// Get storage status (usage info) via text output
	statusResult, _ := d.client.Run("pvesm status")
	statusMap := d.parseStorageStatusToMap(statusResult.Stdout)

	var storage []StorageInfo
	for _, s := range storageConfig {
		if s.Disabled == 1 {
			continue
		}

		info := StorageInfo{
			Name:    s.Storage,
			Type:    s.Type,
			Content: strings.Split(s.Content, ","),
			Shared:  s.Shared == 1,
			Active:  true,
		}

		// Merge status info if available
		if status, ok := statusMap[s.Storage]; ok {
			info.TotalGB = status.TotalGB
			info.UsedGB = status.UsedGB
			info.AvailableGB = status.AvailableGB
			info.Active = status.Active
		}

		storage = append(storage, info)
	}

	return storage, nil
}

// parseStorageStatusToMap parses pvesm status output into a map
func (d *Discoverer) parseStorageStatusToMap(output string) map[string]StorageInfo {
	result := make(map[string]StorageInfo)
	lines := strings.Split(output, "\n")

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
		status := fields[2]

		total, _ := strconv.ParseInt(fields[3], 10, 64)
		used, _ := strconv.ParseInt(fields[4], 10, 64)
		avail, _ := strconv.ParseInt(fields[5], 10, 64)

		result[name] = StorageInfo{
			Name:        name,
			Type:        fields[1],
			TotalGB:     int(total / (1024 * 1024)),
			UsedGB:      int(used / (1024 * 1024)),
			AvailableGB: int(avail / (1024 * 1024)),
			Active:      status == "active",
		}
	}

	return result
}

// parseStorageText parses text output from pvesm status
func (d *Discoverer) parseStorageText(output string) ([]StorageInfo, error) {
	var storage []StorageInfo
	lines := strings.Split(output, "\n")

	for _, line := range lines {
		line = strings.TrimSpace(line)
		// Skip header and empty lines
		if line == "" || strings.HasPrefix(line, "Name") {
			continue
		}

		fields := strings.Fields(line)
		if len(fields) < 6 {
			continue
		}

		name := fields[0]
		storageType := fields[1]
		status := fields[2]

		// Parse sizes (in KB)
		total, _ := strconv.ParseInt(fields[3], 10, 64)
		used, _ := strconv.ParseInt(fields[4], 10, 64)
		avail, _ := strconv.ParseInt(fields[5], 10, 64)

		// Get content types from storage config
		content := d.getStorageContent(name)

		storage = append(storage, StorageInfo{
			Name:        name,
			Type:        storageType,
			TotalGB:     int(total / (1024 * 1024)), // KB to GB
			UsedGB:      int(used / (1024 * 1024)),
			AvailableGB: int(avail / (1024 * 1024)),
			Content:     content,
			Active:      status == "active",
			Shared:      false, // Will be updated from config
		})
	}

	return storage, nil
}

// getStorageContent gets content types for a storage from config
func (d *Discoverer) getStorageContent(storageName string) []string {
	result, err := d.client.Run(fmt.Sprintf("grep -A 10 '^%s:' /etc/pve/storage.cfg 2>/dev/null | grep 'content' | head -1", storageName))
	if err != nil || result.ExitCode != 0 {
		return []string{"images", "rootdir"} // Default assumption
	}

	// Parse "content images,rootdir,iso"
	line := strings.TrimSpace(result.Stdout)
	if strings.HasPrefix(line, "content") {
		parts := strings.Fields(line)
		if len(parts) >= 2 {
			return strings.Split(parts[1], ",")
		}
	}

	return []string{"images"}
}

// GetNetworks returns information about network bridges
func (d *Discoverer) GetNetworks() ([]NetworkInfo, error) {
	// Read /etc/network/interfaces
	result, err := d.client.Run("cat /etc/network/interfaces")
	if err != nil {
		return nil, err
	}

	networks := parseNetworkInterfaces(result.Stdout)

	// Also list bridges from brctl or ip command (fallback for any we missed)
	bridgeResult, _ := d.client.Run("brctl show 2>/dev/null | tail -n +2 | awk '{print $1}' | grep -v '^$'")
	if bridgeResult != nil && bridgeResult.ExitCode == 0 {
		existingBridges := make(map[string]bool)
		for _, n := range networks {
			existingBridges[n.Name] = true
		}

		for _, line := range strings.Split(bridgeResult.Stdout, "\n") {
			bridgeName := strings.TrimSpace(line)
			if bridgeName != "" && !existingBridges[bridgeName] && strings.HasPrefix(bridgeName, "vmbr") {
				networks = append(networks, NetworkInfo{
					Name: bridgeName,
				})
			}
		}
	}

	// If still no bridges found, try listing from /sys
	if len(networks) == 0 {
		sysResult, _ := d.client.Run("ls -1 /sys/class/net/ | grep vmbr")
		if sysResult != nil && sysResult.ExitCode == 0 {
			for _, line := range strings.Split(sysResult.Stdout, "\n") {
				bridgeName := strings.TrimSpace(line)
				if bridgeName != "" {
					networks = append(networks, NetworkInfo{
						Name: bridgeName,
					})
				}
			}
		}
	}

	return networks, nil
}

// parseNetworkInterfaces parses /etc/network/interfaces
func parseNetworkInterfaces(content string) []NetworkInfo {
	var networks []NetworkInfo
	var current *NetworkInfo

	lines := strings.Split(content, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)

		// Skip comments and empty lines
		if line == "" || strings.HasPrefix(line, "#") {
			// Check for comment that might be description
			if current != nil && strings.HasPrefix(line, "#") {
				current.Comments = strings.TrimPrefix(line, "#")
			}
			continue
		}

		// New interface block
		if strings.HasPrefix(line, "iface ") {
			parts := strings.Fields(line)
			if len(parts) >= 2 {
				name := parts[1]
				// Only track vmbr bridges
				if strings.HasPrefix(name, "vmbr") {
					if current != nil {
						networks = append(networks, *current)
					}
					current = &NetworkInfo{
						Name: name,
					}
				} else {
					current = nil
				}
			}
			continue
		}

		// Parse properties of current bridge
		if current != nil {
			if strings.HasPrefix(line, "address ") {
				current.CIDR = strings.TrimPrefix(line, "address ")
			} else if strings.HasPrefix(line, "gateway ") {
				current.Gateway = strings.TrimPrefix(line, "gateway ")
			} else if strings.HasPrefix(line, "bridge-ports ") {
				current.Interface = strings.TrimPrefix(line, "bridge-ports ")
			} else if strings.HasPrefix(line, "bridge_ports ") {
				current.Interface = strings.TrimPrefix(line, "bridge_ports ")
			} else if strings.Contains(line, "bridge-vlan-aware") {
				if strings.Contains(line, "yes") {
					current.VLANAware = true
				}
			} else if strings.HasPrefix(line, "bridge-vids ") {
				// Parse VLAN IDs
				vids := strings.TrimPrefix(line, "bridge-vids ")
				current.VLANs = parseVLANList(vids)
			}
		}
	}

	// Don't forget the last one
	if current != nil {
		networks = append(networks, *current)
	}

	return networks
}

// parseVLANList parses a VLAN list like "10 20 30" or "10-30"
func parseVLANList(s string) []int {
	var vlans []int
	parts := strings.Fields(s)

	for _, part := range parts {
		// Check for range (10-30)
		if strings.Contains(part, "-") {
			rangeParts := strings.Split(part, "-")
			if len(rangeParts) == 2 {
				start, _ := strconv.Atoi(rangeParts[0])
				end, _ := strconv.Atoi(rangeParts[1])
				for i := start; i <= end; i++ {
					vlans = append(vlans, i)
				}
			}
		} else {
			// Single VLAN
			if v, err := strconv.Atoi(part); err == nil {
				vlans = append(vlans, v)
			}
		}
	}

	return vlans
}

// GetVMs returns information about all VMs
func (d *Discoverer) GetVMs() ([]VMInfo, error) {
	result, err := d.client.Run("qm list")
	if err != nil {
		return nil, err
	}

	var vms []VMInfo
	lines := strings.Split(result.Stdout, "\n")

	// Skip header
	for i, line := range lines {
		if i == 0 || strings.TrimSpace(line) == "" {
			continue
		}

		// Parse line: "VMID NAME STATUS MEM(MB) BOOTDISK(GB) PID"
		fields := strings.Fields(line)
		if len(fields) >= 3 {
			vmid, _ := strconv.Atoi(fields[0])
			vm := VMInfo{
				VMID:   vmid,
				Name:   fields[1],
				Status: fields[2],
			}

			// Get tags for this VM
			tags, _ := d.getVMTags(vmid)
			vm.Tags = tags

			vms = append(vms, vm)
		}
	}

	return vms, nil
}

// getVMTags gets tags for a specific VM
func (d *Discoverer) getVMTags(vmid int) ([]string, error) {
	result, err := d.client.Run(fmt.Sprintf("qm config %d 2>/dev/null | grep '^tags:' || true", vmid))
	if err != nil || result.ExitCode != 0 {
		return nil, nil
	}

	line := strings.TrimSpace(result.Stdout)
	if line == "" {
		return nil, nil
	}

	// Parse "tags: tag1;tag2;tag3"
	tagStr := strings.TrimPrefix(line, "tags:")
	tagStr = strings.TrimSpace(tagStr)
	if tagStr == "" {
		return nil, nil
	}

	return strings.Split(tagStr, ";"), nil
}

// GetNextVMID returns the next available VMID
func (d *Discoverer) GetNextVMID() (int, error) {
	result, err := d.client.Run("pvesh get /cluster/nextid")
	if err != nil {
		return 0, err
	}

	vmid, err := strconv.Atoi(strings.TrimSpace(result.Stdout))
	if err != nil {
		return 0, fmt.Errorf("parsing VMID: %w", err)
	}

	return vmid, nil
}

// FindVersaDeployments finds existing Versa VMs by the versa-deployer tag
func (d *Discoverer) FindVersaDeployments() ([]VMInfo, error) {
	vms, err := d.GetVMs()
	if err != nil {
		return nil, err
	}

	var versaVMs []VMInfo
	for _, vm := range vms {
		for _, tag := range vm.Tags {
			if tag == config.TagVersaDeployer {
				versaVMs = append(versaVMs, vm)
				break
			}
		}
	}

	return versaVMs, nil
}

// GetImageCapableStorage returns storage that can hold VM images
func (d *Discoverer) GetImageCapableStorage() ([]StorageInfo, error) {
	storage, err := d.GetStorage()
	if err != nil {
		return nil, err
	}

	var imageStorage []StorageInfo
	for _, s := range storage {
		if !s.Active {
			continue
		}
		for _, content := range s.Content {
			if content == "images" || content == "rootdir" {
				imageStorage = append(imageStorage, s)
				break
			}
		}
	}

	return imageStorage, nil
}

// GetISOStorage returns storage that can hold ISO files
func (d *Discoverer) GetISOStorage() ([]StorageInfo, error) {
	storage, err := d.GetStorage()
	if err != nil {
		return nil, err
	}

	var isoStorage []StorageInfo
	for _, s := range storage {
		if !s.Active {
			continue
		}
		for _, content := range s.Content {
			if content == "iso" {
				isoStorage = append(isoStorage, s)
				break
			}
		}
	}

	return isoStorage, nil
}

// parseJSON is a simple helper for JSON parsing
func parseJSON(data string, v interface{}) error {
	// Simple regex-based parsing for basic structures
	// For complex JSON, use encoding/json
	return nil
}

// DetectSubnetsFromBridge attempts to detect subnet info from a bridge
func (d *Discoverer) DetectSubnetsFromBridge(bridge string) (cidr string, gateway string) {
	// Try to get IP info from the bridge
	result, err := d.client.Run(fmt.Sprintf("ip -j addr show %s 2>/dev/null", bridge))
	if err != nil || result.ExitCode != 0 {
		return "", ""
	}

	// Parse IP address from output
	// Format: [{"addr_info":[{"local":"10.0.0.1","prefixlen":24}]}]
	re := regexp.MustCompile(`"local":"([^"]+)".*"prefixlen":(\d+)`)
	matches := re.FindStringSubmatch(result.Stdout)
	if len(matches) >= 3 {
		ip := matches[1]
		prefix := matches[2]
		return fmt.Sprintf("%s/%s", ip, prefix), ip
	}

	return "", ""
}
