package openstack

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/servers"

	"gitlab.cern.ch/kubernetes/networking/landb-controller/landb-alias-controller/metrics"
	"gitlab.cern.ch/kubernetes/networking/landb-controller/landb-alias-controller/provider"
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
	// authOpts holds the OpenStack authentication options.
	authOpts gophercloud.AuthOptions

	// region selects which service catalog endpoint to use.
	region string

	// password is kept as a byte slice for secure zeroing after use.
	password []byte

	// compute is the abstracted compute API for metadata operations.
	compute computeAPI

	// log is the structured logger for this provider instance.
	log logr.Logger
}

// NewProvider creates and authenticates a new OpenStack Provider.
//
// cfg contains pre-built authentication options and region, either from
// environment variables or from a cloud-config secret.
func NewProvider(log logr.Logger, cfg AuthConfig) (*Provider, error) {
	p := &Provider{
		authOpts: cfg.AuthOptions,
		region:   cfg.Region,
		password: []byte(cfg.AuthOptions.Password),
		log:      log.WithName("openstack"),
	}

	if err := p.validateAuthOptions(); err != nil {
		return nil, fmt.Errorf("invalid OpenStack configuration: %w", err)
	}

	if err := p.authenticate(); err != nil {
		return nil, fmt.Errorf("OpenStack authentication failed: %w", err)
	}

	logFields := []interface{}{"endpoint", p.authOpts.IdentityEndpoint, "region", p.region}
	switch {
	case p.authOpts.ApplicationCredentialID != "":
		logFields = append(logFields, "applicationCredentialID", p.authOpts.ApplicationCredentialID)
	case p.authOpts.UserID != "":
		logFields = append(logFields, "userID", p.authOpts.UserID)
	default:
		logFields = append(logFields, "tenant", p.authOpts.TenantName, "user", p.authOpts.Username)
	}
	p.log.Info("OpenStack provider initialized", logFields...)

	return p, nil
}

// validateAuthOptions ensures all required authentication fields are
// present. It supports three modes:
//   - Application-credential (ApplicationCredentialID set): requires IdentityEndpoint, ApplicationCredentialID, ApplicationCredentialSecret
//   - Trust-based (UserID set): requires IdentityEndpoint, UserID, Password, TrustID
//   - Username-based (Username set): requires IdentityEndpoint, Username, Password, TenantName, DomainName
func (p *Provider) validateAuthOptions() error {
	var missing []string

	if p.authOpts.IdentityEndpoint == "" {
		missing = append(missing, "identity-endpoint")
	}

	switch {
	case p.authOpts.ApplicationCredentialID != "":
		// Application-credential auth (cloud-config mode).
		if p.authOpts.ApplicationCredentialSecret == "" {
			missing = append(missing, "application-credential-secret")
		}
	case p.authOpts.UserID != "":
		// Trust-based auth (cloud-config mode).
		if p.authOpts.Password == "" {
			missing = append(missing, "password")
		}
		if p.authOpts.Scope == nil || p.authOpts.Scope.TrustID == "" {
			missing = append(missing, "trust-id")
		}
	case p.authOpts.Username != "":
		// Username-based auth (env var mode).
		if p.authOpts.Password == "" {
			missing = append(missing, "password")
		}
		if p.authOpts.TenantName == "" {
			missing = append(missing, "tenant-name")
		}
		if p.authOpts.DomainName == "" {
			missing = append(missing, "domain-name")
		}
	default:
		missing = append(missing, "user-id, username, or application-credential-id")
	}

	if len(missing) > 0 {
		return fmt.Errorf("missing required fields: %s", strings.Join(missing, ", "))
	}
	return nil
}

// authenticate creates an OpenStack session and initializes the compute
// client. It can be called again to refresh an expired token.
func (p *Provider) authenticate() error {
	providerClient, err := openstack.AuthenticatedClient(context.Background(), p.authOpts)
	if err != nil {
		return fmt.Errorf("keystone authentication failed: %w", err)
	}

	computeClient, err := openstack.NewComputeV2(providerClient, gophercloud.EndpointOpts{Region: p.region})
	if err != nil {
		return fmt.Errorf("compute client creation failed: %w", err)
	}

	p.compute = &gophercloudCompute{client: computeClient}
	p.log.V(1).Info("OpenStack authentication successful")
	return nil
}

