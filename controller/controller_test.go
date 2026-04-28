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

func withReadyCondition(node *v1.Node, status v1.ConditionStatus) *v1.Node {
	node.Status.Conditions = append(node.Status.Conditions, v1.NodeCondition{
		Type:   v1.NodeReady,
		Status: status,
	})
	return node
}

// --- isNodeReady tests ---

func TestIsNodeReady_True(t *testing.T) {
	node := withReadyCondition(makeNode("n", nil, "1.1.1.1", ""), v1.ConditionTrue)
	assert.True(t, isNodeReady(node))
}

func TestIsNodeReady_False(t *testing.T) {
	node := withReadyCondition(makeNode("n", nil, "1.1.1.1", ""), v1.ConditionFalse)
	assert.False(t, isNodeReady(node))
}

func TestIsNodeReady_Unknown(t *testing.T) {
	node := withReadyCondition(makeNode("n", nil, "1.1.1.1", ""), v1.ConditionUnknown)
	assert.False(t, isNodeReady(node))
}

func TestIsNodeReady_NoCondition(t *testing.T) {
	node := makeNode("n", nil, "1.1.1.1", "")
	assert.False(t, isNodeReady(node))
}

// --- listAliases tests ---

func TestListAliases_ExtractsCernHosts(t *testing.T) {
	client := fake.NewClientBuilder().WithScheme(newScheme()).WithObjects(
		makeIngress("ing-1", "default", "app1.cern.ch"),
		makeIngress("ing-2", "default", "app2.cern.ch"),
		makeIngress("ing-3", "other", "external.example.com"),
	).Build()

	c := &Controller{Client: client, IngressNodeLabels: []LabelSelector{{Key: "node-role.kubernetes.io/ingress"}}}
	aliases, err := c.listAliases(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []string{"app1", "app2"}, aliases)
}

func TestListAliases_DeduplicatesHosts(t *testing.T) {
	client := fake.NewClientBuilder().WithScheme(newScheme()).WithObjects(
		makeIngress("ing-1", "default", "app.cern.ch"),
		makeIngress("ing-2", "other", "app.cern.ch"),
	).Build()

	c := &Controller{Client: client, IngressNodeLabels: []LabelSelector{{Key: "node-role.kubernetes.io/ingress"}}}
	aliases, err := c.listAliases(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []string{"app"}, aliases)
}

func TestListAliases_NoIngresses(t *testing.T) {
	client := fake.NewClientBuilder().WithScheme(newScheme()).Build()

	c := &Controller{Client: client, IngressNodeLabels: []LabelSelector{{Key: "node-role.kubernetes.io/ingress"}}}
	aliases, err := c.listAliases(context.Background())
	require.NoError(t, err)
	assert.Empty(t, aliases)
}

func TestListAliases_SkipsNonCernHosts(t *testing.T) {
	client := fake.NewClientBuilder().WithScheme(newScheme()).WithObjects(
		makeIngress("ing-1", "default", "app.example.com"),
		makeIngress("ing-2", "default", "app.cern.ch"),
	).Build()

	c := &Controller{Client: client, IngressNodeLabels: []LabelSelector{{Key: "node-role.kubernetes.io/ingress"}}}
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

	c := &Controller{Client: client, IngressNodeLabels: []LabelSelector{{Key: "node-role.kubernetes.io/ingress"}}}
	aliases, err := c.listAliases(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []string{"app1", "app2"}, aliases)
}

// --- listNodes tests ---

func TestListNodes_FiltersAndSortsByName(t *testing.T) {
	label := "node-role.kubernetes.io/ingress"
	client := fake.NewClientBuilder().WithScheme(newScheme()).WithObjects(
		withReadyCondition(makeNode("node-b", map[string]string{label: ""}, "2.2.2.2", "10.0.0.2"), v1.ConditionTrue),
		withReadyCondition(makeNode("node-a", map[string]string{label: ""}, "1.1.1.1", "10.0.0.1"), v1.ConditionTrue),
		withReadyCondition(makeNode("node-c", map[string]string{"other": "label"}, "3.3.3.3", "10.0.0.3"), v1.ConditionTrue),
	).Build()

	c := &Controller{Client: client, IngressNodeLabels: []LabelSelector{{Key: label}}}
	nodes, err := c.listNodes(context.Background())
	require.NoError(t, err)

	require.Len(t, nodes, 2)
	assert.Equal(t, "node-a", nodes[0].Name)
	assert.Equal(t, "node-b", nodes[1].Name)
}

