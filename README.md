# Live Pod Migration Controller

A Kubernetes controller that enables live migration of running pods between cluster nodes using CRIU (Checkpoint/Restore In Userspace) technology. The system performs checkpoint operations on source nodes and restores pod state on destination nodes with minimal downtime.

## Description

The Live Pod Migration Controller implements a complete control-plane and node agent architecture for migrating stateful workloads across Kubernetes cluster nodes. It provides three main capabilities:

**🔄 Container-Level Checkpointing**: Create point-in-time snapshots of individual containers within pods, capturing process state, memory contents, and file descriptors.

**📦 Pod-Level Migration**: Orchestrate migration of entire pods by automatically checkpointing all containers and coordinating the restore process on destination nodes.

**🏗️ Distributed Architecture**: A control-plane operator manages the migration lifecycle while privileged node agents perform the actual checkpoint/restore operations via secure gRPC communication.

### Key Features

- **Minimal Downtime**: Live migration preserves application state with checkpoint/restore technology
- **Declarative API**: Kubernetes-native CRDs for `PodCheckpoint`, `ContainerCheckpoint`, and migration resources
- **Cross-Node Mobility**: Move workloads between nodes for maintenance, load balancing, or resource optimization
- **CRIU Integration**: Leverages mature CRIU technology for reliable process state capture
- **Production Ready**: Comprehensive error handling, status reporting, and operational observability

### Architecture Components

- **PodMigration Controller**: Orchestrates end-to-end pod migration workflows
- **PodCheckpoint Controller**: Manages pod-level checkpoint operations across multiple containers  
- **ContainerCheckpoint Controller**: Handles individual container checkpoint lifecycle
- **Checkpoint Agent**: Privileged DaemonSet that interfaces with kubelet checkpoint API and CRIU
- **Storage Integration**: Pluggable storage backends for checkpoint artifacts (local, PVC, object storage)

### Use Cases

- **Node Maintenance**: Drain nodes for updates while preserving long-running job state
- **Resource Optimization**: Move workloads to optimize cluster resource utilization
- **Disaster Recovery**: Create portable checkpoints for cross-cluster recovery scenarios
- **Development/Testing**: Capture and replay application states for debugging and testing

## Demo

🎥 **[View Live Pod Migration Demo](public/lpm.mov)** - See the system in action with real-time process state preservation across nodes.

## Quick Start

For a complete setup guide including CRIU configuration and testing instructions, see [README-TESTING.md](./README-TESTING.md).

### Basic Workflow

1. **Deploy the system** on a Kubernetes cluster with CRIU support
2. **Create a checkpoint** of a running pod:
   ```yaml
   apiVersion: lpm.my.domain/v1
   kind: PodCheckpoint
   metadata:
     name: my-app-checkpoint
   spec:
     podName: my-app-pod
   ```
3. **Monitor progress** with `kubectl get podcheckpoint my-app-checkpoint -w`
4. **Use checkpoint artifacts** for migration or backup scenarios

## Project Structure

```
├── api/v1/                          # CRD definitions and Go types
├── cmd/checkpoint-agent/            # Node agent binary
├── internal/
│   ├── controller/                  # Controller reconciliation logic
│   └── agent/                       # Agent client and gRPC interfaces
├── config/
│   ├── crd/bases/                   # Generated CRD manifests
│   ├── agent/                       # DaemonSet and RBAC for agents
│   └── samples/                     # Example resources
├── vagrant/                         # Development environment setup
└── README-TESTING.md               # Comprehensive testing guide
```

## Documentation

- **[Testing Guide](./README-TESTING.md)**: Complete setup, testing, and troubleshooting instructions
- **[Storage Plan](docs/CHECKPOINT-STORAGE-PLAN.md)**: Design for shared storage implementation
- **API Reference**: Generated CRD documentation (see `config/crd/bases/`)

## Getting Started

### Prerequisites
- go version v1.23.0+
- docker version 17.03+.
- kubectl version v1.11.3+.
- Access to a Kubernetes v1.11.3+ cluster.

### To Deploy on the cluster
**Build and push your image to the location specified by `IMG`:**

