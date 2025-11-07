package main

import (
	"crypto/rand"
	"encoding/hex"
	"flag"
	os "os"

	"gitlab.cern.ch/gfacundo/landb-alias-controller/controller"
	"gitlab.cern.ch/gfacundo/landb-alias-controller/provider/openstack"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	_ "k8s.io/client-go/plugin/pkg/client/auth/gcp"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

const (
	// openstackDomainName is the default OpenStack domain name.
	openstackDomainName = "Default"
)

var (
	// scheme is the Kubernetes scheme.
	scheme = runtime.NewScheme()
	// setupLog is the logger for the setup.
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
}

// main is the main entry point of the application.
func main() {
	if err := Run(); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}

// Run is the main logic of the application.
func Run() error {
	var metricsAddr string
	var enableLeaderElection bool
	var probeAddr string
	var openstackIdentityEndpoint string
	var openstackUsername string
	var openstackPassword string
	var openstackTenantName string

	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "The address the metric endpoint binds to.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")
	flag.StringVar(&openstackIdentityEndpoint, "openstack-identity-endpoint", os.Getenv("OS_AUTH_URL"), "OpenStack identity endpoint")
	flag.StringVar(&openstackUsername, "openstack-username", os.Getenv("OS_USERNAME"), "OpenStack username")
	flag.StringVar(&openstackPassword, "openstack-password", os.Getenv("OS_PASSWORD"), "OpenStack password")
	flag.StringVar(&openstackTenantName, "openstack-tenant-name", os.Getenv("OS_PROJECT_NAME"), "OpenStack tenant name")

	configureLogging()
	flag.Parse()

	leaderElectionID, err := randomHex(8)
	if err != nil {
		return err
	}

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		Metrics:                server.Options{BindAddress: metricsAddr},
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       leaderElectionID,
	})
	if err != nil {
		return err
	}

	if err := addHealthChecks(mgr); err != nil {
		return err
	}

	openstackClient, err := newOpenstackClient(openstackIdentityEndpoint, openstackUsername, openstackPassword, openstackTenantName)
	if err != nil {
		return err
	}

	if err := setupController(mgr, openstackClient); err != nil {
		return err
	}

	setupLog.Info("starting manager")
	return mgr.Start(ctrl.SetupSignalHandler())
}

// configureLogging configures the logging for the application.
func configureLogging() {
	opts := zap.Options{
		Development: true,
	}
	opts.BindFlags(flag.CommandLine)

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))
}

// addHealthChecks adds health checks to the manager.
func addHealthChecks(mgr ctrl.Manager) error {
	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		return err
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		return err
	}
	return nil
}

// newOpenstackClient creates a new OpenStack client.
func newOpenstackClient(identityEndpoint, username, password, tenantName string) (*openstack.Client, error) {
	return openstack.NewClient(openstack.ClientOpts{
		IdentityEndpoint: identityEndpoint,
		Username:         username,
		Password:         password,
		TenantName:       tenantName,
		DomainName:       openstackDomainName,
	})
}

// setupController sets up the controller with the manager.
func setupController(mgr ctrl.Manager, openstackClient *openstack.Client) error {
	return builder.ControllerManagedBy(mgr). // Create the ControllerManagedBy
							For(&networkingv1.Ingress{}). // ReplicaSet is the Application API
							Complete(&controller.Controller{
			Client:          mgr.GetClient(),
			OpenstackClient: openstackClient,
		})
}

// randomHex generates a random hex string of the given length.
func randomHex(n int) (string, error) {
	bytes := make([]byte, n)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}
