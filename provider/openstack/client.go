package openstack

import (
	"context"
	"fmt"
	log "github.com/sirupsen/logrus"
	"strings"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/servers"
)

// DefaultDomainName is the default OpenStack domain name.
const DefaultDomainName = "Default"

// Client is a client for the OpenStack API.
type Client struct {
	// client is the OpenStack compute client.
	client *gophercloud.ServiceClient
	// opts are the client options.
	opts ClientOpts
}

// ClientOpts are the options for the OpenStack client.
type ClientOpts struct {
	// IdentityEndpoint is the OpenStack identity endpoint.
	IdentityEndpoint string
	// DomainName is the OpenStack domain name.
	DomainName string
	// TenantName is the OpenStack tenant name.
	TenantName string
	// Username is the OpenStack username.
	Username string
	// Password is the OpenStack password.
	Password string
}

// NewClient creates a new OpenStack client.
func NewClient(opts ClientOpts) (*Client, error) {
	client := &Client{opts: opts}
	if err := client.authenticate(); err != nil {
		return nil, err
	}
	return client, nil
}

// authenticate authenticates the client with the OpenStack API.
func (c *Client) authenticate() error {
	opts := gophercloud.AuthOptions{
		IdentityEndpoint: c.opts.IdentityEndpoint,
		Username:         c.opts.Username,
		Password:         c.opts.Password,
		TenantName:       c.opts.TenantName,
		DomainName:       c.opts.DomainName,
	}

	// Authenticate clients
	providerClient, err := openstack.AuthenticatedClient(context.Background(), opts)
	if err != nil {
		return fmt.Errorf("failed to authenticate v2: %w", err)
	}

	// Create service clients
	computeClient, err := openstack.NewComputeV2(providerClient, gophercloud.EndpointOpts{})
	if err != nil {
		return fmt.Errorf("failed to create compute client: %w", err)
	}

	c.client = computeClient

	return nil
}

// retryWithReauth retries an operation with re-authentication if it fails with an authentication error.
func (c *Client) retryWithReauth(operation func() error) error {
	err := operation()
	if err != nil && (strings.Contains(err.Error(), "Authentication failed") || strings.Contains(err.Error(), "unauthorized")) {
		log.Info("Authentication failed, attempting to re-authenticate...")
		if authErr := c.authenticate(); authErr != nil {
			return fmt.Errorf("re-authentication failed: %w", authErr)
		}
		log.Info("Re-authentication successful. Retrying operation...")
		return operation()
	}
	return err
}

// GetInstanceProperties gets the properties of an instance.
func (c *Client) GetInstanceProperties(ctx context.Context, name string) (map[string]string, error) {
	var metadata map[string]string
	err := c.retryWithReauth(func() error {
		serverID, err := c.getServerID(ctx, name)
		if err != nil {
			return fmt.Errorf("failed to get server ID for %q: %w", name, err)
		}

		server, err := servers.Get(ctx, c.client, serverID).Extract()
		if err != nil {
			return fmt.Errorf("failed to get server details for %q: %w", name, err)
		}

		metadata = server.Metadata
		return nil
	})
	return metadata, err
}

// GetInstanceLanDBAliases retrieves all landb-alias* metadata entries for an instance.
func (c *Client) GetInstanceLanDBAliases(ctx context.Context, instanceName string) (*PropertySet, error) {
	if ctx == nil {
		log.Errorf("nil context")
	}

	if instanceName == "" {
		return nil, fmt.Errorf("empty instance name")
	}

	var serverPropertySet *PropertySet
	err := c.retryWithReauth(func() error {
		serverId, err := c.getServerID(ctx, instanceName)
		if err != nil {
			return fmt.Errorf("failed to get server ID for %q: %w", instanceName, err)
		}
		server, err := servers.Get(ctx, c.client, serverId).Extract()
		if err != nil {
			return fmt.Errorf("failed to get server details for %q: %w", instanceName, err)
		}
		serverProperties := server.Metadata
		serverPropertySet = NewPropertySet(map[string]string{})

		for property, propertyValue := range serverProperties {
			if strings.HasPrefix(property, "landb-alias") {
				serverPropertySet.Set(property, propertyValue)
			}
		}

		return nil
	})

	return serverPropertySet, err
}

// getServerID looks up the server ID by name.
func (c *Client) getServerID(ctx context.Context, name string) (string, error) {
	request := servers.List(c.client, servers.ListOpts{Name: name})
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
		return "", fmt.Errorf("multiple servers found with name %q", name)
	}

	return allServers[0].ID, nil
}

// SetInstanceProperties updates or creates a metadata key on a server.
func (c *Client) SetInstanceProperties(ctx context.Context, instanceName string, properties *PropertySet) error {
	return c.retryWithReauth(func() error {
		serverId, err := c.getServerID(ctx, instanceName)
		if err != nil {
			log.Errorf("failed to get server ID for %q: %v", instanceName, err)
			return err
		}
		result := servers.UpdateMetadata(ctx, c.client, serverId, properties)
		if result.Err != nil {
			log.Errorf("error setting instace properties for %q: %v", instanceName, result.Err)
			return result.Err
		}
		return nil
	})
}

// DeleteInstanceProperty deletes a metadata key on a server.
func (c *Client) DeleteInstanceProperty(ctx context.Context, instanceName, propertyKey string) error {
	return c.retryWithReauth(func() error {
		serverId, err := c.getServerID(ctx, instanceName)
		if err != nil {
			log.Errorf("failed to get server ID for %q: %v", instanceName, err)
			return err
		}
		result := servers.DeleteMetadatum(ctx, c.client, serverId, propertyKey)
		if result.Err != nil {
			log.Errorf("error deleting instace properties for %q: %v", instanceName, result.Err)
			return result.Err
		}
		return nil
	})
}
