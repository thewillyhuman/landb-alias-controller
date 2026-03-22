package openstack

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/servers"

	"gitlab.cern.ch/gfacundo/landb-alias-controller/metrics"
	"gitlab.cern.ch/gfacundo/landb-alias-controller/provider"

	"github.com/go-logr/logr"
)

// Auth-error sentinel strings used to detect expired tokens.
const (
	authFailedStr  = "Authentication failed"
	unauthorizedStr = "unauthorized"
)

// computeAPI abstracts the OpenStack compute (Nova) operations needed
// by the provider. A concrete implementation wraps gophercloud; tests
// supply a mock.
type computeAPI interface {
	// getServerID resolves an OpenStack server name to its UUID.
	getServerID(ctx context.Context, name string) (string, error)
	// getMetadata returns all metadata key-value pairs for the given server.
	getMetadata(ctx context.Context, serverID string) (map[string]string, error)
	// updateMetadata creates or updates the specified metadata keys on a
	// server. Existing keys not in the map are left untouched.
	updateMetadata(ctx context.Context, serverID string, meta map[string]string) error
	// deleteMetadatum removes a single metadata key from a server.
	deleteMetadatum(ctx context.Context, serverID, key string) error
}

// Provider implements provider.Provider for CERN's OpenStack-based LANDB
// alias system. DNS aliases are stored as server metadata properties on
// ingress nodes; a separate CERN service reads these and updates DNS.
type Provider struct {
	// identityEndpoint is the OpenStack Keystone URL (e.g., "https://host/v3").
	identityEndpoint string
	// domainName is the OpenStack user domain (typically "Default").
	domainName string
	// tenantName is the OpenStack project/tenant name.
	tenantName string
	// userName is the OpenStack authentication username.
	userName string
	// password is kept as a byte slice for secure zeroing after use.
	password []byte

	// compute is the abstracted compute API for metadata operations.
	compute computeAPI

	// log is the structured logger for this provider instance.
	log logr.Logger
}

// NewProvider creates and authenticates a new OpenStack Provider.
//
// Configuration is read from environment variables:
//   - OS_AUTH_URL: Keystone identity endpoint
//   - OS_USER_DOMAIN_NAME: user domain
//   - OS_PROJECT_NAME: project/tenant name
//   - OS_USERNAME: authentication username
//   - OS_PASSWORD: authentication password
func NewProvider(log logr.Logger) (*Provider, error) {
	p := &Provider{
		identityEndpoint: os.Getenv("OS_AUTH_URL"),
		domainName:       os.Getenv("OS_USER_DOMAIN_NAME"),
		tenantName:       os.Getenv("OS_PROJECT_NAME"),
		userName:         os.Getenv("OS_USERNAME"),
		password:         []byte(os.Getenv("OS_PASSWORD")),
		log:              log.WithName("openstack"),
	}

	if err := p.validateConfiguration(); err != nil {
		return nil, fmt.Errorf("invalid OpenStack configuration: %w", err)
	}

	if err := p.authenticate(); err != nil {
		return nil, fmt.Errorf("OpenStack authentication failed: %w", err)
	}

	p.log.Info("OpenStack provider initialized",
		"endpoint", p.identityEndpoint,
		"tenant", p.tenantName,
		"user", p.userName,
	)

	return p, nil
}

// validateConfiguration ensures all required fields are present before
// attempting authentication.
func (p *Provider) validateConfiguration() error {
	missing := make([]string, 0, 5)
	if p.identityEndpoint == "" {
		missing = append(missing, "OS_AUTH_URL")
	}
	if p.domainName == "" {
		missing = append(missing, "OS_USER_DOMAIN_NAME")
	}
	if p.tenantName == "" {
		missing = append(missing, "OS_PROJECT_NAME")
	}
	if p.userName == "" {
		missing = append(missing, "OS_USERNAME")
	}
	if len(p.password) == 0 {
		missing = append(missing, "OS_PASSWORD")
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing required environment variables: %s", strings.Join(missing, ", "))
	}
	return nil
}

// authenticate creates an OpenStack session and initializes the compute
// client. It can be called again to refresh an expired token.
func (p *Provider) authenticate() error {
	opts := gophercloud.AuthOptions{
		IdentityEndpoint: p.identityEndpoint,
		Username:         p.userName,
		Password:         string(p.password),
		TenantName:       p.tenantName,
		DomainName:       p.domainName,
	}

	providerClient, err := openstack.AuthenticatedClient(context.Background(), opts)
	if err != nil {
		return fmt.Errorf("keystone authentication failed: %w", err)
	}

	computeClient, err := openstack.NewComputeV2(providerClient, gophercloud.EndpointOpts{})
	if err != nil {
		return fmt.Errorf("compute client creation failed: %w", err)
	}

	p.compute = &gophercloudCompute{client: computeClient}
	p.log.V(1).Info("OpenStack authentication successful")
	return nil
}

