# Repository Guidelines

## Project Structure & Module Organization

This Go controller watches KubeVirt `VirtualMachineInstance` resources and reconciles External-DNS `DNSEndpoint` resources in the same namespace.

- `cmd/main.go`: manager setup, flags, health probes, and prerequisite CRD checks.
- `internal/controller/`: reconciliation logic and adjacent unit tests in `vmi_controller_test.go`.
- `deploy/`: Kubernetes Deployment and RBAC manifests.
- `.github/workflows/build-and-release.yaml`: validation, container builds, and releases.
- `Dockerfile` and `Makefile`: container packaging and development targets.

## Build, Test, and Development Commands

Use the Go version declared in `go.mod` (currently 1.27.0).

- `make build`: compile the controller to `bin/manager`.
- `make test`: run all tests verbosely without cached results.
- `make vet`: run Go static analysis.
- `make mod-tidy`: synchronize `go.mod` and `go.sum`; review and commit dependency changes together.
- `make run`: run against the current kubeconfig cluster with leader election disabled; this can modify DNS endpoint resources.
- `make docker-build IMG=your-registry/external-dns-kubevirt:dev`: build a container image.
- `make deploy`: apply namespace, RBAC, and Deployment manifests. Edit the image in `deploy/deployment.yaml` when needed; this target does not substitute `IMG`.

## Coding Style & Naming Conventions

Format Go changes with `gofmt` and use standard Go tab indentation. Keep package names lowercase, exported identifiers in PascalCase, and unexported identifiers in mixedCaps. Follow existing Kubernetes API import aliases and controller-runtime patterns. Keep reconciliation behavior in `internal/controller/` and startup configuration in `cmd/`. Run `make vet` before submitting.

## Testing Guidelines

Tests use Go's `testing` package and controller-runtime's fake client. Place tests beside implementation files as `*_test.go`, name functions `Test...`, and use descriptive table-driven subtests. Cover annotation precedence, interface/IP selection, and endpoint creation, updates, and deletion when changing reconciliation behavior. Run `make test`; use `go test ./internal/controller -run TestPreferredAnnotation -v` for a focused check. No numeric coverage threshold is configured. CI also checks module tidiness and runs `go vet`.

## Commit & Pull Request Guidelines

Follow the history's Conventional Commit style: `feat:`, `fix:`, `chore:`, or scoped forms such as `fix(ci):`. Release automation reads commit subjects to determine version bumps. Open pull requests against `main`; explain the behavior change, link relevant issues, and report validation results. Update `README.md` and deployment/RBAC manifests when configuration, permissions, or documented behavior changes.
