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

package controller

import (
	"context"
	"fmt"
	"time"

	aviatorv1alpha1 "aviator/api/v1alpha1"
	"aviator/internal/circuitbreaker"
	"aviator/internal/endpointslice"
	"aviator/internal/latency"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

const (
	finalizerName       = "aviator.io/finalizer"
	defaultTargetPort   = int32(8080)
	defaultPercentage   = int32(50)
	maxStatusPodEntries = 10
)

// AviatorPolicyReconciler reconciles AviatorPolicy objects.
type AviatorPolicyReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	// LatencySource provides pod latency measurements.
	LatencySource latency.Source

	// EndpointSliceManager handles EndpointSlice CRUD operations.
	EndpointSliceManager *endpointslice.Manager

	// Per-policy state (keyed by policy NamespacedName).
	breakers   map[string]*circuitbreaker.Breaker
	dampeners  map[string]*latency.DampeningState
}

// NewReconciler creates a new AviatorPolicyReconciler.
func NewReconciler(
	c client.Client,
	scheme *runtime.Scheme,
	source latency.Source,
	esManager *endpointslice.Manager,
) *AviatorPolicyReconciler {
	return &AviatorPolicyReconciler{
		Client:               c,
		Scheme:               scheme,
		LatencySource:        source,
		EndpointSliceManager: esManager,
		breakers:             make(map[string]*circuitbreaker.Breaker),
		dampeners:            make(map[string]*latency.DampeningState),
	}
}

// +kubebuilder:rbac:groups=aviator.example.com,resources=aviatorpolicies,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=aviator.example.com,resources=aviatorpolicies/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=aviator.example.com,resources=aviatorpolicies/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=endpoints,verbs=get;list;watch
// +kubebuilder:rbac:groups=discovery.k8s.io,resources=endpointslices,verbs=get;list;watch;create;update;patch;delete

