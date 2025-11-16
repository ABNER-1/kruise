# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

OpenKruise is a CNCF incubating project that extends Kubernetes with advanced workload and application management capabilities. It provides enhanced controllers that complement core Kubernetes controllers for stateless, stateful, daemon, and job applications.

## Essential Development Commands

### Build & Development
- `make build` - Build manager binary
- `make run` - Run controller locally (requires running Kubernetes cluster)
- `make docker-build` - Build Docker image locally
- `make docker-multiarch` - Build multi-architecture images (linux/amd64, linux/arm64, linux/ppc64le)

### Code Quality & Generation
- `make generate` - Generate DeepCopy, client, and OpenAPI code
- `make manifests` - Generate CRDs, RBAC, and webhook configurations
- `make fmt` - Format Go code
- `make vet` - Run go vet
- `make lint` - Run golangci-lint with project configuration

### Testing
- `make test` - Run unit tests with coverage
- `make coverage-report` - Generate and open coverage report
- `go test ./pkg/controller/cloneset/... -v` - Run specific controller tests
- `go test ./pkg/controller/cloneset/... -run TestSpecificFunction` - Run single test

### Deployment
- `make install` - Install CRDs into cluster
- `make deploy` - Deploy controller to cluster
- `make undeploy` - Remove controller from cluster

## Architecture Overview

### Core Components
The project follows Kubernetes controller patterns with these key components:

1. **API Definitions** (`/apis/`): CRD definitions for v1alpha1 and v1beta1 versions
2. **Controllers** (`/pkg/controller/`): Reconciliation logic for each workload type
3. **Webhooks** (`/pkg/webhook/`): Admission controllers for validation and mutation
4. **Utilities** (`/pkg/util/`): Shared utilities for workload management

### Workload Controllers
Each major feature has its own controller package:
- `cloneset/` - Stateless application management with in-place updates
- `statefulset/` - Enhanced StatefulSet with advanced features
- `daemonset/` - Advanced DaemonSet capabilities
- `sidecarset/` - Sidecar injection and lifecycle management
- `broadcastjob/` - Job deployment across specific nodes

### Key Design Patterns

1. **In-Place Updates**: Controllers support updating containers without recreating pods
2. **Parallel Operations**: Batch processing for scale-up/down operations
3. **Lifecycle Hooks**: Pre/Post hooks for update workflows
4. **Partition-based Rollouts**: Gradual rollout strategies
5. **Resource Distribution**: Cross-namespace resource management

## Development Workflow

1. **Code Changes**: Always run `make generate manifests` after modifying API types
2. **Testing**: Unit tests use Ginkgo/Gomega framework, located alongside source files
3. **E2E Testing**: Comprehensive tests in `/test/e2e/` for different Kubernetes versions
4. **Code Generation**: Automated via controller-gen and custom scripts in `/scripts/`

## Important Configuration

- **Go Version**: 1.20 (enforced by build scripts)
- **Kubernetes Version**: Targets 1.28.x compatibility
- **Container Runtime**: Supports Docker, containerd via CRI
- **Multi-Architecture**: Supports amd64, arm64, ppc64le

## Testing Approach

- **Unit Tests**: Standard Go testing with Ginkgo/Gomega
- **Integration Tests**: Uses envtest for controller testing
- **E2E Tests**: Kind-based testing for multiple K8s versions (1.18, 1.20, 1.24, 1.26, 1.28)
- **Test Coverage**: Integrated coverage reporting with codecov

## Common Development Tasks

### Adding New Features
1. Define API types in `/apis/apps/v1alpha1/` or `/apis/apps/v1beta1/`
2. Generate code with `make generate`
3. Implement controller in `/pkg/controller/`
4. Add webhooks if needed in `/pkg/webhook/`
5. Write unit tests alongside implementation
6. Add e2e tests in `/test/e2e/apps/`

### Debugging Controllers
- Use `make run` for local development with existing cluster
- Check controller logs for reconciliation details
- Use `kubectl describe` on custom resources for event history
- Enable verbose logging with appropriate log levels

### Working with Webhooks
- Webhook configurations are generated automatically
- Certificate management handled by cert-manager or manual setup
- Test webhooks locally using `make run` with appropriate configurations