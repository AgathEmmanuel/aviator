# Aviator

**Latency-aware, protocol-aware, eBPF-powered traffic management for Kubernetes — without a service mesh.**

Aviator is a Kubernetes operator that routes traffic to the fastest pods by measuring real TCP latency at the kernel level using eBPF. No sidecars, no service mesh, no application changes required.

Based on the paper [Load is not what you should balance: Introducing Prequal](https://arxiv.org/abs/2312.10172).

---

## Why Aviator?

Kubernetes doesn't do latency-aware routing natively:

| Mode | Algorithm | Latency-Aware? |
|---|---|---|
| kube-proxy (iptables) | Random / Round-robin | No |
| kube-proxy (IPVS) | Least-connection | No (connection count, not latency) |
| Gateway API `BackendLBPolicy` | Round-robin, least-request | Experimental |
| **Aviator** | **eBPF-measured P99 latency** | **Yes** |

If you're running a service mesh (Istio, Linkerd), you already have latency-aware routing. Aviator is for everyone else — teams that want latency-based routing without the operational overhead of a full mesh.

---

## Architecture

Aviator has two components:

```
┌─────────────────────────────────────────────────────┐
│  Node 1                                             │
│  ┌─────────────────┐                                │
│  │  eBPF DaemonSet  │ ← hooks into tcp_sendmsg /    │
│  │  (per node)      │   tcp_rcv_established          │
│  └────────┬─────────┘ ← measures real RTT per pod   │
└───────────┼─────────────────────────────────────────┘
            │ HTTP API: pod IP → {p50, p99} latency
            ▼
┌─────────────────────────────────────────────────────┐
│  Aviator Controller (Deployment)                    │
│  ├─ Reads latency from eBPF agents                  │
│  ├─ Selects optimal pods (topN / % / threshold)     │
│  ├─ Circuit breaker: ejects slow pods               │
│  ├─ Dampening: prevents endpoint flapping           │
│  └─ Updates EndpointSlices (owned, no race)         │
└─────────────────────────────────────────────────────┘
```

See [ARCHITECTURE.md](ARCHITECTURE.md) for detailed system design, data flow diagrams, and design patterns.

---

## Features

- **eBPF Latency Measurement** — Kernel-level TCP RTT measurement on real traffic. No synthetic probes, no app instrumentation.
- **Multiple Selection Strategies** — Select pods by top-N fastest, top percentage, or latency threshold.
- **Circuit Breaker** — Automatically eject pods with sustained high P99 latency. Re-admit after recovery.
- **Dampening** — Suppress endpoint updates from transient latency spikes. Prevents flapping.
- **EndpointSlice Ownership** — Creates Aviator-owned EndpointSlices. No race condition with kube-controller-manager.
- **Finalizer Cleanup** — Removes managed EndpointSlices when an AviatorPolicy is deleted.
- **HTTP Probe Fallback** — For environments without eBPF support (kernel < 5.8), falls back to HTTP probe mode.

---

## Prerequisites

- Kubernetes 1.27+
- Kernel 5.8+ with BTF enabled (for eBPF mode)
- `kubectl` configured with cluster access

---

## Quick Start

### 1. Install CRDs

```bash
make install
```

### 2. Deploy the Controller

```bash
# Build and load images
make docker-build IMG=aviator-controller:latest
make docker-build-agent AGENT_IMG=aviator-agent:latest

# Deploy controller and eBPF agent
make deploy-all
```

### 3. Create an AviatorPolicy

```yaml
apiVersion: aviator.example.com/v1alpha1
kind: AviatorPolicy
metadata:
  name: my-app-policy
spec:
  targetRef:
    kind: Service
    name: my-app-service
  latencyThreshold: 100ms
  evaluationInterval: 5s
  latencySource: ebpf
  selection:
    mode: percentage
    percentage: 50
  circuitBreaker:
    enabled: true
    p99Threshold: 500ms
    consecutiveViolations: 3
    recoveryInterval: 30s
  dampening:
    enabled: true
    thresholdPercent: 20
    consecutiveIntervals: 3
```

```bash
kubectl apply -f aviator-policy.yaml
```

### 4. Check Status

```bash
kubectl get avp
```

```
NAME            ACTIVE   TOTAL   P99ms   SOURCE   AGE
my-app-policy   3        6       12      ebpf     5m
```

```bash
kubectl describe avp my-app-policy
```

---

## Configuration Reference

### AviatorPolicySpec

| Field | Type | Default | Description |
|---|---|---|---|
| `targetRef.name` | string | required | Name of the target Service |
| `latencyThreshold` | duration | `100ms` | Max acceptable latency (threshold mode) |
| `evaluationInterval` | duration | `5s` | How often to re-evaluate pod latency |
| `latencySource` | `ebpf` / `probe` | `ebpf` | Source of latency data |
| `targetPort` | int | `8080` | Port for HTTP probe mode |
| `selection.mode` | `topN` / `percentage` / `threshold` | `percentage` | Pod selection strategy |
| `selection.topN` | int | 3 | Number of pods (topN mode) |
| `selection.percentage` | int | 50 | Top percentage of pods |
| `circuitBreaker.enabled` | bool | `false` | Enable circuit breaker |
| `circuitBreaker.p99Threshold` | duration | `500ms` | P99 threshold for violation |
| `circuitBreaker.consecutiveViolations` | int | 3 | Violations before ejection |
| `circuitBreaker.recoveryInterval` | duration | `30s` | Time before recovery probe |
| `dampening.enabled` | bool | `true` | Enable dampening |
| `dampening.thresholdPercent` | int | 20 | Min change % to trigger update |
| `dampening.consecutiveIntervals` | int | 3 | Consecutive intervals required |

---

## Development

### Build

```bash
# Build controller binary
make build

# Build agent binary
make build-agent

# Build all Docker images
make docker-build-all
```

### Test

```bash
# Unit tests (no cluster required)
make test-unit

# Full test suite (requires envtest)
make test

# E2E tests (requires Kind cluster)
make test-e2e
```

### Run Locally (probe mode, no eBPF required)

```bash
make run
```

### Lint

```bash
make lint
```

---

## Functional Testing

A test environment with fast and slow pods is provided:

```bash
# Deploy test apps
kubectl apply -f test/functional/fast-app.yaml
kubectl apply -f test/functional/slow-app.yaml
kubectl apply -f test/functional/test-svc.yaml

# Apply sample policy
kubectl apply -f config/samples/aviator_v1alpha1_aviatorpolicy.yaml

# Watch Aviator route traffic to fast pods
kubectl get avp -w
```

- **fast-app**: responds in 10ms (2 replicas)
- **slow-app**: responds in 500ms (2 replicas)
- Aviator detects latency and routes traffic to fast-app pods.

---

## Roadmap

- [ ] gRPC-aware latency routing (per-RPC measurement via HTTP/2 frame parsing)
- [ ] KEDA/HPA integration (expose latency as custom metrics for autoscaling)
- [ ] AI/ML inference workload support (GPU queue-depth routing)
- [ ] Topology-aware + latency-aware routing (dynamic cross-zone decisions)
- [ ] Gateway API native support (`BackendLBPolicy` controller)
- [ ] Progressive delivery integration (latency-based canary promotion)

---

## Reference

- [Load is not what you should balance: Introducing Prequal](https://arxiv.org/abs/2312.10172)
- [cilium/ebpf](https://github.com/cilium/ebpf) — Go library for eBPF
- [Kubernetes EndpointSlices](https://kubernetes.io/docs/concepts/services-networking/endpoint-slices/)

---

## License

Apache License 2.0. See [LICENSE](LICENSE) for details.
