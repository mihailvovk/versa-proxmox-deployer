package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/mihailvovk/versa-proxmox-deployer/config"
	"github.com/mihailvovk/versa-proxmox-deployer/proxmox"
	"github.com/mihailvovk/versa-proxmox-deployer/sources"
	"github.com/mihailvovk/versa-proxmox-deployer/ssh"
)

// ConnectionPrompt prompts for Proxmox connection details
func ConnectionPrompt(lastHost, lastKeyPath string) (host, user, keyPath, password string, useKey bool, err error) {
	if lastHost == "" {
		lastHost = "192.168.1.100"
	}
	if lastKeyPath == "" {
		lastKeyPath = ssh.FindDefaultKey()
		if lastKeyPath == "" {
			lastKeyPath = "~/.ssh/id_rsa"
		}
	}

	host = lastHost
	user = "root"
	keyPath = lastKeyPath
	useKey = true

	form := huh.NewForm(
		huh.NewGroup(
			huh.NewInput().
				Title("Proxmox Host").
				Description("IP address or hostname of Proxmox server").
				Value(&host),

			huh.NewInput().
				Title("SSH User").
				Description("SSH username (usually root)").
				Value(&user),

			huh.NewConfirm().
				Title("Use SSH Key?").
				Description("Use SSH key authentication (recommended)").
				Value(&useKey),
		),
	)

	if err = form.Run(); err != nil {
		return
	}

	if useKey {
		keyForm := huh.NewForm(
			huh.NewGroup(
				huh.NewInput().
					Title("SSH Key Path").
					Description("Path to SSH private key").
					Value(&keyPath),
			),
		)
		if err = keyForm.Run(); err != nil {
			return
		}
	} else {
		passForm := huh.NewForm(
			huh.NewGroup(
				huh.NewInput().
					Title("Password").
					Description("SSH password").
					EchoMode(huh.EchoModePassword).
					Value(&password),
			),
		)
		if err = passForm.Run(); err != nil {
			return
		}
	}

	return
}

// DeploymentPrefixPrompt prompts for deployment prefix
func DeploymentPrefixPrompt(suggested string) (string, error) {
	if suggested == "" {
		suggested = "versa"
	}

	prefix := suggested

	form := huh.NewForm(
		huh.NewGroup(
			huh.NewInput().
				Title("Deployment Prefix").
				Description("Prefix for VM names (e.g., 'lab' creates 'lab-director-1')").
				Value(&prefix),
		),
	)

	if err := form.Run(); err != nil {
		return "", err
	}

	return prefix, nil
}

// ComponentSelectionPrompt prompts for component selection
func ComponentSelectionPrompt() ([]config.ComponentType, bool, error) {
	var selected []string
	haMode := false

	components := []huh.Option[string]{
		huh.NewOption("Director", string(config.ComponentDirector)).Selected(true),
		huh.NewOption("Analytics", string(config.ComponentAnalytics)).Selected(true),
		huh.NewOption("Controller", string(config.ComponentController)).Selected(true),
		huh.NewOption("Router", string(config.ComponentRouter)).Selected(true),
		huh.NewOption("Concerto", string(config.ComponentConcerto)),
		huh.NewOption("FlexVNF", string(config.ComponentFlexVNF)),
	}

	form := huh.NewForm(
		huh.NewGroup(
			huh.NewMultiSelect[string]().
				Title("Select Components").
				Description("Choose HeadEnd components to deploy").
				Options(components...).
				Value(&selected),

			huh.NewConfirm().
				Title("High Availability Mode?").
				Description("Deploy HA pairs for Director, Analytics, Controller, Router").
				Value(&haMode),
		),
	)

	if err := form.Run(); err != nil {
		return nil, false, err
	}

	var result []config.ComponentType
	for _, s := range selected {
		result = append(result, config.ComponentType(s))
	}

	return result, haMode, nil
}

// NodeSelectionPrompt prompts for node selection
func NodeSelectionPrompt(nodes []proxmox.NodeInfo) (string, error) {
	if len(nodes) == 0 {
		return "", fmt.Errorf("no nodes available")
	}

	if len(nodes) == 1 {
		return nodes[0].Name, nil
	}

	var options []huh.Option[string]
	for _, node := range nodes {
		status := "online"
		if node.Status != "online" {
			status = fmt.Sprintf("(%s)", node.Status)
		}
		label := fmt.Sprintf("%s %s - %d cores, %dGB RAM, %d VMs",
			node.Name, status, node.CPUCores, node.RAMGB, node.RunningVMs)
		options = append(options, huh.NewOption(label, node.Name))
	}

	var selected string
	form := huh.NewForm(
		huh.NewGroup(
			huh.NewSelect[string]().
				Title("Select Target Node").
				Description("Node to deploy VMs on").
				Options(options...).
				Value(&selected),
		),
	)

	if err := form.Run(); err != nil {
		return "", err
	}

	return selected, nil
}

