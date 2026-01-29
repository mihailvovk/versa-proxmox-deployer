package web

import (
	"crypto/tls"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/oliverwhk/versa-proxmox-deployer/config"
	"github.com/oliverwhk/versa-proxmox-deployer/deployer"
	"github.com/oliverwhk/versa-proxmox-deployer/proxmox"
	"github.com/oliverwhk/versa-proxmox-deployer/sources"
	"github.com/oliverwhk/versa-proxmox-deployer/ssh"
)

//go:embed static/*
var staticFiles embed.FS

// Server is the web UI server
type Server struct {
	cfg       *config.Config
	httpsPort int

	sshClient  *ssh.Client
	discoverer *proxmox.Discoverer

	// Cached discovery results
	mu             sync.RWMutex
	discoveryState *DiscoveryState

	// SSE clients for deployment progress
	sseMu      sync.Mutex
	sseClients map[chan string]struct{}

	// Deploy status tracking
	deployMu     sync.RWMutex
	deployStatus *DeployStatus
}

// DeployStatus tracks current deployment state
type DeployStatus struct {
	Active   bool     `json:"active"`
	Stage    string   `json:"stage"`
	Message  string   `json:"message"`
	Logs     []string `json:"logs"`
	Progress struct {
		Current int `json:"current"`
		Total   int `json:"total"`
	} `json:"progress"`
	Error    string `json:"error,omitempty"`
	Complete bool   `json:"complete"`
}

// DiscoveryState holds all discovered data
type DiscoveryState struct {
	Connected   bool                  `json:"connected"`
	Version     string                `json:"version"`
	IsCluster   bool                  `json:"isCluster"`
	ClusterName string                `json:"clusterName"`
	Nodes       []proxmox.NodeInfo    `json:"nodes"`
	Storage     []proxmox.StorageInfo `json:"storage"`
	Networks    []proxmox.NetworkInfo `json:"networks"`
	VMs         []proxmox.VMInfo      `json:"vms"`
	Images      []sources.ISOFile     `json:"images"`
	Error       string                `json:"error,omitempty"`
}

// NewServer creates a new web server
func NewServer(cfg *config.Config, httpsPort int) *Server {
	return &Server{
		cfg:        cfg,
		httpsPort:  httpsPort,
		sseClients: make(map[chan string]struct{}),
	}
}

// getOutboundIP returns the preferred outbound IP of this machine
func getOutboundIP() string {
	conn, err := net.DialTimeout("udp", "8.8.8.8:80", 2*time.Second)
	if err != nil {
		return "127.0.0.1"
	}
	defer conn.Close()
	localAddr := conn.LocalAddr().(*net.UDPAddr)
	return localAddr.IP.String()
}

