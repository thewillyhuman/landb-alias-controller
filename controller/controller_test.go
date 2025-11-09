package controller

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gitlab.cern.ch/gfacundo/landb-alias-controller/dns"
	"gitlab.cern.ch/gfacundo/landb-alias-controller/internal"
	"gitlab.cern.ch/gfacundo/landb-alias-controller/plan"
	"gitlab.cern.ch/gfacundo/landb-alias-controller/provider"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// mockProvider is a mock implementation of the provider.Provider interface.
type mockProvider struct {
	records      []*dns.Record
	reconcileErr error
	recordsErr   error
}

// Records returns the mock records.
func (m *mockProvider) Records() ([]*dns.Record, error) {
	if m.recordsErr != nil {
		return nil, m.recordsErr
	}
	return m.records, nil
}

// Reconcile is a mock implementation of the Reconcile method.
func (m *mockProvider) Reconcile(changes *plan.Changes) error {
	if m.reconcileErr != nil {
		return m.reconcileErr
	}
	return nil
}

// newTestController creates a new controller for testing.
func newTestController(provider provider.Provider, objs ...runtime.Object) *Controller {
	s := runtime.NewScheme()
	corev1.AddToScheme(s)
	networkingv1.AddToScheme(s)
	cl := fake.NewClientBuilder().WithScheme(s).WithRuntimeObjects(objs...).Build()
	return &Controller{
		Client:           cl,
		DnsProvider:      provider,
		IngressNodeLabel: internal.IngressNodeLabelDefault,
		Interval:         1 * time.Minute,
	}
}

// TestReconcile tests the Reconcile method of the controller.
func TestReconcile(t *testing.T) {
	// Create a mock provider.
	provider := &mockProvider{
		records: []*dns.Record{
			{
				Name:   "foo",
				Type:   "A",
				Values: []string{"1.1.1.1"},
			},
		},
	}

	// Create a fake Kubernetes client.
	ingress := &networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo-ingress",
			Namespace: "default",
		},
		Spec: networkingv1.IngressSpec{
			Rules: []networkingv1.IngressRule{
				{
					Host: "foo.cern.ch",
				},
			},
		},
	}
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "node1",
			Labels: map[string]string{
				internal.IngressNodeLabelDefault: internal.TrueString,
			},
		},
		Status: corev1.NodeStatus{
			Addresses: []corev1.NodeAddress{
				{
					Type:    corev1.NodeExternalIP,
					Address: "1.1.1.1",
				},
			},
		},
	}

	// Create a new controller.
	c := newTestController(provider, ingress, node)

	// Call the Reconcile method.
	_, err := c.Reconcile(context.Background(), newTestRequest("foo-ingress", "default"))
	require.NoError(t, err)
}

// TestReconcile_NoIngresses tests the Reconcile method of the controller when no ingresses are found.
func TestReconcile_NoIngresses(t *testing.T) {
	// Create a mock provider.
	provider := &mockProvider{
		records: []*dns.Record{
			{
				Name:   "foo",
				Type:   "A",
				Values: []string{"1.1.1.1"},
			},
		},
	}

	// Create a fake Kubernetes client.
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "node1",
			Labels: map[string]string{
				internal.IngressNodeLabelDefault: internal.TrueString,
			},
		},
		Status: corev1.NodeStatus{
			Addresses: []corev1.NodeAddress{
				{
					Type:    corev1.NodeExternalIP,
					Address: "1.1.1.1",
				},
			},
		},
	}

	// Create a new controller.
	c := newTestController(provider, node)

	// Call the Reconcile method.
	_, err := c.Reconcile(context.Background(), newTestRequest("foo-ingress", "default"))
	require.NoError(t, err)
}

// TestReconcile_NoNodes tests the Reconcile method of the controller when no nodes are found.
func TestReconcile_NoNodes(t *testing.T) {
	// Create a mock provider.
	provider := &mockProvider{
		records: []*dns.Record{
			{
				Name:   "foo",
				Type:   "A",
				Values: []string{"1.1.1.1"},
			},
		},
	}

	// Create a fake Kubernetes client.
	ingress := &networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo-ingress",
			Namespace: "default",
		},
		Spec: networkingv1.IngressSpec{
			Rules: []networkingv1.IngressRule{
				{
					Host: "foo.cern.ch",
				},
			},
		},
	}

	// Create a new controller.
	c := newTestController(provider, ingress)

	// Call the Reconcile method.
	_, err := c.Reconcile(context.Background(), newTestRequest("foo-ingress", "default"))
	require.NoError(t, err)
}

// TestReconcile_ProviderGetRecordsError tests the Reconcile method of the controller when the provider returns an error when getting records.
func TestReconcile_ProviderGetRecordsError(t *testing.T) {
	// Create a mock provider.
	provider := &mockProvider{
		recordsErr: assert.AnError,
	}

	// Create a fake Kubernetes client.
	ingress := &networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo-ingress",
			Namespace: "default",
		},
		Spec: networkingv1.IngressSpec{
			Rules: []networkingv1.IngressRule{
				{
					Host: "foo.cern.ch",
				},
			},
		},
	}
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "node1",
			Labels: map[string]string{
				internal.IngressNodeLabelDefault: internal.TrueString,
			},
		},
		Status: corev1.NodeStatus{
			Addresses: []corev1.NodeAddress{
				{
					Type:    corev1.NodeExternalIP,
					Address: "1.1.1.1",
				},
			},
		},
	}

	// Create a new controller.
	c := newTestController(provider, ingress, node)

	// Call the Reconcile method.
	_, err := c.Reconcile(context.Background(), newTestRequest("foo-ingress", "default"))
	require.Error(t, err)
}

// TestReconcile_ProviderReconcileError tests the Reconcile method of the controller when the provider returns an error when reconciling.
func TestReconcile_ProviderReconcileError(t *testing.T) {
	// Create a mock provider.
	provider := &mockProvider{
		records:      []*dns.Record{},
		reconcileErr: assert.AnError,
	}

	// Create a fake Kubernetes client.
	ingress := &networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo-ingress",
			Namespace: "default",
		},
		Spec: networkingv1.IngressSpec{
			Rules: []networkingv1.IngressRule{
				{
					Host: "foo.cern.ch",
				},
			},
		},
	}
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "node1",
			Labels: map[string]string{
				internal.IngressNodeLabelDefault: internal.TrueString,
			},
		},
		Status: corev1.NodeStatus{
			Addresses: []corev1.NodeAddress{
				{
					Type:    corev1.NodeExternalIP,
					Address: "1.1.1.1",
				},
			},
		},
	}

	// Create a new controller.
	c := newTestController(provider, ingress, node)

	// Call the Reconcile method.
	_, err := c.Reconcile(context.Background(), newTestRequest("foo-ingress", "default"))
	require.Error(t, err)
}

// newTestRequest creates a new reconcile request for testing.
func newTestRequest(name, namespace string) reconcile.Request {
	return reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      name,
			Namespace: namespace,
		},
	}
}
