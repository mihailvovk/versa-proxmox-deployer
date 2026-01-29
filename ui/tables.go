package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/oliverwhk/versa-proxmox-deployer/config"
	"github.com/oliverwhk/versa-proxmox-deployer/director"
	"github.com/oliverwhk/versa-proxmox-deployer/proxmox"
	"github.com/oliverwhk/versa-proxmox-deployer/sources"
)

var (
	titleStyle = lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("12"))

	headerStyle = lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("15")).
		Background(lipgloss.Color("240"))

	cellStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color("15"))

	successStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color("10"))

	warningStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color("11"))

	errorStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color("9"))

	borderStyle = lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("240"))
)

// PrintTitle prints a formatted title
func PrintTitle(title string) {
	fmt.Println()
	fmt.Println(titleStyle.Render(title))
	fmt.Println(strings.Repeat("─", len(title)+4))
}

// PrintNodeTable prints a table of Proxmox nodes
func PrintNodeTable(nodes []proxmox.NodeInfo) {
	PrintTitle("Cluster Nodes")

	// Calculate column widths
	nameWidth := 10
	for _, n := range nodes {
		if len(n.Name) > nameWidth {
			nameWidth = len(n.Name)
		}
	}

	// Header
	header := fmt.Sprintf("%-*s  %-8s  %-10s  %-12s  %-10s",
		nameWidth, "Node", "Status", "CPU", "RAM", "VMs")
	fmt.Println(headerStyle.Render(header))

	// Rows
	for _, node := range nodes {
		status := successStyle.Render("online")
		if node.Status != "online" {
			status = errorStyle.Render(node.Status)
		}

		cpuInfo := fmt.Sprintf("%d/%d", node.CPUUsed, node.CPUCores)
		ramInfo := fmt.Sprintf("%d/%dGB", node.RAMUsedGB, node.RAMGB)
		vmInfo := fmt.Sprintf("%d running", node.RunningVMs)

		row := fmt.Sprintf("%-*s  %-8s  %-10s  %-12s  %-10s",
			nameWidth, node.Name, status, cpuInfo, ramInfo, vmInfo)
		fmt.Println(cellStyle.Render(row))
	}
	fmt.Println()
}

// PrintStorageTable prints a table of storage pools
func PrintStorageTable(storage []proxmox.StorageInfo) {
	PrintTitle("Storage Pools")

	header := fmt.Sprintf("%-15s  %-10s  %-12s  %-10s",
		"Storage", "Type", "Available", "Content")
	fmt.Println(headerStyle.Render(header))

	for _, s := range storage {
		if !s.Active {
			continue
		}

		availStyle := cellStyle
		if s.AvailableGB < 100 {
			availStyle = warningStyle
		}
		if s.AvailableGB < 50 {
			availStyle = errorStyle
		}

		content := strings.Join(s.Content, ",")
		if len(content) > 10 {
			content = content[:10] + "..."
		}

		row := fmt.Sprintf("%-15s  %-10s  %-12s  %-10s",
			s.Name, s.Type, availStyle.Render(fmt.Sprintf("%dGB", s.AvailableGB)), content)
		fmt.Println(cellStyle.Render(row))
	}
	fmt.Println()
}

// PrintNetworkTable prints a table of networks
func PrintNetworkTable(networks []proxmox.NetworkInfo) {
	PrintTitle("Network Bridges")

	header := fmt.Sprintf("%-10s  %-15s  %-15s",
		"Bridge", "Interface", "VLANs")
	fmt.Println(headerStyle.Render(header))

	for _, net := range networks {
		vlans := "native"
		if net.VLANAware && len(net.VLANs) > 0 {
			if len(net.VLANs) <= 5 {
				var vlanStrs []string
				for _, v := range net.VLANs {
					vlanStrs = append(vlanStrs, fmt.Sprintf("%d", v))
				}
				vlans = strings.Join(vlanStrs, ",")
			} else {
				vlans = fmt.Sprintf("%d-%d (%d VLANs)", net.VLANs[0], net.VLANs[len(net.VLANs)-1], len(net.VLANs))
			}
		}

		iface := net.Interface
		if iface == "" {
			iface = "-"
		}

		row := fmt.Sprintf("%-10s  %-15s  %-15s",
			net.Name, iface, vlans)
		fmt.Println(cellStyle.Render(row))
	}
	fmt.Println()
}

// PrintSourcesTable prints a table of image sources
func PrintSourcesTable(summaries []sources.SourceSummary) {
	PrintTitle("Image Sources")

	header := fmt.Sprintf("%-30s  %-10s  %-6s  %-6s  %-10s",
		"Source", "Type", "ISOs", "MD5s", "Status")
	fmt.Println(headerStyle.Render(header))

	for _, s := range summaries {
		name := s.Name
		if len(name) > 30 {
			name = name[:27] + "..."
		}

		status := successStyle.Render("OK")
		if s.Error != "" {
			status = errorStyle.Render("Error")
		} else if s.MD5Count < s.ISOCount {
			status = warningStyle.Render("Partial")
		}

		row := fmt.Sprintf("%-30s  %-10s  %-6d  %-6d  %-10s",
			name, s.Type, s.ISOCount, s.MD5Count, status)
		fmt.Println(cellStyle.Render(row))
	}
	fmt.Println()
}

// PrintISOTable prints a table of ISOs for a component
func PrintISOTable(isos []sources.ISOFile, componentName string) {
	PrintTitle(fmt.Sprintf("%s ISOs", componentName))

	header := fmt.Sprintf("%-12s  %-20s  %-10s  %-10s",
		"Version", "Source", "MD5", "Size")
	fmt.Println(headerStyle.Render(header))

	for _, iso := range isos {
		md5Status := warningStyle.Render("none")
		if iso.HasMD5File || iso.MD5 != "" {
			md5Status = successStyle.Render("verified")
		}

		srcName := iso.SourceName
		if len(srcName) > 20 {
			srcName = srcName[:17] + "..."
		}

		row := fmt.Sprintf("%-12s  %-20s  %-10s  %-10s",
			iso.Version, srcName, md5Status, sources.FormatFileSize(iso.Size))
		fmt.Println(cellStyle.Render(row))
	}
	fmt.Println()
}

