# LANDB Alias Specification

This document describes how DNS aliases work at CERN through OpenStack server
metadata (LANDB), and the conventions this controller relies on.

## Background

At CERN, Kubernetes clusters are provisioned with OpenStack Magnum. This creates
OpenStack servers — either bare-metal (Ironic) or virtual machines (Nova).

When an Ingress or Gateway defines a DNS name (e.g., `test.cern.ch`), the DNS
record must be configured through LANDB by setting metadata on the OpenStack
server that should receive the traffic.

## Server Aliases (`landb-alias`)

Aliases are set as OpenStack server properties using the `landb-alias` key.

### Basic Usage

```bash
openstack server set --property landb-alias="alias1,alias2,alias3" <server>
```

> **Note:** This command **overwrites** the existing alias list. To append a new
> alias, first read the current value with `openstack server show` and include
> all existing aliases in the update.

### Multiple Network Interfaces

Use `;` to separate interface mappings and `:` to bind aliases to a specific
interface FQDN:

```bash
openstack server set \
  --property landb-alias="IFACE1_FQDN:alias1,alias2;IFACE2_FQDN:alias3,alias4" \
  <server>
```

### DNS Record Types (CNAME vs A)

- By default, an alias creates a **CNAME** DNS entry.
- Appending `--LOAD-N-` to the alias name creates an **A** record instead.

For example, `myalias` produces a CNAME, while `myalias--LOAD-1-` produces an A
record. In both cases the server is reachable via `myalias`.

### DNS Load Balancing Across Nodes

To distribute traffic across multiple nodes (DNS round-robin), each node gets
the same aliases with a different `--load-N-` suffix:

```bash
# Node 0
openstack server set \
  --property landb-alias="alias1--load-0-,alias2--load-0-" <node-0>

# Node 1
openstack server set \
  --property landb-alias="alias1--load-1-,alias2--load-1-" <node-1>
```

### Alias Lists Longer Than 255 Characters

A single property value is limited to 255 characters. To work around this, split
aliases across multiple keys that share the `landb-alias` prefix:

```bash
# These two are equivalent:
openstack server set --property landb-alias="alias1,alias2,alias3" <server>

openstack server set \
  --property landb-alias="alias1" \
  --property landb-alias2="alias2,alias3" <server>
```

Any property key starting with `landb-alias` is recognized by LANDB.

## Load Balancer Aliases

When using an OpenStack Load Balancer (instead of node-level ingress), aliases
are managed via **tags** rather than server properties:

```bash
# Set aliases
openstack loadbalancer set \
  --tag "landb-alias=my-domain-one" \
  --tag "landb-alias=my-domain-two" mylb

# Remove a single alias
openstack loadbalancer unset --tag "landb-alias=my-domain-two" mylb

# Remove all aliases
openstack loadbalancer unset \
  --tag "landb-alias=my-domain-one" mylb
```

> **Note:** DNS propagation may take up to 15 minutes.
