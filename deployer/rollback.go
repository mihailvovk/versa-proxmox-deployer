package deployer

import (
	"fmt"
	"time"

	"github.com/oliverwhk/versa-proxmox-deployer/proxmox"
	"github.com/oliverwhk/versa-proxmox-deployer/ssh"
)

// RollbackManager handles deployment rollback operations
type RollbackManager struct {
	client    *ssh.Client
	vmCreator *proxmox.VMCreator
	vmIDs     []int
	onLog     func(message string)
}

// NewRollbackManager creates a new rollback manager
func NewRollbackManager(client *ssh.Client) *RollbackManager {
	return &RollbackManager{
		client:    client,
		vmCreator: proxmox.NewVMCreator(client),
		vmIDs:     []int{},
	}
}

// SetLogCallback sets the logging callback
func (r *RollbackManager) SetLogCallback(fn func(message string)) {
	r.onLog = fn
}

// TrackVM adds a VMID to the rollback list
func (r *RollbackManager) TrackVM(vmid int) {
	r.vmIDs = append(r.vmIDs, vmid)
}

// Clear clears the rollback list (call after successful deployment)
func (r *RollbackManager) Clear() {
	r.vmIDs = []int{}
}

// Rollback destroys all tracked VMs in reverse order
func (r *RollbackManager) Rollback() error {
	if len(r.vmIDs) == 0 {
		return nil
	}

	r.log("Starting rollback...")
	var errors []error

	// Destroy in reverse order (last created first)
	for i := len(r.vmIDs) - 1; i >= 0; i-- {
		vmid := r.vmIDs[i]
		r.log(fmt.Sprintf("Destroying VM %d...", vmid))

		// First try to stop the VM
		if err := r.vmCreator.StopVM(vmid); err != nil {
			// Ignore stop errors, VM might not be running
			r.log(fmt.Sprintf("Note: VM %d stop returned: %v", vmid, err))
		}

		// Wait a moment for stop to complete
		time.Sleep(2 * time.Second)

		// Destroy with purge
		if err := r.vmCreator.DestroyVM(vmid); err != nil {
			r.log(fmt.Sprintf("Warning: Failed to destroy VM %d: %v", vmid, err))
			errors = append(errors, fmt.Errorf("VM %d: %w", vmid, err))
		} else {
			r.log(fmt.Sprintf("VM %d destroyed", vmid))
		}
	}

	r.vmIDs = []int{}

	if len(errors) > 0 {
		return fmt.Errorf("rollback completed with %d errors", len(errors))
	}

	r.log("Rollback complete")
	return nil
}

// GetTrackedVMs returns the list of tracked VMIDs
func (r *RollbackManager) GetTrackedVMs() []int {
	return r.vmIDs
}

// HasTrackedVMs returns true if there are VMs to rollback
func (r *RollbackManager) HasTrackedVMs() bool {
	return len(r.vmIDs) > 0
}

// log sends a log message
func (r *RollbackManager) log(message string) {
	if r.onLog != nil {
		r.onLog(message)
	}
}

// RollbackConfig holds configuration for selective rollback
type RollbackConfig struct {
	VMIDs      []int  // Specific VMIDs to rollback
	StopOnly   bool   // Only stop, don't destroy
	Force      bool   // Force destroy even if stop fails
	Timeout    int    // Timeout in seconds for each operation
}

// SelectiveRollback performs rollback based on configuration
func (r *RollbackManager) SelectiveRollback(cfg RollbackConfig) error {
	vmIDs := cfg.VMIDs
	if len(vmIDs) == 0 {
		vmIDs = r.vmIDs
	}

	if len(vmIDs) == 0 {
		return nil
	}

	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = 60
	}

	r.log(fmt.Sprintf("Selective rollback: %d VMs", len(vmIDs)))
	var errors []error

	for i := len(vmIDs) - 1; i >= 0; i-- {
		vmid := vmIDs[i]

		// Stop VM
		r.log(fmt.Sprintf("Stopping VM %d...", vmid))
		if err := r.vmCreator.StopVM(vmid); err != nil {
			r.log(fmt.Sprintf("Stop VM %d failed: %v", vmid, err))
			if !cfg.Force {
				errors = append(errors, fmt.Errorf("stop VM %d: %w", vmid, err))
				continue
			}
		}

		if cfg.StopOnly {
			continue
		}

		// Wait for stop
		time.Sleep(3 * time.Second)

		// Destroy VM
		r.log(fmt.Sprintf("Destroying VM %d...", vmid))
		if err := r.vmCreator.DestroyVM(vmid); err != nil {
			r.log(fmt.Sprintf("Destroy VM %d failed: %v", vmid, err))
			errors = append(errors, fmt.Errorf("destroy VM %d: %w", vmid, err))
		}
	}

	// Remove destroyed VMs from tracking
	if !cfg.StopOnly {
		r.vmIDs = []int{}
	}

	if len(errors) > 0 {
		return fmt.Errorf("%d rollback operations failed", len(errors))
	}

	return nil
}

// RollbackByTags destroys VMs with specific tags
func RollbackByTags(client *ssh.Client, tags []string) error {
	discoverer := proxmox.NewDiscoverer(client)
	vmCreator := proxmox.NewVMCreator(client)

	vms, err := discoverer.GetVMs()
	if err != nil {
		return fmt.Errorf("getting VMs: %w", err)
	}

	var toDestroy []int
	for _, vm := range vms {
		for _, tag := range vm.Tags {
			for _, targetTag := range tags {
				if tag == targetTag {
					toDestroy = append(toDestroy, vm.VMID)
					break
				}
			}
		}
	}

	if len(toDestroy) == 0 {
		return nil
	}

	for _, vmid := range toDestroy {
		vmCreator.StopVM(vmid)
		time.Sleep(2 * time.Second)
		if err := vmCreator.DestroyVM(vmid); err != nil {
			// Continue with other VMs
		}
	}

	return nil
}
