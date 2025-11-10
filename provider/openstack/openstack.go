package openstack

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/servers"
	"gitlab.cern.ch/gfacundo/landb-alias-controller/dns"
	"gitlab.cern.ch/gfacundo/landb-alias-controller/internal"
	"gitlab.cern.ch/gfacundo/landb-alias-controller/internal/utils"
	"gitlab.cern.ch/gfacundo/landb-alias-controller/plan" // Added import
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

const (
	metadataCharLimit = 200 // It is 256 but better be safe here :).
)

// Pre-compile the regex for alias normalization for efficiency.
var aliasRegex = regexp.MustCompile(`--loadl?-\d+-?$`)

// --- Provider Implementation ---

// Provider represents a logical session with an OpenStack environment.
// It is responsible for authenticating with the OpenStack API and for
// reading and writing DNS records to the OpenStack metadata service.
type Provider struct {
	// IdentityEndpoint is the OpenStack Keystone identity endpoint used
	// for authentication. Typically, this is of the form:
	//   https://<keystone-host>/v3
	IdentityEndpoint string

	// DomainName specifies the OpenStack domain under which the user exists.
	// For most CERN projects, this is "Default".
	DomainName string

	// TenantName (also called "project name") identifies the OpenStack tenant
	// within which resources are managed.
	TenantName string

	// UserName is the OpenStack username used for authentication.
	UserName string

	// password holds the OpenStack user's password in memory during
	// authentication. It is deliberately unexported and stored as a byte
	// slice to allow secure zeroing after use.
	// DO NOT log, serialize, or expose this field.
	password []byte

	// k8sclient is an optional Kubernetes controller-runtime client
	// that allows integration with cluster state and objects. It must
	// be non-nil for most controller-related operations.
	k8sclient client.Client

	// computeClient is the authenticated Gophercloud Compute (Nova) ServiceClient
	// used to perform API calls against OpenStack. It is initialized after
	// successful authentication.
	computeClient *gophercloud.ServiceClient
}

// NewProvider creates a new Provider instance...
// (NewProvider documentation and function remain the same...)
func NewProvider(k8sclient client.Client) (*Provider, error) {
	log := log.Log
	// A nil k8sclient is technically allowed, but functions that need it
	// (like Records) will fail at runtime. We check for it here
	// as a common courtesy, but the real check is in the methods that use it.
	if k8sclient == nil {
		log.V(1).Info("k8sclient is nil; OpenStack provider functions that require " +
			"Kubernetes integration (like Records) will fail.")
	}

	provider := &Provider{
		IdentityEndpoint: os.Getenv("OS_AUTH_URL"),
		DomainName:       os.Getenv("OS_USER_DOMAIN_NAME"),
		TenantName:       os.Getenv("OS_PROJECT_NAME"),
		UserName:         os.Getenv("OS_USERNAME"),
		password:         []byte(os.Getenv("OS_PASSWORD")),
		k8sclient:        k8sclient,
	}

	// Validate required configuration *before* attempting authentication
	if err := provider.validateConfiguration(); err != nil {
		return nil, fmt.Errorf("invalid OpenStack provider configuration: %w", err)
	}

	// Attempt authentication
	if err := provider.authenticate(); err != nil {
		return nil, fmt.Errorf("failed to authenticate with OpenStack: %w", err)
	}

	return provider, nil
}

// (validateConfiguration, ZeroPassword, authenticate, retryWithReauth...
// ... all remain the same ...)

// validateConfiguration checks if all required fields are set on the Provider
// struct *before* attempting to use them for authentication.
func (p *Provider) validateConfiguration() error {
	if p.IdentityEndpoint == "" {
		return fmt.Errorf("missing OS_AUTH_URL")
	}
	if p.DomainName == "" {
		return fmt.Errorf("missing OS_USER_DOMAIN_NAME")
	}
	if p.TenantName == "" {
		return fmt.Errorf("missing OS_PROJECT_NAME")
	}
	if p.UserName == "" {
		return fmt.Errorf("missing OS_USERNAME")
	}
	if len(p.password) == 0 {
		return fmt.Errorf("missing OS_PASSWORD")
	}
	return nil
}