// Start starts both HTTP and HTTPS servers
func (s *Server) Start(httpPort int) error {
	// Scan image sources on startup so images are ready before user connects
	go s.scanAndUpdateImages()

	cert, err := LoadOrGenerateCert(config.ConfigDir())
	if err != nil {
		return fmt.Errorf("failed to load/generate certificate: %w", err)
	}

	mux := http.NewServeMux()

	// API routes
	mux.HandleFunc("/api/config", s.handleConfig)
	mux.HandleFunc("/api/connect", s.handleConnect)
	mux.HandleFunc("/api/discovery", s.handleDiscovery)
	mux.HandleFunc("/api/deploy", s.handleDeploy)
	mux.HandleFunc("/api/deploy/progress", s.handleDeployProgress)
	mux.HandleFunc("/api/deploy/status", s.handleDeployStatus)
	mux.HandleFunc("/api/create-network", s.handleCreateNetwork)
	mux.HandleFunc("/api/scan-sources", s.handleScanSources)
	mux.HandleFunc("/api/sources", s.handleSources)
	mux.HandleFunc("/api/upload-key", s.handleUploadKey)
	mux.HandleFunc("/api/deployments", s.handleDeployments)
	mux.HandleFunc("/api/deployments/stop", s.handleDeploymentsStop)
	mux.HandleFunc("/api/deployments/delete", s.handleDeploymentsDelete)

	// Static files
	staticFS, err := fs.Sub(staticFiles, "static")
	if err != nil {
		return err
	}
	mux.Handle("/", http.FileServer(http.FS(staticFS)))

	hostIP := getOutboundIP()

	httpURL := fmt.Sprintf("http://%s:%d", hostIP, httpPort)
	httpsURL := fmt.Sprintf("https://%s:%d", hostIP, s.httpsPort)

	fmt.Printf("\n")
	fmt.Printf("╔════════════════════════════════════════════════════════════╗\n")
	fmt.Printf("║  Versa HeadEnd Deployer                                    ║\n")
	fmt.Printf("╠════════════════════════════════════════════════════════════╣\n")
	fmt.Printf("║                                                            ║\n")
	fmt.Printf("║  HTTP:   %s%-49s%s║\n", "\033[1;36m", httpURL, "\033[0m")
	fmt.Printf("║  HTTPS:  %s%-49s%s║\n", "\033[1;36m", httpsURL, "\033[0m")
	fmt.Printf("║                                                            ║\n")
	fmt.Printf("║  To change ports:                                          ║\n")
	fmt.Printf("║    --http-port XXXX  --https-port YYYY                     ║\n")
	fmt.Printf("║                                                            ║\n")
	fmt.Printf("║  Press Ctrl+C to stop the server.                          ║\n")
	fmt.Printf("╚════════════════════════════════════════════════════════════╝\n")
	fmt.Printf("\n")

	// Start HTTP server in background
	go func() {
		httpServer := &http.Server{
			Addr:    fmt.Sprintf("0.0.0.0:%d", httpPort),
			Handler: mux,
		}
		if err := httpServer.ListenAndServe(); err != nil {
			fmt.Printf("HTTP server error: %v\n", err)
		}
	}()

	// Start HTTPS server (blocks)
	httpsServer := &http.Server{
		Addr:    fmt.Sprintf("0.0.0.0:%d", s.httpsPort),
		Handler: mux,
		TLSConfig: &tls.Config{
			Certificates: []tls.Certificate{cert},
			MinVersion:   tls.VersionTLS12,
		},
	}

	listener, err := net.Listen("tcp", httpsServer.Addr)
	if err != nil {
		return fmt.Errorf("HTTPS listen failed on port %d: %w", s.httpsPort, err)
	}

	return httpsServer.ServeTLS(listener, "", "")
}

// --- API Handlers ---

