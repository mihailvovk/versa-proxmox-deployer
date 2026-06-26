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

// ImageSourceDTO is a browser-safe view of a config.ImageSource. It deliberately
// omits the Password and SSHKey secrets (exposing presence via Has* booleans),
// while config.ImageSource keeps its json tags so secrets still persist to disk.
type ImageSourceDTO struct {
	URL         string `json:"url"`
	Type        string `json:"type"`
	Name        string `json:"name"`
	HasPassword bool   `json:"hasPassword"`
	HasSSHKey   bool   `json:"hasSshKey"`
}

// sanitizeSources maps stored sources to their browser-safe DTO form.
func sanitizeSources(in []config.ImageSource) []ImageSourceDTO {
	out := make([]ImageSourceDTO, 0, len(in))
	for _, s := range in {
		out = append(out, ImageSourceDTO{
			URL:         s.URL,
			Type:        s.Type,
			Name:        s.Name,
			HasPassword: s.Password != "",
			HasSSHKey:   s.SSHKey != "",
		})
	}
	return out
}

// ConfigResponse is the response for GET /api/config.
type ConfigResponse struct {
	LastProxmoxHost string           `json:"lastProxmoxHost"`
	LastProxmoxUser string           `json:"lastProxmoxUser"`
	LastStorage     string           `json:"lastStorage"`
	LastSSHKeyPath  string           `json:"lastSSHKeyPath"`
	ImageSources    []ImageSourceDTO `json:"imageSources"`
	HasPassword     bool             `json:"hasPassword"`
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
	Sources []ImageSourceDTO `json:"sources,omitempty"`
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
