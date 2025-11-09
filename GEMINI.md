# GEMINI.md: Project Onboarding & Developer Guide

Hello Gemini. This file contains the essential context for the `landb-alias-controller`. Its purpose is to give you all the information you need to maintain the existing code and develop new features, such as new DNS providers.

## 1. Project Overview

The `landb-alias-controller` is a Kubernetes controller that synchronizes DNS aliases from the cluster to an external DNS provider.

* **Its primary goal:** It watches Kubernetes `Ingress` resources and `Node` resources.
* **Its output:** It creates `A` records in a DNS provider, where each Ingress host (e.g., `app.cern.ch`) becomes a DNS record (e.g., `app`) pointing to the IP addresses of all nodes labeled for ingress.

## 2. Core Architecture & Data Flow

The controller operates on a simple, provider-agnostic reconciliation loop:

1.  **Watch:** The `controller` (`controller/controller.go`) watches for any changes to `Ingress` and `Node` resources in Kubernetes.
2.  **Build Desired State:** On a change, it fetches *all* ingresses and *all* ingress-labeled nodes. It builds a "desired state" as a list of `[]*dns.Record` objects (e.g., `app` -> `[1.1.1.1, 2.2.2.2]`, `tools` -> `[1.1.1.1, 2.2.2.2]`).
3.  **Get Current State:** It asks the active `provider` for the "current state" by calling `provider.Records()`.
4.  **Calculate Plan:** It uses the `plan` package (`plan/plan.go`) to "diff" the `current` and `desired` states. This produces a `plan.Changes` object (a list of records to create, update, and delete).
5.  **Reconcile:** It hands the `plan.Changes` object to the active `provider` by calling `provider.Reconcile(changes)`. The provider is then responsible for executing these changes.

## 3. Project Structure

* `cmd/main.go`: The entrypoint. Responsible for parsing flags (like `--provider`), initializing the manager, and **acting as a factory** in `initProvider` to build and inject the correct provider (e.g., `openstack.NewProvider(...)`).
* `controller/controller.go`: The main reconciliation logic. **This package is 100% provider-agnostic.** It only knows about Kubernetes and the `provider.Provider` interface.
* `provider/provider.go`: The **core interface** that all DNS providers must implement (`Records()` and `Reconcile()`). This is the "contract."
* `provider/openstack/`: An **implementation** of the `provider.Provider` interface. This is where all OpenStack-specific logic, API calls, and authentication live.
* `dns/dns.go`: The common data structure (`dns.Record`) used to communicate between the controller and the providers.
* `plan/plan.go`: The provider-agnostic "diff" engine. It takes two `[]*dns.Record` lists and produces a `plan.Changes` struct.

---

## 4. Key Design Decisions (The "Why")

This is the most important section. It explains *why* the code is written the way it is.

### Why is the Controller Provider-Agnostic?
The controller's main job is to understand the *desired state* from Kubernetes. It should not, and *must not*, know anything about *how* that state is stored. This separation of concerns allows us to easily add new providers (like Route53, Cloudflare, etc.) without ever touching the core controller logic.

### Why does the OpenStack Provider use Metadata?
This is a critical piece of domain knowledge. The "OpenStack" provider is not a true DNS provider. It writes DNS information to **OpenStack server metadata** (e.g., `landb-alias="app--load-0-"`). A separate system at CERN reads this metadata and updates the actual "LANDB" DNS system.

### Why is the OpenStack `Reconcile` logic so complex?
The `provider.Reconcile` interface provides a simple diff (`plan.Changes`). However, the OpenStack provider's `Reconcile` function *ignores* this and instead re-builds the entire desired state.

**This is intentional and correct.**

Because this provider writes to metadata, it must assign a unique suffix to each alias on each node (e.g., `app--load-0-`, `app--load-1-`). A simple "create `app` record" change from the planner is not enough information. The provider needs the *full list of all nodes* to correctly calculate all suffixes and pack them into metadata keys.

**In short:** The OpenStack provider performs **state-based reconciliation**, not change-based reconciliation, due to the specific requirements of the LANDB metadata system.

### Why the long, descriptive comments?
We follow a "documentation-first" model. Code tells you *how*, comments tell you *why*. When you maintain this code, please maintain this style. Explain the *intent* of a function, the *reason* for a variable, and the *context* of a complex block.

---

## 5. How to Contribute

### A. How to Add a New Provider (e.g., "Route53")

This is the most common new feature.

1.  **Create Package:** Create a new directory: `provider/route53/`.
2.  **Create Provider:** Create a `provider.go` file inside it.
3.  **Implement Struct:** Define a `Provider` struct that holds its configuration and API client (e.g., `aws.Route53Client`).
4.  **Implement Constructor:** Write a `NewProvider(...)` function that reads config (from env vars or flags), authenticates, and returns a new `*Provider`.
5.  **Implement Interface:** Make the `*Provider` struct satisfy the `provider.Provider` interface:
    * **`Records() ([]*dns.Record, error)`:** Write the code to call the Route53 API (e.g., `ListResourceRecordSets`), parse the response, and convert the results into our standard `[]*dns.Record` format.
    * **`Reconcile(changes *plan.Changes) error`:** Write the code to iterate over `changes.Create`, `changes.Update`, and `changes.Delete`. For each item, build and execute a Route53 API call (e.g., `ChangeResourceRecordSets`). This provider *can* be change-based, unlike the OpenStack one.
6.  **Wire it up:** Go to `cmd/main.go` and add the new provider to the `initProvider` factory function:
    ```go
    // in initProvider()
    switch providerName {
    case "openstack":
        // ...
    case "route53":
        setupLog.Info("using route53 provider")
        return route53.NewProvider(...)
    default:
        return nil, fmt.Errorf("unsupported provider %q", providerName)
    }
    ```

### B. How to Modify Controller Logic

* **To change which Ingresses are selected:** Modify `controller/controller.go` in the `listIngressLanDBAliases` function.
* **To change how Nodes are found:** Modify `controller/controller.go` in the `listIngressNodeIPs` function.
* **To change the structure of the `dns.Record`:** Modify `controller/controller.go` in the `buildDesiredRecords` function.

### C. Testing & Validation

* Run all tests with `go test ./...`.
* The `plan/plan_test.go` file is an excellent example of how to write unit tests for this project.

## 6. Glossary

* **Normalized Alias:** The "base" alias name derived from an Ingress host, without any provider-specific suffixes (e.g., `app`).
* **Suffixed Alias:** The alias as written to the OpenStack provider, including the load-balancing suffix (e.g., `app--load-0-`).
* **Provider:** The *destination* for DNS records (e.g., OpenStack, Route53).
* **Planner:** The *diff engine* that compares desired and current state.