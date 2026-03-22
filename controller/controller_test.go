package controller

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"gitlab.cern.ch/gfacundo/landb-alias-controller/provider"
)

// --- mockProvider: test double for provider.Provider ---

type mockProvider struct {
	lastDesired *provider.AliasSet
	syncErr     error
}

func (m *mockProvider) Sync(_ context.Context, desired provider.AliasSet) error {
	m.lastDesired = &desired
	return m.syncErr
}

// --- helpers ---

func newScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = v1.AddToScheme(s)
	_ = networkingv1.AddToScheme(s)
	return s
}

func makeIngress(name, namespace, host string) *networkingv1.Ingress {
	return &networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: networkingv1.IngressSpec{
			Rules: []networkingv1.IngressRule{
				{Host: host},
			},
		},
	}
}

func makeNode(name string, labels map[string]string, externalIP, internalIP string) *v1.Node {
	var addresses []v1.NodeAddress
	if externalIP != "" {
		addresses = append(addresses, v1.NodeAddress{Type: v1.NodeExternalIP, Address: externalIP})
	}
	if internalIP != "" {
		addresses = append(addresses, v1.NodeAddress{Type: v1.NodeInternalIP, Address: internalIP})
	}
	return &v1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels},
		Status:     v1.NodeStatus{Addresses: addresses},
	}
}

// --- listAliases tests ---

func TestListAliases_ExtractsCernHosts(t *testing.T) {
	client := fake.NewClientBuilder().WithScheme(newScheme()).WithObjects(
		makeIngress("ing-1", "default", "app1.cern.ch"),
		makeIngress("ing-2", "default", "app2.cern.ch"),
		makeIngress("ing-3", "other", "external.example.com"),
	).Build()

	c := &Controller{Client: client, IngressNodeLabel: "node-role.kubernetes.io/ingress"}
	aliases, err := c.listAliases(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []string{"app1", "app2"}, aliases)
}

func TestListAliases_DeduplicatesHosts(t *testing.T) {
	client := fake.NewClientBuilder().WithScheme(newScheme()).WithObjects(
		makeIngress("ing-1", "default", "app.cern.ch"),
		makeIngress("ing-2", "other", "app.cern.ch"),
	).Build()

	c := &Controller{Client: client, IngressNodeLabel: "node-role.kubernetes.io/ingress"}
	aliases, err := c.listAliases(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []string{"app"}, aliases)
}

func TestListAliases_NoIngresses(t *testing.T) {
	client := fake.NewClientBuilder().WithScheme(newScheme()).Build()

	c := &Controller{Client: client, IngressNodeLabel: "node-role.kubernetes.io/ingress"}
	aliases, err := c.listAliases(context.Background())
	require.NoError(t, err)
	assert.Empty(t, aliases)
}

func TestListAliases_SkipsNonCernHosts(t *testing.T) {
	client := fake.NewClientBuilder().WithScheme(newScheme()).WithObjects(
		makeIngress("ing-1", "default", "app.example.com"),
		makeIngress("ing-2", "default", "app.cern.ch"),
	).Build()

	c := &Controller{Client: client, IngressNodeLabel: "node-role.kubernetes.io/ingress"}
	aliases, err := c.listAliases(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []string{"app"}, aliases)
}

func TestListAliases_MultipleRulesPerIngress(t *testing.T) {
	ingress := &networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{Name: "multi", Namespace: "default"},
		Spec: networkingv1.IngressSpec{
			Rules: []networkingv1.IngressRule{
				{Host: "app1.cern.ch"},
				{Host: "app2.cern.ch"},
				{Host: "external.io"},
			},
		},
	}
	client := fake.NewClientBuilder().WithScheme(newScheme()).WithObjects(ingress).Build()

	c := &Controller{Client: client, IngressNodeLabel: "node-role.kubernetes.io/ingress"}
	aliases, err := c.listAliases(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []string{"app1", "app2"}, aliases)
}

// --- listNodes tests ---

func TestListNodes_FiltersAndSortsByName(t *testing.T) {
	label := "node-role.kubernetes.io/ingress"
	client := fake.NewClientBuilder().WithScheme(newScheme()).WithObjects(
		makeNode("node-b", map[string]string{label: ""}, "2.2.2.2", "10.0.0.2"),
		makeNode("node-a", map[string]string{label: ""}, "1.1.1.1", "10.0.0.1"),
		makeNode("node-c", map[string]string{"other": "label"}, "3.3.3.3", "10.0.0.3"),
	).Build()

	c := &Controller{Client: client, IngressNodeLabel: label}
	nodes, err := c.listNodes(context.Background())
	require.NoError(t, err)

	require.Len(t, nodes, 2)
	assert.Equal(t, "node-a", nodes[0].Name)
	assert.Equal(t, "1.1.1.1", nodes[0].IP) // Prefers ExternalIP.
	assert.Equal(t, "node-b", nodes[1].Name)
	assert.Equal(t, "2.2.2.2", nodes[1].IP)
}

