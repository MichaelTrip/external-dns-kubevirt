# external-dns-kubevirt

A Kubernetes controller that automatically creates and manages [External-DNS](https://github.com/kubernetes-sigs/external-dns) `DNSEndpoint` resources for [KubeVirt](https://kubevirt.io/) `VirtualMachineInstance` objects.

## How it works

The controller watches all `VirtualMachineInstance` (VMI) resources cluster-wide. When a VMI has the `external-dns.alpha.kubernetes.io/hostname` annotation **and** IP addresses are available from a supported interface source, the controller creates or updates a `DNSEndpoint` CR in the same namespace.

External-DNS reads these `DNSEndpoint` CRs via its built-in `crd` source and manages the actual DNS records in your provider.

```
VirtualMachineInstance (annotated)
        │
        ▼
  external-dns-kubevirt controller
        │  resolves IPs from .status.interfaces
        │  priority: guest-agent → multus-status
        ▼
     DNSEndpoint CR (A + AAAA records)
        │
        ▼
  External-DNS (crd source)
        │
        ▼
  DNS Provider (Route53, Cloudflare, etc.)
```

## Prerequisites

- Kubernetes cluster with [KubeVirt](https://kubevirt.io/user-guide/operations/installation/) installed
- [External-DNS](https://github.com/kubernetes-sigs/external-dns) deployed with `--source=crd`
- The `DNSEndpoint` CRD from External-DNS installed in your cluster
- [Multus CNI](https://github.com/k8snetworkplumbingwg/multus-cni) configured for your VMs

## Annotation reference

Add these annotations to your `VirtualMachineInstance` objects:

| Annotation | Required | Description | Example |
|---|---|---|---|
| `external-dns.alpha.kubernetes.io/hostname` | ✅ Yes | Comma-separated list of DNS hostnames to register | `my-vm.example.com` |
| `external-dns.alpha.kubernetes.io/ttl` | ❌ No | DNS record TTL in seconds (default: `300`) | `60` |

### Example VMI

```yaml
apiVersion: kubevirt.io/v1
kind: VirtualMachineInstance
metadata:
  name: my-vm
  namespace: default
  annotations:
    external-dns.alpha.kubernetes.io/hostname: "my-vm.example.com"
    external-dns.alpha.kubernetes.io/ttl: "300"
spec:
  # ... VMI spec ...
```

Once the VMI is running and its interfaces are populated with `multus-status` IPs, the controller creates:

```yaml
apiVersion: externaldns.k8s.io/v1alpha1
kind: DNSEndpoint
metadata:
  name: my-vm
  namespace: default
  ownerReferences:
    - apiVersion: kubevirt.io/v1
      kind: VirtualMachineInstance
      name: my-vm
spec:
  endpoints:
    - dnsName: my-vm.example.com
      recordType: A
      targets:
        - 192.168.1.100
      recordTTL: 300
    - dnsName: my-vm.example.com
      recordType: AAAA
      targets:
        - "2001:db8::1"
      recordTTL: 300
```

## IP address selection

The controller selects IP addresses using a two-source priority scheme based on the `infoSource` field in `VirtualMachineInstance.status.interfaces[]`:

| Priority | Source | Field used | Notes |
|---|---|---|---|
| 1 (preferred) | `guest-agent` | `iface.IPs` (full list) | Available when `qemu-guest-agent` is installed in the VM. Link-local IPv6 (`fe80::/10`) addresses are skipped. |
| 2 (fallback) | `multus-status` | `iface.IP` (single IP) | Always available when Multus CNI is configured. |

The `infoSource` field can contain multiple comma-separated values (e.g. `domain, guest-agent, multus-status`). The controller checks for each source independently.

### Why prefer the guest-agent?

The `guest-agent` source populates `iface.IPs` with all addresses assigned to the interface, including global IPv6 unicast addresses. The `multus-status` source only sets the single `iface.IP` field (typically the primary IPv4 address).

### Behaviour table

| Annotation | IPs available | Action |
|---|---|---|
| ❌ absent | any | Delete existing `DNSEndpoint` |
| ✅ present | ❌ none yet | Do nothing — wait for next interface update |
| ✅ present | ✅ guest-agent IPs | Create/update `DNSEndpoint` using `iface.IPs` |
| ✅ present | ✅ multus-status only | Create/update `DNSEndpoint` using `iface.IP` |

- IPv4 addresses → `A` records
- IPv6 global unicast addresses → `AAAA` records

## Lifecycle

- When the VMI is **deleted**, the `DNSEndpoint` is automatically garbage-collected (via `OwnerReference`).
- When the hostname annotation is **removed**, the controller deletes the `DNSEndpoint`.
- When IPs are **not yet available** (VM still starting), the controller skips reconciliation without touching existing records.

## Deployment

### 1. Install prerequisites

Optionally install `qemu-guest-agent` inside your VMs to enable richer IP resolution (recommended for IPv6 support):

```bash
# Debian/Ubuntu
apt install qemu-guest-agent
# RHEL/Fedora
dnf install qemu-guest-agent
```

Ensure External-DNS is configured with `--source=crd`. Example External-DNS flag:

```
--source=crd
--crd-source-apiversion=externaldns.k8s.io/v1alpha1
--crd-source-kind=DNSEndpoint
```

### 2. Deploy the controller

```bash
# Create namespace, RBAC, and the controller Deployment
make deploy IMG=ghcr.io/michaeltrip/external-dns-kubevirt:latest
```

Or apply manifests directly:

```bash
kubectl create namespace external-dns-kubevirt
kubectl apply -f deploy/rbac.yaml
kubectl apply -f deploy/deployment.yaml
```

### 3. Verify

```bash
kubectl -n external-dns-kubevirt get pods
kubectl -n external-dns-kubevirt logs -l app.kubernetes.io/name=external-dns-kubevirt -f
```

## Development

### Prerequisites

- Go 1.23+
- `kubectl` configured against a cluster
- Docker (for image builds)

### Build

```bash
make build       # compile binary to bin/manager
make test        # run unit tests
make vet         # run go vet
```

### Run locally

```bash
make run         # runs the controller against your current kubeconfig cluster
```

### Build and push Docker image

```bash
make docker-build IMG=your-registry/external-dns-kubevirt:dev
make docker-push  IMG=your-registry/external-dns-kubevirt:dev
```

## Architecture

```
.
├── cmd/
│   └── main.go                       # Manager entrypoint
├── internal/
│   └── controller/
│       ├── vmi_controller.go         # Reconcile loop + business logic
│       └── vmi_controller_test.go    # Unit tests
├── deploy/
│   ├── rbac.yaml                     # ServiceAccount, ClusterRole, ClusterRoleBinding
│   └── deployment.yaml               # Controller Deployment
├── Dockerfile                        # Multi-stage image build
├── Makefile                          # Developer targets
└── go.mod
```

## License

Apache 2.0