func TestListNodes_IncludesNodesWithoutIP(t *testing.T) {
	label := "node-role.kubernetes.io/ingress"
	client := fake.NewClientBuilder().WithScheme(newScheme()).WithObjects(
		withReadyCondition(makeNode("node-a", map[string]string{label: ""}, "", ""), v1.ConditionTrue),
	).Build()

	c := &Controller{Client: client, IngressNodeLabels: []LabelSelector{{Key: label}}}
	nodes, err := c.listNodes(context.Background())
	require.NoError(t, err)
	require.Len(t, nodes, 1)
	assert.Equal(t, "node-a", nodes[0].Name)
}

// --- runOnce integration test ---

func TestRunOnce_BuildsCorrectAliasSet(t *testing.T) {
	label := "node-role.kubernetes.io/ingress"
	k8sClient := fake.NewClientBuilder().WithScheme(newScheme()).WithObjects(
		makeIngress("ing-1", "default", "app1.cern.ch"),
		makeIngress("ing-2", "default", "app2.cern.ch"),
		withReadyCondition(makeNode("node-b", map[string]string{label: ""}, "2.2.2.2", ""), v1.ConditionTrue),
		withReadyCondition(makeNode("node-a", map[string]string{label: ""}, "1.1.1.1", ""), v1.ConditionTrue),
	).Build()

	mock := &mockProvider{}
	c := &Controller{
		Client:            k8sClient,
		Provider:          mock,
		IngressNodeLabels: []LabelSelector{{Key: label}},
	}

	err := c.runOnce(context.Background())
	require.NoError(t, err)

	require.NotNil(t, mock.lastDesired)
	assert.Equal(t, []string{"app1", "app2"}, mock.lastDesired.Aliases)
	require.Len(t, mock.lastDesired.Nodes, 2)
	assert.Equal(t, "node-a", mock.lastDesired.Nodes[0].Name) // Sorted.
	assert.Equal(t, "node-b", mock.lastDesired.Nodes[1].Name)
	assert.Empty(t, mock.lastDesired.StaleNodes)
}

func TestRunOnce_PropagatesProviderError(t *testing.T) {
	label := "node-role.kubernetes.io/ingress"
	k8sClient := fake.NewClientBuilder().WithScheme(newScheme()).WithObjects(
		makeIngress("ing-1", "default", "app.cern.ch"),
		withReadyCondition(makeNode("node-a", map[string]string{label: ""}, "1.1.1.1", ""), v1.ConditionTrue),
	).Build()

	mock := &mockProvider{syncErr: errors.New("sync failed")}
	c := &Controller{
		Client:            k8sClient,
		Provider:          mock,
		IngressNodeLabels: []LabelSelector{{Key: label}},
	}

	err := c.runOnce(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "sync failed")
}

func TestRunOnce_IdentifiesStaleNodes(t *testing.T) {
	label := "node-role.kubernetes.io/ingress"
	k8sClient := fake.NewClientBuilder().WithScheme(newScheme()).WithObjects(
		makeIngress("ing-1", "default", "app.cern.ch"),
		withReadyCondition(makeNode("node-a", map[string]string{label: ""}, "1.1.1.1", ""), v1.ConditionTrue),
		withReadyCondition(makeNode("node-b", map[string]string{label: ""}, "2.2.2.2", ""), v1.ConditionTrue),
		withReadyCondition(makeNode("node-c", map[string]string{"other": "label"}, "3.3.3.3", ""), v1.ConditionTrue),
		withReadyCondition(makeNode("node-d", nil, "4.4.4.4", ""), v1.ConditionTrue),
	).Build()

	mock := &mockProvider{}
	c := &Controller{
		Client:            k8sClient,
		Provider:          mock,
		IngressNodeLabels: []LabelSelector{{Key: label}},
	}

	err := c.runOnce(context.Background())
	require.NoError(t, err)
	require.NotNil(t, mock.lastDesired)

	require.Len(t, mock.lastDesired.Nodes, 2)
	assert.Equal(t, "node-a", mock.lastDesired.Nodes[0].Name)
	assert.Equal(t, "node-b", mock.lastDesired.Nodes[1].Name)

	require.Len(t, mock.lastDesired.StaleNodes, 2)
	assert.Equal(t, "node-c", mock.lastDesired.StaleNodes[0].Name)
	assert.Equal(t, "node-d", mock.lastDesired.StaleNodes[1].Name)
}

func TestRunOnce_AllNodesIngress_NoStaleNodes(t *testing.T) {
	label := "node-role.kubernetes.io/ingress"
	k8sClient := fake.NewClientBuilder().WithScheme(newScheme()).WithObjects(
		makeIngress("ing-1", "default", "app.cern.ch"),
		withReadyCondition(makeNode("node-a", map[string]string{label: ""}, "1.1.1.1", ""), v1.ConditionTrue),
		withReadyCondition(makeNode("node-b", map[string]string{label: ""}, "2.2.2.2", ""), v1.ConditionTrue),
	).Build()

	mock := &mockProvider{}
	c := &Controller{
		Client:            k8sClient,
		Provider:          mock,
		IngressNodeLabels: []LabelSelector{{Key: label}},
	}

	err := c.runOnce(context.Background())
	require.NoError(t, err)
	require.NotNil(t, mock.lastDesired)
	require.Len(t, mock.lastDesired.Nodes, 2)
	assert.Empty(t, mock.lastDesired.StaleNodes)
}