// ZeroPassword securely wipes the password from the Provider's memory.
func (p *Provider) ZeroPassword() {
	for i := range p.password {
		p.password[i] = 0
	}
	p.password = nil
}

// authenticate authenticates the client with the OpenStack API.
func (p *Provider) authenticate() error {
	opts := gophercloud.AuthOptions{
		IdentityEndpoint: p.IdentityEndpoint,
		Username:         p.UserName,
		Password:         string(p.password),
		TenantName:       p.TenantName,
		DomainName:       p.DomainName,
	}

	// Authenticate against Keystone
	providerClient, err := openstack.AuthenticatedClient(context.Background(), opts)
	if err != nil {
		return fmt.Errorf("failed to authenticate v2: %w", err)
	}

	// Create the Compute (Nova) service client
	computeClient, err := openstack.NewComputeV2(providerClient, gophercloud.EndpointOpts{})
	if err != nil {
		return fmt.Errorf("failed to create compute client: %w", err)
	}

	p.computeClient = computeClient
	return nil
}

// retryWithReauth retries an operation once if it fails with a common
// authentication error string.
func (p *Provider) retryWithReauth(operation func() error) error {
	log := log.Log
	err := operation()
	if err == nil {
		return nil
	}

	isAuthError := strings.Contains(err.Error(), internal.AuthFailed) ||
		strings.Contains(err.Error(), internal.Unauthorized)

	if isAuthError {
		log.Info("OpenStack token expired, re-authenticating")
		if authErr := p.authenticate(); authErr != nil {
			return fmt.Errorf("re-authentication failed: %w (original error: %s)", authErr, err)
		}
		log.Info("Successfully re-authenticated with OpenStack, retrying operation")
		return operation()
	}

	return err
}

// Records gathers DNS aliases from OpenStack metadata for all Kubernetes
// nodes labeled as ingress nodes.
// This function satisfies the Provider interface.
// (Function documentation and logic remain the same...)
func (p *Provider) Records() ([]*dns.Record, error) {
	log := log.Log
	// These checks are critical. A Provider must be fully initialized
	// by NewProvider to be in a valid state to call Records.
	if p.k8sclient == nil {
		return nil, fmt.Errorf("kubernetes client is not initialized")
	}
	if p.computeClient == nil {
		return nil, fmt.Errorf("openstack client is not initialized")
	}

	ctx := context.Background()
	var nodes corev1.NodeList
	if err := p.k8sclient.List(ctx, &nodes,
		client.MatchingLabels{internal.IngressNodeLabelDefault: internal.TrueString}); err != nil {
		return nil, fmt.Errorf("failed to list ingress nodes: %w", err)
	}

	var records = make([]*dns.Record, 0)

	for _, node := range nodes.Items {
		currentNode := node
		instanceName := currentNode.Name

		// Find the node's IP address robustly
		nodeIP, err := utils.GetNodeIP(&currentNode)
		if err != nil {
			log.Error(err, "Skipping node", "node", instanceName)
			continue
		}

		var server *servers.Server
		opErr := p.retryWithReauth(func() error {
			serverID, err := p.getServerID(ctx, instanceName)
			if err != nil {
				return fmt.Errorf("failed to get server ID for %q: %w", instanceName, err)
			}

			srv, err := servers.Get(ctx, p.computeClient, serverID).Extract()
			if err != nil {
				return fmt.Errorf("failed to get server details for %q: %w", instanceName, err)
			}
			server = srv
			return nil
		})

		if opErr != nil {
			log.Error(opErr, "Skipping node: failed to get OpenStack metadata", "node", instanceName)
			continue
		}

		// Extract and normalize aliases
		for key, value := range server.Metadata {
			if !strings.HasPrefix(key, internal.LandbAliasPrefix) || strings.TrimSpace(value) == "" {
				continue
			}

			aliases := strings.Split(value, ",")
			for _, alias := range aliases {
				alias = strings.TrimSpace(alias)
				if alias == "" {
					continue
				}

				normalizedAlias := normalizeAlias(alias)
				if normalizedAlias == "" {
					continue
				}

				rec := &dns.Record{
					Name:   normalizedAlias,
					Type:   internal.ARecord,
					TTL:    300, // Hardcoded TTL
					Values: []string{nodeIP},
				}
				records = append(records, rec)
			}
		}
	}

	return records, nil
}

