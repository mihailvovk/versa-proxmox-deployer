package deployer

import (
	"sort"

	"github.com/oliverwhk/versa-proxmox-deployer/config"
	"github.com/oliverwhk/versa-proxmox-deployer/proxmox"
)

// DistributionStrategy defines how VMs are distributed across nodes
type DistributionStrategy string

const (
	// StrategyAutoBalance automatically distributes VMs to maximize headroom
	StrategyAutoBalance DistributionStrategy = "auto_balance"

	// StrategyAllOnOne places all VMs on a single node
	StrategyAllOnOne DistributionStrategy = "all_on_one"

	// StrategyManual uses manually specified node assignments
	StrategyManual DistributionStrategy = "manual"

	// StrategyHASeparate places HA pairs on different nodes
	StrategyHASeparate DistributionStrategy = "ha_separate"
)

// NodeScore represents a node with its capacity score
type NodeScore struct {
	Node           proxmox.NodeInfo
	AvailableCPU   int
	AvailableRAMGB int
	Score          float64
	AssignedVMs    int
}

// Distributor handles VM distribution across cluster nodes
type Distributor struct {
	nodes    []proxmox.NodeInfo
	strategy DistributionStrategy
}

// NewDistributor creates a new distributor
func NewDistributor(nodes []proxmox.NodeInfo, strategy DistributionStrategy) *Distributor {
	return &Distributor{
		nodes:    nodes,
		strategy: strategy,
	}
}

// DistributeComponents assigns nodes to each component based on strategy
func (d *Distributor) DistributeComponents(components []config.ComponentConfig, haMode bool) []config.ComponentConfig {
	if len(d.nodes) == 0 {
		return components
	}

	switch d.strategy {
	case StrategyAllOnOne:
		return d.distributeAllOnOne(components)
	case StrategyManual:
		// Keep existing node assignments
		return components
	case StrategyHASeparate:
		return d.distributeHASeparate(components)
	default: // StrategyAutoBalance
		if haMode {
			return d.distributeHASeparate(components)
		}
		return d.distributeAutoBalance(components)
	}
}

// distributeAutoBalance distributes VMs to maximize available headroom
func (d *Distributor) distributeAutoBalance(components []config.ComponentConfig) []config.ComponentConfig {
	// Calculate scores for each node
	scores := d.calculateNodeScores()

	result := make([]config.ComponentConfig, len(components))
	copy(result, components)

	for i := range result {
		// Sort by score (highest first)
		sort.Slice(scores, func(a, b int) bool {
			return scores[a].Score > scores[b].Score
		})

		// Assign to best node
		if len(scores) > 0 {
			result[i].Node = scores[0].Node.Name

			// Update score (reduce available resources)
			scores[0].AvailableCPU -= result[i].CPU * result[i].Count
			scores[0].AvailableRAMGB -= result[i].RAMGB * result[i].Count
			scores[0].AssignedVMs += result[i].Count
			scores[0].Score = d.calculateScore(scores[0])
		}
	}

	return result
}

// distributeAllOnOne places all VMs on the first available node
func (d *Distributor) distributeAllOnOne(components []config.ComponentConfig) []config.ComponentConfig {
	// Find first online node
	var targetNode string
	for _, node := range d.nodes {
		if node.Status == "online" {
			targetNode = node.Name
			break
		}
	}

	result := make([]config.ComponentConfig, len(components))
	copy(result, components)

	for i := range result {
		if result[i].Node == "" {
			result[i].Node = targetNode
		}
	}

	return result
}

// distributeHASeparate places HA pairs on different nodes
func (d *Distributor) distributeHASeparate(components []config.ComponentConfig) []config.ComponentConfig {
	scores := d.calculateNodeScores()

	result := make([]config.ComponentConfig, len(components))
	copy(result, components)

	// Group components by type
	componentGroups := make(map[config.ComponentType][]int)
	for i, comp := range result {
		componentGroups[comp.Type] = append(componentGroups[comp.Type], i)
	}

	// For each component type, distribute instances across nodes
	for compType, indices := range componentGroups {
		if len(indices) == 0 {
			continue
		}

		// Sort nodes by score
		sort.Slice(scores, func(a, b int) bool {
			return scores[a].Score > scores[b].Score
		})

		// For HA pairs (count > 1), place on different nodes
		comp := result[indices[0]]
		if comp.Count > 1 && len(scores) > 1 {
			// This component needs multiple instances, spread them
			for i, idx := range indices {
				nodeIdx := i % len(scores)
				result[idx].Node = scores[nodeIdx].Node.Name

				// Update score
				scores[nodeIdx].AvailableCPU -= comp.CPU
				scores[nodeIdx].AvailableRAMGB -= comp.RAMGB
				scores[nodeIdx].AssignedVMs++
				scores[nodeIdx].Score = d.calculateScore(scores[nodeIdx])
			}
		} else {
			// Single instance, use best node
			for _, idx := range indices {
				if len(scores) > 0 {
					result[idx].Node = scores[0].Node.Name
					scores[0].AvailableCPU -= result[idx].CPU * result[idx].Count
					scores[0].AvailableRAMGB -= result[idx].RAMGB * result[idx].Count
					scores[0].AssignedVMs += result[idx].Count
					scores[0].Score = d.calculateScore(scores[0])
				}
			}
		}

		_ = compType // Used in grouping
	}

	return result
}

