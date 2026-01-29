package deployer

import (
	"fmt"
	"sync"
	"time"

	"github.com/mihailvovk/versa-proxmox-deployer/config"
	"github.com/mihailvovk/versa-proxmox-deployer/downloader"
	"github.com/mihailvovk/versa-proxmox-deployer/proxmox"
	"github.com/mihailvovk/versa-proxmox-deployer/sources"
	"github.com/mihailvovk/versa-proxmox-deployer/ssh"
)

// Deployer orchestrates the HeadEnd deployment
type Deployer struct {
	sshClient  *ssh.Client
	discoverer *proxmox.Discoverer
	vmCreator  *proxmox.VMCreator
	storage    *proxmox.StorageManager
	downloader *downloader.Downloader
	config     *config.DeploymentConfig
	proxmoxInfo *proxmox.ProxmoxInfo
	knownImages []sources.ISOFile

	// Rollback tracking
	createdVMIDs []int

	// Progress callbacks
	OnProgress    func(stage string, current, total int)
	OnLog         func(message string)
	OnError       func(err error)
}

// DeploymentStage represents a stage of the deployment
type DeploymentStage string

const (
	StageDiscovery    DeploymentStage = "discovery"
	StageValidation   DeploymentStage = "validation"
	StageImagePrep    DeploymentStage = "image_prep"
	StageVMCreation   DeploymentStage = "vm_creation"
	StageNetworking   DeploymentStage = "networking"
	StageStartup      DeploymentStage = "startup"
	StageRollback     DeploymentStage = "rollback"
	StageComplete     DeploymentStage = "complete"
)

// DeploymentResult holds the result of a deployment
type DeploymentResult struct {
	Success      bool
	VMs          []VMResult
	Errors       []string
	Duration     time.Duration
	RolledBack   bool
	ConsoleURLs  map[string]string
}

// VMResult holds the result of a single VM creation
type VMResult struct {
	VMID        int
	Name        string
	Component   config.ComponentType
	Node        string
	Status      string
	IP          string
	ConsoleURL  string
}

// NewDeployer creates a new deployer
func NewDeployer(client *ssh.Client, srcs []sources.ImageSource) *Deployer {
	return &Deployer{
		sshClient:    client,
		discoverer:   proxmox.NewDiscoverer(client),
		vmCreator:    proxmox.NewVMCreator(client),
		storage:      proxmox.NewStorageManager(client),
		downloader:   downloader.NewDownloader(srcs),
		createdVMIDs: []int{},
	}
}

// SetConfig sets the deployment configuration
func (d *Deployer) SetConfig(cfg *config.DeploymentConfig) {
	d.config = cfg
}

// SetKnownImages sets the scanned ISO images available from sources
func (d *Deployer) SetKnownImages(images []sources.ISOFile) {
	d.knownImages = images
}

// Discover performs Proxmox environment discovery
func (d *Deployer) Discover() (*proxmox.ProxmoxInfo, error) {
	d.log("Discovering Proxmox environment...")

	info, err := d.discoverer.Discover()
	if err != nil {
		return nil, fmt.Errorf("discovery failed: %w", err)
	}

	d.proxmoxInfo = info
	return info, nil
}