```sh
make docker-build docker-push IMG=<some-registry>/live-pod-migration-controller:tag
```

**NOTE:** This image ought to be published in the personal registry you specified.
And it is required to have access to pull the image from the working environment.
Make sure you have the proper permission to the registry if the above commands don’t work.

**Install the CRDs into the cluster:**

```sh
make install
```

**Deploy the Manager to the cluster with the image specified by `IMG`:**

```sh
make deploy IMG=<some-registry>/live-pod-migration-controller:tag
```

> **NOTE**: If you encounter RBAC errors, you may need to grant yourself cluster-admin
privileges or be logged in as admin.

**Create instances of your solution**
You can apply the samples (examples) from the config/sample:

```sh
kubectl apply -k config/samples/
```

>**NOTE**: Ensure that the samples has default values to test it out.

### To Uninstall
**Delete the instances (CRs) from the cluster:**

```sh
kubectl delete -k config/samples/
```

**Delete the APIs(CRDs) from the cluster:**

```sh
make uninstall
```

**UnDeploy the controller from the cluster:**

```sh
make undeploy
```

## Project Distribution

Following the options to release and provide this solution to the users.

### By providing a bundle with all YAML files

1. Build the installer for the image built and published in the registry:

```sh
make build-installer IMG=<some-registry>/live-pod-migration-controller:tag
```

**NOTE:** The makefile target mentioned above generates an 'install.yaml'
file in the dist directory. This file contains all the resources built
with Kustomize, which are necessary to install this project without its
dependencies.

2. Using the installer

Users can just run 'kubectl apply -f <URL for YAML BUNDLE>' to install
the project, i.e.:

```sh
kubectl apply -f https://raw.githubusercontent.com/<org>/live-pod-migration-controller/<tag or branch>/dist/install.yaml
```

### By providing a Helm Chart

1. Build the chart using the optional helm plugin

```sh
kubebuilder edit --plugins=helm/v1-alpha
```

2. See that a chart was generated under 'dist/chart', and users
can obtain this solution from there.

**NOTE:** If you change the project, you need to update the Helm Chart
using the same command above to sync the latest changes. Furthermore,
if you create webhooks, you need to use the above command with
the '--force' flag and manually ensure that any custom configuration
previously added to 'dist/chart/values.yaml' or 'dist/chart/manager/manager.yaml'
is manually re-applied afterwards.

## Roadmap

### Current Features (v0.1)
- ✅ Container-level checkpointing via kubelet API
- ✅ Pod-level checkpoint orchestration  
- ✅ Local checkpoint storage
- ✅ gRPC agent communication
- ✅ Comprehensive testing framework

### Planned Features
- 🔄 **Shared Storage Integration**: PVC-based checkpoint artifact sharing
- 🔄 **Pod Restore Operations**: Complete migration workflow with destination restore
- 🔄 **Incremental Checkpoints**: Delta-based storage for large containers
- 🔄 **Cross-Cluster Migration**: Portable checkpoints for disaster recovery
- 🔄 **Performance Optimization**: Compression, deduplication, and streaming

### Long-term Vision
- 🎯 **Production Hardening**: HA storage, encryption, multi-tenancy
- 🎯 **Advanced Scheduling**: Migration-aware pod placement
- 🎯 **Observability**: Comprehensive metrics and distributed tracing
- 🎯 **Cloud Integration**: Native support for cloud storage backends

## Contributing

We welcome contributions! This project is part of CP4101 coursework but aims to be a production-ready Kubernetes extension.

### Development Setup
1. Clone the repository
2. Set up the Vagrant development environment: `cd vagrant && vagrant up`
3. Follow the [Testing Guide](./README-TESTING.md) for local development

### Areas for Contribution
- **Storage Backends**: Implement additional checkpoint storage options
- **Testing**: Expand test coverage and performance benchmarks  
- **Documentation**: Improve API documentation and user guides
- **Performance**: Optimize checkpoint/restore performance
- **Security**: Enhance multi-tenancy and encryption features

**NOTE:** Run `make help` for more information on all potential `make` targets

More information can be found via the [Kubebuilder Documentation](https://book.kubebuilder.io/introduction.html)

## License

Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.

