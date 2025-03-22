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
	"net/http"
	"sort"
	"time"

	aviatorv1alpha1 "aviator/api/v1alpha1"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// AviatorPolicyReconciler reconciles a AviatorPolicy object
type AviatorPolicyReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=aviator.example.com,resources=aviatorpolicies,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=aviator.example.com,resources=aviatorpolicies/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=aviator.example.com,resources=aviatorpolicies/finalizers,verbs=update

// Reconcile periodically checks latency of Service pods and updates routing
func (r *AviatorPolicyReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	// Fetch AviatorPolicy
	var aviatorPolicy aviatorv1alpha1.AviatorPolicy
	if err := r.Get(ctx, req.NamespacedName, &aviatorPolicy); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Fetch associated Service
	var service corev1.Service
	serviceKey := types.NamespacedName{Name: aviatorPolicy.Spec.TargetRef.Name, Namespace: req.Namespace}
	if err := r.Get(ctx, serviceKey, &service); err != nil {
		return ctrl.Result{}, err
	}

	// Get all pods behind the Service
	pods, err := r.getPodsForService(ctx, service)
	if err != nil || len(pods) == 0 {
		return ctrl.Result{}, err
	}
	// add a print statement to print the pods
	// fmt.Printf("Pods: %v\n", pods)

	// Measure latency of each pod
	latencyMap := r.measureLatency(pods)

	// Determine how many pods to keep active (adaptive scaling)
	activePods := r.selectOptimalPods(latencyMap, time.Duration(aviatorPolicy.Spec.LatencyThreshold)*time.Millisecond)
	// print the active pods
	fmt.Printf("Active Pods: %v\n", activePods)

	// Sort active pods by lowest latency
	sortedActivePods := sortPodsByLatency(latencyMap, activePods)

	// Select top N pods with the lowest latency (e.g., top 3)
	topN := 3
	if len(sortedActivePods) < topN {
		topN = len(sortedActivePods)
	}
	selectedPods := sortedActivePods[:topN]

	// Update Service Endpoints to route traffic only to selected pods
	err = r.updateServiceEndpoints(ctx, &service, selectedPods, pods)
	if err != nil {
		return ctrl.Result{}, err
	}

	// Requeue after PingInterval (default 5s) to re-evaluate latency
	return ctrl.Result{RequeueAfter: time.Duration(aviatorPolicy.Spec.PingInterval) * time.Second}, nil
}

// getPodsForService fetches all pods behind a given Service
func (r *AviatorPolicyReconciler) getPodsForService(ctx context.Context, service corev1.Service) ([]corev1.Pod, error) {
	var podList corev1.PodList
	selector := client.MatchingLabels(service.Spec.Selector)
	if err := r.List(ctx, &podList, selector, client.InNamespace(service.Namespace)); err != nil {
		return nil, err
	}
	// add a print statement to print the service
	//fmt.Printf("Service: %v\n", service)
	// add a print statement to print the podList
	//fmt.Printf("PodList: %v\n", podList.Items)
	return podList.Items, nil
}

// measureLatency checks response time of each pod
func (r *AviatorPolicyReconciler) measureLatency(pods []corev1.Pod) map[string]time.Duration {
	latencyMap := make(map[string]time.Duration)

	for _, pod := range pods {
		start := time.Now()
		url := fmt.Sprintf("http://%s:8080/", pod.Status.PodIP) // Assumes health check at port 8080
		_, err := http.Get(url)
		latency := time.Since(start)

		if err != nil {
			latency = 9999 * time.Millisecond // Assign high latency to unreachable pods
		}

		latencyMap[pod.Name] = latency

		fmt.Printf("Pod %s latency: %v, PodIP: %v\n", pod.Name, latency, pod.Status.PodIP)

	}

	return latencyMap
}

// sortPodsByLatency sorts the given pods by response time (fastest first)
func sortPodsByLatency(latencyMap map[string]time.Duration, pods []string) []string {
	sort.Slice(pods, func(i, j int) bool {
		return latencyMap[pods[i]] < latencyMap[pods[j]]
	})
	// print the pods
	fmt.Printf("Sorted Pods: %v\n", pods)
	return pods
}

// selectOptimalPods dynamically selects pods based on latency threshold
func (r *AviatorPolicyReconciler) selectOptimalPods(latencyMap map[string]time.Duration, threshold time.Duration) []string {
	var selectedPods []string

	for pod, latency := range latencyMap {
		if latency <= threshold {
			selectedPods = append(selectedPods, pod) // Include pods below threshold
		}
	}

	// Ensure at least 1 pod is always active
	if len(selectedPods) == 0 {
		selectedPods = append(selectedPods, sortPodsByLatency(latencyMap, nil)[0])
	}

	return selectedPods
}

// updateServiceEndpoints modifies Kubernetes Service endpoints to route traffic to selected pods
func (r *AviatorPolicyReconciler) updateServiceEndpoints(ctx context.Context, service *corev1.Service, selectedPods []string, pods []corev1.Pod) error {
	if len(selectedPods) == 0 {
		return fmt.Errorf("no available pods to route traffic")
	}

	// Fetch existing Endpoints resource
	var endpoints corev1.Endpoints
	// print the endpoints
	fmt.Printf("Endpoints: %v\n", endpoints)
	endpointsKey := types.NamespacedName{Name: service.Name, Namespace: service.Namespace}
	// print the endpointsKey
	fmt.Printf("EndpointsKey: %v\n", endpointsKey)
	if err := r.Get(ctx, endpointsKey, &endpoints); err != nil {
		return err
	}

	// Construct new endpoint list
	newAddresses := []corev1.EndpointAddress{}
	for _, podName := range selectedPods {
		for _, pod := range pods {
			if pod.Name == podName {
				newAddresses = append(newAddresses, corev1.EndpointAddress{IP: pod.Status.PodIP}) // Update to selected pods
				break
			}
		}
	}

	newEndpointSubset := []corev1.EndpointSubset{
		{
			Addresses: newAddresses,
			Ports:     endpoints.Subsets[0].Ports, // Keep existing ports
		},
	}

	// print the newAddresses
	fmt.Printf("New Addresses: %v\n", newAddresses)

	// Update Endpoints resource
	endpoints.Subsets = newEndpointSubset
	return r.Update(ctx, &endpoints)
}

// SetupWithManager sets up the controller with the Manager
func (r *AviatorPolicyReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&aviatorv1alpha1.AviatorPolicy{}).
		Owns(&corev1.Service{}). // Watch for Service changes
		Complete(r)
}
