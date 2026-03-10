/*
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
*/

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// SelectionMode defines how pods are selected for routing.
// +kubebuilder:validation:Enum=topN;percentage;threshold
type SelectionMode string

const (
	SelectionModeTopN       SelectionMode = "topN"
	SelectionModePercentage SelectionMode = "percentage"
	SelectionModeThreshold  SelectionMode = "threshold"
)

// LatencySourceType defines where latency data comes from.
// +kubebuilder:validation:Enum=ebpf;probe
type LatencySourceType string

const (
	LatencySourceEBPF  LatencySourceType = "ebpf"
	LatencySourceProbe LatencySourceType = "probe"
)

// TargetRef references the Kubernetes Service to manage.
type TargetRef struct {
	// API version of the target resource.
	// +kubebuilder:default="v1"
	APIVersion string `json:"apiVersion,omitempty"`

	// Kind of the target resource. Must be "Service".
	// +kubebuilder:default="Service"
	// +kubebuilder:validation:Enum=Service
	Kind string `json:"kind,omitempty"`

	// Name of the target Service.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
}

// SelectionPolicy configures how pods are selected for traffic routing.
type SelectionPolicy struct {
	// Mode determines the selection strategy.
	// +kubebuilder:default="percentage"
	Mode SelectionMode `json:"mode"`

	// TopN selects the N fastest pods. Used when mode is "topN".
	// +kubebuilder:validation:Minimum=1
	// +optional
	TopN *int32 `json:"topN,omitempty"`

	// Percentage selects the top X% of pods. Used when mode is "percentage".
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=100
	// +kubebuilder:default=50
	// +optional
	Percentage *int32 `json:"percentage,omitempty"`
}

// CircuitBreakerSpec configures automatic pod ejection on sustained high latency.
type CircuitBreakerSpec struct {
	// Enable circuit breaker functionality.
	// +kubebuilder:default=false
	Enabled bool `json:"enabled"`

	// P99 latency threshold that triggers a violation.
	// +kubebuilder:default="500ms"
	P99Threshold metav1.Duration `json:"p99Threshold,omitempty"`

	// Number of consecutive violations before ejecting a pod.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=3
	ConsecutiveViolations int32 `json:"consecutiveViolations,omitempty"`

	// How often to probe ejected pods for recovery.
	// +kubebuilder:default="30s"
	RecoveryInterval metav1.Duration `json:"recoveryInterval,omitempty"`
}

// DampeningSpec prevents endpoint flapping from transient latency spikes.
type DampeningSpec struct {
	// Enable dampening.
	// +kubebuilder:default=true
	Enabled bool `json:"enabled"`

	// Minimum latency change percentage to trigger an endpoint update.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=100
	// +kubebuilder:default=20
	ThresholdPercent int32 `json:"thresholdPercent,omitempty"`

	// Number of consecutive intervals the delta must exceed before updating.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=3
	ConsecutiveIntervals int32 `json:"consecutiveIntervals,omitempty"`
}

// AviatorPolicySpec defines the desired state of AviatorPolicy.
type AviatorPolicySpec struct {
	// Reference to the target Kubernetes Service.
	TargetRef TargetRef `json:"targetRef"`

	// Maximum acceptable latency for pod selection (threshold mode).
	// +kubebuilder:default="100ms"
	LatencyThreshold metav1.Duration `json:"latencyThreshold,omitempty"`

	// How often the controller re-evaluates pod latency.
	// +kubebuilder:default="5s"
	EvaluationInterval metav1.Duration `json:"evaluationInterval,omitempty"`

	// Pod selection strategy.
	// +optional
	Selection SelectionPolicy `json:"selection,omitempty"`

	// Circuit breaker configuration.
	// +optional
	CircuitBreaker *CircuitBreakerSpec `json:"circuitBreaker,omitempty"`

	// Dampening prevents endpoint flapping.
	// +optional
	Dampening *DampeningSpec `json:"dampening,omitempty"`

	// Source of latency data.
	// +kubebuilder:default="ebpf"
	LatencySource LatencySourceType `json:"latencySource,omitempty"`

	// Port to probe when using "probe" latency source.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	// +optional
	TargetPort *int32 `json:"targetPort,omitempty"`
}

// PodLatencyInfo captures per-pod latency observations.
type PodLatencyInfo struct {
	// Pod name.
	Name string `json:"name"`
	// Pod IP address.
	PodIP string `json:"podIP,omitempty"`
	// Observed P50 latency.
	P50 metav1.Duration `json:"p50,omitempty"`
	// Observed P99 latency.
	P99 metav1.Duration `json:"p99,omitempty"`
	// Whether the pod is circuit-broken.
	CircuitBroken bool `json:"circuitBroken,omitempty"`
}

// AviatorPolicyStatus defines the observed state of AviatorPolicy.
type AviatorPolicyStatus struct {
	// Timestamp of the last latency evaluation.
	LastEvaluationTime metav1.Time `json:"lastEvaluationTime,omitempty"`

	// Number of pods actively receiving traffic.
	ActivePods int32 `json:"activePods"`

	// Total number of pods behind the target Service.
	TotalPods int32 `json:"totalPods"`

	// Fleet-wide average P99 latency.
	AverageLatencyMs int64 `json:"averageLatencyMs,omitempty"`

	// Fleet-wide P99 latency.
	P99LatencyMs int64 `json:"p99LatencyMs,omitempty"`

	// List of pods ejected by the circuit breaker.
	CircuitBrokenPods []string `json:"circuitBrokenPods,omitempty"`

	// Per-pod latency details (top 10 pods).
	PodLatencies []PodLatencyInfo `json:"podLatencies,omitempty"`

	// Standard conditions for the policy.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=avp
// +kubebuilder:printcolumn:name="Active",type=integer,JSONPath=`.status.activePods`
// +kubebuilder:printcolumn:name="Total",type=integer,JSONPath=`.status.totalPods`
// +kubebuilder:printcolumn:name="P99ms",type=integer,JSONPath=`.status.p99LatencyMs`
// +kubebuilder:printcolumn:name="Source",type=string,JSONPath=`.spec.latencySource`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// AviatorPolicy is the Schema for the aviatorpolicies API.
type AviatorPolicy struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   AviatorPolicySpec   `json:"spec,omitempty"`
	Status AviatorPolicyStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// AviatorPolicyList contains a list of AviatorPolicy.
type AviatorPolicyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []AviatorPolicy `json:"items"`
}

func init() {
	SchemeBuilder.Register(&AviatorPolicy{}, &AviatorPolicyList{})
}
