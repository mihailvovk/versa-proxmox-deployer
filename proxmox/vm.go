package proxmox

import (
	"fmt"
	"strings"

	"github.com/oliverwhk/versa-proxmox-deployer/config"
	"github.com/oliverwhk/versa-proxmox-deployer/ssh"
)

// VMCreator handles VM creation on Proxmox
type VMCreator struct {
	client *ssh.Client
}

// NewVMCreator creates a new VM creator
func NewVMCreator(client *ssh.Client) *VMCreator {
	return &VMCreator{client: client}
}

// VMConfig holds configuration for creating a VM
type VMConfig struct {
	VMID        int
	Name        string
	Description string
	Node        string // Target node (for cluster)
	CPUCores    int
	RAMGB       int
	DiskGB      int
	Storage     string // Storage pool for disk
	ISOStorage  string // Storage pool for ISO
	ISOFile     string // ISO filename
	Networks    []VMNetwork
	Tags        []string
	StartOnBoot bool
	OnBoot      bool
}

// VMNetwork holds network interface configuration
type VMNetwork struct {
	Bridge   string
	VLAN     int    // 0 for native/untagged
	Model    string // virtio, e1000, etc.
	Firewall bool
	Name     string // Descriptive name for the network purpose
}

// NetworkPurpose defines the purpose of a network interface
type NetworkPurpose string

const (
	NetworkNorthbound        NetworkPurpose = "northbound"        // Management network (all components)
	NetworkDirectorRouter    NetworkPurpose = "director-router"   // Director <-> Router
	NetworkControllerRouter  NetworkPurpose = "controller-router" // Controller <-> Router
	NetworkControllerWAN     NetworkPurpose = "controller-wan"    // Controller WAN
	NetworkAnalyticsCluster  NetworkPurpose = "analytics-cluster" // Analytics cluster sync
	NetworkRouterHA          NetworkPurpose = "router-ha"         // Router HA sync
	NetworkDirectorSouthbound NetworkPurpose = "director-south"   // Director southbound
	NetworkAnalyticsSouthbound NetworkPurpose = "analytics-south" // Analytics southbound
	NetworkConcertoSouthbound NetworkPurpose = "concerto-south"   // Concerto southbound
	NetworkFlexVNFWAN        NetworkPurpose = "flexvnf-wan"       // FlexVNF WAN
	NetworkFlexVNFLAN        NetworkPurpose = "flexvnf-lan"       // FlexVNF LAN
)

// GetNetworkDescription returns a human-readable description for a network purpose
func GetNetworkDescription(purpose NetworkPurpose) string {
	descriptions := map[NetworkPurpose]string{
		NetworkNorthbound:        "Management/Northbound Network",
		NetworkDirectorRouter:    "Director to Router Link",
		NetworkControllerRouter:  "Controller to Router Link",
		NetworkControllerWAN:     "Controller WAN Interface",
		NetworkAnalyticsCluster:  "Analytics Cluster Sync",
		NetworkRouterHA:          "Router HA Synchronization",
		NetworkDirectorSouthbound: "Director Southbound",
		NetworkAnalyticsSouthbound: "Analytics Southbound",
		NetworkConcertoSouthbound: "Concerto Southbound",
		NetworkFlexVNFWAN:        "FlexVNF WAN Interface",
		NetworkFlexVNFLAN:        "FlexVNF LAN Interface",
	}
	if desc, ok := descriptions[purpose]; ok {
		return desc
	}
	return string(purpose)
}