func TestRunOnce_EmptyCluster(t *testing.T) {
	k8sClient := fake.NewClientBuilder().WithScheme(newScheme()).Build()
	mock := &mockProvider{}
	c := &Controller{
		Client:            k8sClient,
		Provider:          mock,
		IngressNodeLabels: []LabelSelector{{Key: "node-role.kubernetes.io/ingress"}},
	}

	err := c.runOnce(context.Background())
	require.NoError(t, err)
	require.NotNil(t, mock.lastDesired)
	assert.Empty(t, mock.lastDesired.Aliases)
	assert.Empty(t, mock.lastDesired.Nodes)
}

// --- NotReady node tests ---

func TestListNodes_ExcludesNotReadyNodes(t *testing.T) {
	label := "node-role.kubernetes.io/ingress"
	client := fake.NewClientBuilder().WithScheme(newScheme()).WithObjects(
		withReadyCondition(makeNode("node-a", map[string]string{label: ""}, "1.1.1.1", ""), v1.ConditionTrue),
		withReadyCondition(makeNode("node-b", map[string]string{label: ""}, "2.2.2.2", ""), v1.ConditionFalse),
		makeNode("node-c", map[string]string{label: ""}, "3.3.3.3", ""), // No Ready condition.
	).Build()

	c := &Controller{Client: client, IngressNodeLabels: []LabelSelector{{Key: label}}}
	nodes, err := c.listNodes(context.Background())
	require.NoError(t, err)
	require.Len(t, nodes, 1)
	assert.Equal(t, "node-a", nodes[0].Name)
}

func TestRunOnce_NotReadyIngressNodeIsStale(t *testing.T) {
	label := "node-role.kubernetes.io/ingress"
	k8sClient := fake.NewClientBuilder().WithScheme(newScheme()).WithObjects(
		makeIngress("ing-1", "default", "app.cern.ch"),
		withReadyCondition(makeNode("node-a", map[string]string{label: ""}, "1.1.1.1", ""), v1.ConditionTrue),
		withReadyCondition(makeNode("node-b", map[string]string{label: ""}, "2.2.2.2", ""), v1.ConditionFalse),
		withReadyCondition(makeNode("node-c", map[string]string{"other": "label"}, "3.3.3.3", ""), v1.ConditionTrue),
	).Build()

	mock := &mockProvider{}
	c := &Controller{
		Client:            k8sClient,
		Provider:          mock,
		IngressNodeLabels: []LabelSelector{{Key: label}},
	}

	err := c.runOnce(context.Background())
	require.NoError(t, err)
	require.NotNil(t, mock.lastDesired)

	// Only node-a is Ready and has the ingress label.
	require.Len(t, mock.lastDesired.Nodes, 1)
	assert.Equal(t, "node-a", mock.lastDesired.Nodes[0].Name)

	// node-b (ingress but NotReady) and node-c (non-ingress) are stale.
	require.Len(t, mock.lastDesired.StaleNodes, 2)
	assert.Equal(t, "node-b", mock.lastDesired.StaleNodes[0].Name)
	assert.Equal(t, "node-c", mock.lastDesired.StaleNodes[1].Name)
}

// --- Multi-label tests ---

func TestListNodes_MultipleLabels_ORLogic(t *testing.T) {
	ingress := "ingress"
	client := fake.NewClientBuilder().WithScheme(newScheme()).WithObjects(
		withReadyCondition(makeNode("node-a", map[string]string{"node-role.kubernetes.io/ingress": ""}, "1.1.1.1", ""), v1.ConditionTrue),
		withReadyCondition(makeNode("node-b", map[string]string{"role": "ingress"}, "2.2.2.2", ""), v1.ConditionTrue),
		withReadyCondition(makeNode("node-c", map[string]string{"other": "label"}, "3.3.3.3", ""), v1.ConditionTrue),
	).Build()

	c := &Controller{
		Client: client,
		IngressNodeLabels: []LabelSelector{
			{Key: "node-role.kubernetes.io/ingress"},
			{Key: "role", Value: &ingress},
		},
	}
	nodes, err := c.listNodes(context.Background())
	require.NoError(t, err)
	require.Len(t, nodes, 2)
	assert.Equal(t, "node-a", nodes[0].Name)
	assert.Equal(t, "node-b", nodes[1].Name)
}

