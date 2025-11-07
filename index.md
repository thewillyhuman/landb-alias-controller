# landb-alias-controller

The `landb-alias-controller` is a Kubernetes controller that automatically manages LANDB aliases for OpenStack instances based on Kubernetes Ingress resources. It monitors Ingress objects in a Kubernetes cluster and ensures that the corresponding OpenStack instances have the correct LANDB alias metadata.

[[_TOC_]]

## Overview

This controller is designed to bridge the gap between Kubernetes networking and OpenStack infrastructure. It simplifies the management of DNS aliases in CERN's LANDB system by automating the process of updating OpenStack server metadata. The controller watches for changes to Ingress resources and reconciles the desired state with the actual state of the OpenStack instances.

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

    The controller can be run as a standalone binary. It requires OpenStack credentials to be provided either as command-line flags or environment variables.

    ```bash
    ./landb-alias-controller \
        --openstack-identity-endpoint <OS_AUTH_URL> \
        --openstack-username <OS_USERNAME> \
        --openstack-password <OS_PASSWORD> \
        --openstack-tenant-name <OS_PROJECT_NAME>
    ```

    Alternatively, you can use environment variables:

    ```bash
    export OS_AUTH_URL=<your_auth_url>
    export OS_USERNAME=<your_username>
    export OS_PASSWORD=<your_password>
    export OS_PROJECT_NAME=<your_project_name>

    ./landb-alias-controller
    ```

## Configuration

The controller can be configured using the following command-line flags:

| Flag                            | Environment Variable  | Description                                                                                             | Default |
| ------------------------------- | --------------------- | ------------------------------------------------------------------------------------------------------- | ------- |
| `metrics-bind-address`          | -                     | The address the metric endpoint binds to.                                                               | `:8080` |
| `health-probe-bind-address`     | -                     | The address the probe endpoint binds to.                                                                | `:8081` |
| `leader-elect`                  | -                     | Enable leader election for controller manager.                                                          | `false` |
| `openstack-identity-endpoint`   | `OS_AUTH_URL`         | OpenStack identity endpoint.                                                                            | -       |
| `openstack-username`            | `OS_USERNAME`         | OpenStack username.                                                                                     | -       |
| `openstack-password`            | `OS_PASSWORD`         | OpenStack password.                                                                                     | -       |
| `openstack-tenant-name`         | `OS_PROJECT_NAME`     | OpenStack tenant name.                                                                                  | -       |

## Usage

The controller will watch for Ingress resources in the Kubernetes cluster. When an Ingress is created or updated, the controller will:

1.  Extract the hostnames from the Ingress rules.
2.  For each hostname ending in `.cern.ch`, it will generate a corresponding LANDB alias.
3.  It will then list the nodes with the `node-role.kubernetes.io/ingress=true` label.
4.  For each of these nodes, it will update the OpenStack server metadata to include the LANDB aliases.

## Contributing

Contributions are welcome! Please feel free to submit a pull request or open an issue.

## License

This project is licensed under the Apache 2.0 License. See the [LICENSE](LICENSE) file for details.