func TestListNodes_FallsBackToInternalIP(t *testing.T) {
	label := "node-role.kubernetes.io/ingress"
	client := fake.NewClientBuilder().WithScheme(newScheme()).WithObjects(
		makeNode("node-a", map[string]string{label: ""}, "", "10.0.0.1"),
	).Build()

	c := &Controller{Client: client, IngressNodeLabel: label}
	nodes, err := c.listNodes(context.Background())
	require.NoError(t, err)
	require.Len(t, nodes, 1)
	assert.Equal(t, "10.0.0.1", nodes[0].IP)
}

func TestListNodes_SkipsNodesWithoutIP(t *testing.T) {
	label := "node-role.kubernetes.io/ingress"
	client := fake.NewClientBuilder().WithScheme(newScheme()).WithObjects(
		makeNode("node-a", map[string]string{label: ""}, "", ""),
		makeNode("node-b", map[string]string{label: ""}, "1.1.1.1", ""),
	).Build()

	c := &Controller{Client: client, IngressNodeLabel: label}
	nodes, err := c.listNodes(context.Background())
	require.NoError(t, err)
	require.Len(t, nodes, 1)
	assert.Equal(t, "node-b", nodes[0].Name)
}

// --- getNodeIP tests ---

func TestGetNodeIP_PrefersExternal(t *testing.T) {
	node := makeNode("n", nil, "1.2.3.4", "10.0.0.1")
	ip, err := getNodeIP(node)
	require.NoError(t, err)
	assert.Equal(t, "1.2.3.4", ip)
}

func TestGetNodeIP_FallsBackToInternal(t *testing.T) {
	node := makeNode("n", nil, "", "10.0.0.1")
	ip, err := getNodeIP(node)
	require.NoError(t, err)
	assert.Equal(t, "10.0.0.1", ip)
}

func TestGetNodeIP_ErrorWhenNoIP(t *testing.T) {
	node := makeNode("n", nil, "", "")
	_, err := getNodeIP(node)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no ExternalIP or InternalIP")
}

// --- runOnce integration test ---

func TestRunOnce_BuildsCorrectAliasSet(t *testing.T) {
	label := "node-role.kubernetes.io/ingress"
	k8sClient := fake.NewClientBuilder().WithScheme(newScheme()).WithObjects(
		makeIngress("ing-1", "default", "app1.cern.ch"),
		makeIngress("ing-2", "default", "app2.cern.ch"),
		makeNode("node-b", map[string]string{label: ""}, "2.2.2.2", ""),
		makeNode("node-a", map[string]string{label: ""}, "1.1.1.1", ""),
	).Build()

	mock := &mockProvider{}
	c := &Controller{
		Client:           k8sClient,
		Provider:         mock,
		IngressNodeLabel: label,
	}

	err := c.runOnce(context.Background())
	require.NoError(t, err)

	require.NotNil(t, mock.lastDesired)
	assert.Equal(t, []string{"app1", "app2"}, mock.lastDesired.Aliases)
	require.Len(t, mock.lastDesired.Nodes, 2)
	assert.Equal(t, "node-a", mock.lastDesired.Nodes[0].Name) // Sorted.
	assert.Equal(t, "node-b", mock.lastDesired.Nodes[1].Name)
}

func TestRunOnce_PropagatesProviderError(t *testing.T) {
	label := "node-role.kubernetes.io/ingress"
	k8sClient := fake.NewClientBuilder().WithScheme(newScheme()).WithObjects(
		makeIngress("ing-1", "default", "app.cern.ch"),
		makeNode("node-a", map[string]string{label: ""}, "1.1.1.1", ""),
	).Build()

	mock := &mockProvider{syncErr: errors.New("sync failed")}
	c := &Controller{
		Client:           k8sClient,
		Provider:         mock,
		IngressNodeLabel: label,
	}

	err := c.runOnce(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "sync failed")
}

func TestRunOnce_EmptyCluster(t *testing.T) {
	k8sClient := fake.NewClientBuilder().WithScheme(newScheme()).Build()
	mock := &mockProvider{}
	c := &Controller{
		Client:           k8sClient,
		Provider:         mock,
		IngressNodeLabel: "node-role.kubernetes.io/ingress",
	}

	err := c.runOnce(context.Background())
	require.NoError(t, err)
	require.NotNil(t, mock.lastDesired)
	assert.Empty(t, mock.lastDesired.Aliases)
	assert.Empty(t, mock.lastDesired.Nodes)
}