// Validate validates the deployment configuration against available resources
func (d *Deployer) Validate() error {
	if d.config == nil {
		return fmt.Errorf("no deployment configuration set")
	}

	if d.proxmoxInfo == nil {
		return fmt.Errorf("discovery not performed")
	}

	d.log("Validating deployment configuration...")

	// Check total resources required
	totalCPU, totalRAM, totalDisk := d.config.GetTotalResources()

	// Find target storage
	var targetStorage *proxmox.StorageInfo
	for _, s := range d.proxmoxInfo.Storage {
		if s.Name == d.config.StoragePool {
			targetStorage = &s
			break
		}
	}

	if targetStorage == nil {
		return fmt.Errorf("storage pool '%s' not found", d.config.StoragePool)
	}

	if targetStorage.AvailableGB < totalDisk {
		return fmt.Errorf("insufficient storage: need %dGB but only %dGB available", totalDisk, targetStorage.AvailableGB)
	}

	// Check each target node has enough resources
	for _, comp := range d.config.Components {
		node := comp.Node
		if node == "" && len(d.proxmoxInfo.Nodes) > 0 {
			node = d.proxmoxInfo.Nodes[0].Name
		}

		var targetNode *proxmox.NodeInfo
		for _, n := range d.proxmoxInfo.Nodes {
			if n.Name == node {
				targetNode = &n
				break
			}
		}

		if targetNode == nil {
			return fmt.Errorf("node '%s' not found", node)
		}

		if targetNode.Status != "online" {
			return fmt.Errorf("node '%s' is not online", node)
		}

		availableRAM := targetNode.RAMGB - targetNode.RAMUsedGB
		if comp.RAMGB*comp.Count > availableRAM {
			return fmt.Errorf("insufficient RAM on node '%s': need %dGB but only %dGB available",
				node, comp.RAMGB*comp.Count, availableRAM)
		}
	}

	d.log(fmt.Sprintf("Validation passed: %d vCPU, %dGB RAM, %dGB disk required", totalCPU, totalRAM, totalDisk))
	return nil
}

// Deploy executes the full deployment
func (d *Deployer) Deploy() (*DeploymentResult, error) {
	startTime := time.Now()
	result := &DeploymentResult{
		ConsoleURLs: make(map[string]string),
	}

	defer func() {
		result.Duration = time.Since(startTime)
	}()

	// Validate first
	if err := d.Validate(); err != nil {
		result.Errors = append(result.Errors, err.Error())
		return result, err
	}

	// Prepare images
	d.progress(StageImagePrep, 0, len(d.config.Components))
	if err := d.prepareImages(); err != nil {
		result.Errors = append(result.Errors, err.Error())
		d.rollback()
		result.RolledBack = true
		return result, err
	}

	// Create VMs
	d.progress(StageVMCreation, 0, d.config.VMCount())
	vmResults, err := d.createVMs()
	if err != nil {
		result.Errors = append(result.Errors, err.Error())
		d.rollback()
		result.RolledBack = true
		return result, err
	}
	result.VMs = vmResults

	// Start VMs
	d.progress(StageStartup, 0, len(vmResults))
	for i, vm := range vmResults {
		d.log(fmt.Sprintf("Starting %s...", vm.Name))
		if err := d.vmCreator.StartVM(vm.VMID); err != nil {
			d.log(fmt.Sprintf("WARNING: Failed to start %s: %v", vm.Name, err))
			result.Errors = append(result.Errors, fmt.Sprintf("failed to start %s: %v", vm.Name, err))
			result.VMs[i].Status = "stopped"
		} else {
			// Verify it's actually running
			status, err := d.vmCreator.GetVMStatus(vm.VMID)
			if err == nil && status == "running" {
				result.VMs[i].Status = "running"
				d.log(fmt.Sprintf("VM %s is running", vm.Name))
			} else {
				result.VMs[i].Status = status
				d.log(fmt.Sprintf("WARNING: VM %s status is '%s' after start (expected 'running')", vm.Name, status))
			}
		}
		d.progress(StageStartup, i+1, len(vmResults))
	}

	// Generate console URLs
	for _, vm := range result.VMs {
		url := d.vmCreator.GetConsoleURL(vm.VMID, d.sshClient.Host())
		result.ConsoleURLs[vm.Name] = url
		result.VMs[findVMIndex(result.VMs, vm.VMID)].ConsoleURL = url
	}

	result.Success = len(result.Errors) == 0
	d.progress(StageComplete, 1, 1)

	return result, nil
}