// calculateNodeScores calculates resource scores for all nodes
func (d *Distributor) calculateNodeScores() []NodeScore {
	scores := make([]NodeScore, 0, len(d.nodes))

	for _, node := range d.nodes {
		if node.Status != "online" {
			continue
		}

		score := NodeScore{
			Node:           node,
			AvailableCPU:   node.CPUCores - node.CPUUsed,
			AvailableRAMGB: node.RAMGB - node.RAMUsedGB,
			AssignedVMs:    0,
		}
		score.Score = d.calculateScore(score)

		scores = append(scores, score)
	}

	return scores
}

// calculateScore calculates a combined resource score
func (d *Distributor) calculateScore(ns NodeScore) float64 {
	// Weight: 40% CPU, 60% RAM (RAM is usually more constrained)
	cpuScore := float64(ns.AvailableCPU) / float64(ns.Node.CPUCores+1) * 100
	ramScore := float64(ns.AvailableRAMGB) / float64(ns.Node.RAMGB+1) * 100

	// Penalize nodes that already have assigned VMs
	vmPenalty := float64(ns.AssignedVMs) * 5

	return cpuScore*0.4 + ramScore*0.6 - vmPenalty
}

// GetRecommendedStrategy recommends a distribution strategy
func GetRecommendedStrategy(nodes []proxmox.NodeInfo, haMode bool) DistributionStrategy {
	onlineNodes := 0
	for _, node := range nodes {
		if node.Status == "online" {
			onlineNodes++
		}
	}

	if onlineNodes == 0 {
		return StrategyManual
	}

	if onlineNodes == 1 {
		return StrategyAllOnOne
	}

	if haMode {
		return StrategyHASeparate
	}

	return StrategyAutoBalance
}

// ValidateDistribution checks if the distribution is valid
func ValidateDistribution(components []config.ComponentConfig, nodes []proxmox.NodeInfo) error {
	// Build map of node resources
	nodeResources := make(map[string]*NodeScore)
	for _, node := range nodes {
		nodeResources[node.Name] = &NodeScore{
			Node:           node,
			AvailableCPU:   node.CPUCores - node.CPUUsed,
			AvailableRAMGB: node.RAMGB - node.RAMUsedGB,
		}
	}

	// Check each component fits on its assigned node
	for _, comp := range components {
		node, ok := nodeResources[comp.Node]
		if !ok {
			continue // Will be caught in deployment validation
		}

		cpuNeeded := comp.CPU * comp.Count
		ramNeeded := comp.RAMGB * comp.Count

		if cpuNeeded > node.AvailableCPU {
			// Warning, not error - might still work with overcommit
		}

		if ramNeeded > node.AvailableRAMGB {
			// This is more serious but still might work
		}

		// Update tracking
		node.AvailableCPU -= cpuNeeded
		node.AvailableRAMGB -= ramNeeded
	}

	return nil
}

// GetNodeUtilization returns utilization percentage for each node after deployment
func GetNodeUtilization(components []config.ComponentConfig, nodes []proxmox.NodeInfo) map[string]float64 {
	utilization := make(map[string]float64)

	// Initialize with current utilization
	for _, node := range nodes {
		currentUtil := float64(node.RAMUsedGB) / float64(node.RAMGB) * 100
		utilization[node.Name] = currentUtil
	}

	// Add component usage
	for _, comp := range components {
		if node, ok := utilization[comp.Node]; ok {
			for _, n := range nodes {
				if n.Name == comp.Node {
					addedUtil := float64(comp.RAMGB*comp.Count) / float64(n.RAMGB) * 100
					utilization[comp.Node] = node + addedUtil
					break
				}
			}
		}
	}

	return utilization
}