// PrintComponentTable prints deployment components
func PrintComponentTable(components []config.ComponentConfig) {
	PrintTitle("Deployment Components")

	header := fmt.Sprintf("%-12s  %-5s  %-6s  %-8s  %-8s  %-10s  %-10s",
		"Component", "Count", "vCPU", "RAM", "Disk", "Node", "ISO")
	fmt.Println(headerStyle.Render(header))

	for _, comp := range components {
		count := comp.Count
		if count == 0 {
			count = 1
		}

		isoVer := "-"
		if comp.Version != "" {
			isoVer = comp.Version
		}

		row := fmt.Sprintf("%-12s  %-5d  %-6d  %-8s  %-8s  %-10s  %-10s",
			comp.Type, count, comp.CPU, fmt.Sprintf("%dGB", comp.RAMGB),
			fmt.Sprintf("%dGB", comp.DiskGB), comp.Node, isoVer)
		fmt.Println(cellStyle.Render(row))
	}

	// Total row
	totalCPU, totalRAM, totalDisk := 0, 0, 0
	vmCount := 0
	for _, comp := range components {
		count := comp.Count
		if count == 0 {
			count = 1
		}
		vmCount += count
		totalCPU += comp.CPU * count
		totalRAM += comp.RAMGB * count
		totalDisk += comp.DiskGB * count
	}

	fmt.Println(strings.Repeat("─", 70))
	total := fmt.Sprintf("%-12s  %-5d  %-6d  %-8s  %-8s",
		"Total", vmCount, totalCPU, fmt.Sprintf("%dGB", totalRAM), fmt.Sprintf("%dGB", totalDisk))
	fmt.Println(headerStyle.Render(total))
	fmt.Println()
}

// PrintHeadEndStatus prints the HeadEnd status
func PrintHeadEndStatus(status *director.HeadEndStatus) {
	PrintTitle("HeadEnd Status")

	healthStyle := successStyle
	if status.OverallHealth == "degraded" {
		healthStyle = warningStyle
	} else if status.OverallHealth == "critical" {
		healthStyle = errorStyle
	}

	fmt.Printf("Overall Health: %s\n", healthStyle.Render(status.OverallHealth))
	fmt.Printf("Components: %d total, %d healthy, %d unhealthy\n\n",
		status.TotalComponents, status.HealthyCount, status.UnhealthyCount)

	header := fmt.Sprintf("%-15s  %-15s  %-10s  %-12s  %-10s",
		"Component", "IP", "Status", "Version", "Uptime")
	fmt.Println(headerStyle.Render(header))

	printComponent := func(c *director.ComponentStatus) {
		if c == nil {
			return
		}
		statusStyle := successStyle
		if c.Status == "degraded" {
			statusStyle = warningStyle
		} else if c.Status == "offline" {
			statusStyle = errorStyle
		}

		row := fmt.Sprintf("%-15s  %-15s  %-10s  %-12s  %-10s",
			c.Name, c.IP, statusStyle.Render(c.Status), c.Version, c.Uptime)
		fmt.Println(cellStyle.Render(row))
	}

	printComponent(status.Director)
	printComponent(status.Analytics)
	for _, ctrl := range status.Controllers {
		printComponent(ctrl)
	}
	for _, router := range status.Routers {
		printComponent(router)
	}
	if status.Concerto != nil {
		printComponent(status.Concerto)
	}

	fmt.Println()
}

// PrintVMList prints a list of VMs
func PrintVMList(vms []proxmox.VMInfo) {
	PrintTitle("Virtual Machines")

	header := fmt.Sprintf("%-6s  %-25s  %-10s  %-20s",
		"VMID", "Name", "Status", "Tags")
	fmt.Println(headerStyle.Render(header))

	for _, vm := range vms {
		statusStyle := cellStyle
		if vm.Status == "running" {
			statusStyle = successStyle
		} else if vm.Status == "stopped" {
			statusStyle = warningStyle
		}

		tags := strings.Join(vm.Tags, ", ")
		if len(tags) > 20 {
			tags = tags[:17] + "..."
		}

		row := fmt.Sprintf("%-6d  %-25s  %-10s  %-20s",
			vm.VMID, vm.Name, statusStyle.Render(vm.Status), tags)
		fmt.Println(cellStyle.Render(row))
	}
	fmt.Println()
}

// PrintDeploymentResult prints the deployment result
func PrintDeploymentResult(success bool, vms []struct {
	Name       string
	VMID       int
	ConsoleURL string
}, errors []string) {
	if success {
		PrintTitle("Deployment Complete!")

		fmt.Println("VMs have been created and are booting from ISO.")
		fmt.Println("Complete the Versa installer via Proxmox console:\n")

		for _, vm := range vms {
			fmt.Printf("  • %s (VMID %d)\n", vm.Name, vm.VMID)
			fmt.Printf("    %s\n", vm.ConsoleURL)
		}

		fmt.Println("\nOr via terminal:")
		for _, vm := range vms {
			fmt.Printf("  qm terminal %d\n", vm.VMID)
		}
	} else {
		fmt.Println(errorStyle.Render("\nDeployment Failed"))

		if len(errors) > 0 {
			fmt.Println("\nErrors:")
			for _, err := range errors {
				fmt.Printf("  • %s\n", err)
			}
		}
	}
	fmt.Println()
}
