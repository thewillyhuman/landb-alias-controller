# LANDB Alias Controller

A Kubernetes controller that automatically synchronizes DNS aliases from Ingress
resources to OpenStack server metadata, enabling CERN's LANDB DNS system to
create the corresponding DNS records.

## How It Works

1. The controller watches all Ingress resources in the cluster.
2. It extracts `.cern.ch` hosts from Ingress specs (e.g., `myapp.cern.ch` becomes alias `myapp`).
3. It identifies ingress nodes via a configurable Kubernetes label.
4. It writes the aliases as `landb-alias` metadata on the corresponding OpenStack servers.
5. It writes optional node-level LANDB set metadata from Kubernetes node metadata.
6. CERN's LANDB system reads that metadata and creates DNS records pointing to the ingress nodes.

When multiple ingress nodes exist, load-balancing suffixes (`--load-0-`, `--load-1-`, etc.)
are appended automatically so that LANDB creates A records for DNS round-robin.

### Load-Balancing Suffix Assignment

Suffixes are assigned based on the **alphabetical order** of node names, ensuring
deterministic and stable assignment across reconciliation runs. For example, with
nodes `node-a`, `node-b`, and `node-c`, the suffixes are always `--load-0-`,
`--load-1-`, and `--load-2-` respectively.

Note: if a node is added or removed from the middle of the sorted list, all nodes
after it get their suffixes shifted (e.g., removing `node-b` causes `node-c` to
move from `--load-2-` to `--load-1-`). This triggers metadata updates on all
affected nodes but ensures the suffix range remains contiguous.

### When Are Aliases Added or Removed?

| Event | Action |
|-------|--------|
| Node labeled as ingress | Aliases are added to the node's OpenStack metadata |
| Node unlabeled as ingress | Aliases are removed from the node's OpenStack metadata |
| Ingress node becomes NotReady | Aliases are removed until the node recovers |
| Ingress node becomes Ready again | Aliases are re-added on the next reconciliation |
| Ingress resource created/updated | Aliases are synced across all ready ingress nodes |
| Ingress resource deleted | Stale aliases are removed from all ingress nodes |

### Landb Set Metadata

Nodes can declare one or more OpenStack `landb-set` metadata values with the
following annotation:

```yaml
metadata:
  annotations:
    landb.cern.ch/set: "MY-LANDB-SET,MY-OTHER-LANDB-SET"
```

For compatibility, a single value can also be declared as a label:

```yaml
metadata:
  labels:
    landb.cern.ch/set: "MY-LANDB-SET"
```

The controller reconciles this as node-level metadata for Ready nodes,
independently from ingress alias membership:

The value is parsed as a comma-separated list. Whitespace and empty entries are
removed, duplicate entries are ignored, and the normalized comma-separated value
is written to OpenStack metadata key `landb-set`. For multiple values, use the
annotation form because Kubernetes label values cannot contain commas.

| Node metadata | OpenStack action |
|---------------|------------------|
| Ready node with `landb.cern.ch/set: "MY-LANDB-SET,MY-OTHER-LANDB-SET"` annotation present | Create or update `landb-set=MY-LANDB-SET,MY-OTHER-LANDB-SET` |
| Ready node with single-value `landb.cern.ch/set: "MY-LANDB-SET"` label present | Create or update `landb-set=MY-LANDB-SET` |
| Annotation absent or empty | Remove stale `landb-set` metadata |
| Node is NotReady or has no Ready condition | Remove stale `landb-set` metadata |
| Node is not an ingress node | Still reconcile `landb-set`; only `landb-alias*` is tied to ingress status |


Any manually added `landb-set` metadata on OpenStack servers whose Kubernetes nodes are missing the corresponding label/annotation, or are not Ready, will be removed by the controller, as it treats Kubernetes as the source of truth for both aliases and sets.

## Prerequisites