// prepareImages ensures all required ISOs are available
func (d *Deployer) prepareImages() error {
	// Get unique ISOs needed
	isoNeeded := make(map[string]bool)
	for _, comp := range d.config.Components {
		if comp.ISOPath != "" {
			isoNeeded[comp.ISOPath] = true
		}
	}

	// Check/upload each ISO
	i := 0
	for isoFile := range isoNeeded {
		d.progress(StageImagePrep, i, len(isoNeeded))
		d.log(fmt.Sprintf("Checking ISO: %s", isoFile))

		// Check if ISO exists on Proxmox
		// First get ISO storage
		isoStorage, err := d.discoverer.GetISOStorage()
		if err != nil || len(isoStorage) == 0 {
			return fmt.Errorf("no ISO storage available")
		}
		storName := isoStorage[0].Name

		exists, err := d.storage.ISOExists(storName, isoFile)
		if err == nil && exists {
			d.log(fmt.Sprintf("ISO already on Proxmox: %s", isoFile))
			i++
			continue
		}

		// Find the ISOFile metadata for this filename
		var isoMeta *sources.ISOFile
		for idx := range d.knownImages {
			if d.knownImages[idx].Filename == isoFile {
				isoMeta = &d.knownImages[idx]
				break
			}
		}

		if isoMeta == nil {
			return fmt.Errorf("ISO metadata not found for %s — ensure image sources are configured", isoFile)
		}

		// Download from source (or use cache)
		d.log(fmt.Sprintf("Downloading ISO: %s (source: %s, size: %s)", isoFile, isoMeta.SourceName, formatBytes(isoMeta.Size)))
		dlResult, err := d.downloader.EnsureISO(*isoMeta, makeThrottledProgress(d, "Download", isoFile))
		if err != nil {
			return fmt.Errorf("downloading ISO %s: %w", isoFile, err)
		}

		if dlResult.WasCached {
			d.log(fmt.Sprintf("ISO already cached locally: %s (size: %s, MD5 verified: %v)", isoFile, formatBytes(dlResult.Size), dlResult.MD5Verified))
		} else {
			d.log(fmt.Sprintf("ISO downloaded: %s (size: %s, MD5 verified: %v)", isoFile, formatBytes(dlResult.Size), dlResult.MD5Verified))
		}

		// Upload to Proxmox via SCP
		d.log(fmt.Sprintf("Uploading to Proxmox storage '%s': %s (%s)", storName, isoFile, formatBytes(dlResult.Size)))
		if err := d.storage.UploadISO(dlResult.LocalPath, storName, makeThrottledProgress(d, "Upload", isoFile)); err != nil {
			return fmt.Errorf("uploading ISO %s: %w", isoFile, err)
		}
		d.log(fmt.Sprintf("Upload complete: %s", isoFile))

		i++
	}

	return nil
}

// makeThrottledProgress returns a progress callback that logs at most every 10 seconds
func makeThrottledProgress(d *Deployer, action, filename string) func(done, total int64) {
	var mu sync.Mutex
	lastLog := time.Time{}
	return func(done, total int64) {
		if total <= 0 {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		now := time.Now()
		if now.Sub(lastLog) >= 20*time.Second || done >= total {
			pct := done * 100 / total
			d.log(fmt.Sprintf("  %s %s: %d%% (%s / %s)", action, filename, pct, formatBytes(done), formatBytes(total)))
			lastLog = now
		}
	}
}

func formatBytes(b int64) string {
	switch {
	case b >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(b)/(1<<30))
	case b >= 1<<20:
		return fmt.Sprintf("%.0f MB", float64(b)/(1<<20))
	case b >= 1<<10:
		return fmt.Sprintf("%.0f KB", float64(b)/(1<<10))
	default:
		return fmt.Sprintf("%d B", b)
	}
}

