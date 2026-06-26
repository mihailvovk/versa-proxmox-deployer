package config

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Config represents the application configuration stored in ./config.json (current working directory)
type Config struct {
	// Image sources (up to 10)
	ImageSources []ImageSource `json:"image_sources,omitempty"`

	// Custom image overrides per component
	CustomImages map[string]string `json:"custom_images,omitempty"`

	// Last used settings for convenience
	LastProxmoxHost     string `json:"last_proxmox_host,omitempty"`
	LastProxmoxUser     string `json:"last_proxmox_user,omitempty"`
	LastProxmoxPassword string `json:"last_proxmox_password,omitempty"`
	LastStorage         string `json:"last_storage,omitempty"`
	LastSSHKeyPath      string `json:"last_ssh_key_path,omitempty"`

	// Director connection info (saved after successful deployment)
	DirectorIP       string `json:"director_ip,omitempty"`
	DirectorUsername string `json:"director_username,omitempty"`
}

// ImageSource represents a source for Versa ISO images
type ImageSource struct {
	URL      string `json:"url"`
	Type     string `json:"type"` // dropbox, http, sftp, local
	Name     string `json:"name,omitempty"`
	SSHKey   string `json:"ssh_key,omitempty"`   // For SFTP sources
	Password string `json:"password,omitempty"` // For SFTP sources (not recommended)
}

var (
	configDirOnce sync.Once
	configDirPath string
)

// ConfigDir returns the per-user configuration directory. It prefers the OS
// config location (e.g. %AppData% on Windows, ~/Library/Application Support on
// macOS) so secrets, certs and the image cache don't land in a cloud-synced
// working directory; it falls back to the executable's directory, then the cwd.
// The directory is created and a legacy cwd config.json is migrated once.
func ConfigDir() string {
	configDirOnce.Do(func() {
		configDirPath = resolveConfigDir()
		migrateLegacyConfig(configDirPath)
	})
	return configDirPath
}

func resolveConfigDir() string {
	if base, err := os.UserConfigDir(); err == nil {
		dir := filepath.Join(base, "versa-proxmox-deployer")
		if err := os.MkdirAll(dir, 0700); err == nil {
			return dir
		}
	}
	if exe, err := os.Executable(); err == nil {
		return filepath.Dir(exe)
	}
	if wd, err := os.Getwd(); err == nil {
		return wd
	}
	return "."
}

// migrateLegacyConfig copies a config.json from the current working directory
// (the old storage location) into the new config dir once, so upgrading users
// keep their saved settings.
func migrateLegacyConfig(dir string) {
	newPath := filepath.Join(dir, "config.json")
	if _, err := os.Stat(newPath); err == nil {
		return // already have a config in the new location
	}
	wd, err := os.Getwd()
	if err != nil {
		return
	}
	oldPath := filepath.Join(wd, "config.json")
	if oldPath == newPath {
		return
	}
	data, err := os.ReadFile(oldPath)
	if err != nil {
		return // no legacy config to migrate
	}
	// Write atomically (temp + rename) — a plain WriteFile interrupted mid-write
	// would leave a truncated canonical config that the os.Stat guard then never
	// re-migrates, so Load would fail to parse forever.
	tmp, err := os.CreateTemp(dir, "config-*.json.tmp")
	if err != nil {
		return
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after a successful rename
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return
	}
	tmp.Chmod(0600) // best-effort
	if err := tmp.Close(); err != nil {
		return
	}
	if err := os.Rename(tmpName, newPath); err == nil {
		slog.Info("migrated config to new location", "from", oldPath, "to", newPath)
	}
}

// ConfigPath returns the full path to the config file
func ConfigPath() string {
	return filepath.Join(ConfigDir(), "config.json")
}

// CacheDir returns the cache directory for downloaded images
func CacheDir() string {
	return filepath.Join(ConfigDir(), "images")
}

// Default returns a Config with all maps/slices initialized. Use this as a
// fallback when Load fails so callers never dereference a nil Config or write
// to a nil map.
func Default() *Config {
	return &Config{
		ImageSources: []ImageSource{},
		CustomImages: make(map[string]string),
	}
}

