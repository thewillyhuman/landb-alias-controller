package openstack

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/go-logr/logr"
	"github.com/gophercloud/gophercloud/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"gitlab.cern.ch/gfacundo/landb-alias-controller/provider"
)

// --- mockCompute: test double for computeAPI ---

type mockCompute struct {
	// serverIDs maps server name → server ID.
	serverIDs map[string]string
	// metadata maps server ID → metadata.
	metadata map[string]map[string]string
	// updatedMetadata captures the last updateMetadata call per server.
	updatedMetadata map[string]map[string]string
	// deletedKeys captures all deleteMetadatum calls as serverID:key.
	deletedKeys []string

	// Inject errors for specific operations.
	getServerIDErr    error
	getMetadataErr    error
	updateMetadataErr error
	deleteMetadatumErr error
}

func newMockCompute() *mockCompute {
	return &mockCompute{
		serverIDs:       make(map[string]string),
		metadata:        make(map[string]map[string]string),
		updatedMetadata: make(map[string]map[string]string),
	}
}

func (m *mockCompute) getServerID(_ context.Context, name string) (string, error) {
	if m.getServerIDErr != nil {
		return "", m.getServerIDErr
	}
	id, ok := m.serverIDs[name]
	if !ok {
		return "", errors.New("no server found with name " + name)
	}
	return id, nil
}

func (m *mockCompute) getMetadata(_ context.Context, serverID string) (map[string]string, error) {
	if m.getMetadataErr != nil {
		return nil, m.getMetadataErr
	}
	meta, ok := m.metadata[serverID]
	if !ok {
		return map[string]string{}, nil
	}
	// Return a copy to avoid test interference.
	cp := make(map[string]string, len(meta))
	for k, v := range meta {
		cp[k] = v
	}
	return cp, nil
}

func (m *mockCompute) updateMetadata(_ context.Context, serverID string, meta map[string]string) error {
	if m.updateMetadataErr != nil {
		return m.updateMetadataErr
	}
	m.updatedMetadata[serverID] = meta
	// Also update the stored metadata so subsequent reads reflect changes.
	if m.metadata[serverID] == nil {
		m.metadata[serverID] = make(map[string]string)
	}
	for k, v := range meta {
		m.metadata[serverID][k] = v
	}
	return nil
}

func (m *mockCompute) deleteMetadatum(_ context.Context, serverID, key string) error {
	if m.deleteMetadatumErr != nil {
		return m.deleteMetadatumErr
	}
	m.deletedKeys = append(m.deletedKeys, serverID+":"+key)
	delete(m.metadata[serverID], key)
	return nil
}

// newTestProvider creates a Provider with a mock compute backend.
func newTestProvider(mock *mockCompute) *Provider {
	return &Provider{
		compute: mock,
		log:     logr.Discard(),
	}
}

// --- Sync tests ---

func TestSync_NoNodes(t *testing.T) {
	mock := newMockCompute()
	p := newTestProvider(mock)

	err := p.Sync(context.Background(), provider.AliasSet{
		Aliases: []string{"app1"},
		Nodes:   nil,
	})
	require.NoError(t, err)
}

func TestSync_NoAliases_ClearsExistingMetadata(t *testing.T) {
	mock := newMockCompute()
	mock.serverIDs["node-0"] = "id-0"
	mock.metadata["id-0"] = map[string]string{
		"landb-alias": "old-app--load-0-",
		"other-key":   "keep-me",
	}

	p := newTestProvider(mock)
	err := p.Sync(context.Background(), provider.AliasSet{
		Aliases: []string{},
		Nodes:   []provider.NodeInfo{{Name: "node-0", IP: "1.1.1.1"}},
	})
	require.NoError(t, err)

	// The landb-alias key should be deleted; "other-key" should be untouched.
	assert.Contains(t, mock.deletedKeys, "id-0:landb-alias")
	assert.Empty(t, mock.updatedMetadata["id-0"])
}

