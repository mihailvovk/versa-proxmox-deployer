package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/oliverwhk/versa-proxmox-deployer/config"
	"github.com/oliverwhk/versa-proxmox-deployer/deployer"
	"github.com/oliverwhk/versa-proxmox-deployer/director"
	"github.com/oliverwhk/versa-proxmox-deployer/downloader"
	"github.com/oliverwhk/versa-proxmox-deployer/sources"
	"github.com/oliverwhk/versa-proxmox-deployer/ssh"
	"github.com/oliverwhk/versa-proxmox-deployer/ui"
	"github.com/oliverwhk/versa-proxmox-deployer/web"
)

var (
	// Version info set at build time
	Version   = "dev"
	BuildTime = "unknown"
)

func main() {
	var httpPort int
	var httpsPort int

	rootCmd := &cobra.Command{
		Use:   "versa-deployer",
		Short: "Versa HeadEnd Proxmox Deployer",
		Long:  `A tool to automate Versa HeadEnd deployment on Proxmox VE via a local web UI.`,
		Run: func(cmd *cobra.Command, args []string) {
			runWebUI(httpPort, httpsPort)
		},
	}

	rootCmd.Flags().IntVar(&httpPort, "http-port", 1050, "HTTP port for web UI")
	rootCmd.Flags().IntVar(&httpsPort, "https-port", 1051, "HTTPS port for web UI")

	// Version command
	rootCmd.AddCommand(&cobra.Command{
		Use:   "version",
		Short: "Print version information",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Printf("Versa HeadEnd Proxmox Deployer\n")
			fmt.Printf("Version: %s\n", Version)
			fmt.Printf("Built: %s\n", BuildTime)
		},
	})

	// Deploy command (non-interactive)
	deployCmd := &cobra.Command{
		Use:   "deploy",
		Short: "Deploy HeadEnd components (non-interactive)",
		Run:   runDeploy,
	}
	deployCmd.Flags().String("host", "", "Proxmox host IP/hostname")
	deployCmd.Flags().String("user", "root", "SSH username")
	deployCmd.Flags().String("ssh-key", "", "Path to SSH private key")
	deployCmd.Flags().String("password", "", "SSH password (if not using key)")
	deployCmd.Flags().String("prefix", "versa", "Deployment prefix for VM names")
	deployCmd.Flags().StringSlice("components", []string{"director", "analytics", "controller", "router"}, "Components to deploy")
	deployCmd.Flags().String("node", "", "Target Proxmox node")
	deployCmd.Flags().String("storage", "", "Storage pool for VM disks")
	deployCmd.Flags().String("mgmt-bridge", "vmbr0", "Management network bridge")
	deployCmd.Flags().Bool("ha", false, "Enable HA mode")
	rootCmd.AddCommand(deployCmd)

	// Status command
	statusCmd := &cobra.Command{
		Use:   "status",
		Short: "Check HeadEnd status via Director",
		Run:   runStatus,
	}
	statusCmd.Flags().String("director", "", "Director IP address")
	statusCmd.Flags().String("username", "Administrator", "Director username")
	statusCmd.Flags().String("password", "", "Director password")
	rootCmd.AddCommand(statusCmd)

	// Releases command
	releasesCmd := &cobra.Command{
		Use:   "releases",
		Short: "List available ISO releases from configured sources",
		Run:   runReleases,
	}
	rootCmd.AddCommand(releasesCmd)

	// Generate MD5 command
	md5Cmd := &cobra.Command{
		Use:   "generate-md5",
		Short: "Generate MD5 files for ISOs in a directory",
		Run:   runGenerateMD5,
	}
	md5Cmd.Flags().String("path", ".", "Path to directory containing ISOs")
	rootCmd.AddCommand(md5Cmd)

	// Add source command
	addSourceCmd := &cobra.Command{
		Use:   "add-source [url]",
		Short: "Add an image source",
		Args:  cobra.MaximumNArgs(1),
		Run:   runAddSource,
	}
	rootCmd.AddCommand(addSourceCmd)

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func runWebUI(httpPort, httpsPort int) {
	cfg, err := config.Load()
	if err != nil {
		fmt.Printf("Warning: Could not load config: %v\n", err)
		cfg = &config.Config{}
	}

	srv := web.NewServer(cfg, httpsPort)
	if err := srv.Start(httpPort); err != nil {
		fmt.Fprintf(os.Stderr, "Server error: %v\n", err)
		os.Exit(1)
	}
}


