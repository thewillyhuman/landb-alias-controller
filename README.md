# Kubernetes LanDB Alias Controller

The `kubernetes-alias-controller` is a Kubernetes controller that automatically manages DNS aliases based on Kubernetes Ingress resources. It monitors Ingress objects in a Kubernetes cluster and ensures that the corresponding DNS records are correctly configured in the configured DNS provider.

[[_TOC_]]

## Overview

This controller is designed to bridge the gap between Kubernetes networking and external DNS providers. It simplifies the management of DNS aliases by automating the process of updating DNS records based on Kubernetes Ingress resources. The controller watches for changes to Ingress resources and reconciles the desired state with the actual state of the DNS provider.

## Features

- **Automatic Alias Management:** Automatically creates, updates, and deletes LANDB aliases based on Kubernetes Ingress hosts.
- **OpenStack Integration:** Communicates with the OpenStack API to manage server metadata.
- **Resilient and Robust:** Includes retry logic with re-authentication to handle transient API errors.
- **Configurable:** Provides command-line flags and environment variables for flexible configuration.
- **Health Checks:** Implements health and readiness probes for monitoring.

## Architecture

The controller is composed of several key packages:

- **`main.go`**: The main entry point for the application. It initializes the controller manager, sets up health checks, and configures the OpenStack client.
- **`controller/`**: Contains the core reconciliation logic. The `Controller` struct implements the `Reconcile` method, which is triggered by changes to Ingress resources.
- **`provider/openstack/`**: Implements the client for interacting with the OpenStack API. It handles authentication, and server metadata operations.
- **`dns/`**: Defines a generic, provider-agnostic `Record` struct for representing DNS records.
- **`plan/`**: Implements the logic for creating a reconciliation plan. It compares the current state of OpenStack metadata with the desired state and calculates the necessary changes.
- **`internal/`**: Contains internal packages used by the controller.
- **`internal/utils/`**: Contains utility functions used by the controller.

## Getting Started

### Prerequisites

- A running Kubernetes cluster.
- `kubectl` configured to communicate with the cluster.
- Go 1.16+ installed.
- Access to an OpenStack environment with credentials.

### Installation

1.  **Clone the repository:**

    ```bash
    git clone https://gitlab.cern.ch/gfacundo/landb-alias-controller.git
    cd landb-alias-controller
    ```

2.  **Build the controller:**

    ```bash
    go build -o landb-alias-controller main.go
    ```

3.  **Run the controller:**

    The controller can be run as a standalone binary. It requires provider-specific credentials to be provided as environment variables.

    For example, to run the controller with the OpenStack provider:

    ```bash
    export OS_AUTH_URL=<your_auth_url>
    export OS_USERNAME=<your_username>
    export OS_PASSWORD=<your_password>
    export OS_PROJECT_NAME=<your_project_name>
    export OS_USER_DOMAIN_NAME=<your_user_domain_name>

    ./landb-alias-controller
    ```

## Configuration

The controller can be configured using the following command-line flags:

| Flag | Description | Default |
| --- | --- | --- |
| `metrics-bind-address` | The address the metric endpoint binds to. | `:8080` |
| `health-probe-bind-address` | The address the probe endpoint binds to. | `:8081` |
| `leader-elect` | Enable leader election for controller manager. | `false` |
| `provider` | The DNS provider to use (e.g., 'openstack'). | `openstack` |
| `ingress-node-label` | The label to use for selecting ingress nodes. | `node-role.kubernetes.io/ingress` |
| `log-level` | The log level to use (e.g., 'debug', 'info', 'warn', 'error'). | `info` |

### Provider Configuration

The `landb-alias-controller` supports multiple DNS providers. The provider is selected using the `--provider` flag. Each provider may have its own configuration options, which are typically provided via environment variables.

#### OpenStack Provider

The OpenStack provider is the default provider. It is configured using the standard OpenStack environment variables:

- `OS_AUTH_URL`: The OpenStack identity endpoint.
- `OS_USERNAME`: The OpenStack username.
- `OS_PASSWORD`: The OpenStack password.
- `OS_PROJECT_NAME`: The OpenStack project name.
- `OS_USER_DOMAIN_NAME`: The OpenStack user domain name.

## Usage

The controller will watch for Ingress resources in the Kubernetes cluster. When an Ingress is created or updated, the controller will:

1.  Extract the hostnames from the Ingress rules.
2.  For each hostname ending in `.cern.ch`, it will generate a corresponding LANDB alias.
3.  It will then list the nodes with the label specified by the `--ingress-node-label` flag (default: `node-role.kubernetes.io/ingress=true`).
4.  For each of these nodes, it will update the OpenStack server metadata to include the LANDB aliases.

## Development

### Running the tests

To run the tests, use the following command:

```bash
go test ./...
```

## Contributing


Contributions are welcome! Please feel free to submit a pull request or open an issue.

## License

This project is licensed under the Apache 2.0 License. See the [LICENSE](LICENSE) file for details.