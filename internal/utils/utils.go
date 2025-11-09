package utils

import (
	"fmt"
	v1 "k8s.io/api/core/v1"
)

// GetNodeIP robustly selects the IP address from a Kubernetes node's status.
// It prioritizes ExternalIP, falling back to InternalIP.
func GetNodeIP(node *v1.Node) (string, error) {
	// First, look for ExternalIP
	for _, addr := range node.Status.Addresses {
		if addr.Type == v1.NodeExternalIP && addr.Address != "" {
			return addr.Address, nil
		}
	}
	// If no ExternalIP, look for InternalIP
	for _, addr := range node.Status.Addresses {
		if addr.Type == v1.NodeInternalIP && addr.Address != "" {
			return addr.Address, nil
		}
	}
	return "", fmt.Errorf("no ExternalIP or InternalIP found for node %s", node.Name)
}