func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	switch r.Method {
	case "GET":
		resp := map[string]interface{}{
			"lastProxmoxHost": s.cfg.LastProxmoxHost,
			"lastProxmoxUser": s.cfg.LastProxmoxUser,
			"lastStorage":     s.cfg.LastStorage,
			"lastSSHKeyPath":  s.cfg.LastSSHKeyPath,
			"imageSources":    s.cfg.ImageSources,
			"hasPassword":     s.cfg.LastProxmoxPassword != "",
		}
		json.NewEncoder(w).Encode(resp)

	case "POST":
		var updates map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&updates); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if v, ok := updates["lastProxmoxHost"].(string); ok {
			s.cfg.LastProxmoxHost = v
		}
		if v, ok := updates["lastProxmoxUser"].(string); ok {
			s.cfg.LastProxmoxUser = v
		}
		if v, ok := updates["lastProxmoxPassword"].(string); ok {
			s.cfg.LastProxmoxPassword = v
		}
		if v, ok := updates["lastStorage"].(string); ok {
			s.cfg.LastStorage = v
		}
		if v, ok := updates["lastSSHKeyPath"].(string); ok {
			s.cfg.LastSSHKeyPath = v
		}
		if err := s.cfg.Save(); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		json.NewEncoder(w).Encode(map[string]bool{"success": true})

	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleConnect(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Host         string `json:"host"`
		User         string `json:"user"`
		Password     string `json:"password"`
		SSHKeyPath   string `json:"sshKeyPath"`
		SavePassword bool   `json:"savePassword"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if req.User == "" {
		req.User = "root"
	}

	// Build SSH client options
	opts := ssh.ClientOptions{
		Host:    req.Host,
		User:    req.User,
		Timeout: 30 * time.Second,
	}
	if req.SSHKeyPath != "" {
		opts.KeyPath = req.SSHKeyPath
	} else if s.cfg.LastSSHKeyPath != "" {
		opts.KeyPath = s.cfg.LastSSHKeyPath
	}
	if req.Password != "" {
		opts.Password = req.Password
	} else if s.cfg.LastProxmoxPassword != "" {
		// Use saved password when user leaves the field empty
		opts.Password = s.cfg.LastProxmoxPassword
	}

	client, err := ssh.NewClient(opts)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("SSH client creation failed: %v", err),
		})
		return
	}

	if err := client.Connect(); err != nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("SSH connection failed: %v", err),
		})
		return
	}

	// Save connection info
	s.cfg.LastProxmoxHost = req.Host
	s.cfg.LastProxmoxUser = req.User
	if req.SavePassword && req.Password != "" {
		s.cfg.LastProxmoxPassword = req.Password
	}
	if req.SSHKeyPath != "" {
		s.cfg.LastSSHKeyPath = req.SSHKeyPath
	}
	s.cfg.Save()

	// Close any previous connection
	if s.sshClient != nil {
		s.sshClient.Close()
	}

	s.sshClient = client
	s.discoverer = proxmox.NewDiscoverer(client)

	// Run parallel discovery in background
	go s.runParallelDiscovery()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
	})
}

func (s *Server) runParallelDiscovery() {
	state := &DiscoveryState{Connected: true}

	info, err := s.discoverer.DiscoverParallel()
	if err != nil {
		state.Error = err.Error()
		s.mu.Lock()
		s.discoveryState = state
		s.mu.Unlock()
		return
	}

	state.Version = info.Version
	state.IsCluster = info.IsCluster
	state.ClusterName = info.ClusterName
	state.Nodes = info.Nodes
	state.Storage = info.Storage
	state.Networks = info.Networks
	state.VMs = info.ExistingVMs

	s.mu.Lock()
	s.discoveryState = state
	s.mu.Unlock()

	// Scan image sources in background (can be slow)
	go func() {
		imageSources, err := sources.CreateSourcesFromConfig(s.cfg)
		if err != nil {
			return
		}

		collection, err := sources.ScanAllSources(imageSources)
		if err != nil {
			return
		}

		var allImages []sources.ISOFile
		allImages = append(allImages, collection.Director...)
		allImages = append(allImages, collection.Analytics...)
		allImages = append(allImages, collection.FlexVNF...)
		allImages = append(allImages, collection.Concerto...)

		s.mu.Lock()
		if s.discoveryState != nil {
			s.discoveryState.Images = allImages
		}
		s.mu.Unlock()
	}()
}

func (s *Server) handleDiscovery(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	s.mu.RLock()
	state := s.discoveryState
	s.mu.RUnlock()

	if state == nil {
		state = &DiscoveryState{Connected: false}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(state)
}

// ensureBridgesExist checks all bridges referenced in the network config and creates
// any that don't exist on Proxmox. Writes directly to /etc/network/interfaces and
// brings bridges up with ifup. Verifies each step.
func (s *Server) ensureBridgesExist(networks config.NetworkConfig) error {
	// Collect all unique bridge names from the config
	bridges := make(map[string]bool)
	for _, b := range []string{
		networks.NorthboundBridge,
		networks.DirectorRouterBridge,
		networks.ControllerRouterBridge,
		networks.AnalyticsClusterBridge,
		networks.RouterHABridge,
	} {
		if b != "" {
			bridges[b] = true
		}
	}
	for _, b := range networks.ControllerWANBridges {
		if b != "" {
			bridges[b] = true
		}
	}

	if len(bridges) == 0 {
		return nil
	}

	// Check which bridges actually exist on the live system
	existing := make(map[string]bool)
	result, err := s.sshClient.Run("ls /sys/class/net/")
	if err != nil {
		return fmt.Errorf("listing network interfaces: %w", err)
	}
	for _, name := range strings.Fields(result.Stdout) {
		existing[strings.TrimSpace(name)] = true
	}

	// Also check what's already defined in /etc/network/interfaces
	defined := make(map[string]bool)
	ifResult, _ := s.sshClient.Run("grep -oP '(?<=^iface )vmbr\\d+' /etc/network/interfaces")
	if ifResult != nil {
		for _, name := range strings.Fields(ifResult.Stdout) {
			defined[strings.TrimSpace(name)] = true
		}
	}

	// Find which bridges need to be created
	var missing []string
	for bridge := range bridges {
		if !existing[bridge] {
			missing = append(missing, bridge)
		}
	}

	if len(missing) == 0 {
		return nil
	}

	fmt.Printf("Need to create bridges: %v\n", missing)

	// Append missing bridges to /etc/network/interfaces
	for _, bridge := range missing {
		if defined[bridge] {
			// Already in config but not active — just needs ifup
			fmt.Printf("Bridge %s is defined but not active, will ifup\n", bridge)
			continue
		}

		fmt.Printf("Adding bridge %s to /etc/network/interfaces\n", bridge)

		// Append bridge config block
		appendCmd := fmt.Sprintf(
			`printf '\nauto %s\niface %s inet manual\n\tbridge-ports none\n\tbridge-stp off\n\tbridge-fd 0\n' >> /etc/network/interfaces`,
			bridge, bridge,
		)
		r, err := s.sshClient.Run(appendCmd)
		if err != nil {
			return fmt.Errorf("writing bridge %s to interfaces file: %w", bridge, err)
		}
		if r.ExitCode != 0 {
			return fmt.Errorf("writing bridge %s failed (exit %d): %s", bridge, r.ExitCode, r.Stderr)
		}
	}

	// Bring up each missing bridge
	for _, bridge := range missing {
		fmt.Printf("Bringing up bridge %s\n", bridge)
		r, err := s.sshClient.Run(fmt.Sprintf("ifup %s", bridge))
		if err != nil {
			return fmt.Errorf("ifup %s: %w", bridge, err)
		}
		if r.ExitCode != 0 {
			// Try ifreload as fallback
			fmt.Printf("ifup %s failed, trying ifreload -a\n", bridge)
			r2, _ := s.sshClient.Run("ifreload -a")
			if r2 != nil && r2.ExitCode != 0 {
				return fmt.Errorf("bringing up bridge %s failed — ifup exit %d: %s, ifreload exit %d: %s",
					bridge, r.ExitCode, r.Stderr, r2.ExitCode, r2.Stderr)
			}
		}
	}

	// Verify every bridge now exists on the live system
	r, err := s.sshClient.Run("ls /sys/class/net/")
	if err != nil {
		return fmt.Errorf("verifying bridges: %w", err)
	}
	nowExisting := make(map[string]bool)
	for _, name := range strings.Fields(r.Stdout) {
		nowExisting[strings.TrimSpace(name)] = true
	}
	for _, bridge := range missing {
		if !nowExisting[bridge] {
			return fmt.Errorf("bridge %s was configured but is not active after ifup — check /etc/network/interfaces on Proxmox host", bridge)
		}
		fmt.Printf("Verified bridge %s is active\n", bridge)
	}

	fmt.Printf("Successfully created and verified bridges: %v\n", missing)
	return nil
}

func (s *Server) handleDeploy(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Prefix     string                   `json:"prefix"`
		HAMode     bool                     `json:"haMode"`
		Components []config.ComponentConfig `json:"components"`
		Storage    string                   `json:"storage"`
		Networks   config.NetworkConfig     `json:"networks"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("Invalid request: %v", err),
		})
		return
	}

	if s.sshClient == nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   "Not connected to Proxmox",
		})
		return
	}

	// Auto-create any bridges that don't exist on Proxmox
	if err := s.ensureBridgesExist(req.Networks); err != nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("Failed to create bridges: %v", err),
		})
		return
	}

	deployCfg := config.NewDeploymentConfig()
	deployCfg.ProxmoxHost = s.cfg.LastProxmoxHost
	deployCfg.SSHUser = s.cfg.LastProxmoxUser
	deployCfg.Prefix = req.Prefix
	deployCfg.HAMode = req.HAMode
	deployCfg.StoragePool = req.Storage
	deployCfg.Networks = req.Networks
	deployCfg.Components = req.Components

	imageSources, _ := sources.CreateSourcesFromConfig(s.cfg)

	dep := deployer.NewDeployer(s.sshClient, imageSources)
	dep.SetConfig(deployCfg)

	// Pass scanned images so deployer can download from sources
	s.mu.Lock()
	if s.discoveryState != nil {
		dep.SetKnownImages(s.discoveryState.Images)
	}
	s.mu.Unlock()

	// Init deploy status tracking
	s.deployMu.Lock()
	s.deployStatus = &DeployStatus{Active: true, Stage: "initializing"}
	s.deployMu.Unlock()

	// Create deploy log file
	logDir := filepath.Join(config.ConfigDir(), "logs")
	os.MkdirAll(logDir, 0755)
	logPath := filepath.Join(logDir, fmt.Sprintf("deploy-%s.log", time.Now().Format("2006-01-02_15-04-05")))
	logFile, logErr := os.Create(logPath)
	if logErr != nil {
		fmt.Printf("Warning: could not create deploy log file: %v\n", logErr)
	} else {
		fmt.Printf("Deploy log: %s\n", logPath)
	}

	writeLog := func(msg string) {
		if logFile != nil {
			fmt.Fprintf(logFile, "[%s] %s\n", time.Now().Format("15:04:05"), msg)
		}
	}

	dep.OnLog = func(msg string) {
		s.broadcastSSE(fmt.Sprintf(`{"type":"log","message":%q}`, msg))
		writeLog(msg)
		s.deployMu.Lock()
		if s.deployStatus != nil {
			s.deployStatus.Message = msg
			// Keep last 200 log lines
			if len(s.deployStatus.Logs) >= 200 {
				s.deployStatus.Logs = s.deployStatus.Logs[1:]
			}
			s.deployStatus.Logs = append(s.deployStatus.Logs, msg)
		}
		s.deployMu.Unlock()
	}
	dep.OnProgress = func(stage string, current, total int) {
		s.broadcastSSE(fmt.Sprintf(`{"type":"progress","stage":%q,"current":%d,"total":%d}`, stage, current, total))
		s.deployMu.Lock()
		if s.deployStatus != nil {
			s.deployStatus.Stage = stage
			s.deployStatus.Progress.Current = current
			s.deployStatus.Progress.Total = total
		}
		s.deployMu.Unlock()
	}

	if _, err := dep.Discover(); err != nil {
		writeLog(fmt.Sprintf("ERROR: Discovery failed: %v", err))
		if logFile != nil {
			logFile.Close()
		}
		s.deployMu.Lock()
		s.deployStatus.Active = false
		s.deployStatus.Error = fmt.Sprintf("Discovery failed: %v", err)
		s.deployMu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("Discovery failed: %v", err),
		})
		return
	}

	// Deploy asynchronously, send progress via SSE
	go func() {
		defer func() {
			if logFile != nil {
				logFile.Close()
			}
		}()

		result, err := dep.Deploy()
		if err != nil {
			writeLog(fmt.Sprintf("ERROR: Deployment failed: %v", err))
			s.broadcastSSE(fmt.Sprintf(`{"type":"error","message":%q}`, err.Error()))
			s.deployMu.Lock()
			s.deployStatus.Active = false
			s.deployStatus.Error = err.Error()
			s.deployMu.Unlock()
			return
		}

		writeLog("Deployment complete")
		resultJSON, _ := json.Marshal(result)
		s.broadcastSSE(fmt.Sprintf(`{"type":"complete","result":%s}`, string(resultJSON)))

		s.deployMu.Lock()
		s.deployStatus.Active = false
		s.deployStatus.Complete = true
		s.deployStatus.Stage = "complete"
		s.deployMu.Unlock()

		for _, vm := range result.VMs {
			if vm.Component == config.ComponentDirector && vm.IP != "" {
				s.cfg.DirectorIP = vm.IP
				s.cfg.Save()
				break
			}
		}
	}()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"message": "Deployment started",
	})
}