// StorageSelectionPrompt prompts for storage selection
func StorageSelectionPrompt(storage []proxmox.StorageInfo, requiredGB int) (string, error) {
	var options []huh.Option[string]
	for _, s := range storage {
		if !s.Active {
			continue
		}

		// Check if storage supports images
		supportsImages := false
		for _, content := range s.Content {
			if content == "images" || content == "rootdir" {
				supportsImages = true
				break
			}
		}
		if !supportsImages {
			continue
		}

		label := fmt.Sprintf("%s (%s) - %dGB free", s.Name, s.Type, s.AvailableGB)
		if s.AvailableGB < requiredGB {
			label += " [insufficient]"
		}
		options = append(options, huh.NewOption(label, s.Name))
	}

	if len(options) == 0 {
		return "", fmt.Errorf("no suitable storage found")
	}

	var selected string
	form := huh.NewForm(
		huh.NewGroup(
			huh.NewSelect[string]().
				Title("Select Storage Pool").
				Description(fmt.Sprintf("Storage for VM disks (need %dGB)", requiredGB)).
				Options(options...).
				Value(&selected),
		),
	)

	if err := form.Run(); err != nil {
		return "", err
	}

	return selected, nil
}

// NetworkConfigPrompt prompts for network configuration
func NetworkConfigPrompt(networks []proxmox.NetworkInfo) (*config.NetworkConfig, error) {
	var bridgeOptions []huh.Option[string]
	for _, net := range networks {
		label := net.Name
		if net.Interface != "" {
			label += fmt.Sprintf(" (%s)", net.Interface)
		}
		if net.VLANAware {
			label += " [VLAN-aware]"
		}
		bridgeOptions = append(bridgeOptions, huh.NewOption(label, net.Name))
	}

	if len(bridgeOptions) == 0 {
		return nil, fmt.Errorf("no network bridges found")
	}

	cfg := &config.NetworkConfig{}

	// Management network
	form := huh.NewForm(
		huh.NewGroup(
			huh.NewSelect[string]().
				Title("Management Network (Northbound)").
				Description("Bridge for management traffic (all components)").
				Options(bridgeOptions...).
				Value(&cfg.NorthboundBridge),

			huh.NewInput().
				Title("Management VLAN (0 for native)").
				Description("VLAN tag for management network").
				Placeholder("0").
				Validate(validateVLAN).
				Value(new(string)),
		),
	)

	if err := form.Run(); err != nil {
		return nil, err
	}

	// Internal networks
	form2 := huh.NewForm(
		huh.NewGroup(
			huh.NewSelect[string]().
				Title("Director ↔ Router Network").
				Description("Bridge for Director to Router communication").
				Options(bridgeOptions...).
				Value(&cfg.DirectorRouterBridge),

			huh.NewSelect[string]().
				Title("Controller ↔ Router Network").
				Description("Bridge for Controller to Router communication").
				Options(bridgeOptions...).
				Value(&cfg.ControllerRouterBridge),
		),
	)

	if err := form2.Run(); err != nil {
		return nil, err
	}

	return cfg, nil
}

// ISOSelectionPrompt prompts for ISO selection
func ISOSelectionPrompt(collection *sources.ISOCollection, component config.ComponentType) (*sources.ISOFile, error) {
	isos := collection.GetISOsForComponent(component)
	if len(isos) == 0 {
		return nil, fmt.Errorf("no ISOs found for %s", component)
	}

	var options []huh.Option[int]
	for i, iso := range isos {
		md5Status := "no MD5"
		if iso.HasMD5File || iso.MD5 != "" {
			md5Status = "MD5 verified"
		}
		label := fmt.Sprintf("%s [%s] %s (%s)",
			iso.Version, iso.SourceName, md5Status, sources.FormatFileSize(iso.Size))
		options = append(options, huh.NewOption(label, i))
	}

	var selected int
	form := huh.NewForm(
		huh.NewGroup(
			huh.NewSelect[int]().
				Title(fmt.Sprintf("Select %s ISO", strings.Title(string(component)))).
				Description("Choose the version to deploy").
				Options(options...).
				Value(&selected),
		),
	)

	if err := form.Run(); err != nil {
		return nil, err
	}

	return &isos[selected], nil
}