// createVMs creates all the VMs
func (d *Deployer) createVMs() ([]VMResult, error) {
	var results []VMResult
	vmIndex := 0

	// Get first ISO storage for reference
	isoStorage, err := d.discoverer.GetISOStorage()
	if err != nil || len(isoStorage) == 0 {
		return nil, fmt.Errorf("no ISO storage available")
	}
	isoStorName := isoStorage[0].Name

	for _, comp := range d.config.Components {
		count := comp.Count
		if count == 0 {
			count = 1
		}

		for i := 0; i < count; i++ {
			d.progress(StageVMCreation, vmIndex, d.config.VMCount())

			// Get next VMID
			vmid, err := d.discoverer.GetNextVMID()
			if err != nil {
				return results, fmt.Errorf("getting next VMID: %w", err)
			}

			// Build network configuration
			networks := proxmox.BuildNetworksForComponent(comp.Type, d.config.Networks, d.config.HAMode)

			// Add HA network for Router if in HA mode
			if comp.Type == config.ComponentRouter && d.config.HAMode && i > 0 {
				// This is the second router in HA pair, needs HA sync interface
				// User would have configured this in Networks.RouterHA
			}

			// Build VM config
			vmConfig := proxmox.BuildVMConfigForComponent(
				comp,
				d.config.Prefix,
				i,
				d.config.StoragePool,
				isoStorName,
				networks,
				vmid,
			)

			// Set target node
			if comp.Node != "" {
				vmConfig.Node = comp.Node
			} else if len(d.proxmoxInfo.Nodes) > 0 {
				vmConfig.Node = d.proxmoxInfo.Nodes[0].Name
			}

			d.log(fmt.Sprintf("Creating VM: %s (VMID %d) on %s", vmConfig.Name, vmid, vmConfig.Node))

			// Create the VM
			if err := d.vmCreator.CreateVM(vmConfig); err != nil {
				return results, fmt.Errorf("creating VM %s: %w", vmConfig.Name, err)
			}

			// Track for rollback
			d.createdVMIDs = append(d.createdVMIDs, vmid)

			// Get assigned IP if configured
			ip := ""
			if d.config.IPConfig.ManualIPs != nil {
				ip = d.config.IPConfig.ManualIPs[vmConfig.Name]
			}

			results = append(results, VMResult{
				VMID:      vmid,
				Name:      vmConfig.Name,
				Component: comp.Type,
				Node:      vmConfig.Node,
				Status:    "created",
				IP:        ip,
			})

			vmIndex++
		}
	}

	return results, nil
}

// rollback destroys all created VMs
func (d *Deployer) rollback() {
	if len(d.createdVMIDs) == 0 {
		return
	}

	d.log("Rolling back deployment...")
	d.progress(StageRollback, 0, len(d.createdVMIDs))

	// Destroy in reverse order
	for i := len(d.createdVMIDs) - 1; i >= 0; i-- {
		vmid := d.createdVMIDs[i]
		d.log(fmt.Sprintf("Destroying VM %d...", vmid))

		if err := d.vmCreator.DestroyVM(vmid); err != nil {
			d.log(fmt.Sprintf("Warning: failed to destroy VM %d: %v", vmid, err))
		}

		d.progress(StageRollback, len(d.createdVMIDs)-i, len(d.createdVMIDs))
	}

	d.createdVMIDs = []int{}
	d.log("Rollback complete")
}

// log sends a log message
func (d *Deployer) log(message string) {
	if d.OnLog != nil {
		d.OnLog(message)
	}
}

// progress reports progress
func (d *Deployer) progress(stage DeploymentStage, current, total int) {
	if d.OnProgress != nil {
		d.OnProgress(string(stage), current, total)
	}
}

// findVMIndex finds the index of a VM by VMID
func findVMIndex(vms []VMResult, vmid int) int {
	for i, vm := range vms {
		if vm.VMID == vmid {
			return i
		}
	}
	return -1
}

// GetProxmoxInfo returns the discovered Proxmox information
func (d *Deployer) GetProxmoxInfo() *proxmox.ProxmoxInfo {
	return d.proxmoxInfo
}

// FindExistingDeployments finds existing Versa deployments
func (d *Deployer) FindExistingDeployments() ([]proxmox.VMInfo, error) {
	return d.discoverer.FindVersaDeployments()
}
