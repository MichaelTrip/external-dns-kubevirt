# external-dns-kubevirt

A Kubernetes controller that automatically creates and manages [External-DNS](https://github.com/kubernetes-sigs/external-dns) `DNSEndpoint` resources for [KubeVirt](https://kubevirt.io/) `VirtualMachineInstance` objects.

## How it works

The controller watches all `VirtualMachineInstance` (VMI) resources cluster-wide. When a VMI has the `external-dns.alpha.kubernetes.io/hostname` annotation and has IP addresses available from Multus interfaces, the controller creates or updates a `DNSEndpoint` CR in the same namespace.

External-DNS reads these `DNSEndpoint` CRs via its built-in `crd` source and manages the actual DNS records in your provider.

```
VirtualMachineInstance (annotated)
        │
        ▼
  external-dns-kubevirt controller
        │  extracts IPs from .status.interfaces
        │  where infoSource contains "multus-status"
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

The controller only uses IP addresses from network interfaces where the `infoSource` field **contains** `multus-status`. This field is set in `VirtualMachineInstance.status.interfaces[].infoSource` by the KubeVirt Multus integration.

- IPv4 addresses (`net.ParseIP().To4() != nil`) → `A` records
- IPv6 addresses (`net.ParseIP().To16() != nil`, not IPv4) → `AAAA` records

## Lifecycle

- When the VMI is **deleted**, the `DNSEndpoint` is automatically garbage-collected (via `OwnerReference`).
- When the hostname annotation is **removed**, the controller deletes the `DNSEndpoint`.
- When all Multus IPs are **lost**, the controller deletes the `DNSEndpoint`.

## Deployment

### 1. Install prerequisites

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
