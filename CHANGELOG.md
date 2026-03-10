# CHANGELOG  

---
Implementation Status

Phase 1: Foundation Cleanup

- Removed all fmt.Printf debug statements and "Hello, World!"
- Fixed the nil panic in selectOptimalPods (replaced with safe SelectByThreshold)
- Added missing RBAC permissions for Services, Pods, Endpoints, EndpointSlices in
config/rbac/role.yaml
- Added finalizer for clean resource deletion

Phase 2: CRD Redesign

- api/v1alpha1/aviatorpolicy_types.go — Complete rewrite with:
  - metav1.Duration instead of bare int for latency/interval fields
  - SelectionPolicy with 3 modes: topN, percentage, threshold
  - CircuitBreakerSpec with configurable threshold, violations, recovery
  - DampeningSpec to prevent endpoint flapping
  - LatencySourceType enum: ebpf or probe
  - Configurable targetPort
  - Rich status: activePods, totalPods, p99LatencyMs, podLatencies[], conditions[]
  - kubectl get avp printer columns: Active, Total, P99ms, Source, Age
- Updated zz_generated.deepcopy.go for all new types

Phase 3: EndpointSlice Migration

- internal/endpointslice/manager.go — Creates Aviator-owned EndpointSlices with:
  - managed-by: aviator-controller label (no race with kube-controller-manager)
  - Owner references to the AviatorPolicy
  - Proper port copying from Service spec

Phase 4: eBPF DaemonSet Agent

- internal/ebpf/bpf/tcp_latency.c — eBPF C program:
  - kprobe/tcp_sendmsg → records send timestamp
  - kprobe/tcp_rcv_established → computes RTT
  - Ring buffer for userspace events
  - Per-IP aggregation map
- internal/ebpf/loader.go — Go eBPF loader using cilium/ebpf:
  - Loads compiled BPF object, attaches kprobes
  - Reads ring buffer events in a goroutine
- internal/ebpf/collector.go — Event aggregator:
  - Per-IP sample collection with cap (10K samples)
  - P50/P99 percentile computation with interpolation
  - Stale IP eviction
- cmd/agent/main.go — Agent binary:
  - HTTP API at :9100 (/latencies, /healthz, /readyz)
  - JSON response with per-pod P50/P99 in microseconds
- Dockerfile.agent — Multi-stage build (clang/llvm → Go → distroless)
- config/agent/ — DaemonSet + RBAC + ServiceAccount manifests
  - CAP_BPF, CAP_NET_ADMIN, CAP_PERFMON (not privileged)
  - HostNetwork for node-level capture
  - Resource limits: 50m-200m CPU, 64Mi-128Mi memory

Phase 5: Controller Rewrite

- internal/controller/aviatorpolicy_controller.go — Full rewrite:
  - LatencySource interface injection (eBPF or probe)
  - Circuit breaker integration (per-policy state)
  - Dampening logic (suppresses flapping)
  - EndpointSlice creation instead of Endpoint patching
  - Finalizer-based cleanup on policy deletion
  - Structured logging via logr
  - Only routes to Running pods with non-empty IPs
  - Rich status updates with conditions
- internal/latency/ — Latency abstraction layer:
  - Source interface with GetLatencies(), Name(), Ready()
  - EBPFSource — parallel HTTP fetches from all agents
  - ProbeSource — concurrent HTTP probes with 3-sample P50/P99
  - aggregator.go — ranking, selection strategies, fleet stats, dampening state
- internal/circuitbreaker/ — Three-state machine (Closed→Open→HalfOpen)
- cmd/main.go — Updated with --latency-source and --probe-port flags

Phase 6: Testing & CI

- 25 unit tests, all passing:
  - 12 tests in internal/latency/ (ranking, selection, fleet stats, dampening)
  - 7 tests in internal/circuitbreaker/ (ejection, recovery, reset)
  - 6 tests in internal/ebpf/ (collector, parser, percentiles)
- internal/controller/aviatorpolicy_controller_test.go — Integration test with mock latency
source
- .github/workflows/ci.yaml — GitHub Actions: lint, test, build-controller, build-agent

Documentation

- README.md — Complete rewrite with architecture diagram, quick start, full config reference,
 dev commands
- ARCHITECTURE.md — 12 Mermaid diagrams covering:
  - System overview, component architecture, data flow sequence diagram
  - CRD class diagram, selection mode visualization
  - eBPF program architecture, BPF data structures
  - Controller reconciliation state machine
  - Circuit breaker state machine with timeline example
  - EndpointSlice ownership model
  - Dampening algorithm flowchart
  - Deployment topology
  - Full CLI reference and kernel requirements

Build Artifacts

- go build ./... — all packages compile
- go vet ./... — no issues
- Updated .gitignore for eBPF objects, vmlinux.h, generated YAMLs
- Updated Makefile with: build-agent, docker-build-agent, docker-build-all, deploy-agent,
deploy-all, undeploy-all, test-unit, generate-yaml (includes agent)