// handleDeployProgress serves SSE stream for deployment progress
func (s *Server) handleDeployProgress(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	ch := make(chan string, 64)
	s.sseMu.Lock()
	s.sseClients[ch] = struct{}{}
	s.sseMu.Unlock()

	defer func() {
		s.sseMu.Lock()
		delete(s.sseClients, ch)
		s.sseMu.Unlock()
	}()

	ctx := r.Context()
	heartbeat := time.NewTicker(5 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case msg := <-ch:
			fmt.Fprintf(w, "data: %s\n\n", msg)
			flusher.Flush()
		case <-heartbeat.C:
			// SSE comment as keepalive — prevents browser from thinking connection is dead
			fmt.Fprintf(w, ": keepalive\n\n")
			flusher.Flush()
		}
	}
}

// broadcastSSE sends a message to all connected SSE clients
func (s *Server) broadcastSSE(msg string) {
	s.sseMu.Lock()
	defer s.sseMu.Unlock()

	for ch := range s.sseClients {
		select {
		case ch <- msg:
		default:
		}
	}
}

func (s *Server) handleDeployStatus(w http.ResponseWriter, r *http.Request) {
	s.deployMu.RLock()
	status := s.deployStatus
	s.deployMu.RUnlock()

	w.Header().Set("Content-Type", "application/json")
	if status == nil {
		json.NewEncoder(w).Encode(map[string]interface{}{"active": false})
		return
	}
	json.NewEncoder(w).Encode(status)
}