// CreateVM creates a new VM on Proxmox
func (c *VMCreator) CreateVM(cfg VMConfig) error {
	// Build qm create command
	args := []string{
		fmt.Sprintf("%d", cfg.VMID),
		fmt.Sprintf("--name %s", cfg.Name),
		fmt.Sprintf("--memory %d", cfg.RAMGB*1024),
		fmt.Sprintf("--cores %d", cfg.CPUCores),
		"--cpu cputype=host",
		"--ostype l26",
		"--scsihw virtio-scsi-pci",
	}

	// Add description if provided
	if cfg.Description != "" {
		args = append(args, fmt.Sprintf("--description '%s'", cfg.Description))
	}

	// Add IDE for CD-ROM with ISO
	if cfg.ISOFile != "" {
		isoPath := fmt.Sprintf("%s:iso/%s", cfg.ISOStorage, cfg.ISOFile)
		args = append(args, fmt.Sprintf("--ide2 %s,media=cdrom", isoPath))
	}

	// Boot order: disk first so after OS install the VM boots from disk, not ISO again
	args = append(args, "--boot 'order=scsi0;ide2'")

	// Add network interfaces
	for i, net := range cfg.Networks {
		model := net.Model
		if model == "" {
			model = "virtio"
		}

		netArg := fmt.Sprintf("--net%d %s,bridge=%s", i, model, net.Bridge)
		if net.VLAN > 0 {
			netArg += fmt.Sprintf(",tag=%d", net.VLAN)
		}
		if net.Firewall {
			netArg += ",firewall=1"
		}
		args = append(args, netArg)
	}

	// Create disk
	diskArg := fmt.Sprintf("--scsi0 %s:%d", cfg.Storage, cfg.DiskGB)
	args = append(args, diskArg)

	// Add tags
	if len(cfg.Tags) > 0 {
		args = append(args, fmt.Sprintf("--tags '%s'", strings.Join(cfg.Tags, ";")))
	}

	// Add start on boot
	if cfg.StartOnBoot {
		args = append(args, "--onboot 1")
	}

	// Execute command
	cmd := fmt.Sprintf("qm create %s", strings.Join(args, " "))
	if err := c.client.RunQuiet(cmd); err != nil {
		return fmt.Errorf("creating VM: %w", err)
	}

	return nil
}

// StartVM starts a VM
func (c *VMCreator) StartVM(vmid int) error {
	return c.client.RunQuiet(fmt.Sprintf("qm start %d", vmid))
}

// StopVM stops a VM (force after 10s timeout)
func (c *VMCreator) StopVM(vmid int) error {
	return c.client.RunQuiet(fmt.Sprintf("qm stop %d --timeout 10", vmid))
}

// DestroyVM destroys a VM and purges its disks
func (c *VMCreator) DestroyVM(vmid int) error {
	// First try to stop if running
	c.client.Run(fmt.Sprintf("qm stop %d 2>/dev/null || true", vmid))

	// Then destroy with purge
	return c.client.RunQuiet(fmt.Sprintf("qm destroy %d --purge", vmid))
}

// SetVMTags sets tags on a VM
func (c *VMCreator) SetVMTags(vmid int, tags []string) error {
	return c.client.RunQuiet(fmt.Sprintf("qm set %d --tags %s", vmid, strings.Join(tags, ";")))
}

// GetVMStatus gets the status of a VM
func (c *VMCreator) GetVMStatus(vmid int) (string, error) {
	result, err := c.client.Run(fmt.Sprintf("qm status %d", vmid))
	if err != nil {
		return "", err
	}

	// Parse "status: running" or "status: stopped"
	output := strings.TrimSpace(result.Stdout)
	parts := strings.Split(output, ":")
	if len(parts) >= 2 {
		return strings.TrimSpace(parts[1]), nil
	}

	return output, nil
}

// GetConsoleURL returns the URL for VM console access
func (c *VMCreator) GetConsoleURL(vmid int, host string) string {
	return fmt.Sprintf("https://%s:8006/#v1:0:qemu/%d", host, vmid)
}

// BuildVMConfigForComponent creates a VMConfig for a Versa component
func BuildVMConfigForComponent(
	comp config.ComponentConfig,
	prefix string,
	index int,
	storage string,
	isoStorage string,
	networks []VMNetwork,
	vmid int,
) VMConfig {
	// Build name
	name := fmt.Sprintf("%s-%s", prefix, comp.Type)
	if index > 0 || comp.Count > 1 {
		name = fmt.Sprintf("%s-%d", name, index+1)
	}

	// Build tags
	tags := []string{
		config.TagVersaDeployer,
		config.GetComponentTag(comp.Type),
		fmt.Sprintf("versa-deploy-%s", prefix),
	}
	if comp.Count > 1 {
		tags = append(tags, fmt.Sprintf("versa-ha-%d", index+1))
	}

	// Build description
	spec := config.DefaultVMSpecs[comp.Type]
	description := spec.Description
	if comp.Version != "" {
		description += fmt.Sprintf(" (v%s)", comp.Version)
	}

	return VMConfig{
		VMID:        vmid,
		Name:        name,
		Description: description,
		Node:        comp.Node,
		CPUCores:    comp.CPU,
		RAMGB:       comp.RAMGB,
		DiskGB:      comp.DiskGB,
		Storage:     storage,
		ISOStorage:  isoStorage,
		ISOFile:     comp.ISOPath,
		Networks:    networks,
		Tags:        tags,
		OnBoot:      true,
	}
}

