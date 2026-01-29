package deployer

import (
	"fmt"

	"github.com/mihailvovk/versa-proxmox-deployer/config"
	"github.com/mihailvovk/versa-proxmox-deployer/proxmox"
)

// NetworkPlan holds the planned network configuration
type NetworkPlan struct {
	// Network assignments with meaningful names
	Networks []NetworkAssignment

	// Summary for display
	Summary string
}

// NetworkAssignment represents a network bridge/VLAN assignment
type NetworkAssignment struct {
	Purpose     string // e.g., "Management (Northbound)"
	Bridge      string // e.g., "vmbr0"
	VLAN        int    // 0 for native/untagged
	Description string // Human-readable description
	Components  []config.ComponentType // Components using this network
}

// NetworkPurposeLabels provides human-readable labels for network purposes
var NetworkPurposeLabels = map[string]string{
	"northbound":         "Management (Northbound)",
	"director-router":    "Director ↔ Router Link",
	"controller-router":  "Controller ↔ Router Link",
	"controller-wan":     "Controller WAN",
	"analytics-cluster":  "Analytics Cluster Sync",
	"router-ha":          "Router HA Sync",
	"analytics-south":    "Analytics Southbound",
	"concerto-south":     "Concerto Southbound",
	"flexvnf-wan":        "FlexVNF WAN",
	"flexvnf-lan":        "FlexVNF LAN",
}

// BuildNetworkPlan creates a network plan from configuration
func BuildNetworkPlan(netConfig config.NetworkConfig, components []config.ComponentConfig) *NetworkPlan {
	plan := &NetworkPlan{}

	// Management network (used by all components)
	if netConfig.NorthboundBridge != "" {
		allComponents := []config.ComponentType{}
		for _, c := range components {
			allComponents = append(allComponents, c.Type)
		}

		plan.Networks = append(plan.Networks, NetworkAssignment{
			Purpose:     "northbound",
			Bridge:      netConfig.NorthboundBridge,
			VLAN:        netConfig.NorthboundVLAN,
			Description: formatNetworkDescription("Management (Northbound)", netConfig.NorthboundBridge, netConfig.NorthboundVLAN),
			Components:  allComponents,
		})
	}

	// Director-Router network
	if netConfig.DirectorRouterBridge != "" {
		plan.Networks = append(plan.Networks, NetworkAssignment{
			Purpose:     "director-router",
			Bridge:      netConfig.DirectorRouterBridge,
			VLAN:        netConfig.DirectorRouterVLAN,
			Description: formatNetworkDescription("Director ↔ Router", netConfig.DirectorRouterBridge, netConfig.DirectorRouterVLAN),
			Components:  []config.ComponentType{config.ComponentDirector, config.ComponentRouter, config.ComponentAnalytics},
		})
	}

	// Controller-Router network
	if netConfig.ControllerRouterBridge != "" {
		plan.Networks = append(plan.Networks, NetworkAssignment{
			Purpose:     "controller-router",
			Bridge:      netConfig.ControllerRouterBridge,
			VLAN:        netConfig.ControllerRouterVLAN,
			Description: formatNetworkDescription("Controller ↔ Router", netConfig.ControllerRouterBridge, netConfig.ControllerRouterVLAN),
			Components:  []config.ComponentType{config.ComponentController, config.ComponentRouter},
		})
	}

	// Controller WAN networks
	for i, bridge := range netConfig.ControllerWANBridges {
		vlan := 0
		if i < len(netConfig.ControllerWANVLANs) {
			vlan = netConfig.ControllerWANVLANs[i]
		}
		plan.Networks = append(plan.Networks, NetworkAssignment{
			Purpose:     fmt.Sprintf("controller-wan-%d", i+1),
			Bridge:      bridge,
			VLAN:        vlan,
			Description: formatNetworkDescription(fmt.Sprintf("Controller WAN %d", i+1), bridge, vlan),
			Components:  []config.ComponentType{config.ComponentController},
		})
	}

	// Analytics cluster network
	if netConfig.AnalyticsClusterBridge != "" {
		plan.Networks = append(plan.Networks, NetworkAssignment{
			Purpose:     "analytics-cluster",
			Bridge:      netConfig.AnalyticsClusterBridge,
			VLAN:        netConfig.AnalyticsClusterVLAN,
			Description: formatNetworkDescription("Analytics Cluster", netConfig.AnalyticsClusterBridge, netConfig.AnalyticsClusterVLAN),
			Components:  []config.ComponentType{config.ComponentAnalytics},
		})
	}

	return plan
}