// ExistingDeploymentPrompt prompts when existing deployment is found
func ExistingDeploymentPrompt(vms []proxmox.VMInfo) (action string, err error) {
	fmt.Println("\nExisting Versa deployment detected:")
	for _, vm := range vms {
		status := "stopped"
		if vm.Status == "running" {
			status = "running"
		}
		fmt.Printf("  • %s (VMID %d) - %s\n", vm.Name, vm.VMID, status)
	}
	fmt.Println()

	options := []huh.Option[string]{
		huh.NewOption("Deploy additional components (keep existing)", "add"),
		huh.NewOption("Replace existing deployment (delete and redeploy)", "replace"),
		huh.NewOption("View status of existing deployment", "status"),
		huh.NewOption("Cancel", "cancel"),
	}

	form := huh.NewForm(
		huh.NewGroup(
			huh.NewSelect[string]().
				Title("What would you like to do?").
				Options(options...).
				Value(&action),
		),
	)

	err = form.Run()
	return
}

// ConfirmDeploymentPrompt shows summary and asks for confirmation
func ConfirmDeploymentPrompt(cfg *config.DeploymentConfig, proxmoxInfo *proxmox.ProxmoxInfo) (bool, error) {
	fmt.Println("\n" + strings.Repeat("=", 60))
	fmt.Println("DEPLOYMENT SUMMARY")
	fmt.Println(strings.Repeat("=", 60))

	fmt.Printf("Prefix: %s\n", cfg.Prefix)
	fmt.Printf("Target: %s (Proxmox %s)\n", cfg.ProxmoxHost, proxmoxInfo.Version)
	fmt.Printf("Storage: %s\n", cfg.StoragePool)
	fmt.Printf("Mode: %s\n", map[bool]string{true: "High Availability", false: "Standard"}[cfg.HAMode])

	fmt.Println("\nComponents:")
	totalCPU, totalRAM, totalDisk := cfg.GetTotalResources()
	for _, comp := range cfg.Components {
		count := comp.Count
		if count == 0 {
			count = 1
		}
		fmt.Printf("  • %s: %dx (%d vCPU, %dGB RAM, %dGB disk) → %s\n",
			comp.Type, count, comp.CPU, comp.RAMGB, comp.DiskGB, comp.Node)
	}

	fmt.Printf("\nTotal Resources: %d vCPU, %dGB RAM, %dGB disk\n", totalCPU, totalRAM, totalDisk)

	fmt.Println("\nNetworks:")
	fmt.Printf("  Northbound: %s", cfg.Networks.NorthboundBridge)
	if cfg.Networks.NorthboundVLAN > 0 {
		fmt.Printf(" (VLAN %d)", cfg.Networks.NorthboundVLAN)
	}
	fmt.Println()

	fmt.Println(strings.Repeat("=", 60))

	var confirm bool
	form := huh.NewForm(
		huh.NewGroup(
			huh.NewConfirm().
				Title("Proceed with deployment?").
				Affirmative("Deploy").
				Negative("Cancel").
				Value(&confirm),
		),
	)

	if err := form.Run(); err != nil {
		return false, err
	}

	return confirm, nil
}

// AddSourcePrompt prompts to add a new image source
func AddSourcePrompt() (*config.ImageSource, error) {
	var url, name, srcType string

	form := huh.NewForm(
		huh.NewGroup(
			huh.NewInput().
				Title("Source URL or Path").
				Description("Dropbox URL, HTTP URL, SFTP URL, or local path").
				Value(&url),

			huh.NewInput().
				Title("Source Name (optional)").
				Description("Friendly name for this source").
				Value(&name),
		),
	)

	if err := form.Run(); err != nil {
		return nil, err
	}

	// Detect type
	srcType = string(sources.DetectSourceType(url))

	// If SFTP, ask for auth
	var sshKey, password string
	if srcType == "sftp" {
		useKey := true
		authForm := huh.NewForm(
			huh.NewGroup(
				huh.NewConfirm().
					Title("Use SSH Key for SFTP?").
					Value(&useKey),
			),
		)
		if err := authForm.Run(); err != nil {
			return nil, err
		}

		if useKey {
			keyForm := huh.NewForm(
				huh.NewGroup(
					huh.NewInput().
						Title("SSH Key Path").
						Value(&sshKey),
				),
			)
			if err := keyForm.Run(); err != nil {
				return nil, err
			}
		} else {
			passForm := huh.NewForm(
				huh.NewGroup(
					huh.NewInput().
						Title("Password").
						EchoMode(huh.EchoModePassword).
						Value(&password),
				),
			)
			if err := passForm.Run(); err != nil {
				return nil, err
			}
		}
	}

	return &config.ImageSource{
		URL:      url,
		Type:     srcType,
		Name:     name,
		SSHKey:   sshKey,
		Password: password,
	}, nil
}

// validateVLAN validates VLAN input
func validateVLAN(s string) error {
	if s == "" || s == "0" {
		return nil
	}
	var vlan int
	if _, err := fmt.Sscanf(s, "%d", &vlan); err != nil {
		return fmt.Errorf("invalid VLAN number")
	}
	if vlan < 0 || vlan > 4095 {
		return fmt.Errorf("VLAN must be 0-4095")
	}
	return nil
}