// (getServerID, normalizeAlias... remain the same ...)

// getServerID looks up the server ID by name.
func (p *Provider) getServerID(ctx context.Context, name string) (string, error) {
	request := servers.List(p.computeClient, servers.ListOpts{Name: name})
	allPages, err := request.AllPages(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to list servers: %w", err)
	}

	allServers, err := servers.ExtractServers(allPages)
	if err != nil {
		return "", fmt.Errorf("failed to extract servers: %w", err)
	}

	if len(allServers) == 0 {
		return "", fmt.Errorf("no server found with name %q", name)
	}
	if len(allServers) > 1 {
		// This is a critical ambiguity. The caller must handle it.
		return "", fmt.Errorf("multiple servers found with name %q", name)
	}

	return allServers[0].ID, nil
}

// normalizeAlias removes any trailing load-balancer suffix.
func normalizeAlias(alias string) string {
	return aliasRegex.ReplaceAllString(alias, "")
}

// Reconcile applies the calculated changes to the OpenStack node metadata.
// This function satisfies the Provider interface by accepting a `plan.Changes`
// object.
//
// **Implementation Note:**
// The OpenStack provider is not a pure DNS provider. The "desired state"
// (i.e., which `landb-alias` key gets which suffix) depends on the *full*
// sorted list of ingress nodes. A simple diff (`plan.Changes`) is not
// enough information to perform the reconciliation.
//
// To solve this, this function *re-calculates* the full desired state:
//  1. It fetches the *current* state from OpenStack using `p.Records()`.
//  2. It applies the `changes` to this `current` state to get the *full desired state*.
//  3. It then runs the full, state-based reconciliation logic against this
//     `desiredRecords` list.
//
// This is inefficient (it reads all node metadata twice per cycle) but
// correctly implements the interface while preserving the robust, state-based
// reconciliation logic required by this provider.
func (p *Provider) Reconcile(changes *plan.Changes) error {
	log := log.Log
	if p.k8sclient == nil {
		return fmt.Errorf("kubernetes client is not initialized")
	}
	if p.computeClient == nil {
		return fmt.Errorf("openstack client is not initialized")
	}

	// --- Step 1: Re-calculate Full Desired State from Changes ---
	// We must do this because the reconciliation logic needs the *full*
	// desired state to calculate suffixes, not just the diff.

	log.Info("Reconciling OpenStack metadata")
	currentRecords, err := p.Records()
	if err != nil {
		return fmt.Errorf("failed to get current records to calculate desired state: %w", err)
	}

	desiredRecords := applyChanges(currentRecords, changes)
	log.V(1).Info("Successfully calculated full desired state.", "totalDesiredRecords", len(desiredRecords))

	// --- Step 2: Run State-Based Reconciliation ---
	// The following logic is identical to the old `SetRecords` function,
	// but it uses the `desiredRecords` list we just calculated.

	ctx := context.Background()
	var nodes corev1.NodeList
	if err := p.k8sclient.List(ctx, &nodes,
		client.MatchingLabels{internal.IngressNodeLabelDefault: internal.TrueString}); err != nil {
		return fmt.Errorf("failed to list ingress nodes: %w", err)
	}

	// Sort nodes alphabetically to ensure stable --load-N- indices
	sort.Slice(nodes.Items, func(i, j int) bool {
		return nodes.Items[i].Name < nodes.Items[j].Name
	})

	// Create a reverse map of IP -> nodeName for efficient lookup
	nodeIPMap := make(map[string]string)
	sortedNodeNames := make([]string, 0, len(nodes.Items))

	for _, node := range nodes.Items {
		nodeIP, err := utils.GetNodeIP(&node)
		if err != nil {
			log.V(1).Info("Skipping node", "node", node.Name, "error", err)
			continue
		}
		nodeIPMap[nodeIP] = node.Name
		sortedNodeNames = append(sortedNodeNames, node.Name)
	}

	log.V(1).Info("IP to node name map", "map", sortedNodeNames)

	// Map desired *normalized* aliases to each node
	desiredNodeAliases := make(map[string][]string)
	for _, rec := range desiredRecords { // Use desiredRecords
		if len(rec.Values) == 0 {
			continue
		}
		ip := rec.Values[0]

		nodeName, ok := nodeIPMap[ip]
		if !ok {
			log.V(1).Info("Skipping record: its IP does not match any known ingress node", "record", rec.Name, "ip", ip)
			continue
		}

		desiredNodeAliases[nodeName] = append(desiredNodeAliases[nodeName], rec.Name)
	}

	// Iterate and Reconcile Each Node
	for i, nodeName := range sortedNodeNames {
		log.V(1).Info("Reconciling aliases for node", "node", nodeName, "index", i)

		// Get this node's actual metadata from OpenStack
		actualMetadata, err := p.getInstanceMetadata(ctx, nodeName)
		if err != nil {
			log.Error(err, "Failed to get metadata for node, skipping", "node", nodeName)
			continue
		}

		// Build this node's desired metadata map
		suffix := fmt.Sprintf("--load-%d-", i)
		normalizedAliases := desiredNodeAliases[nodeName]

		suffixedAliases := make([]string, len(normalizedAliases))
		for j, alias := range normalizedAliases {
			suffixedAliases[j] = alias + suffix
		}

		desiredMetadata := packAliases(suffixedAliases)

		// Diff and Apply (Update/Create)
		propsToUpdate := make(map[string]string)
		for key, desiredValue := range desiredMetadata {
			actualValue, exists := actualMetadata[key]
			if !exists || actualValue != desiredValue {
				log.Info("Updating node metadata", "node", nodeName, "key", key, "value", desiredValue)
				propsToUpdate[key] = desiredValue
			}
		}

		if len(propsToUpdate) > 0 {
			if err := p.SetInstanceProperties(ctx, nodeName, NewPropertySet(propsToUpdate)); err != nil {
				log.Error(err, "Failed to set properties for node", "node", nodeName)
				// Continue to next node even if this one fails
			}
		}

		// Diff and Apply (Delete)
		for key := range actualMetadata {
			// Only inspect keys we manage
			if !strings.HasPrefix(key, internal.LandbAliasPrefix) {
				continue
			}

			if _, exists := desiredMetadata[key]; !exists {
				log.Info("Deleting stale node metadata", "node", nodeName, "key", key)
				if err := p.DeleteInstanceProperty(ctx, nodeName, key); err != nil {
					log.Error(err, "Failed to delete property for node", "key", key, "node", nodeName)
				}
			}
		}
	}

	return nil
}

