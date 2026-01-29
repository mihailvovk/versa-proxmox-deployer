package web

import (
	"github.com/mihailvovk/versa-proxmox-deployer/config"
	"github.com/mihailvovk/versa-proxmox-deployer/sources"
)

// APIResponse is the base response for all API endpoints.
type APIResponse struct {
	Success bool   `json:"success"`
	Error   string `json:"error,omitempty"`
}

// ConfigResponse is the response for GET /api/config.
type ConfigResponse struct {
	LastProxmoxHost string               `json:"lastProxmoxHost"`
	LastProxmoxUser string               `json:"lastProxmoxUser"`
	LastStorage     string               `json:"lastStorage"`
	LastSSHKeyPath  string               `json:"lastSSHKeyPath"`
	ImageSources    []config.ImageSource `json:"imageSources"`
	HasPassword     bool                 `json:"hasPassword"`
}

// ConnectionStatusResponse is the response for GET /api/connection/status.
type ConnectionStatusResponse struct {
	Connected bool   `json:"connected"`
	Host      string `json:"host"`
}

// DeployStartResponse is the response for POST /api/deploy when the deployment starts.
type DeployStartResponse struct {
	APIResponse
	Message string `json:"message,omitempty"`
}

// ScanSourcesResponse is the response for POST /api/scan-sources.
type ScanSourcesResponse struct {
	APIResponse
	Images  []sources.ISOFile    `json:"images,omitempty"`
	Sources []sources.SourceSummary `json:"sources,omitempty"`
}

// SourcesResponse is the response for GET/POST/DELETE /api/sources.
type SourcesResponse struct {
	APIResponse
	Sources []config.ImageSource `json:"sources,omitempty"`
}

// UploadKeyResponse is the response for POST /api/upload-key.
type UploadKeyResponse struct {
	APIResponse
	KeyPath string `json:"keyPath,omitempty"`
	KeyName string `json:"keyName,omitempty"`
}

// DeploymentsResponse is the response for GET /api/deployments.
type DeploymentsResponse struct {
	APIResponse
	Deployments map[string]*DeploymentGroup `json:"deployments,omitempty"`
}

// VMActionResponse is the response for POST /api/deployments/stop and /api/deployments/delete.
type VMActionResponse struct {
	APIResponse
	Results []VMActionResult `json:"results,omitempty"`
}

// VMActionResult holds the result of a per-VM action (stop, delete).
type VMActionResult struct {
	VMID    int    `json:"vmid"`
	Name    string `json:"name"`
	Success bool   `json:"success"`
	Error   string `json:"error,omitempty"`
}
