package deployer

import (
	"fmt"
	"net"
	"strings"

	"github.com/oliverwhk/versa-proxmox-deployer/config"
)

// IPPlan holds IP address assignments for deployment
type IPPlan struct {
	Subnet   string
	Gateway  string
	Assigned map[string]string // VM name -> IP address
}

// IPAllocator handles IP address allocation
type IPAllocator struct {
	subnet    *net.IPNet
	gateway   net.IP
	nextIP    net.IP
	allocated map[string]bool
}

// NewIPAllocator creates a new IP allocator from a CIDR
func NewIPAllocator(cidr, gateway string) (*IPAllocator, error) {
	_, subnet, err := net.ParseCIDR(cidr)
	if err != nil {
		return nil, fmt.Errorf("invalid CIDR: %w", err)
	}

	gwIP := net.ParseIP(gateway)
	if gwIP == nil {
		// Try to derive gateway from subnet
		gwIP = make(net.IP, len(subnet.IP))
		copy(gwIP, subnet.IP)
		// Assume gateway is .1
		gwIP[len(gwIP)-1] = 1
	}

	// Start allocating from .10 by default
	startIP := make(net.IP, len(subnet.IP))
	copy(startIP, subnet.IP)
	startIP[len(startIP)-1] = 10

	return &IPAllocator{
		subnet:    subnet,
		gateway:   gwIP,
		nextIP:    startIP,
		allocated: make(map[string]bool),
	}, nil
}

// Allocate returns the next available IP address
func (a *IPAllocator) Allocate() (string, error) {
	for {
		ip := a.nextIP.String()

		// Skip gateway and broadcast
		if a.nextIP.Equal(a.gateway) || a.isBroadcast(a.nextIP) {
			a.incrementIP()
			continue
		}

		// Check if IP is in subnet
		if !a.subnet.Contains(a.nextIP) {
			return "", fmt.Errorf("no more IPs available in subnet")
		}

		// Check if already allocated
		if a.allocated[ip] {
			a.incrementIP()
			continue
		}

		a.allocated[ip] = true
		a.incrementIP()
		return ip, nil
	}
}

// AllocateSpecific allocates a specific IP address
func (a *IPAllocator) AllocateSpecific(ip string) error {
	parsedIP := net.ParseIP(ip)
	if parsedIP == nil {
		return fmt.Errorf("invalid IP address: %s", ip)
	}

	if !a.subnet.Contains(parsedIP) {
		return fmt.Errorf("IP %s not in subnet %s", ip, a.subnet.String())
	}

	if a.allocated[ip] {
		return fmt.Errorf("IP %s already allocated", ip)
	}

	a.allocated[ip] = true
	return nil
}

// GetGateway returns the gateway IP
func (a *IPAllocator) GetGateway() string {
	return a.gateway.String()
}

// GetSubnet returns the subnet CIDR
func (a *IPAllocator) GetSubnet() string {
	return a.subnet.String()
}

// incrementIP increments the IP address by 1
func (a *IPAllocator) incrementIP() {
	ip := a.nextIP
	for i := len(ip) - 1; i >= 0; i-- {
		ip[i]++
		if ip[i] != 0 {
			break
		}
	}
}

// isBroadcast checks if an IP is the broadcast address
func (a *IPAllocator) isBroadcast(ip net.IP) bool {
	// Get the network portion
	network := a.subnet.IP
	mask := a.subnet.Mask

	for i := range ip {
		if ip[i] != (network[i] | ^mask[i]) {
			return false
		}
	}
	return true
}

// GenerateIPPlan creates an IP plan for the deployment
func GenerateIPPlan(components []config.ComponentConfig, prefix string, subnet, gateway string) (*IPPlan, error) {
	allocator, err := NewIPAllocator(subnet, gateway)
	if err != nil {
		return nil, err
	}

	plan := &IPPlan{
		Subnet:   subnet,
		Gateway:  allocator.GetGateway(),
		Assigned: make(map[string]string),
	}

	for _, comp := range components {
		count := comp.Count
		if count == 0 {
			count = 1
		}

		for i := 0; i < count; i++ {
			// Build VM name
			name := fmt.Sprintf("%s-%s", prefix, comp.Type)
			if count > 1 {
				name = fmt.Sprintf("%s-%d", name, i+1)
			}

			ip, err := allocator.Allocate()
			if err != nil {
				return nil, fmt.Errorf("allocating IP for %s: %w", name, err)
			}

			plan.Assigned[name] = ip
		}
	}

	return plan, nil
}

// ValidateIPConfig validates the IP configuration
func ValidateIPConfig(ipConfig config.IPConfig) []string {
	var errors []string

	if ipConfig.ManagementSubnet != "" {
		_, _, err := net.ParseCIDR(ipConfig.ManagementSubnet)
		if err != nil {
			errors = append(errors, fmt.Sprintf("invalid management subnet: %s", ipConfig.ManagementSubnet))
		}
	}

	if ipConfig.ManagementGateway != "" {
		if net.ParseIP(ipConfig.ManagementGateway) == nil {
			errors = append(errors, fmt.Sprintf("invalid management gateway: %s", ipConfig.ManagementGateway))
		}
	}

	// Validate manual IPs
	for name, ip := range ipConfig.ManualIPs {
		if net.ParseIP(ip) == nil {
			errors = append(errors, fmt.Sprintf("invalid IP for %s: %s", name, ip))
		}
	}

	return errors
}

// SuggestSubnetFromBridge suggests a subnet based on bridge configuration
func SuggestSubnetFromBridge(bridgeIP string) (subnet, gateway string) {
	ip := net.ParseIP(bridgeIP)
	if ip == nil {
		return "", ""
	}

	// If it's an IPv4 address
	if ip.To4() != nil {
		ip4 := ip.To4()
		// Assume /24 subnet
		subnet = fmt.Sprintf("%d.%d.%d.0/24", ip4[0], ip4[1], ip4[2])
		gateway = fmt.Sprintf("%d.%d.%d.1", ip4[0], ip4[1], ip4[2])
	}

	return subnet, gateway
}

// FormatIPWithCIDR formats an IP with CIDR notation
func FormatIPWithCIDR(ip, cidr string) string {
	// Extract prefix length from CIDR
	parts := strings.Split(cidr, "/")
	if len(parts) == 2 {
		return fmt.Sprintf("%s/%s", ip, parts[1])
	}
	return ip
}

// IPConfigToCloudInit converts IP config to cloud-init format
func IPConfigToCloudInit(ip, gateway, cidr string) string {
	// Format: ip=10.0.0.10/24,gw=10.0.0.1
	ipWithPrefix := FormatIPWithCIDR(ip, cidr)
	return fmt.Sprintf("ip=%s,gw=%s", ipWithPrefix, gateway)
}