// retryWithReauth executes an operation and retries once after
// re-authenticating if the error indicates an expired token (HTTP 401).
func (p *Provider) retryWithReauth(op func() error) error {
	err := op()
	if err == nil {
		return nil
	}

	var errCode gophercloud.ErrUnexpectedResponseCode
	if !errors.As(err, &errCode) || errCode.Actual != 401 {
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

// Sync reconciles OpenStack server metadata to match the desired state.
//
// Alias metadata and landb-set metadata are reconciled independently so any
// node can carry a landb-set value, regardless of ingress membership.
func (p *Provider) Sync(ctx context.Context, desired provider.DesiredState) error {
	log := p.log.WithValues(
		"aliases", len(desired.Aliases),
		"ingressNodes", len(desired.IngressNodes),
		"staleAliasNodes", len(desired.StaleAliasNodes),
		"landbSetNodes", len(desired.LandbSetNodes),
		"staleLandbSetNodes", len(desired.StaleLandbSetNodes),
	)
	log.Info("Starting metadata synchronization")

	var errs []error

	for i, node := range desired.IngressNodes {
		nodeLog := log.WithValues("node", node.Name, "index", i, "metadata", "landb-alias")
		if err := p.syncAliasNode(ctx, nodeLog, node, i, desired.Aliases); err != nil {
			nodeLog.Error(err, "Failed to sync alias metadata")
			errs = append(errs, fmt.Errorf("alias node %s: %w", node.Name, err))
		}
	}

	// Clean up landb-alias metadata from nodes that are no longer ingress.
	for _, node := range desired.StaleAliasNodes {
		nodeLog := log.WithValues("node", node.Name, "metadata", "landb-alias", "stale", true)
		if err := p.cleanupAliasNode(ctx, nodeLog, node); err != nil {
			nodeLog.Error(err, "Failed to cleanup stale alias metadata")
			errs = append(errs, fmt.Errorf("stale alias node %s: %w", node.Name, err))
		}
	}

	for _, node := range desired.LandbSetNodes {
		nodeLog := log.WithValues("node", node.Name, "metadata", "landb-set")
		if err := p.syncLandbSetNode(ctx, nodeLog, node); err != nil {
			nodeLog.Error(err, "Failed to sync landb-set metadata")
			errs = append(errs, fmt.Errorf("landb-set node %s: %w", node.Name, err))
		}
	}

	for _, node := range desired.StaleLandbSetNodes {
		nodeLog := log.WithValues("node", node.Name, "metadata", "landb-set", "stale", true)
		if err := p.cleanupLandbSetNode(ctx, nodeLog, node); err != nil {
			nodeLog.Error(err, "Failed to cleanup stale landb-set metadata")
			errs = append(errs, fmt.Errorf("stale landb-set node %s: %w", node.Name, err))
		}
	}

	totalNodes := len(desired.IngressNodes) + len(desired.StaleAliasNodes) +
		len(desired.LandbSetNodes) + len(desired.StaleLandbSetNodes)
	if len(errs) > 0 {
		return fmt.Errorf("sync failed for %d/%d node operations: %w",
			len(errs), totalNodes, errors.Join(errs...))
	}

	log.Info("Metadata synchronization completed successfully")
	return nil
}

// syncAliasNode reconciles landb-alias metadata for a single ingress node.
func (p *Provider) syncAliasNode(
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

	// Step 5: Diff — find stale metadata keys to delete.
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

// cleanupAliasNode removes all landb-alias metadata from a node that is no
// longer serving ingress traffic.
func (p *Provider) cleanupAliasNode(
	ctx context.Context,
	log logr.Logger,
	node provider.NodeInfo,
) error {
	// Step 1: Resolve the OpenStack server ID.
	serverID, err := p.getServerIDInstrumented(ctx, node.Name)
	if err != nil {
		return fmt.Errorf("resolving server ID: %w", err)
	}

	// Step 2: Read current metadata.
	currentMeta, err := p.getMetadataInstrumented(ctx, serverID)
	if err != nil {
		return fmt.Errorf("reading metadata: %w", err)
	}

	// Step 3: Find all landb-alias keys.
	var toDelete []string
	for key := range currentMeta {
		if strings.HasPrefix(key, landbAliasPrefix) {
			toDelete = append(toDelete, key)
		}
	}

	if len(toDelete) > 0 {
		log.Info("Removing stale landb-alias metadata", "keys", len(toDelete))
	}
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

	if len(toDelete) == 0 {
		log.V(1).Info("No changes needed")
	}

	return nil
}

func (p *Provider) syncLandbSetNode(ctx context.Context, log logr.Logger, node provider.NodeInfo) error {
	serverID, err := p.getServerIDInstrumented(ctx, node.Name)
	if err != nil {
		return fmt.Errorf("resolving server ID: %w", err)
	}
	log.V(1).Info("Resolved server ID", "serverID", serverID)

	currentMeta, err := p.getMetadataInstrumented(ctx, serverID)
	if err != nil {
		return fmt.Errorf("reading metadata: %w", err)
	}

	toUpdate := make(map[string]string)
	reconcileLandbSet(log, node, currentMeta, toUpdate)
	if len(toUpdate) == 0 {
		log.V(1).Info("No changes needed")
		return nil
	}

	if err := p.updateMetadataInstrumented(ctx, serverID, toUpdate); err != nil {
		return fmt.Errorf("updating metadata: %w", err)
	}
	log.Info("Updated metadata keys", "count", len(toUpdate))
	return nil
}

func (p *Provider) cleanupLandbSetNode(ctx context.Context, log logr.Logger, node provider.NodeInfo) error {
	serverID, err := p.getServerIDInstrumented(ctx, node.Name)
	if err != nil {
		return fmt.Errorf("resolving server ID: %w", err)
	}
	log.V(1).Info("Resolved server ID", "serverID", serverID)

	currentMeta, err := p.getMetadataInstrumented(ctx, serverID)
	if err != nil {
		return fmt.Errorf("reading metadata: %w", err)
	}

	var toDelete []string
	reconcileStaleLandbSet(log, currentMeta, &toDelete)
	if len(toDelete) == 0 {
		log.V(1).Info("No changes needed")
		return nil
	}

	var deleteErrs []error
	for _, key := range toDelete {
		if err := p.deleteMetadatumInstrumented(ctx, serverID, key); err != nil {
			deleteErrs = append(deleteErrs, fmt.Errorf("deleting key %q: %w", key, err))
		} else {
			log.Info("Deleted stale metadata key", "key", key)
		}
	}
	return errors.Join(deleteErrs...)
}

func reconcileLandbSet(
	log logr.Logger,
	node provider.NodeInfo,
	currentMeta map[string]string,
	toUpdate map[string]string,
) {
	if node.LandbSet == "" {
		return
	}
	if currentVal, exists := currentMeta[landbSetMetadataKey]; !exists || currentVal != node.LandbSet {
		log.Info("Metadata key needs update", "key", landbSetMetadataKey,
			"current", currentMeta[landbSetMetadataKey], "desired", node.LandbSet)
		toUpdate[landbSetMetadataKey] = node.LandbSet
	}
}

func reconcileStaleLandbSet(
	log logr.Logger,
	currentMeta map[string]string,
	toDelete *[]string,
) {
	if _, exists := currentMeta[landbSetMetadataKey]; exists {
		log.Info("Stale metadata key will be deleted", "key", landbSetMetadataKey)
		*toDelete = append(*toDelete, landbSetMetadataKey)
	}
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
	// TODO: Resolve servers from the Kubernetes node spec.providerID instead of
	// name. Nova names are not guaranteed to be unique, and providerID exposes
	// the real instance UUID for both bare metal and VM-backed nodes.
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