func TestListNodes_MultipleLabels_DeduplicatesNodes(t *testing.T) {
	ingress := "ingress"
	// A node with both labels should appear only once.
	client := fake.NewClientBuilder().WithScheme(newScheme()).WithObjects(
		withReadyCondition(makeNode("node-a", map[string]string{
			"node-role.kubernetes.io/ingress": "",
			"role":                            "ingress",
		}, "1.1.1.1", ""), v1.ConditionTrue),
	).Build()

	c := &Controller{
		Client: client,
		IngressNodeLabels: []LabelSelector{
			{Key: "node-role.kubernetes.io/ingress"},
			{Key: "role", Value: &ingress},
		},
	}
	nodes, err := c.listNodes(context.Background())
	require.NoError(t, err)
	require.Len(t, nodes, 1)
	assert.Equal(t, "node-a", nodes[0].Name)
}

func TestListNodes_KeyValueLabel_IgnoresWrongValue(t *testing.T) {
	ingress := "ingress"
	client := fake.NewClientBuilder().WithScheme(newScheme()).WithObjects(
		withReadyCondition(makeNode("node-a", map[string]string{"role": "ingress"}, "1.1.1.1", ""), v1.ConditionTrue),
		withReadyCondition(makeNode("node-b", map[string]string{"role": "worker"}, "2.2.2.2", ""), v1.ConditionTrue),
	).Build()

	c := &Controller{
		Client:            client,
		IngressNodeLabels: []LabelSelector{{Key: "role", Value: &ingress}},
	}
	nodes, err := c.listNodes(context.Background())
	require.NoError(t, err)
	require.Len(t, nodes, 1)
	assert.Equal(t, "node-a", nodes[0].Name)
}

func TestRunOnce_MultipleLabels_AllIngress(t *testing.T) {
	ingress := "ingress"
	k8sClient := fake.NewClientBuilder().WithScheme(newScheme()).WithObjects(
		makeIngress("ing-1", "default", "app.cern.ch"),
		withReadyCondition(makeNode("node-a", map[string]string{"node-role.kubernetes.io/ingress": ""}, "1.1.1.1", ""), v1.ConditionTrue),
		withReadyCondition(makeNode("node-b", map[string]string{"role": "ingress"}, "2.2.2.2", ""), v1.ConditionTrue),
		withReadyCondition(makeNode("node-c", map[string]string{"other": "label"}, "3.3.3.3", ""), v1.ConditionTrue),
	).Build()

	mock := &mockProvider{}
	c := &Controller{
		Client:   k8sClient,
		Provider: mock,
		IngressNodeLabels: []LabelSelector{
			{Key: "node-role.kubernetes.io/ingress"},
			{Key: "role", Value: &ingress},
		},
	}

	err := c.runOnce(context.Background())
	require.NoError(t, err)
	require.NotNil(t, mock.lastDesired)

	require.Len(t, mock.lastDesired.Nodes, 2)
	assert.Equal(t, "node-a", mock.lastDesired.Nodes[0].Name)
	assert.Equal(t, "node-b", mock.lastDesired.Nodes[1].Name)

	// node-c is the only stale node.
	require.Len(t, mock.lastDesired.StaleNodes, 1)
	assert.Equal(t, "node-c", mock.lastDesired.StaleNodes[0].Name)
}

func TestParseLabelSelector_KeyOnly(t *testing.T) {
	sel := ParseLabelSelector("node-role.kubernetes.io/ingress")
	assert.Equal(t, "node-role.kubernetes.io/ingress", sel.Key)
	assert.Nil(t, sel.Value)
	assert.Equal(t, "node-role.kubernetes.io/ingress", sel.String())
}

func TestParseLabelSelector_KeyValue(t *testing.T) {
	sel := ParseLabelSelector("role=ingress")
	assert.Equal(t, "role", sel.Key)
	require.NotNil(t, sel.Value)
	assert.Equal(t, "ingress", *sel.Value)
	assert.Equal(t, "role=ingress", sel.String())
}

func TestLabelSelector_Matches(t *testing.T) {
	ingress := "ingress"

	// Key-only selector matches any value.
	sel := LabelSelector{Key: "role"}
	assert.True(t, sel.Matches(map[string]string{"role": "ingress"}))
	assert.True(t, sel.Matches(map[string]string{"role": "worker"}))
	assert.False(t, sel.Matches(map[string]string{"other": "value"}))

	// Key=value selector matches only exact value.
	sel = LabelSelector{Key: "role", Value: &ingress}
	assert.True(t, sel.Matches(map[string]string{"role": "ingress"}))
	assert.False(t, sel.Matches(map[string]string{"role": "worker"}))
	assert.False(t, sel.Matches(map[string]string{"other": "value"}))
}