func TestSync_CreatesMetadata(t *testing.T) {
	mock := newMockCompute()
	mock.serverIDs["node-a"] = "id-a"
	mock.serverIDs["node-b"] = "id-b"
	mock.metadata["id-a"] = map[string]string{}
	mock.metadata["id-b"] = map[string]string{}

	p := newTestProvider(mock)
	err := p.Sync(context.Background(), provider.AliasSet{
		Aliases: []string{"app1", "app2"},
		Nodes: []provider.NodeInfo{
			{Name: "node-a", IP: "1.1.1.1"},
			{Name: "node-b", IP: "2.2.2.2"},
		},
	})
	require.NoError(t, err)

	// Node-a (index 0) should get --load-0- suffixes.
	updated0 := mock.updatedMetadata["id-a"]
	assert.Equal(t, "app1--load-0-,app2--load-0-", updated0["landb-alias"])

	// Node-b (index 1) should get --load-1- suffixes.
	updated1 := mock.updatedMetadata["id-b"]
	assert.Equal(t, "app1--load-1-,app2--load-1-", updated1["landb-alias"])
}

func TestSync_NoChangesNeeded(t *testing.T) {
	mock := newMockCompute()
	mock.serverIDs["node-0"] = "id-0"
	mock.metadata["id-0"] = map[string]string{
		"landb-alias": "app1--load-0-,app2--load-0-",
	}

	p := newTestProvider(mock)
	err := p.Sync(context.Background(), provider.AliasSet{
		Aliases: []string{"app1", "app2"},
		Nodes:   []provider.NodeInfo{{Name: "node-0", IP: "1.1.1.1"}},
	})
	require.NoError(t, err)

	// No updates or deletes should have been issued.
	assert.Empty(t, mock.updatedMetadata["id-0"])
	assert.Empty(t, mock.deletedKeys)
}

func TestSync_DeletesStaleKeys(t *testing.T) {
	mock := newMockCompute()
	mock.serverIDs["node-0"] = "id-0"
	mock.metadata["id-0"] = map[string]string{
		"landb-alias":  "app1--load-0-",
		"landb-alias2": "stale-data",
	}

	p := newTestProvider(mock)
	err := p.Sync(context.Background(), provider.AliasSet{
		Aliases: []string{"app1"},
		Nodes:   []provider.NodeInfo{{Name: "node-0", IP: "1.1.1.1"}},
	})
	require.NoError(t, err)

	// landb-alias2 should be deleted (no longer needed).
	assert.Contains(t, mock.deletedKeys, "id-0:landb-alias2")
}

func TestSync_AggregatesErrors(t *testing.T) {
	mock := newMockCompute()
	mock.serverIDs["node-a"] = "id-a"
	// node-b is missing from serverIDs → will fail to resolve.
	mock.serverIDs["node-b"] = ""

	mock.metadata["id-a"] = map[string]string{}
	// Override getServerID to fail for node-b.
	mock.serverIDs = map[string]string{"node-a": "id-a"}

	p := newTestProvider(mock)
	err := p.Sync(context.Background(), provider.AliasSet{
		Aliases: []string{"app1"},
		Nodes: []provider.NodeInfo{
			{Name: "node-a", IP: "1.1.1.1"},
			{Name: "node-b", IP: "2.2.2.2"},
		},
	})

	// Should return an error mentioning node-b, but node-a should still succeed.
	require.Error(t, err)
	assert.Contains(t, err.Error(), "node-b")
	// Node-a should have been updated successfully.
	assert.NotEmpty(t, mock.updatedMetadata["id-a"])
}