// Load reads the configuration from disk
func Load() (*Config, error) {
	cfg := Default()

	data, err := os.ReadFile(ConfigPath())
	if err != nil {
		if os.IsNotExist(err) {
			// Return default config if file doesn't exist
			return cfg, nil
		}
		return nil, fmt.Errorf("reading config: %w", err)
	}

	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}

	// Ensure maps are initialized
	if cfg.CustomImages == nil {
		cfg.CustomImages = make(map[string]string)
	}
	if cfg.ImageSources == nil {
		cfg.ImageSources = []ImageSource{}
	}

	return cfg, nil
}

// Save writes the configuration to disk atomically (temp file + rename) so a
// crash or concurrent writer can never leave a truncated/interleaved config.json.
// The temp file is created in the same directory so the rename is atomic on the
// same filesystem (and uses MoveFileEx replace semantics on Windows).
func (c *Config) Save() error {
	dir := ConfigDir()
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("creating config directory: %w", err)
	}

	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling config: %w", err)
	}

	tmp, err := os.CreateTemp(dir, "config-*.json.tmp")
	if err != nil {
		return fmt.Errorf("creating temp config: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after a successful rename

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("writing temp config: %w", err)
	}
	if err := tmp.Chmod(0600); err != nil { // best-effort; ineffective on Windows ACLs
		tmp.Close()
		return fmt.Errorf("setting config permissions: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing temp config: %w", err)
	}
	if err := os.Rename(tmpName, ConfigPath()); err != nil {
		return fmt.Errorf("writing config: %w", err)
	}

	return nil
}

// AddImageSource adds a new image source to the config
func (c *Config) AddImageSource(source ImageSource) error {
	if len(c.ImageSources) >= 10 {
		return fmt.Errorf("maximum of 10 image sources allowed")
	}

	// Check for duplicate URLs
	for _, existing := range c.ImageSources {
		if existing.URL == source.URL {
			return fmt.Errorf("source already exists: %s", source.URL)
		}
	}

	c.ImageSources = append(c.ImageSources, source)
	return nil
}

// RemoveImageSource removes an image source by URL or name
func (c *Config) RemoveImageSource(url string) bool {
	for i, source := range c.ImageSources {
		if source.URL == url || source.Name == url {
			c.ImageSources = append(c.ImageSources[:i], c.ImageSources[i+1:]...)
			return true
		}
	}
	return false
}