// BuildNetworksForComponent returns the network configuration for a component
func BuildNetworksForComponent(
	compType config.ComponentType,
	netConfig config.NetworkConfig,
	haMode bool,
) []VMNetwork {
	var networks []VMNetwork

	// Helper to add network
	addNet := func(bridge string, vlan int, name string) {
		if bridge != "" {
			networks = append(networks, VMNetwork{
				Bridge: bridge,
				VLAN:   vlan,
				Model:  "virtio",
				Name:   name,
			})
		}
	}

	switch compType {
	case config.ComponentDirector:
		// eth0: Northbound (management)
		addNet(netConfig.NorthboundBridge, netConfig.NorthboundVLAN, string(NetworkNorthbound))
		// eth1: Southbound / to router
		addNet(netConfig.DirectorRouterBridge, netConfig.DirectorRouterVLAN, string(NetworkDirectorRouter))

	case config.ComponentAnalytics:
		// eth0: Northbound (management)
		addNet(netConfig.NorthboundBridge, netConfig.NorthboundVLAN, string(NetworkNorthbound))
		// eth1: Southbound (can share Director's southbound)
		addNet(netConfig.DirectorRouterBridge, netConfig.DirectorRouterVLAN, string(NetworkAnalyticsSouthbound))
		// eth2: Cluster sync (optional)
		if netConfig.AnalyticsClusterBridge != "" {
			addNet(netConfig.AnalyticsClusterBridge, netConfig.AnalyticsClusterVLAN, string(NetworkAnalyticsCluster))
		}

	case config.ComponentController:
		// eth0: Northbound (management)
		addNet(netConfig.NorthboundBridge, netConfig.NorthboundVLAN, string(NetworkNorthbound))
		// eth1: To router
		addNet(netConfig.ControllerRouterBridge, netConfig.ControllerRouterVLAN, string(NetworkControllerRouter))
		// eth2-4: WAN interfaces (1-3)
		for i, bridge := range netConfig.ControllerWANBridges {
			vlan := 0
			if i < len(netConfig.ControllerWANVLANs) {
				vlan = netConfig.ControllerWANVLANs[i]
			}
			addNet(bridge, vlan, fmt.Sprintf("%s-%d", NetworkControllerWAN, i+1))
		}

	case config.ComponentRouter:
		// eth0: Northbound (management) - same bridge as Director/Analytics
		addNet(netConfig.NorthboundBridge, netConfig.NorthboundVLAN, string(NetworkNorthbound))
		// eth1: To Director
		addNet(netConfig.DirectorRouterBridge, netConfig.DirectorRouterVLAN, string(NetworkDirectorRouter))
		// eth2: To Controller
		addNet(netConfig.ControllerRouterBridge, netConfig.ControllerRouterVLAN, string(NetworkControllerRouter))
		// eth3: HA interface (only if HA mode and RouterHA network is configured)
		if haMode && netConfig.RouterHABridge != "" {
			addNet(netConfig.RouterHABridge, netConfig.RouterHAVLAN, string(NetworkRouterHA))
		}

	case config.ComponentConcerto:
		// eth0: Northbound (management)
		addNet(netConfig.NorthboundBridge, netConfig.NorthboundVLAN, string(NetworkNorthbound))
		// eth1: Southbound
		addNet(netConfig.DirectorRouterBridge, netConfig.DirectorRouterVLAN, string(NetworkConcertoSouthbound))

	case config.ComponentFlexVNF:
		// eth0: Management
		addNet(netConfig.NorthboundBridge, netConfig.NorthboundVLAN, string(NetworkNorthbound))
		// eth1: WAN
		if len(netConfig.ControllerWANBridges) > 0 {
			vlan := 0
			if len(netConfig.ControllerWANVLANs) > 0 {
				vlan = netConfig.ControllerWANVLANs[0]
			}
			addNet(netConfig.ControllerWANBridges[0], vlan, string(NetworkFlexVNFWAN))
		}
		// eth2: LAN - typically a separate bridge/VLAN
		// This should be configurable by the user
	}

	return networks
}

// AddRouterHANetwork adds the HA synchronization network to a Router's network config
func AddRouterHANetwork(networks []VMNetwork, bridge string, vlan int) []VMNetwork {
	return append(networks, VMNetwork{
		Bridge: bridge,
		VLAN:   vlan,
		Model:  "virtio",
		Name:   string(NetworkRouterHA),
	})
}