// recordKey generates a unique identifier (Name:Type) for a DNS record.
func recordKey(r *dns.Record) string {
	if r == nil {
		return ""
	}
	return fmt.Sprintf("%s:%s", strings.ToLower(r.Name), strings.ToUpper(r.Type))
}

// applyChanges takes the current records and a set of changes,
// and returns the new, full list of desired records.
func applyChanges(current []*dns.Record, changes *plan.Changes) []*dns.Record {
	// 1. Start with the current records in a map for easy manipulation
	desiredMap := make(map[string]*dns.Record)
	for _, r := range current {
		desiredMap[recordKey(r)] = r
	}

	// 2. Apply Deletes
	for _, r := range changes.Delete {
		delete(desiredMap, recordKey(r))
	}

	// 3. Apply Updates
	for _, u := range changes.Update {
		// Just replace the old one with the new desired one
		desiredMap[recordKey(u.Desired)] = u.Desired
	}

	// 4. Apply Creates
	for _, r := range changes.Create {
		desiredMap[recordKey(r)] = r
	}

	// 5. Convert map back to slice
	desiredList := make([]*dns.Record, 0, len(desiredMap))
	for _, r := range desiredMap {
		desiredList = append(desiredList, r)
	}

	return desiredList
}

// (packAliases, getKey, SetInstanceProperties, DeleteInstanceProperty,
// ... and getInstanceMetadata all remain the same ...)