func (s *Server) handleCreateNetwork(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Type      string `json:"type"`
		Name      string `json:"name"`
		Node      string `json:"node"`
		VLANAware bool   `json:"vlanAware"`
		Interface string `json:"interface"`
		Address   string `json:"address"`
		Gateway   string `json:"gateway"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if s.sshClient == nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   "Not connected to Proxmox",
		})
		return
	}

	cmd := fmt.Sprintf("pvesh create /nodes/%s/network -iface %s -type bridge", req.Node, req.Name)
	if req.VLANAware {
		cmd += " -bridge_vlan_aware 1"
	}
	if req.Interface != "" {
		cmd += fmt.Sprintf(" -bridge_ports %s", req.Interface)
	}
	if req.Address != "" {
		cmd += fmt.Sprintf(" -address %s", req.Address)
	}
	if req.Gateway != "" {
		cmd += fmt.Sprintf(" -gateway %s", req.Gateway)
	}

	result, err := s.sshClient.Run(cmd)
	if err != nil || result.ExitCode != 0 {
		errMsg := "command failed"
		if err != nil {
			errMsg = err.Error()
		}
		if result != nil && result.Stderr != "" {
			errMsg += ": " + result.Stderr
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   errMsg,
		})
		return
	}

	// Apply network changes
	s.sshClient.Run(fmt.Sprintf("pvesh set /nodes/%s/network", req.Node))

	go s.runParallelDiscovery()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
	})
}

func (s *Server) handleScanSources(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	imageSources, err := sources.CreateSourcesFromConfig(s.cfg)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   err.Error(),
		})
		return
	}

	collection, err := sources.ScanAllSources(imageSources)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   err.Error(),
		})
		return
	}

	var allImages []sources.ISOFile
	allImages = append(allImages, collection.Director...)
	allImages = append(allImages, collection.Analytics...)
	allImages = append(allImages, collection.FlexVNF...)
	allImages = append(allImages, collection.Concerto...)

	s.mu.Lock()
	if s.discoveryState != nil {
		s.discoveryState.Images = allImages
	}
	s.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"images":  allImages,
		"sources": collection.Sources,
	})
}

func (s *Server) handleSources(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	switch r.Method {
	case "GET":
		// Return configured sources
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true,
			"sources": s.cfg.ImageSources,
		})

	case "POST":
		// Add a new source
		var req struct {
			URL      string `json:"url"`
			Name     string `json:"name"`
			Type     string `json:"type"`
			SSHKey   string `json:"sshKey"`
			Password string `json:"password"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			json.NewEncoder(w).Encode(map[string]interface{}{
				"success": false,
				"error":   err.Error(),
			})
			return
		}

		if req.URL == "" {
			json.NewEncoder(w).Encode(map[string]interface{}{
				"success": false,
				"error":   "URL is required",
			})
			return
		}

		// Auto-detect type if not provided
		if req.Type == "" {
			req.Type = string(sources.DetectSourceType(req.URL))
		}

		// Auto-generate name if not provided
		if req.Name == "" {
			req.Name = req.URL
			if len(req.Name) > 40 {
				req.Name = req.Name[:37] + "..."
			}
		}

		newSource := config.ImageSource{
			URL:      req.URL,
			Name:     req.Name,
			Type:     req.Type,
			SSHKey:   req.SSHKey,
			Password: req.Password,
		}

		// Validate by testing connection
		src, err := sources.CreateSource(newSource)
		if err != nil {
			json.NewEncoder(w).Encode(map[string]interface{}{
				"success": false,
				"error":   fmt.Sprintf("Invalid source: %v", err),
			})
			return
		}

		if err := sources.TestSourceConnection(src); err != nil {
			json.NewEncoder(w).Encode(map[string]interface{}{
				"success": false,
				"error":   fmt.Sprintf("Connection test failed: %v", err),
			})
			return
		}

		if err := s.cfg.AddImageSource(newSource); err != nil {
			json.NewEncoder(w).Encode(map[string]interface{}{
				"success": false,
				"error":   err.Error(),
			})
			return
		}

		s.cfg.Save()

		// Trigger a rescan in background
		go s.scanAndUpdateImages()

		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true,
			"sources": s.cfg.ImageSources,
		})

	case "DELETE":
		// Remove a source by URL
		var req struct {
			URL string `json:"url"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			json.NewEncoder(w).Encode(map[string]interface{}{
				"success": false,
				"error":   err.Error(),
			})
			return
		}

		s.cfg.RemoveImageSource(req.URL)
		s.cfg.Save()

		// Trigger a rescan in background
		go s.scanAndUpdateImages()

		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true,
			"sources": s.cfg.ImageSources,
		})

	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// scanAndUpdateImages scans all configured sources and updates discovery state
func (s *Server) scanAndUpdateImages() {
	imageSources, err := sources.CreateSourcesFromConfig(s.cfg)
	if err != nil {
		return
	}

	collection, err := sources.ScanAllSources(imageSources)
	if err != nil {
		return
	}

	var allImages []sources.ISOFile
	allImages = append(allImages, collection.Director...)
	allImages = append(allImages, collection.Analytics...)
	allImages = append(allImages, collection.FlexVNF...)
	allImages = append(allImages, collection.Concerto...)

	s.mu.Lock()
	if s.discoveryState != nil {
		s.discoveryState.Images = allImages
	}
	s.mu.Unlock()
}

func (s *Server) handleUploadKey(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	w.Header().Set("Content-Type", "application/json")

	// Limit upload to 64KB (SSH keys are small)
	r.Body = http.MaxBytesReader(w, r.Body, 64*1024)

	file, header, err := r.FormFile("key")
	if err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("Failed to read uploaded file: %v", err),
		})
		return
	}
	defer file.Close()

	keyData, err := io.ReadAll(file)
	if err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("Failed to read key data: %v", err),
		})
		return
	}

	// Save to ~/.versa-deployer/ssh_key (or use original filename)
	keyDir := config.ConfigDir()
	keyName := header.Filename
	if keyName == "" {
		keyName = "ssh_key"
	}
	keyPath := filepath.Join(keyDir, keyName)

	if err := os.WriteFile(keyPath, keyData, 0600); err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("Failed to save key: %v", err),
		})
		return
	}

	// Save path in config
	s.cfg.LastSSHKeyPath = keyPath
	s.cfg.Save()

	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"keyPath": keyPath,
		"keyName": keyName,
	})
}

// DeploymentGroup represents a group of VMs from a single deployment
type DeploymentGroup struct {
	Prefix string           `json:"prefix"`
	VMs    []proxmox.VMInfo `json:"vms"`
}

func (s *Server) handleDeployments(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	w.Header().Set("Content-Type", "application/json")

	if s.sshClient == nil || s.discoverer == nil {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   "Not connected to Proxmox",
		})
		return
	}

	versaVMs, err := s.discoverer.FindVersaDeployments()
	if err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("Failed to find deployments: %v", err),
		})
		return
	}

	// Group VMs by deployment prefix
	groups := make(map[string]*DeploymentGroup)
	for _, vm := range versaVMs {
		prefix := extractDeployPrefix(vm)
		if prefix == "" {
			prefix = "_unknown"
		}
		if groups[prefix] == nil {
			groups[prefix] = &DeploymentGroup{Prefix: prefix}
		}
		groups[prefix].VMs = append(groups[prefix].VMs, vm)
	}

	json.NewEncoder(w).Encode(map[string]interface{}{
		"success":     true,
		"deployments": groups,
	})
}

// extractDeployPrefix extracts the deployment prefix from a VM's tags or name.
// Looks for versa-deploy-{prefix} tag first, then falls back to parsing the VM name.
func extractDeployPrefix(vm proxmox.VMInfo) string {
	for _, tag := range vm.Tags {
		if strings.HasPrefix(tag, "versa-deploy-") {
			return strings.TrimPrefix(tag, "versa-deploy-")
		}
	}
	// Fallback: extract prefix from VM name (e.g., "v-15bbff87-director" -> "v-15bbff87")
	name := vm.Name
	// Find the last dash-separated component type suffix
	suffixes := []string{"-director", "-analytics", "-controller", "-router", "-concerto", "-flexvnf"}
	for _, suffix := range suffixes {
		idx := strings.LastIndex(name, suffix)
		if idx > 0 {
			candidate := name[:idx]
			// Strip trailing -N (HA index like "-1", "-2")
			if len(candidate) > 2 && candidate[len(candidate)-2] == '-' && candidate[len(candidate)-1] >= '0' && candidate[len(candidate)-1] <= '9' {
				candidate = candidate[:len(candidate)-2]
			}
			return candidate
		}
	}
	return ""
}

func (s *Server) handleDeploymentsStop(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	w.Header().Set("Content-Type", "application/json")

	var req struct {
		VMIDs  []int  `json:"vmids"`
		Prefix string `json:"prefix"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("Invalid request: %v", err),
		})
		return
	}

	if s.sshClient == nil || s.discoverer == nil {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   "Not connected to Proxmox",
		})
		return
	}

	// Safety: verify all VMIDs have the versa-deployer tag
	versaVMs, err := s.discoverer.FindVersaDeployments()
	if err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("Failed to verify VMs: %v", err),
		})
		return
	}

	versaLookup := make(map[int]proxmox.VMInfo)
	for _, vm := range versaVMs {
		versaLookup[vm.VMID] = vm
	}

	for _, vmid := range req.VMIDs {
		if _, ok := versaLookup[vmid]; !ok {
			json.NewEncoder(w).Encode(map[string]interface{}{
				"success": false,
				"error":   fmt.Sprintf("VM %d does not have versa-deployer tag — refusing to stop", vmid),
			})
			return
		}
	}

	vmCreator := proxmox.NewVMCreator(s.sshClient)
	results := make([]map[string]interface{}, 0, len(req.VMIDs))

	for _, vmid := range req.VMIDs {
		vm := versaLookup[vmid]
		entry := map[string]interface{}{
			"vmid": vmid,
			"name": vm.Name,
		}

		if err := vmCreator.StopVM(vmid); err != nil {
			entry["success"] = false
			entry["error"] = err.Error()
		} else {
			entry["success"] = true
		}
		results = append(results, entry)
	}

	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"results": results,
	})
}