func runDeploy(cmd *cobra.Command, args []string) {
	host, _ := cmd.Flags().GetString("host")
	if host == "" {
		fmt.Fprintln(os.Stderr, "Error: --host is required")
		os.Exit(1)
	}

	user, _ := cmd.Flags().GetString("user")
	keyPath, _ := cmd.Flags().GetString("ssh-key")
	password, _ := cmd.Flags().GetString("password")

	if keyPath == "" && password == "" {
		// Try default key
		keyPath = ssh.FindDefaultKey()
		if keyPath == "" {
			fmt.Fprintln(os.Stderr, "Error: --ssh-key or --password required")
			os.Exit(1)
		}
	}

	// Connect
	sshOpts := ssh.ClientOptions{
		Host:     host,
		User:     user,
		KeyPath:  keyPath,
		Password: password,
	}

	client, err := ssh.NewClient(sshOpts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	if err := client.Connect(); err != nil {
		fmt.Fprintf(os.Stderr, "Connection failed: %v\n", err)
		os.Exit(1)
	}
	defer client.Close()

	fmt.Println("Connected to Proxmox")

	// Build deployment config from flags
	deployCfg := config.NewDeploymentConfig()
	deployCfg.ProxmoxHost = host
	deployCfg.SSHUser = user
	deployCfg.SSHKeyPath = keyPath
	deployCfg.SSHPassword = password

	deployCfg.Prefix, _ = cmd.Flags().GetString("prefix")
	deployCfg.HAMode, _ = cmd.Flags().GetBool("ha")
	deployCfg.StoragePool, _ = cmd.Flags().GetString("storage")

	mgmtBridge, _ := cmd.Flags().GetString("mgmt-bridge")
	deployCfg.Networks.NorthboundBridge = mgmtBridge

	componentStrs, _ := cmd.Flags().GetStringSlice("components")
	for _, cs := range componentStrs {
		compType := config.ComponentType(cs)
		spec := config.DefaultVMSpecs[compType]
		deployCfg.Components = append(deployCfg.Components, config.ComponentConfig{
			Type:   compType,
			Count:  1,
			CPU:    spec.DefaultCPU,
			RAMGB:  spec.DefaultRAMGB,
			DiskGB: spec.DefaultDiskGB,
		})
	}

	targetNode, _ := cmd.Flags().GetString("node")
	for i := range deployCfg.Components {
		deployCfg.Components[i].Node = targetNode
	}

	// Create sources and deployer
	cfg, _ := config.Load()
	imageSources, _ := sources.CreateSourcesFromConfig(cfg)

	d := deployer.NewDeployer(client, imageSources)
	d.SetConfig(deployCfg)

	d.OnLog = func(msg string) {
		fmt.Println(msg)
	}

	// Discover first
	if _, err := d.Discover(); err != nil {
		fmt.Fprintf(os.Stderr, "Discovery failed: %v\n", err)
		os.Exit(1)
	}

	// Deploy
	result, err := d.Deploy()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Deployment failed: %v\n", err)
		os.Exit(1)
	}

	if result.Success {
		fmt.Println("\nDeployment successful!")
		for _, vm := range result.VMs {
			fmt.Printf("  %s (VMID %d): %s\n", vm.Name, vm.VMID, vm.ConsoleURL)
		}
	}
}

func runStatus(cmd *cobra.Command, args []string) {
	directorIP, _ := cmd.Flags().GetString("director")
	username, _ := cmd.Flags().GetString("username")
	password, _ := cmd.Flags().GetString("password")

	if directorIP == "" {
		// Try to load from config
		cfg, _ := config.Load()
		if cfg.DirectorIP != "" {
			directorIP = cfg.DirectorIP
		} else {
			fmt.Fprintln(os.Stderr, "Error: --director IP is required")
			os.Exit(1)
		}
	}

	if password == "" {
		fmt.Fprintln(os.Stderr, "Error: --password is required")
		os.Exit(1)
	}

	client := director.NewClient(director.ClientConfig{
		Host:     directorIP,
		Username: username,
		Password: password,
		Insecure: true,
	})

	fmt.Printf("Connecting to Director at %s...\n", directorIP)

	status, err := client.GetHeadEndStatus()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to get status: %v\n", err)
		os.Exit(1)
	}

	ui.PrintHeadEndStatus(status)

	// Also get branch status
	branchStatus, err := client.GetBranchStatus()
	if err == nil && branchStatus != nil {
		fmt.Printf("\nBranch Devices: %d online, %d offline\n",
			branchStatus.OnlineCount, branchStatus.OfflineCount)
	}
}

func runReleases(cmd *cobra.Command, args []string) {
	cfg, _ := config.Load()
	imageSources, err := sources.CreateSourcesFromConfig(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("Scanning image sources...")

	collection, err := sources.ScanAllSources(imageSources)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Scan failed: %v\n", err)
		os.Exit(1)
	}

	ui.PrintSourcesTable(collection.Sources)

	if len(collection.Director) > 0 {
		ui.PrintISOTable(collection.Director, "Director")
	}
	if len(collection.Analytics) > 0 {
		ui.PrintISOTable(collection.Analytics, "Analytics")
	}
	if len(collection.FlexVNF) > 0 {
		ui.PrintISOTable(collection.FlexVNF, "FlexVNF/Controller/Router")
	}
	if len(collection.Concerto) > 0 {
		ui.PrintISOTable(collection.Concerto, "Concerto")
	}
}

func runGenerateMD5(cmd *cobra.Command, args []string) {
	path, _ := cmd.Flags().GetString("path")

	fmt.Printf("Generating MD5 files in %s...\n", path)

	generated, err := downloader.GenerateAllMD5Files(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	if len(generated) == 0 {
		fmt.Println("No new MD5 files generated (all ISOs already have MD5 files)")
	} else {
		fmt.Printf("Generated %d MD5 files:\n", len(generated))
		for _, g := range generated {
			fmt.Printf("  • %s\n", g)
		}
	}
}

func runAddSource(cmd *cobra.Command, args []string) {
	cfg, _ := config.Load()

	var source *config.ImageSource
	var err error

	if len(args) > 0 {
		// URL provided as argument
		sourceType := sources.DetectSourceType(args[0])
		source = &config.ImageSource{
			URL:  args[0],
			Type: string(sourceType),
		}
	} else {
		// Interactive prompt
		source, err = ui.AddSourcePrompt()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
	}

	// Validate source
	fmt.Printf("Testing connection to %s...\n", source.URL)

	src, err := sources.CreateSource(*source)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Invalid source: %v\n", err)
		os.Exit(1)
	}

	if err := sources.TestSourceConnection(src); err != nil {
		fmt.Fprintf(os.Stderr, "Connection test failed: %v\n", err)
		os.Exit(1)
	}

	// Add to config
	if err := cfg.AddImageSource(*source); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	if err := cfg.Save(); err != nil {
		fmt.Fprintf(os.Stderr, "Error saving config: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Source added successfully: %s (%s)\n", source.Name, source.Type)
}