// Reconcile evaluates pod latency and updates EndpointSlices for the target Service.
func (r *AviatorPolicyReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// 1. Fetch the AviatorPolicy.
	var policy aviatorv1alpha1.AviatorPolicy
	if err := r.Get(ctx, req.NamespacedName, &policy); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// 2. Handle deletion with finalizer.
	if !policy.DeletionTimestamp.IsZero() {
		return r.handleDeletion(ctx, &policy)
	}

	// 3. Ensure finalizer is set.
	if !controllerutil.ContainsFinalizer(&policy, finalizerName) {
		controllerutil.AddFinalizer(&policy, finalizerName)
		if err := r.Update(ctx, &policy); err != nil {
			return ctrl.Result{}, err
		}
	}

	// 4. Fetch the target Service.
	var service corev1.Service
	serviceKey := types.NamespacedName{
		Name:      policy.Spec.TargetRef.Name,
		Namespace: req.Namespace,
	}
	if err := r.Get(ctx, serviceKey, &service); err != nil {
		if errors.IsNotFound(err) {
			logger.Info("target Service not found", "service", serviceKey)
			r.setCondition(&policy, "Ready", metav1.ConditionFalse, "ServiceNotFound", "Target Service does not exist")
			_ = r.Status().Update(ctx, &policy)
		}
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// 5. Fetch all pods behind the Service.
	pods, err := r.getPodsForService(ctx, service)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("listing pods: %w", err)
	}
	if len(pods) == 0 {
		logger.Info("no pods found for Service", "service", serviceKey)
		r.setCondition(&policy, "Ready", metav1.ConditionFalse, "NoPodsFound", "No pods match the Service selector")
		_ = r.Status().Update(ctx, &policy)
		return ctrl.Result{RequeueAfter: r.getEvaluationInterval(&policy)}, nil
	}

	// 6. Collect pod IPs.
	podIPMap := make(map[string]corev1.Pod, len(pods))
	podIPs := make([]string, 0, len(pods))
	for _, pod := range pods {
		if pod.Status.PodIP != "" {
			podIPMap[pod.Status.PodIP] = pod
			podIPs = append(podIPs, pod.Status.PodIP)
		}
	}

	// 7. Measure latency.
	latencies, err := r.LatencySource.GetLatencies(ctx, podIPs)
	if err != nil {
		logger.Error(err, "failed to get latencies")
		r.setCondition(&policy, "Ready", metav1.ConditionFalse, "LatencyFetchFailed", err.Error())
		_ = r.Status().Update(ctx, &policy)
		return ctrl.Result{RequeueAfter: r.getEvaluationInterval(&policy)}, nil
	}

	// 8. Build rankings.
	rankings := make([]latency.PodRanking, 0, len(latencies))
	for ip, stats := range latencies {
		pod, ok := podIPMap[ip]
		if !ok {
			continue
		}
		rankings = append(rankings, latency.PodRanking{
			PodName: pod.Name,
			PodIP:   ip,
			Stats:   stats,
		})
	}
	rankings = latency.RankPods(rankings)

	// 9. Circuit breaker processing.
	policyKey := req.NamespacedName.String()
	breaker := r.getOrCreateBreaker(&policy, policyKey)
	if breaker != nil {
		breaker.CheckRecovery()
		for _, rank := range rankings {
			breaker.RecordLatency(rank.PodIP, rank.Stats.P99)
		}
		// Filter out ejected pods.
		var healthy []latency.PodRanking
		for _, rank := range rankings {
			if !breaker.IsEjected(rank.PodIP) {
				healthy = append(healthy, rank)
			}
		}
		if len(healthy) > 0 {
			rankings = healthy
		}
		// else: if all pods are ejected, use all pods as fallback
	}

	// 10. Select pods based on policy.
	selected := r.selectPods(&policy, rankings)

	// 11. Dampening — suppress flapping.
	dampener := r.getOrCreateDampener(policyKey)
	selectedIPs := make([]string, len(selected))
	for i, s := range selected {
		selectedIPs[i] = s.PodIP
	}

	if policy.Spec.Dampening != nil && policy.Spec.Dampening.Enabled {
		if !dampener.ShouldUpdate(
			selectedIPs,
			int(policy.Spec.Dampening.ThresholdPercent),
			int(policy.Spec.Dampening.ConsecutiveIntervals),
		) {
			logger.V(1).Info("dampening: suppressing endpoint update", "policy", policyKey)
			return ctrl.Result{RequeueAfter: r.getEvaluationInterval(&policy)}, nil
		}
	}

	// 12. Build EndpointSlice pod list.
	podEndpoints := make([]endpointslice.PodEndpoint, 0, len(selected))
	for _, s := range selected {
		pod := podIPMap[s.PodIP]
		podEndpoints = append(podEndpoints, endpointslice.PodEndpoint{
			PodName:  s.PodName,
			PodIP:    s.PodIP,
			NodeName: pod.Spec.NodeName,
			Ready:    true,
		})
	}

	// 13. Update EndpointSlice.
	if err := r.EndpointSliceManager.Reconcile(ctx, &policy, &service, podEndpoints); err != nil {
		logger.Error(err, "failed to update EndpointSlice")
		r.setCondition(&policy, "Ready", metav1.ConditionFalse, "EndpointSliceUpdateFailed", err.Error())
		_ = r.Status().Update(ctx, &policy)
		return ctrl.Result{RequeueAfter: r.getEvaluationInterval(&policy)}, nil
	}

	// 14. Update status.
	r.updateStatus(&policy, rankings, selected, breaker)
	r.setCondition(&policy, "Ready", metav1.ConditionTrue, "Reconciled", "Successfully updated routing")
	if err := r.Status().Update(ctx, &policy); err != nil {
		logger.Error(err, "failed to update policy status")
	}

	logger.Info("reconcile complete",
		"policy", req.NamespacedName,
		"activePods", len(selected),
		"totalPods", len(pods),
		"source", r.LatencySource.Name(),
	)

	return ctrl.Result{RequeueAfter: r.getEvaluationInterval(&policy)}, nil
}

// handleDeletion cleans up resources when an AviatorPolicy is deleted.
func (r *AviatorPolicyReconciler) handleDeletion(ctx context.Context, policy *aviatorv1alpha1.AviatorPolicy) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	if controllerutil.ContainsFinalizer(policy, finalizerName) {
		logger.Info("cleaning up resources for deleted policy", "policy", policy.Name)

		if err := r.EndpointSliceManager.Cleanup(ctx, policy.Namespace, policy.Spec.TargetRef.Name); err != nil {
			return ctrl.Result{}, fmt.Errorf("cleaning up EndpointSlice: %w", err)
		}

		// Remove per-policy state.
		policyKey := types.NamespacedName{Name: policy.Name, Namespace: policy.Namespace}.String()
		delete(r.breakers, policyKey)
		delete(r.dampeners, policyKey)

		controllerutil.RemoveFinalizer(policy, finalizerName)
		if err := r.Update(ctx, policy); err != nil {
			return ctrl.Result{}, err
		}
	}
	return ctrl.Result{}, nil
}

// getPodsForService lists all Running pods matching the Service selector.
func (r *AviatorPolicyReconciler) getPodsForService(ctx context.Context, service corev1.Service) ([]corev1.Pod, error) {
	var podList corev1.PodList
	selector := client.MatchingLabels(service.Spec.Selector)
	if err := r.List(ctx, &podList, selector, client.InNamespace(service.Namespace)); err != nil {
		return nil, err
	}

	var readyPods []corev1.Pod
	for _, pod := range podList.Items {
		if pod.Status.Phase == corev1.PodRunning && pod.DeletionTimestamp.IsZero() {
			readyPods = append(readyPods, pod)
		}
	}
	return readyPods, nil
}