- A Kubernetes cluster running on OpenStack (e.g., created with Magnum).
- Nodes serving ingress traffic labeled with `node-role.kubernetes.io/ingress`
  (or a custom label — see [Flags](#flags)).
- OpenStack credentials with permissions to read servers and update metadata via Nova.

## Configuration

### Authentication

The controller supports two authentication methods:

#### Option 1: Cloud-Config Secret (recommended for Magnum clusters)

On Magnum-created clusters, a `cloud-config` secret with OpenStack credentials already exists
in the `kube-system` namespace. The controller can read it directly, requiring no additional
credential configuration:

```bash
--cloud-config-secret=kube-system/cloud-config
```

The secret's `cloud.conf` key is read for either trust-based authentication (`user-id` +
`password` + `trust-id`) or application-credential authentication (`application-credential-id` +
`application-credential-secret`), depending on which fields are present. The controller's service
account needs RBAC permission to `get` the secret (see the Helm chart's `cloudConfig.enabled`
option which sets this up automatically).

#### Option 2: Environment Variables

Alternatively, provide credentials via environment variables:

| Variable             | Description                          | Example                              |
|----------------------|--------------------------------------|--------------------------------------|
| `OS_AUTH_URL`        | Keystone identity endpoint           | `https://keystone.cern.ch:5000/v3`   |
| `OS_USERNAME`        | OpenStack username                   | `svc-landb-ctrl`                     |
| `OS_PASSWORD`        | OpenStack password                   |                                      |
| `OS_PROJECT_NAME`    | OpenStack project / tenant name      | `my-project`                         |
| `OS_USER_DOMAIN_NAME`| OpenStack user domain                | `Default`                            |

### Flags

| Flag                      | Default                                            | Description                                           |
|---------------------------|----------------------------------------------------|-------------------------------------------------------|
| `--provider`              | `openstack`                                        | DNS provider (currently only `openstack`)              |
| `--ingress-node-labels`   | `node-role.kubernetes.io/ingress,role=ingress`      | Comma-separated label selectors for ingress nodes (OR). Use `key` for presence or `key=value` for exact match |
| `--cloud-config-secret`   |                                                    | Read credentials from a K8s secret (`namespace/name`)  |
| `--zap-log-level`         | `info`                                             | Log verbosity (`debug`, `info`, `error`)               |
| `--zap-devel`             | `false`                                            | Enable development-mode logging                        |

## Deployment

### Build the Image

```bash
docker build -t landb-alias-controller:latest .
```

### Example Kubernetes Manifest

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: landb-alias-controller
  namespace: kube-system
spec:
  replicas: 1
  selector:
    matchLabels:
      app: landb-alias-controller
  template:
    metadata:
      labels:
        app: landb-alias-controller
    spec:
      serviceAccountName: landb-alias-controller
      containers:
        - name: controller
          image: landb-alias-controller:latest
          args:
            - --zap-log-level=info
          env:
            - name: OS_AUTH_URL
              value: "https://keystone.cern.ch:5000/v3"
            - name: OS_USERNAME
              valueFrom:
                secretKeyRef:
                  name: openstack-credentials
                  key: username
            - name: OS_PASSWORD
              valueFrom:
                secretKeyRef:
                  name: openstack-credentials
                  key: password
            - name: OS_PROJECT_NAME
              value: "my-project"
            - name: OS_USER_DOMAIN_NAME
              value: "Default"
```

### RBAC

The controller needs permission to list and watch Ingress and Node resources:

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: landb-alias-controller
rules:
  - apiGroups: ["networking.k8s.io"]
    resources: ["ingresses"]
    verbs: ["get", "list", "watch"]
  - apiGroups: [""]
    resources: ["nodes"]
    verbs: ["get", "list", "watch"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: landb-alias-controller
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: landb-alias-controller
subjects:
  - kind: ServiceAccount
    name: landb-alias-controller
    namespace: kube-system
```

## Metrics

The controller exposes Prometheus metrics on the default controller-runtime metrics endpoint (`:8080/metrics`):

| Metric | Type | Description |
|--------|------|-------------|
| `landb_reconciliations_total` | Counter | Completed reconciliation cycles (label: `result`) |
| `landb_reconciliation_duration_seconds` | Histogram | Reconciliation cycle duration |
| `landb_aliases_desired_total` | Gauge | Unique desired aliases from Ingress resources |
| `landb_nodes_managed_total` | Gauge | Kubernetes nodes considered for alias or landb-set reconciliation |
| `landb_ingress_nodes_managed_total` | Gauge | Ready ingress nodes managed for `landb-alias*` metadata |
| `landb_alias_cleanup_nodes_total` | Gauge | Nodes checked for stale `landb-alias*` metadata |
| `landb_set_nodes_managed_total` | Gauge | Ready nodes declaring `landb-set` metadata |
| `landb_set_cleanup_nodes_total` | Gauge | Nodes checked for stale `landb-set` metadata, including NotReady nodes |
| `landb_openstack_api_calls_total` | Counter | OpenStack API calls (labels: `operation`, `result`) |
| `landb_openstack_api_duration_seconds` | Histogram | OpenStack API call latency (label: `operation`) |