// ExpandPath expands a leading ~ to the home directory. Handles both POSIX
// (~/foo) and Windows (~\foo) separators. filepath.Join is correct here because
// this is a local path on the machine the binary runs on, not a remote path.
func ExpandPath(path string) string {
	if strings.HasPrefix(path, "~/") || strings.HasPrefix(path, `~\`) {
		home, err := os.UserHomeDir()
		if err != nil {
			return path
		}
		return filepath.Join(home, path[2:])
	}
	return path
}

// DeploymentConfig holds the configuration for a specific deployment
type DeploymentConfig struct {
	// Proxmox connection
	ProxmoxHost string
	SSHUser     string
	SSHKeyPath  string
	SSHPassword string

	// Deployment naming
	Prefix string // e.g., "lab", "prod"

	// Cluster/node selection
	ClusterMode  bool     // true if deploying to cluster
	TargetNodes  []string // Nodes to deploy to
	StoragePool  string   // Storage pool name
	HAMode       bool     // High availability mode

	// Component selection
	Components []ComponentConfig

	// Network configuration
	Networks NetworkConfig

	// IP configuration
	IPConfig IPConfig
}

// ComponentConfig holds configuration for a single component deployment
type ComponentConfig struct {
	Type     ComponentType
	Count    int    // 1 for standard, 2 for HA
	CPU      int    // vCPU cores
	RAMGB    int    // RAM in GB
	DiskGB   int    // Disk in GB
	Node     string // Target Proxmox node
	ISOPath  string // Path to ISO on Proxmox
	Version  string // ISO version string
}

// EffectiveCount returns the number of VMs this component produces, applying the
// "Count == 0 means 1" convention used throughout deployment.
func (c ComponentConfig) EffectiveCount() int {
	if c.Count == 0 {
		return 1
	}
	return c.Count
}

// NetworkConfig holds network bridge and VLAN configuration
type NetworkConfig struct {
	// Northbound (management) network - all components
	NorthboundBridge string
	NorthboundVLAN   int // 0 for native/untagged

	// Director <-> Router network
	DirectorRouterBridge string
	DirectorRouterVLAN   int

	// Controller <-> Router network
	ControllerRouterBridge string
	ControllerRouterVLAN   int

	// Controller WAN networks (1-3)
	ControllerWANBridges []string
	ControllerWANVLANs   []int

	// Analytics cluster network (optional)
	AnalyticsClusterBridge string
	AnalyticsClusterVLAN   int

	// Router HA synchronization network (only for HA mode)
	RouterHABridge string
	RouterHAVLAN   int

	// Per-component interface ordering. Keys are component type names (e.g. "controller").
	// Values are ordered lists of interface IDs (e.g. ["base:0", "base:1", "wan:0"]).
	// When set, BuildNetworksForComponent uses this to reorder the network interfaces.
	InterfaceOrder map[string][]string
}

// VLANPurpose represents the purpose/name of a VLAN configuration
type VLANPurpose string

const (
	VLANManagement       VLANPurpose = "Management"
	VLANDirectorRouter   VLANPurpose = "Director-Router"
	VLANControllerRouter VLANPurpose = "Controller-Router"
	VLANControllerWAN1   VLANPurpose = "Controller-WAN-1"
	VLANControllerWAN2   VLANPurpose = "Controller-WAN-2"
	VLANControllerWAN3   VLANPurpose = "Controller-WAN-3"
	VLANAnalyticsCluster VLANPurpose = "Analytics-Cluster"
	VLANRouterHA         VLANPurpose = "Router-HA-Sync"
)

// GetVLANDescription returns a human-readable description for a VLAN purpose
func GetVLANDescription(purpose VLANPurpose) string {
	descriptions := map[VLANPurpose]string{
		VLANManagement:       "Management network for all components (northbound access)",
		VLANDirectorRouter:   "Internal link between Director and Router",
		VLANControllerRouter: "Internal link between Controller and Router",
		VLANControllerWAN1:   "Controller WAN interface 1 (Internet/MPLS)",
		VLANControllerWAN2:   "Controller WAN interface 2 (backup link)",
		VLANControllerWAN3:   "Controller WAN interface 3 (tertiary link)",
		VLANAnalyticsCluster: "Analytics cluster synchronization",
		VLANRouterHA:         "Router HA pair synchronization",
	}
	if desc, ok := descriptions[purpose]; ok {
		return desc
	}
	return string(purpose)
}

// IPConfig holds IP address configuration
type IPConfig struct {
	// Management subnet for auto-assignment
	ManagementSubnet  string // e.g., "10.0.0.0/24"
	ManagementGateway string // e.g., "10.0.0.1"

	// Manual IP assignments (component name -> IP)
	ManualIPs map[string]string
}

// NewDeploymentConfig creates a new deployment config with defaults
func NewDeploymentConfig() *DeploymentConfig {
	return &DeploymentConfig{
		SSHUser:    "root",
		Components: []ComponentConfig{},
		Networks:   NetworkConfig{},
		IPConfig: IPConfig{
			ManualIPs: make(map[string]string),
		},
	}
}

// GetTotalResources calculates total resource requirements
func (dc *DeploymentConfig) GetTotalResources() (cpu int, ramGB int, diskGB int) {
	for _, comp := range dc.Components {
		count := comp.EffectiveCount()
		cpu += comp.CPU * count
		ramGB += comp.RAMGB * count
		diskGB += comp.DiskGB * count
	}
	return
}

// VMCount returns the total number of VMs to be created
func (dc *DeploymentConfig) VMCount() int {
	count := 0
	for _, comp := range dc.Components {
		count += comp.EffectiveCount()
	}
	return count
}