// selectPods applies the configured selection strategy.
func (r *AviatorPolicyReconciler) selectPods(policy *aviatorv1alpha1.AviatorPolicy, ranked []latency.PodRanking) []latency.PodRanking {
	if len(ranked) == 0 {
		return ranked
	}

	switch policy.Spec.Selection.Mode {
	case aviatorv1alpha1.SelectionModeTopN:
		n := int32(3)
		if policy.Spec.Selection.TopN != nil {
			n = *policy.Spec.Selection.TopN
		}
		return latency.SelectTopN(ranked, int(n))

	case aviatorv1alpha1.SelectionModePercentage:
		pct := defaultPercentage
		if policy.Spec.Selection.Percentage != nil {
			pct = *policy.Spec.Selection.Percentage
		}
		return latency.SelectTopPercent(ranked, int(pct))

	case aviatorv1alpha1.SelectionModeThreshold:
		return latency.SelectByThreshold(ranked, policy.Spec.LatencyThreshold.Duration)

	default:
		// Default to percentage mode.
		pct := defaultPercentage
		if policy.Spec.Selection.Percentage != nil {
			pct = *policy.Spec.Selection.Percentage
		}
		return latency.SelectTopPercent(ranked, int(pct))
	}
}

func (r *AviatorPolicyReconciler) getEvaluationInterval(policy *aviatorv1alpha1.AviatorPolicy) time.Duration {
	if policy.Spec.EvaluationInterval.Duration > 0 {
		return policy.Spec.EvaluationInterval.Duration
	}
	return 5 * time.Second
}

func (r *AviatorPolicyReconciler) getOrCreateBreaker(policy *aviatorv1alpha1.AviatorPolicy, key string) *circuitbreaker.Breaker {
	if policy.Spec.CircuitBreaker == nil || !policy.Spec.CircuitBreaker.Enabled {
		return nil
	}

	b, ok := r.breakers[key]
	if !ok {
		b = circuitbreaker.New(circuitbreaker.Config{
			P99Threshold:          policy.Spec.CircuitBreaker.P99Threshold.Duration,
			ConsecutiveViolations: policy.Spec.CircuitBreaker.ConsecutiveViolations,
			RecoveryInterval:      policy.Spec.CircuitBreaker.RecoveryInterval.Duration,
		})
		r.breakers[key] = b
	}
	return b
}

func (r *AviatorPolicyReconciler) getOrCreateDampener(key string) *latency.DampeningState {
	d, ok := r.dampeners[key]
	if !ok {
		d = latency.NewDampeningState()
		r.dampeners[key] = d
	}
	return d
}

func (r *AviatorPolicyReconciler) updateStatus(
	policy *aviatorv1alpha1.AviatorPolicy,
	allRankings []latency.PodRanking,
	selected []latency.PodRanking,
	breaker *circuitbreaker.Breaker,
) {
	policy.Status.LastEvaluationTime = metav1.Now()
	policy.Status.ActivePods = int32(len(selected))
	policy.Status.TotalPods = int32(len(allRankings))

	if len(allRankings) > 0 {
		policy.Status.P99LatencyMs = latency.ComputeFleetP99(allRankings).Milliseconds()
		policy.Status.AverageLatencyMs = latency.ComputeFleetAverage(allRankings).Milliseconds()
	}

	// Per-pod latency info (capped).
	podInfos := make([]aviatorv1alpha1.PodLatencyInfo, 0, maxStatusPodEntries)
	for i, r := range allRankings {
		if i >= maxStatusPodEntries {
			break
		}
		info := aviatorv1alpha1.PodLatencyInfo{
			Name:  r.PodName,
			PodIP: r.PodIP,
			P50:   metav1.Duration{Duration: r.Stats.P50},
			P99:   metav1.Duration{Duration: r.Stats.P99},
		}
		if breaker != nil {
			info.CircuitBroken = breaker.IsEjected(r.PodIP)
		}
		podInfos = append(podInfos, info)
	}
	policy.Status.PodLatencies = podInfos

	if breaker != nil {
		policy.Status.CircuitBrokenPods = breaker.GetEjectedPods()
	} else {
		policy.Status.CircuitBrokenPods = nil
	}
}

func (r *AviatorPolicyReconciler) setCondition(policy *aviatorv1alpha1.AviatorPolicy, condType string, status metav1.ConditionStatus, reason, message string) {
	meta.SetStatusCondition(&policy.Status.Conditions, metav1.Condition{
		Type:               condType,
		Status:             status,
		ObservedGeneration: policy.Generation,
		LastTransitionTime: metav1.Now(),
		Reason:             reason,
		Message:            message,
	})
}

// SetupWithManager sets up the controller with the Manager.
func (r *AviatorPolicyReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&aviatorv1alpha1.AviatorPolicy{}).
		Owns(&discoveryv1.EndpointSlice{}).
		Complete(r)
}
