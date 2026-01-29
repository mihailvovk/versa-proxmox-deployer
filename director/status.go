package director

import (
	"fmt"
)

// HeadEndStatus holds the status of the entire HeadEnd deployment
type HeadEndStatus struct {
	Director    *ComponentStatus
	Analytics   *ComponentStatus
	Controllers []*ComponentStatus
	Routers     []*ComponentStatus
	Concerto    *ComponentStatus

	// Summary
	TotalComponents  int
	HealthyCount     int
	UnhealthyCount   int
	OverallHealth    string
}

// ComponentStatus holds the status of a single HeadEnd component
type ComponentStatus struct {
	Name      string
	Type      string
	IP        string
	Status    string // healthy, degraded, offline
	Version   string
	Uptime    string
	Role      string // For HA: primary, secondary
	SyncState string // For HA: synced, syncing, out-of-sync
}

// BranchStatus holds the status of branch devices
type BranchStatus struct {
	TotalDevices  int
	OnlineCount   int
	OfflineCount  int
	Devices       []BranchDevice
}

// BranchDevice represents a single branch device
type BranchDevice struct {
	Name        string
	IP          string
	Status      string
	LastSeen    string
	Version     string
	Template    string
	Organization string
}

// TenantInfo holds information about tenants/organizations
type TenantInfo struct {
	Name        string
	Description string
	DeviceCount int
	Status      string
}

// GetHeadEndStatus retrieves the status of all HeadEnd components
func (c *Client) GetHeadEndStatus() (*HeadEndStatus, error) {
	status := &HeadEndStatus{}

	// Get Director status (we're connected to it)
	dirInfo, err := c.GetDirectorInfo()
	if err == nil {
		status.Director = &ComponentStatus{
			Name:    dirInfo.Hostname,
			Type:    "Director",
			Version: dirInfo.Version,
			Status:  normalizeStatus(dirInfo.Status),
			Uptime:  dirInfo.Uptime,
		}
		if dirInfo.HAStatus != "" {
			status.Director.SyncState = dirInfo.HAStatus
			status.Director.Role = "primary" // If we're connected, it's primary
		}
	}

	// Get Analytics status
	analytics, err := c.getAnalyticsStatus()
	if err == nil {
		status.Analytics = analytics
	}

	// Get Controllers status
	controllers, err := c.getControllersStatus()
	if err == nil {
		status.Controllers = controllers
	}

	// Calculate summary
	status.TotalComponents = 1 // Director
	if status.Analytics != nil {
		status.TotalComponents++
	}
	status.TotalComponents += len(status.Controllers)

	status.HealthyCount = 0
	if status.Director != nil && status.Director.Status == "healthy" {
		status.HealthyCount++
	}
	if status.Analytics != nil && status.Analytics.Status == "healthy" {
		status.HealthyCount++
	}
	for _, ctrl := range status.Controllers {
		if ctrl.Status == "healthy" {
			status.HealthyCount++
		}
	}

	status.UnhealthyCount = status.TotalComponents - status.HealthyCount

	if status.UnhealthyCount == 0 {
		status.OverallHealth = "healthy"
	} else if status.HealthyCount > 0 {
		status.OverallHealth = "degraded"
	} else {
		status.OverallHealth = "critical"
	}

	return status, nil
}

// getAnalyticsStatus retrieves Analytics node status
func (c *Client) getAnalyticsStatus() (*ComponentStatus, error) {
	var result struct {
		Nodes []struct {
			IP      string `json:"ipAddress"`
			Status  string `json:"status"`
			Version string `json:"version"`
			Uptime  int64  `json:"uptimeSeconds"`
		} `json:"nodes"`
	}

	if err := c.get("/api/v1/analytics/status", &result); err != nil {
		// Try alternative endpoint
		if err := c.get("/vnms/analytics/nodes", &result); err != nil {
			return nil, err
		}
	}

	if len(result.Nodes) == 0 {
		return nil, fmt.Errorf("no analytics nodes found")
	}

	node := result.Nodes[0]
	return &ComponentStatus{
		Name:    "Analytics",
		Type:    "Analytics",
		IP:      node.IP,
		Status:  normalizeStatus(node.Status),
		Version: node.Version,
		Uptime:  formatUptime(node.Uptime),
	}, nil
}