// formatNetworkDescription formats a network description
func formatNetworkDescription(name, bridge string, vlan int) string {
	if vlan > 0 {
		return fmt.Sprintf("%s: %s / VLAN %d", name, bridge, vlan)
	}
	return fmt.Sprintf("%s: %s / native", name, bridge)
}

// ValidateNetworkConfig validates the network configuration
func ValidateNetworkConfig(netConfig config.NetworkConfig, available []proxmox.NetworkInfo) []string {
	var errors []string

	// Build map of available bridges
	bridges := make(map[string]bool)
	for _, net := range available {
		bridges[net.Name] = true
	}

	// Check required networks exist
	checkBridge := func(name, bridge string) {
		if bridge != "" && !bridges[bridge] {
			errors = append(errors, fmt.Sprintf("%s: bridge '%s' not found", name, bridge))
		}
	}

	checkBridge("Northbound", netConfig.NorthboundBridge)
	checkBridge("Director-Router", netConfig.DirectorRouterBridge)
	checkBridge("Controller-Router", netConfig.ControllerRouterBridge)
	checkBridge("Analytics Cluster", netConfig.AnalyticsClusterBridge)

	for i, bridge := range netConfig.ControllerWANBridges {
		checkBridge(fmt.Sprintf("Controller WAN %d", i+1), bridge)
	}

	return errors
}

// SuggestNetworkConfig suggests a network configuration based on available networks
func SuggestNetworkConfig(available []proxmox.NetworkInfo) config.NetworkConfig {
	cfg := config.NetworkConfig{}

	if len(available) == 0 {
		return cfg
	}

	// Use first bridge for management
	cfg.NorthboundBridge = available[0].Name

	// If more bridges available, use them for internal networks
	if len(available) > 1 {
		cfg.DirectorRouterBridge = available[1].Name
		cfg.ControllerRouterBridge = available[1].Name

		// Suggest VLANs if bridge is VLAN-aware
		if available[1].VLANAware {
			cfg.DirectorRouterVLAN = suggestVLAN(available[1].VLANs, 100)
			cfg.ControllerRouterVLAN = suggestVLAN(available[1].VLANs, 101)
		}
	}

	// If three or more bridges, use third for WAN
	if len(available) > 2 {
		cfg.ControllerWANBridges = []string{available[2].Name}
		if available[2].VLANAware {
			cfg.ControllerWANVLANs = []int{suggestVLAN(available[2].VLANs, 200)}
		}
	}

	return cfg
}

// suggestVLAN suggests a VLAN ID, preferring the default if available
func suggestVLAN(available []int, preferred int) int {
	if len(available) == 0 {
		return preferred
	}

	// Check if preferred is available
	for _, v := range available {
		if v == preferred {
			return preferred
		}
	}

	// Return first available
	return available[0]
}

// GetNetworkSummary returns a human-readable summary of network configuration
func GetNetworkSummary(netConfig config.NetworkConfig) string {
	summary := ""

	if netConfig.NorthboundBridge != "" {
		summary += fmt.Sprintf("Mgmt: %s", netConfig.NorthboundBridge)
		if netConfig.NorthboundVLAN > 0 {
			summary += fmt.Sprintf(" (VLAN %d)", netConfig.NorthboundVLAN)
		}
	}

	return summary
}

// NetworkRequirements returns the network interfaces needed for each component
func NetworkRequirements() map[config.ComponentType][]string {
	return map[config.ComponentType][]string{
		config.ComponentDirector: {
			"eth0: Management (Northbound)",
			"eth1: Southbound (to Router)",
		},
		config.ComponentAnalytics: {
			"eth0: Management (Northbound)",
			"eth1: Southbound (to Director/Router)",
			"eth2: Cluster Sync (optional)",
		},
		config.ComponentController: {
			"eth0: Management (Northbound)",
			"eth1: To Router",
			"eth2-4: WAN Interfaces (1-3)",
		},
		config.ComponentRouter: {
			"eth0: Management (Northbound)",
			"eth1: To Director",
			"eth2: To Controller",
			"eth3: HA Sync (if HA mode)",
		},
		config.ComponentConcerto: {
			"eth0: Management (Northbound)",
			"eth1: Southbound",
		},
		config.ComponentFlexVNF: {
			"eth0: Management",
			"eth1: WAN",
			"eth2: LAN",
		},
	}
}