// packAliases takes a list of suffixed aliases and packs them into a
// map of { "landb-alias": "...", "landb-alias2": "...", ... }
// respecting the metadataCharLimit.
func packAliases(aliases []string) map[string]string {
	packed := make(map[string]string)
	if len(aliases) == 0 {
		return packed // Return empty map
	}

	// Sort aliases to ensure a stable string for diffing
	sort.Strings(aliases)

	var b strings.Builder
	keyIndex := 0

	for _, alias := range aliases {
		// Check if adding this alias (plus a comma) would exceed the limit
		commaLen := 0
		if b.Len() > 0 {
			commaLen = 1 // for the comma separator
		}

		if b.Len()+len(alias)+commaLen > metadataCharLimit {
			// Current key is full. Store it.
			packed[getKey(keyIndex)] = b.String()

			// Start a new key
			b.Reset()
			keyIndex++
		}

		// Add the alias to the current key
		if b.Len() > 0 {
			b.WriteByte(',')
		}
		b.WriteString(alias)
	}

	// Add the last key
	if b.Len() > 0 {
		packed[getKey(keyIndex)] = b.String()
	}

	return packed
}

// getKey generates the metadata key name (landb-alias, landb-alias2, etc.)
func getKey(index int) string {
	if index == 0 {
		return internal.LandbAliasPrefix
	}
	return fmt.Sprintf("%s%d", internal.LandbAliasPrefix, index+1)
}

// SetInstanceProperties updates or creates metadata keys on a server.
func (p *Provider) SetInstanceProperties(ctx context.Context, instanceName string, properties *PropertySet) error {
	log := log.Log
	return p.retryWithReauth(func() error {
		serverID, err := p.getServerID(ctx, instanceName)
		if err != nil {
			return fmt.Errorf("failed to get server ID for %q: %w", instanceName, err)
		}

		// Use UpdateMetadata, which replaces keys specified in the map
		// and leaves other keys untouched.
		result := servers.UpdateMetadata(ctx, p.computeClient, serverID, properties)
		if result.Err != nil {
			log.Error(result.Err, "error setting instance properties", "instance", instanceName)
			return result.Err
		}
		return nil
	})
}

// DeleteInstanceProperty deletes a metadata key on a server.
func (p *Provider) DeleteInstanceProperty(ctx context.Context, instanceName, propertyKey string) error {
	log := log.Log
	return p.retryWithReauth(func() error {
		serverID, err := p.getServerID(ctx, instanceName)
		if err != nil {
			return fmt.Errorf("failed to get server ID for %q: %w", instanceName, err)
		}

		result := servers.DeleteMetadatum(ctx, p.computeClient, serverID, propertyKey)
		if result.Err != nil {
			log.Error(result.Err, "error deleting instance property", "key", propertyKey, "instance", instanceName)
			return result.Err
		}
		return nil
	})
}

// getInstanceMetadata retrieves the full metadata map for a given server.
func (p *Provider) getInstanceMetadata(ctx context.Context, instanceName string) (map[string]string, error) {
	var metadata map[string]string

	err := p.retryWithReauth(func() error {
		serverID, err := p.getServerID(ctx, instanceName)
		if err != nil {
			return fmt.Errorf("failed to get server ID for %q: %w", instanceName, err)
		}

		srv, err := servers.Get(ctx, p.computeClient, serverID).Extract()
		if err != nil {
			return fmt.Errorf("failed to get server details for %q: %w", instanceName, err)
		}
		metadata = srv.Metadata
		return nil
	})

	if err != nil {
		return nil, err
	}
	return metadata, nil
}