// getControllersStatus retrieves Controller node statuses
func (c *Client) getControllersStatus() ([]*ComponentStatus, error) {
	var result struct {
		Controllers []struct {
			Name    string `json:"name"`
			IP      string `json:"ipAddress"`
			Status  string `json:"status"`
			Version string `json:"version"`
			Uptime  int64  `json:"uptimeSeconds"`
			Role    string `json:"role"`
		} `json:"controllers"`
	}

	if err := c.get("/api/v1/controllers/status", &result); err != nil {
		// Try alternative endpoint
		if err := c.get("/vnms/sdwan/controllers", &result); err != nil {
			return nil, err
		}
	}

	var controllers []*ComponentStatus
	for _, ctrl := range result.Controllers {
		controllers = append(controllers, &ComponentStatus{
			Name:    ctrl.Name,
			Type:    "Controller",
			IP:      ctrl.IP,
			Status:  normalizeStatus(ctrl.Status),
			Version: ctrl.Version,
			Uptime:  formatUptime(ctrl.Uptime),
			Role:    ctrl.Role,
		})
	}

	return controllers, nil
}

// GetBranchStatus retrieves the status of branch devices
func (c *Client) GetBranchStatus() (*BranchStatus, error) {
	var result struct {
		Devices []struct {
			Name         string `json:"name"`
			IP           string `json:"ipAddress"`
			Status       string `json:"status"`
			LastSeen     string `json:"lastSeen"`
			Version      string `json:"softwareVersion"`
			Template     string `json:"templateName"`
			Organization string `json:"organizationName"`
		} `json:"devices"`
		Total   int `json:"totalCount"`
		Online  int `json:"onlineCount"`
		Offline int `json:"offlineCount"`
	}

	if err := c.get("/api/v1/appliances/status", &result); err != nil {
		// Try alternative
		if err := c.get("/vnms/appliance/appliances", &result); err != nil {
			return nil, err
		}
	}

	status := &BranchStatus{
		TotalDevices: result.Total,
		OnlineCount:  result.Online,
		OfflineCount: result.Offline,
	}

	for _, dev := range result.Devices {
		status.Devices = append(status.Devices, BranchDevice{
			Name:        dev.Name,
			IP:          dev.IP,
			Status:      normalizeStatus(dev.Status),
			LastSeen:    dev.LastSeen,
			Version:     dev.Version,
			Template:    dev.Template,
			Organization: dev.Organization,
		})
	}

	return status, nil
}

// GetTenants retrieves list of tenants/organizations
func (c *Client) GetTenants() ([]TenantInfo, error) {
	var result struct {
		Organizations []struct {
			Name        string `json:"name"`
			Description string `json:"description"`
			DeviceCount int    `json:"applianceCount"`
			Status      string `json:"status"`
		} `json:"organizations"`
	}

	if err := c.get("/api/v1/organizations", &result); err != nil {
		// Try alternative
		if err := c.get("/vnms/organization/organizations", &result); err != nil {
			return nil, err
		}
	}

	var tenants []TenantInfo
	for _, org := range result.Organizations {
		tenants = append(tenants, TenantInfo{
			Name:        org.Name,
			Description: org.Description,
			DeviceCount: org.DeviceCount,
			Status:      normalizeStatus(org.Status),
		})
	}

	return tenants, nil
}

// normalizeStatus converts various status strings to standard values
func normalizeStatus(status string) string {
	switch status {
	case "up", "UP", "online", "ONLINE", "running", "RUNNING", "active", "ACTIVE":
		return "healthy"
	case "down", "DOWN", "offline", "OFFLINE", "stopped", "STOPPED":
		return "offline"
	case "degraded", "DEGRADED", "warning", "WARNING":
		return "degraded"
	default:
		if status == "" {
			return "unknown"
		}
		return status
	}
}

// CheckHealth performs a health check and returns true if all components are healthy
func (c *Client) CheckHealth() (bool, error) {
	status, err := c.GetHeadEndStatus()
	if err != nil {
		return false, err
	}

	return status.OverallHealth == "healthy", nil
}