// retryWithReauth executes an operation and retries once after
// re-authenticating if the error indicates an expired token.
func (p *Provider) retryWithReauth(op func() error) error {
	err := op()
	if err == nil {
		return nil
	}

	errMsg := err.Error()
	if !strings.Contains(errMsg, authFailedStr) && !strings.Contains(errMsg, unauthorizedStr) {
		return err
	}

	p.log.Info("OpenStack token expired, re-authenticating")
	if authErr := p.authenticate(); authErr != nil {
		return fmt.Errorf("re-authentication failed: %w (original: %s)", authErr, err)
	}
	p.log.Info("Re-authentication successful, retrying operation")
	return op()
}

// ZeroPassword securely wipes the password from memory.
func (p *Provider) ZeroPassword() {
	for i := range p.password {
		p.password[i] = 0
	}
	p.password = nil
}

// --- provider.Provider implementation ---

// Sync reconciles the OpenStack server metadata for all nodes in the
// desired AliasSet.
//
// For each node (at index i in the sorted node list):
//  1. Compute desired metadata by appending --load-i- to each alias
//     and packing them into metadata keys.
//  2. Read the node's current metadata from OpenStack.
//  3. Diff: identify keys to create/update and stale keys to delete.
//  4. Apply changes via the OpenStack API.
//
// All nodes are attempted even if some fail; errors are aggregated.
func (p *Provider) Sync(ctx context.Context, desired provider.AliasSet) error {
	log := p.log.WithValues("aliases", len(desired.Aliases), "nodes", len(desired.Nodes))
	log.Info("Starting alias synchronization")

	var errs []error

	for i, node := range desired.Nodes {
		nodeLog := log.WithValues("node", node.Name, "index", i)
		if err := p.syncNode(ctx, nodeLog, node, i, desired.Aliases); err != nil {
			nodeLog.Error(err, "Failed to sync node")
			errs = append(errs, fmt.Errorf("node %s: %w", node.Name, err))
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("sync failed for %d/%d nodes: %w",
			len(errs), len(desired.Nodes), errors.Join(errs...))
	}

	log.Info("Alias synchronization completed successfully")
	return nil
}

// syncNode reconciles metadata for a single node.
func (p *Provider) syncNode(
	ctx context.Context,
	log logr.Logger,
	node provider.NodeInfo,
	nodeIndex int,
	aliases []string,
) error {
	// Step 1: Compute desired metadata for this node.
	suffixed := addSuffix(aliases, nodeIndex)
	desiredMeta, err := packAliases(suffixed)
	if err != nil {
		return fmt.Errorf("packing aliases: %w", err)
	}
	log.V(1).Info("Computed desired metadata", "keys", len(desiredMeta))

	// Step 2: Resolve the OpenStack server ID.
	serverID, err := p.getServerIDInstrumented(ctx, node.Name)
	if err != nil {
		return fmt.Errorf("resolving server ID: %w", err)
	}
	log.V(1).Info("Resolved server ID", "serverID", serverID)

	// Step 3: Read current metadata.
	currentMeta, err := p.getMetadataInstrumented(ctx, serverID)
	if err != nil {
		return fmt.Errorf("reading metadata: %w", err)
	}

	// Step 4: Diff — find keys to update.
	toUpdate := make(map[string]string)
	for key, desiredVal := range desiredMeta {
		if currentVal, exists := currentMeta[key]; !exists || currentVal != desiredVal {
			log.Info("Metadata key needs update", "key", key,
				"current", currentMeta[key], "desired", desiredVal)
			toUpdate[key] = desiredVal
		}
	}

	// Step 5: Diff — find stale landb-alias keys to delete.
	var toDelete []string
	for key := range currentMeta {
		if !strings.HasPrefix(key, landbAliasPrefix) {
			continue
		}
		if _, needed := desiredMeta[key]; !needed {
			log.Info("Stale metadata key will be deleted", "key", key)
			toDelete = append(toDelete, key)
		}
	}

	// Step 6: Apply updates.
	if len(toUpdate) > 0 {
		if err := p.updateMetadataInstrumented(ctx, serverID, toUpdate); err != nil {
			return fmt.Errorf("updating metadata: %w", err)
		}
		log.Info("Updated metadata keys", "count", len(toUpdate))
	}

	// Step 7: Apply deletes.
	var deleteErrs []error
	for _, key := range toDelete {
		if err := p.deleteMetadatumInstrumented(ctx, serverID, key); err != nil {
			deleteErrs = append(deleteErrs, fmt.Errorf("deleting key %q: %w", key, err))
		} else {
			log.Info("Deleted stale metadata key", "key", key)
		}
	}
	if len(deleteErrs) > 0 {
		return errors.Join(deleteErrs...)
	}

	if len(toUpdate) == 0 && len(toDelete) == 0 {
		log.V(1).Info("No changes needed")
	}

	return nil
}

// --- Instrumented API wrappers ---
// Each wrapper adds retry-with-reauth, timing, and metrics.

func (p *Provider) getServerIDInstrumented(ctx context.Context, name string) (string, error) {
	var id string
	err := p.retryWithReauth(func() error {
		start := time.Now()
		var opErr error
		id, opErr = p.compute.getServerID(ctx, name)
		dur := time.Since(start).Seconds()
		metrics.OpenStackAPIDuration.WithLabelValues("get_server_id").Observe(dur)
		if opErr != nil {
			metrics.OpenStackAPICalls.WithLabelValues("get_server_id", "error").Inc()
			return opErr
		}
		metrics.OpenStackAPICalls.WithLabelValues("get_server_id", "success").Inc()
		return nil
	})
	return id, err
}

func (p *Provider) getMetadataInstrumented(ctx context.Context, serverID string) (map[string]string, error) {
	var meta map[string]string
	err := p.retryWithReauth(func() error {
		start := time.Now()
		var opErr error
		meta, opErr = p.compute.getMetadata(ctx, serverID)
		dur := time.Since(start).Seconds()
		metrics.OpenStackAPIDuration.WithLabelValues("get_metadata").Observe(dur)
		if opErr != nil {
			metrics.OpenStackAPICalls.WithLabelValues("get_metadata", "error").Inc()
			return opErr
		}
		metrics.OpenStackAPICalls.WithLabelValues("get_metadata", "success").Inc()
		return nil
	})
	return meta, err
}

func (p *Provider) updateMetadataInstrumented(ctx context.Context, serverID string, meta map[string]string) error {
	return p.retryWithReauth(func() error {
		start := time.Now()
		err := p.compute.updateMetadata(ctx, serverID, meta)
		dur := time.Since(start).Seconds()
		metrics.OpenStackAPIDuration.WithLabelValues("update_metadata").Observe(dur)
		if err != nil {
			metrics.OpenStackAPICalls.WithLabelValues("update_metadata", "error").Inc()
			return err
		}
		metrics.OpenStackAPICalls.WithLabelValues("update_metadata", "success").Inc()
		return nil
	})
}

func (p *Provider) deleteMetadatumInstrumented(ctx context.Context, serverID, key string) error {
	return p.retryWithReauth(func() error {
		start := time.Now()
		err := p.compute.deleteMetadatum(ctx, serverID, key)
		dur := time.Since(start).Seconds()
		metrics.OpenStackAPIDuration.WithLabelValues("delete_metadatum").Observe(dur)
		if err != nil {
			metrics.OpenStackAPICalls.WithLabelValues("delete_metadatum", "error").Inc()
			return err
		}
		metrics.OpenStackAPICalls.WithLabelValues("delete_metadatum", "success").Inc()
		return nil
	})
}

// --- gophercloudCompute: real implementation of computeAPI ---

// gophercloudCompute wraps the gophercloud Nova client to implement
// the computeAPI interface.
type gophercloudCompute struct {
	client *gophercloud.ServiceClient
}

func (g *gophercloudCompute) getServerID(ctx context.Context, name string) (string, error) {
	allPages, err := servers.List(g.client, servers.ListOpts{Name: name}).AllPages(ctx)
	if err != nil {
		return "", fmt.Errorf("listing servers: %w", err)
	}

	allServers, err := servers.ExtractServers(allPages)
	if err != nil {
		return "", fmt.Errorf("extracting servers: %w", err)
	}

	switch len(allServers) {
	case 0:
		return "", fmt.Errorf("no server found with name %q", name)
	case 1:
		return allServers[0].ID, nil
	default:
		return "", fmt.Errorf("multiple servers (%d) found with name %q", len(allServers), name)
	}
}

func (g *gophercloudCompute) getMetadata(ctx context.Context, serverID string) (map[string]string, error) {
	srv, err := servers.Get(ctx, g.client, serverID).Extract()
	if err != nil {
		return nil, fmt.Errorf("getting server %s: %w", serverID, err)
	}
	return srv.Metadata, nil
}

func (g *gophercloudCompute) updateMetadata(ctx context.Context, serverID string, meta map[string]string) error {
	result := servers.UpdateMetadata(ctx, g.client, serverID, servers.MetadataOpts(meta))
	if result.Err != nil {
		return fmt.Errorf("updating metadata on %s: %w", serverID, result.Err)
	}
	return nil
}

func (g *gophercloudCompute) deleteMetadatum(ctx context.Context, serverID, key string) error {
	result := servers.DeleteMetadatum(ctx, g.client, serverID, key)
	if result.Err != nil {
		return fmt.Errorf("deleting metadatum %q on %s: %w", key, serverID, result.Err)
	}
	return nil
}
