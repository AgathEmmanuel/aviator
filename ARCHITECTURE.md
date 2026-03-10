# Aviator Architecture

Comprehensive system design documentation for the Aviator latency-aware traffic routing operator.

---

## Table of Contents

- [System Overview](#system-overview)
- [Component Architecture](#component-architecture)
- [Data Flow](#data-flow)
- [CRD Design](#crd-design)
- [eBPF Agent Design](#ebpf-agent-design)
- [Controller Design](#controller-design)
- [Circuit Breaker State Machine](#circuit-breaker-state-machine)
- [EndpointSlice Ownership Model](#endpointslice-ownership-model)
- [Dampening Algorithm](#dampening-algorithm)
- [Design Patterns](#design-patterns)
- [Directory Structure](#directory-structure)
- [Deployment Topology](#deployment-topology)
- [CLI Reference](#cli-reference)
- [Kernel Requirements](#kernel-requirements)

---

## System Overview

```mermaid
graph TB
    subgraph "Kubernetes Cluster"
        subgraph "Control Plane"
            API[API Server]
            KCM[kube-controller-manager]
        end

        subgraph "aviator-system namespace"
            CTRL[Aviator Controller<br/>Deployment]
        end

        subgraph "Node 1"
            AGENT1[eBPF Agent<br/>DaemonSet Pod]
            subgraph "eBPF Programs"
                KP1[kprobe/tcp_sendmsg]
                KP2[kprobe/tcp_rcv_established]
                RB[Ring Buffer]
            end
            POD1[App Pod 1]
            POD2[App Pod 2]
        end

        subgraph "Node 2"
            AGENT2[eBPF Agent<br/>DaemonSet Pod]
            POD3[App Pod 3]
            POD4[App Pod 4]
        end

        SVC[Service]
        ES[EndpointSlice<br/>managed-by: aviator]
        CRD[AviatorPolicy CR]
    end

    CLIENT[Client Traffic] --> SVC
    SVC --> ES
    ES --> POD1
    ES --> POD2

    KP1 -->|timestamp| RB
    KP2 -->|RTT calculation| RB
    RB -->|latency events| AGENT1
    AGENT1 -->|HTTP /latencies| CTRL
    AGENT2 -->|HTTP /latencies| CTRL

    CTRL -->|reads| CRD
    CTRL -->|reads| SVC
    CTRL -->|creates/updates| ES
    CTRL -->|reads pods| API

    style CTRL fill:#2563eb,color:#fff
    style AGENT1 fill:#059669,color:#fff
    style AGENT2 fill:#059669,color:#fff
    style ES fill:#d97706,color:#fff
    style CRD fill:#7c3aed,color:#fff
```

---

## Component Architecture

```mermaid
graph LR
    subgraph "Controller Binary (cmd/main.go)"
        R[Reconciler]
        LS[LatencySource<br/>Interface]
        ESM[EndpointSlice<br/>Manager]
        CB[Circuit Breaker]
        DMP[Dampener]

        R --> LS
        R --> ESM
        R --> CB
        R --> DMP
    end

    subgraph "Latency Sources"
        EBPF[EBPFSource]
        PROBE[ProbeSource]
    end

    LS --> EBPF
    LS --> PROBE

    subgraph "Agent Binary (cmd/agent/main.go)"
        LDR[eBPF Loader]
        COL[Collector]
        HTTP[HTTP API]

        LDR --> COL
        COL --> HTTP
    end

    EBPF -->|HTTP GET /latencies| HTTP

    style R fill:#2563eb,color:#fff
    style EBPF fill:#059669,color:#fff
    style PROBE fill:#6b7280,color:#fff
    style LDR fill:#059669,color:#fff
```

### Component Responsibilities

| Component | Location | Responsibility |
|---|---|---|
| **Reconciler** | `internal/controller/` | Orchestrates the reconciliation loop |
| **LatencySource** | `internal/latency/source.go` | Interface for latency data backends |
| **EBPFSource** | `internal/latency/ebpf_source.go` | Fetches latency from eBPF agents |
| **ProbeSource** | `internal/latency/probe_source.go` | HTTP probe fallback |
| **Aggregator** | `internal/latency/aggregator.go` | Pod ranking, selection, fleet stats |
| **CircuitBreaker** | `internal/circuitbreaker/` | Pod ejection/recovery state machine |
| **EndpointSliceManager** | `internal/endpointslice/` | Creates/updates owned EndpointSlices |
| **eBPF Loader** | `internal/ebpf/loader.go` | Loads and attaches BPF programs |
| **Collector** | `internal/ebpf/collector.go` | Aggregates ring buffer events into stats |

---

## Data Flow

### Reconciliation Loop

```mermaid
sequenceDiagram
    participant Timer as Requeue Timer
    participant R as Reconciler
    participant K8s as Kubernetes API
    participant LS as LatencySource
    participant CB as CircuitBreaker
    participant DMP as Dampener
    participant ESM as EndpointSlice Manager

    Timer->>R: Reconcile(policy)
    R->>K8s: Get AviatorPolicy
    R->>K8s: Get target Service
    R->>K8s: List pods (by selector)

    R->>LS: GetLatencies(podIPs)
    LS-->>R: map[podIP] → {p50, p99}

    R->>R: RankPods (sort by P99)
    R->>CB: RecordLatency per pod
    CB-->>R: ejected pod list
    R->>R: Filter ejected pods
    R->>R: SelectPods (topN/pct/threshold)

    R->>DMP: ShouldUpdate(selectedIPs)?
    alt Change is significant
        DMP-->>R: true
        R->>ESM: Reconcile EndpointSlice
        ESM->>K8s: Create/Update EndpointSlice
        R->>K8s: Update policy status
    else Change is minor (dampened)
        DMP-->>R: false
        Note over R: Skip update, requeue
    end

    R-->>Timer: RequeueAfter(evaluationInterval)
```

### eBPF Agent Data Path

```mermaid
sequenceDiagram
    participant App as Application Pod
    participant Kernel as Linux Kernel
    participant BPF as eBPF Programs
    participant RB as Ring Buffer
    participant COL as Collector
    participant API as HTTP API
    participant CTRL as Controller

    App->>Kernel: tcp_sendmsg()
    Kernel->>BPF: kprobe fires
    BPF->>BPF: Store timestamp in BPF hashmap

    Note over Kernel: ... network round trip ...

    Kernel->>BPF: tcp_rcv_established() kprobe
    BPF->>BPF: Compute RTT = now - stored_ts
    BPF->>RB: Submit latency_event

    loop Every 500ms
        COL->>RB: Drain events
        COL->>COL: Update per-IP histogram
    end

    CTRL->>API: GET /latencies
    API->>COL: GetStats()
    COL-->>API: map[podIP] → {p50_us, p99_us}
    API-->>CTRL: JSON response
```

---

## CRD Design

### AviatorPolicy Resource

```mermaid
classDiagram
    class AviatorPolicy {
        +TypeMeta
        +ObjectMeta
        +Spec AviatorPolicySpec
        +Status AviatorPolicyStatus
    }

    class AviatorPolicySpec {
        +TargetRef TargetRef
        +LatencyThreshold Duration
        +EvaluationInterval Duration
        +Selection SelectionPolicy
        +CircuitBreaker *CircuitBreakerSpec
        +Dampening *DampeningSpec
        +LatencySource LatencySourceType
        +TargetPort *int32
    }

    class TargetRef {
        +APIVersion string
        +Kind string
        +Name string
    }

    class SelectionPolicy {
        +Mode SelectionMode
        +TopN *int32
        +Percentage *int32
    }

    class CircuitBreakerSpec {
        +Enabled bool
        +P99Threshold Duration
        +ConsecutiveViolations int32
        +RecoveryInterval Duration
    }

    class DampeningSpec {
        +Enabled bool
        +ThresholdPercent int32
        +ConsecutiveIntervals int32
    }

    class AviatorPolicyStatus {
        +LastEvaluationTime Time
        +ActivePods int32
        +TotalPods int32
        +AverageLatencyMs int64
        +P99LatencyMs int64
        +CircuitBrokenPods []string
        +PodLatencies []PodLatencyInfo
        +Conditions []Condition
    }

    AviatorPolicy --> AviatorPolicySpec
    AviatorPolicy --> AviatorPolicyStatus
    AviatorPolicySpec --> TargetRef
    AviatorPolicySpec --> SelectionPolicy
    AviatorPolicySpec --> CircuitBreakerSpec
    AviatorPolicySpec --> DampeningSpec
```

### Selection Modes

```mermaid
graph LR
    subgraph "topN (select N fastest)"
        TN1[Pod 1: 5ms ✅]
        TN2[Pod 2: 12ms ✅]
        TN3[Pod 3: 45ms ✅]
        TN4[Pod 4: 200ms ❌]
        TN5[Pod 5: 500ms ❌]
    end

    subgraph "percentage (top X%)"
        P1[Pod 1: 5ms ✅]
        P2[Pod 2: 12ms ✅]
        P3[Pod 3: 45ms ❌<br/>50% of 4 = 2 pods]
        P4[Pod 4: 200ms ❌]
    end

    subgraph "threshold (below Xms)"
        T1[Pod 1: 5ms ✅]
        T2[Pod 2: 12ms ✅]
        T3[Pod 3: 45ms ✅]
        T4[Pod 4: 200ms ❌<br/>threshold = 100ms]
    end
```

---

## eBPF Agent Design

### BPF Program Architecture

```mermaid
graph TB
    subgraph "Kernel Space"
        TCP_SEND[kprobe/tcp_sendmsg]
        TCP_RECV[kprobe/tcp_rcv_established]

        subgraph "BPF Maps"
            TS_MAP["tcp_send_timestamps<br/>(HASH: flow_key → timestamp)"]
            RING["latency_events<br/>(RINGBUF: 1MB)"]
            AGG_MAP["per_ip_latency<br/>(HASH: dst_ip → aggregated stats)"]
        end
    end

    subgraph "User Space"
        READER[Ring Buffer Reader]
        COLLECTOR[Collector<br/>HDR Histogram per IP]
        API[HTTP API :9100]
    end

    TCP_SEND -->|"store ts"| TS_MAP
    TCP_RECV -->|"lookup ts"| TS_MAP
    TCP_RECV -->|"rtt = now - ts"| RING
    TCP_RECV -->|"aggregate"| AGG_MAP

    RING -->|"drain events"| READER
    READER -->|"RecordEvent()"| COLLECTOR
    COLLECTOR -->|"GetStats()"| API

    style TCP_SEND fill:#dc2626,color:#fff
    style TCP_RECV fill:#dc2626,color:#fff
    style RING fill:#059669,color:#fff
```

### BPF Data Structures

```c
// Flow key - identifies a TCP connection
struct flow_key {
    __u32 src_ip;
    __u32 dst_ip;
    __u16 src_port;
    __u16 dst_port;
};

// Latency event - sent to userspace
struct latency_event {
    __u32 src_ip;
    __u32 dst_ip;
    __u16 src_port;
    __u16 dst_port;
    __u64 rtt_ns;
    __u64 timestamp_ns;
};
```

---

## Controller Design

### Reconciler Interface Composition

```mermaid
graph TB
    subgraph "AviatorPolicyReconciler"
        CLIENT[client.Client]
        SCHEME[runtime.Scheme]
        SRC[LatencySource interface]
        ESM[EndpointSliceManager]
        BREAKERS["map[string]*Breaker"]
        DAMPENERS["map[string]*DampeningState"]
    end

    subgraph "LatencySource Interface"
        METHOD1["GetLatencies(ctx, podIPs) → map[string]Stats"]
        METHOD2["Name() → string"]
        METHOD3["Ready(ctx) → bool"]
    end

    SRC -.implements.-> METHOD1
    SRC -.implements.-> METHOD2
    SRC -.implements.-> METHOD3

    IMPL1[EBPFSource] -.-> SRC
    IMPL2[ProbeSource] -.-> SRC

    style SRC fill:#7c3aed,color:#fff
    style IMPL1 fill:#059669,color:#fff
    style IMPL2 fill:#6b7280,color:#fff
```

### Reconciliation State Machine

```mermaid
stateDiagram-v2
    [*] --> FetchPolicy
    FetchPolicy --> HandleDeletion: DeletionTimestamp set
    FetchPolicy --> EnsureFinalizer: Active policy

    HandleDeletion --> CleanupEndpointSlice
    CleanupEndpointSlice --> RemoveFinalizer
    RemoveFinalizer --> [*]

    EnsureFinalizer --> FetchService
    FetchService --> FetchPods: Service found
    FetchService --> SetConditionFalse: Service not found

    FetchPods --> MeasureLatency: Pods found
    FetchPods --> Requeue: No pods

    MeasureLatency --> RankPods
    RankPods --> CircuitBreakerCheck
    CircuitBreakerCheck --> SelectPods
    SelectPods --> DampeningCheck

    DampeningCheck --> UpdateEndpointSlice: Change significant
    DampeningCheck --> Requeue: Change suppressed

    UpdateEndpointSlice --> UpdateStatus
    UpdateStatus --> Requeue

    SetConditionFalse --> [*]
    Requeue --> [*]: RequeueAfter(evaluationInterval)
```

---

## Circuit Breaker State Machine

```mermaid
stateDiagram-v2
    [*] --> Closed: Pod first seen

    Closed --> Closed: P99 ≤ threshold<br/>(reset violation count)
    Closed --> Closed: P99 > threshold<br/>(violations < N)
    Closed --> Open: P99 > threshold<br/>(violations ≥ N)

    Open --> Open: recovery interval<br/>not elapsed
    Open --> HalfOpen: recovery interval<br/>elapsed

    HalfOpen --> Closed: P99 ≤ threshold<br/>(pod recovered)
    HalfOpen --> Open: P99 > threshold<br/>(still unhealthy)

    note right of Closed
        Traffic flows normally.
        Violations tracked.
    end note

    note right of Open
        Pod ejected from EndpointSlice.
        No traffic routed here.
    end note

    note right of HalfOpen
        Pod re-tested with real traffic.
        Single measurement decides outcome.
    end note
```

### Circuit Breaker Example Timeline

```
Time    Pod-A P99    Violations    State
─────────────────────────────────────────
t=0     45ms         0            CLOSED
t=5     120ms        1            CLOSED     (threshold: 100ms)
t=10    150ms        2            CLOSED
t=15    200ms        3            → OPEN     (ejected!)
t=20    ---          ---          OPEN       (no traffic)
t=25    ---          ---          OPEN
t=30    ---          ---          OPEN
t=45    ---          ---          → HALF_OPEN (recovery interval: 30s)
t=50    60ms         0            → CLOSED   (recovered!)
```

---

## EndpointSlice Ownership Model

```mermaid
graph TB
    subgraph "Service: my-app"
        SVC[Service<br/>my-app]
    end

    subgraph "EndpointSlices"
        DEFAULT["my-app-xxxxx<br/>managed-by: endpointslice-controller<br/>(K8s default - all pods)"]
        AVIATOR["aviator-my-app<br/>managed-by: aviator-controller<br/>(selected fast pods only)"]
    end

    subgraph "Pods"
        P1[Pod 1: 5ms ✅]
        P2[Pod 2: 12ms ✅]
        P3[Pod 3: 200ms ❌]
        P4[Pod 4: 500ms ❌]
    end

    SVC --> DEFAULT
    SVC --> AVIATOR

    DEFAULT --> P1
    DEFAULT --> P2
    DEFAULT --> P3
    DEFAULT --> P4

    AVIATOR --> P1
    AVIATOR --> P2

    style AVIATOR fill:#d97706,color:#fff
    style DEFAULT fill:#6b7280,color:#fff

    note1["kube-proxy reads ALL EndpointSlices<br/>for a Service. Aviator's slice contains<br/>only fast pods. The default slice<br/>can be disabled by annotation."]
```

### Ownership Labels

```yaml
apiVersion: discovery.k8s.io/v1
kind: EndpointSlice
metadata:
  name: aviator-my-app
  labels:
    kubernetes.io/service-name: my-app          # Links to Service
    endpointslice.kubernetes.io/managed-by: aviator-controller  # Ownership
    aviator.io/policy-name: my-app-policy       # Links to AviatorPolicy
  ownerReferences:
    - apiVersion: aviator.example.com/v1alpha1
      kind: AviatorPolicy
      name: my-app-policy
```

---

## Dampening Algorithm

Prevents endpoint flapping when pod latencies fluctuate around selection boundaries.

```mermaid
graph TD
    A[New selected pod set] --> B{First evaluation?}
    B -->|Yes| C[Apply immediately]
    B -->|No| D[Compute change %<br/>vs previous set]

    D --> E{Change ≥ threshold%?}
    E -->|No| F[Reset violation counter<br/>Keep current endpoints]
    E -->|Yes| G[Increment violation counter]

    G --> H{Counter ≥ required<br/>consecutive intervals?}
    H -->|No| I[Keep current endpoints<br/>Wait for next interval]
    H -->|Yes| J[Apply update<br/>Reset counter]

    F --> K[Requeue]
    I --> K
    C --> K
    J --> K
```

**Example**: With `thresholdPercent: 20` and `consecutiveIntervals: 3`:

```
Interval 1: Selected = [A, B, C]     → Apply (first time)
Interval 2: Selected = [A, B, D]     → 33% change, violation=1, suppress
Interval 3: Selected = [A, B, D]     → 33% change, violation=2, suppress
Interval 4: Selected = [A, B, D]     → 33% change, violation=3 → APPLY
Interval 5: Selected = [A, B, C]     → 33% change, violation=1, suppress
Interval 6: Selected = [A, B, D]     → Different from interval 5's pending, reset
```

---

## Design Patterns

### 1. Strategy Pattern — Pod Selection

The controller uses the Strategy pattern for pod selection, configured via the CRD:

```go
type SelectionMode string
const (
    SelectionModeTopN       SelectionMode = "topN"
    SelectionModePercentage SelectionMode = "percentage"
    SelectionModeThreshold  SelectionMode = "threshold"
)
```

Each mode is implemented as a pure function in `internal/latency/aggregator.go`:
- `SelectTopN(ranked, n)` — Fixed count
- `SelectTopPercent(ranked, percent)` — Percentage-based
- `SelectByThreshold(ranked, threshold)` — Latency ceiling

### 2. Interface Segregation — LatencySource

```go
type Source interface {
    GetLatencies(ctx context.Context, podIPs []string) (map[string]Stats, error)
    Name() string
    Ready(ctx context.Context) bool
}
```

Two implementations:
- `EBPFSource` — Reads from DaemonSet agents over HTTP
- `ProbeSource` — Direct HTTP probing (fallback)

The controller doesn't know or care which backend is active.

### 3. State Machine — Circuit Breaker

The circuit breaker uses a classic three-state machine (Closed → Open → Half-Open) with per-pod tracking. State transitions are driven by latency observations, not timers.

### 4. Observer Pattern — Controller-Runtime Watches

The controller watches:
- `AviatorPolicy` resources (primary)
- `EndpointSlice` resources owned by Aviator (secondary)

Changes to either trigger reconciliation.

### 5. Finalizer Pattern — Resource Cleanup

When an AviatorPolicy is deleted:
1. Finalizer prevents immediate deletion
2. Controller cleans up owned EndpointSlices
3. Controller removes circuit breaker and dampener state
4. Finalizer is removed, allowing garbage collection

---

## Directory Structure

```
aviator/
├── api/
│   └── v1alpha1/
│       ├── aviatorpolicy_types.go      # CRD type definitions
│       ├── groupversion_info.go        # API group metadata
│       └── zz_generated.deepcopy.go    # Auto-generated (make generate)
│
├── cmd/
│   ├── main.go                         # Controller entrypoint
│   └── agent/
│       └── main.go                     # eBPF agent entrypoint
│
├── internal/
│   ├── controller/
│   │   ├── aviatorpolicy_controller.go      # Main reconciler
│   │   ├── aviatorpolicy_controller_test.go # Integration tests
│   │   └── suite_test.go                    # Test suite setup
│   │
│   ├── latency/
│   │   ├── source.go                   # LatencySource interface
│   │   ├── ebpf_source.go             # eBPF agent client
│   │   ├── probe_source.go            # HTTP probe fallback
│   │   ├── aggregator.go              # Ranking, selection, fleet stats
│   │   └── aggregator_test.go         # Unit tests
│   │
│   ├── circuitbreaker/
│   │   ├── circuitbreaker.go          # State machine implementation
│   │   └── circuitbreaker_test.go     # Unit tests
│   │
│   ├── endpointslice/
│   │   └── manager.go                 # EndpointSlice CRUD with ownership
│   │
│   └── ebpf/
│       ├── bpf/
│       │   └── tcp_latency.c          # eBPF C program (kernel space)
│       ├── loader.go                  # BPF program lifecycle management
│       ├── collector.go               # Event aggregation + histograms
│       └── collector_test.go          # Unit tests
│
├── config/
│   ├── crd/                           # CRD manifests
│   ├── rbac/                          # Controller RBAC
│   ├── manager/                       # Controller Deployment
│   ├── agent/                         # eBPF DaemonSet + RBAC
│   ├── samples/                       # Example AviatorPolicy
│   ├── prometheus/                    # Monitoring config
│   └── network-policy/               # Network policies
│
├── test/
│   ├── functional/                    # Fast/slow test apps
│   └── e2e/                          # End-to-end tests
│
├── Dockerfile                         # Controller image
├── Dockerfile.agent                   # eBPF agent image
├── Makefile                          # Build, test, deploy targets
├── go.mod / go.sum                   # Go dependencies
└── ARCHITECTURE.md                   # This file
```

---

## Deployment Topology

```mermaid
graph TB
    subgraph "aviator-system namespace"
        subgraph "Controller (Deployment, 1 replica)"
            CM[controller-manager<br/>--latency-source=ebpf<br/>--leader-elect=true]
        end

        subgraph "Agent (DaemonSet, 1 per node)"
            A1[aviator-ebpf-agent<br/>Node 1]
            A2[aviator-ebpf-agent<br/>Node 2]
            A3[aviator-ebpf-agent<br/>Node 3]
        end
    end

    CM -->|"GET :9100/latencies"| A1
    CM -->|"GET :9100/latencies"| A2
    CM -->|"GET :9100/latencies"| A3

    subgraph "Capabilities"
        CAP1[CAP_BPF]
        CAP2[CAP_NET_ADMIN]
        CAP3[CAP_PERFMON]
    end

    A1 -.- CAP1
    A1 -.- CAP2
    A1 -.- CAP3

    subgraph "Volumes"
        V1["/sys/fs/bpf (hostPath)"]
        V2["/sys/kernel/debug (readOnly)"]
    end

    A1 -.- V1
    A1 -.- V2
```

---

## CLI Reference

### Build Commands

```bash
make build                 # Build controller binary
make build-agent           # Build eBPF agent binary
make docker-build          # Build controller Docker image
make docker-build-agent    # Build agent Docker image
make docker-build-all      # Build both images
```

### Deploy Commands

```bash
make install               # Install CRDs only
make deploy                # Deploy controller to cluster
make deploy-agent          # Deploy eBPF agent DaemonSet
make deploy-all            # Deploy controller + agent
make undeploy-all          # Remove everything
```

### Test Commands

```bash
make test-unit             # Unit tests (no cluster)
make test                  # Full suite with envtest
make test-e2e              # E2E tests (requires Kind)
make lint                  # Run golangci-lint
```

### Development Commands

```bash
make run                   # Run controller locally (probe mode)
make manifests             # Regenerate CRD/RBAC manifests
make generate              # Regenerate DeepCopy methods
make generate-yaml         # Generate deployment YAMLs
```

### kubectl Commands

```bash
kubectl get avp                         # List all AviatorPolicies (short name)
kubectl describe avp <name>             # Detailed status with pod latencies
kubectl get endpointslices -l endpointslice.kubernetes.io/managed-by=aviator-controller
```

---

## Kernel Requirements

### eBPF Mode

| Requirement | Minimum | Recommended |
|---|---|---|
| Kernel version | 5.8 | 5.15+ |
| BTF support | Required | Required |
| BPF JIT | Recommended | Enabled |
| BPF filesystem | `/sys/fs/bpf` mounted | Mounted |

Check BTF support:

```bash
ls /sys/kernel/btf/vmlinux  # Should exist
```

Check kernel version:

```bash
uname -r  # Must be >= 5.8
```

### Probe Mode (Fallback)

No special kernel requirements. Works on any Kubernetes node. Requires pods to expose an HTTP endpoint on the configured port.

---

## Security Considerations

### eBPF Agent Capabilities

The agent DaemonSet requests minimal capabilities:
- `CAP_BPF` — Load and manage BPF programs
- `CAP_NET_ADMIN` — Attach to network hooks
- `CAP_PERFMON` — Access performance monitoring

The agent does **not** run as privileged. All other capabilities are dropped.

### Network Access

- Agent listens on port 9100 (node network)
- Controller-to-agent communication is cluster-internal HTTP
- No external network access required

### RBAC

- Controller: read Services, Pods, Endpoints; full CRUD on EndpointSlices and AviatorPolicies
- Agent: read Pods and Nodes only
