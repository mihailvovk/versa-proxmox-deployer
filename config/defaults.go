package config


// ComponentType represents the type of Versa component
type ComponentType string

const (
	ComponentDirector   ComponentType = "director"
	ComponentAnalytics  ComponentType = "analytics"
	ComponentController ComponentType = "controller"
	ComponentConcerto   ComponentType = "concerto"
	ComponentRouter     ComponentType = "router"
	ComponentFlexVNF    ComponentType = "flexvnf"
)

// VMSpec defines the default resource specifications for a VM
type VMSpec struct {
	MinCPU         int    // Minimum vCPU cores
	DefaultCPU     int    // Default vCPU cores
	MinRAMGB       int    // Minimum RAM in GB
	DefaultRAMGB   int    // Default RAM in GB
	MinDiskGB      int    // Minimum disk in GB
	DefaultDiskGB  int    // Default disk in GB
	NetworkCount   int    // Number of network interfaces
	ISOPattern     string // Pattern to match ISO filename
	Description    string // Human-readable description
}

// DefaultVMSpecs contains the default specifications for each Versa component
var DefaultVMSpecs = map[ComponentType]VMSpec{
	ComponentDirector: {
		MinCPU:        8,
		DefaultCPU:    8,
		MinRAMGB:      16,
		DefaultRAMGB:  16,
		MinDiskGB:     100,
		DefaultDiskGB: 100,
		NetworkCount:  2, // eth0 (northbound), eth1 (southbound/router)
		ISOPattern:    "versa-director",
		Description:   "Versa Director - Central management and orchestration",
	},
	ComponentAnalytics: {
		MinCPU:        4,
		DefaultCPU:    4,
		MinRAMGB:      8,
		DefaultRAMGB:  8,
		MinDiskGB:     200,
		DefaultDiskGB: 200,
		NetworkCount:  3, // eth0 (northbound), eth1 (southbound), eth2 (cluster - optional)
		ISOPattern:    "versa-analytics",
		Description:   "Versa Analytics - Log collection and reporting",
	},
	ComponentController: {
		MinCPU:        4,
		DefaultCPU:    4,
		MinRAMGB:      8,
		DefaultRAMGB:  8,
		MinDiskGB:     50,
		DefaultDiskGB: 50,
		NetworkCount:  5, // eth0 (northbound), eth1 (router), eth2-4 (WAN 1-3)
		ISOPattern:    "versa-flexvnf",
		Description:   "Versa Controller - SD-WAN controller",
	},
	ComponentConcerto: {
		MinCPU:        4,
		DefaultCPU:    4,
		MinRAMGB:      8,
		DefaultRAMGB:  8,
		MinDiskGB:     50,
		DefaultDiskGB: 50,
		NetworkCount:  2, // eth0 (northbound), eth1 (southbound)
		ISOPattern:    "concerto",
		Description:   "Versa Concerto - Multi-tenant orchestration",
	},
	ComponentRouter: {
		MinCPU:        4,
		DefaultCPU:    4,
		MinRAMGB:      4,
		DefaultRAMGB:  4,
		MinDiskGB:     20,
		DefaultDiskGB: 20,
		NetworkCount:  3, // eth0 (northbound), eth1 (to-director), eth2 (to-controller)
		ISOPattern:    "versa-flexvnf",
		Description:   "Versa Router - HeadEnd router component",
	},
	ComponentFlexVNF: {
		MinCPU:        4,
		DefaultCPU:    4,
		MinRAMGB:      4,
		DefaultRAMGB:  4,
		MinDiskGB:     20,
		DefaultDiskGB: 20,
		NetworkCount:  3, // eth0 (mgmt), eth1 (wan), eth2 (lan)
		ISOPattern:    "versa-flexvnf",
		Description:   "Versa FlexVNF - Branch CPE device",
	},
}

// VMTags for deployment tracking
const (
	TagVersaDeployer   = "versa-deployer"
	TagVersaDirector   = "versa-director"
	TagVersaAnalytics  = "versa-analytics"
	TagVersaController = "versa-controller"
	TagVersaConcerto   = "versa-concerto"
	TagVersaRouter     = "versa-router"
	TagVersaFlexVNF    = "versa-flexvnf"
)

// GetComponentTag returns the tag for a component type
func GetComponentTag(ct ComponentType) string {
	switch ct {
	case ComponentDirector:
		return TagVersaDirector
	case ComponentAnalytics:
		return TagVersaAnalytics
	case ComponentController:
		return TagVersaController
	case ComponentConcerto:
		return TagVersaConcerto
	case ComponentRouter:
		return TagVersaRouter
	case ComponentFlexVNF:
		return TagVersaFlexVNF
	default:
		return ""
	}
}

// AllComponents returns all available component types
func AllComponents() []ComponentType {
	return []ComponentType{
		ComponentDirector,
		ComponentAnalytics,
		ComponentController,
		ComponentRouter,
		ComponentConcerto,
		ComponentFlexVNF,
	}
}

// HeadEndComponents returns the core HeadEnd components
func HeadEndComponents() []ComponentType {
	return []ComponentType{
		ComponentDirector,
		ComponentAnalytics,
		ComponentController,
		ComponentRouter,
		ComponentConcerto,
	}
}