func TestSync_UpdateMetadataError(t *testing.T) {
	mock := newMockCompute()
	mock.serverIDs["node-0"] = "id-0"
	mock.metadata["id-0"] = map[string]string{}
	mock.updateMetadataErr = errors.New("API error")

	p := newTestProvider(mock)
	err := p.Sync(context.Background(), provider.AliasSet{
		Aliases: []string{"app1"},
		Nodes:   []provider.NodeInfo{{Name: "node-0", IP: "1.1.1.1"}},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "updating metadata")
}

// --- StaleNodes cleanup tests ---

func TestSync_CleansStaleNodes(t *testing.T) {
	mock := newMockCompute()
	mock.serverIDs["stale-node"] = "id-stale"
	mock.metadata["id-stale"] = map[string]string{
		"landb-alias":  "old-app--load-0-",
		"landb-alias2": "old-app2--load-0-",
		"other-key":    "keep-me",
	}

	p := newTestProvider(mock)
	err := p.Sync(context.Background(), provider.AliasSet{
		Aliases:    []string{"app1"},
		StaleNodes: []provider.NodeInfo{{Name: "stale-node", IP: "3.3.3.3"}},
	})
	require.NoError(t, err)

	assert.Contains(t, mock.deletedKeys, "id-stale:landb-alias")
	assert.Contains(t, mock.deletedKeys, "id-stale:landb-alias2")
	// Non-landb keys should not be deleted.
	for _, dk := range mock.deletedKeys {
		assert.NotContains(t, dk, "other-key")
	}
}

func TestSync_StaleNodeWithoutMetadata_NoOp(t *testing.T) {
	mock := newMockCompute()
	mock.serverIDs["clean-node"] = "id-clean"
	mock.metadata["id-clean"] = map[string]string{
		"other-key": "some-value",
	}

	p := newTestProvider(mock)
	err := p.Sync(context.Background(), provider.AliasSet{
		StaleNodes: []provider.NodeInfo{{Name: "clean-node", IP: "3.3.3.3"}},
	})
	require.NoError(t, err)
	assert.Empty(t, mock.deletedKeys)
}

func TestSync_StaleNodeError_Aggregated(t *testing.T) {
	mock := newMockCompute()
	// Ingress node succeeds.
	mock.serverIDs["node-a"] = "id-a"
	mock.metadata["id-a"] = map[string]string{}
	// Stale node fails to resolve.

	p := newTestProvider(mock)
	err := p.Sync(context.Background(), provider.AliasSet{
		Aliases: []string{"app1"},
		Nodes:   []provider.NodeInfo{{Name: "node-a", IP: "1.1.1.1"}},
		StaleNodes: []provider.NodeInfo{{Name: "missing-node", IP: "3.3.3.3"}},
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "stale node missing-node")
	// Ingress node should still have been synced.
	assert.NotEmpty(t, mock.updatedMetadata["id-a"])
}

// --- retryWithReauth tests ---

func TestRetryWithReauth_SuccessOnFirstTry(t *testing.T) {
	p := newTestProvider(newMockCompute())

	calls := 0
	err := p.retryWithReauth(func() error {
		calls++
		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, 1, calls)
}

func TestRetryWithReauth_NonAuthError_NoRetry(t *testing.T) {
	p := newTestProvider(newMockCompute())

	calls := 0
	err := p.retryWithReauth(func() error {
		calls++
		return errors.New("some other error")
	})
	require.Error(t, err)
	assert.Equal(t, 1, calls)
	assert.Contains(t, err.Error(), "some other error")
}

func TestRetryWithReauth_401Error_TriggersReauth(t *testing.T) {
	mock := newMockCompute()
	p := newTestProvider(mock)

	calls := 0
	err := p.retryWithReauth(func() error {
		calls++
		if calls == 1 {
			return gophercloud.ErrUnexpectedResponseCode{
				Actual: 401,
			}
		}
		return nil
	})
	// retryWithReauth calls p.authenticate() which will fail because
	// the provider has no real OpenStack credentials. That's expected.
	// The important thing is that a 401 triggers the reauth path.
	require.Error(t, err)
	assert.Equal(t, 1, calls)
	assert.Contains(t, err.Error(), "re-authentication failed")
}

func TestRetryWithReauth_Non401HTTPError_NoRetry(t *testing.T) {
	p := newTestProvider(newMockCompute())

	calls := 0
	err := p.retryWithReauth(func() error {
		calls++
		return gophercloud.ErrUnexpectedResponseCode{
			Actual: 404,
		}
	})
	require.Error(t, err)
	assert.Equal(t, 1, calls)
}

func TestRetryWithReauth_Wrapped401Error_TriggersReauth(t *testing.T) {
	p := newTestProvider(newMockCompute())

	calls := 0
	err := p.retryWithReauth(func() error {
		calls++
		wrapped := fmt.Errorf("listing servers: %w", gophercloud.ErrUnexpectedResponseCode{
			Actual: 401,
		})
		return wrapped
	})
	// Should detect the 401 via errors.As even when wrapped.
	require.Error(t, err)
	assert.Equal(t, 1, calls)
	assert.Contains(t, err.Error(), "re-authentication failed")
}

func TestSync_DeleteMetadatumError(t *testing.T) {
	mock := newMockCompute()
	mock.serverIDs["node-0"] = "id-0"
	mock.metadata["id-0"] = map[string]string{
		"landb-alias":  "app1--load-0-",
		"landb-alias2": "stale",
	}
	mock.deleteMetadatumErr = errors.New("delete failed")

	p := newTestProvider(mock)
	err := p.Sync(context.Background(), provider.AliasSet{
		Aliases: []string{"app1"},
		Nodes:   []provider.NodeInfo{{Name: "node-0", IP: "1.1.1.1"}},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "deleting key")
}