func (s *Server) handleDeploymentsDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	w.Header().Set("Content-Type", "application/json")

	var req struct {
		VMIDs  []int  `json:"vmids"`
		Prefix string `json:"prefix"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("Invalid request: %v", err),
		})
		return
	}

	if s.sshClient == nil || s.discoverer == nil {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   "Not connected to Proxmox",
		})
		return
	}

	if len(req.VMIDs) == 0 {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   "No VMIDs provided",
		})
		return
	}

	// Safety: verify all VMIDs have the versa-deployer tag
	versaVMs, err := s.discoverer.FindVersaDeployments()
	if err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("Failed to verify VMs: %v", err),
		})
		return
	}

	// Build lookup of versa-deployer tagged VMs
	versaLookup := make(map[int]proxmox.VMInfo)
	for _, vm := range versaVMs {
		versaLookup[vm.VMID] = vm
	}

	// Validate every requested VMID has the versa-deployer tag
	for _, vmid := range req.VMIDs {
		if _, ok := versaLookup[vmid]; !ok {
			json.NewEncoder(w).Encode(map[string]interface{}{
				"success": false,
				"error":   fmt.Sprintf("VM %d does not have versa-deployer tag — refusing to delete", vmid),
			})
			return
		}
	}

	// All checks passed — stop and destroy each VM
	vmCreator := proxmox.NewVMCreator(s.sshClient)
	results := make([]map[string]interface{}, 0, len(req.VMIDs))

	for _, vmid := range req.VMIDs {
		vm := versaLookup[vmid]
		entry := map[string]interface{}{
			"vmid": vmid,
			"name": vm.Name,
		}

		if err := vmCreator.DestroyVM(vmid); err != nil {
			entry["success"] = false
			entry["error"] = err.Error()
		} else {
			entry["success"] = true
		}
		results = append(results, entry)
	}

	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"results": results,
	})
}
