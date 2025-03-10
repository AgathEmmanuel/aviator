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
	"time"

	aviatorv1alpha1 "aviator/api/v1alpha1"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	//"k8s.io/apimachinery/pkg/types"
	//"k8s.io/apimachinery/pkg/util/intstr"
	//"k8s.io/apimachinery/pkg/util/wait"
	//"k8s.io/apimachinery/pkg/util/runtime"
	//"k8s.io/apimachinery/pkg/util/sets"
	//"k8s.io/apimachinery/pkg/util/validation/field"
	//"k8s.io/apimachinery/pkg/util/wait"
	//"k8s.io/apimachinery/pkg/util/wait"
)

// AviatorPolicyReconciler reconciles a AviatorPolicy object
type AviatorPolicyReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=aviator.example.com,resources=aviatorpolicies,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=aviator.example.com,resources=aviatorpolicies/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=aviator.example.com,resources=aviatorpolicies/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.

// TODO(user): Modify the Reconcile function to compare the state specified by
// the AviatorPolicy object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.19.4/pkg/reconcile

func (r *AviatorPolicyReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	_ = log.FromContext(ctx)

	var policy aviatorv1alpha1.AviatorPolicy
	if err := r.Get(ctx, req.NamespacedName, &policy); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// Fetch deployment pods
	var podList corev1.PodList
	if err := r.List(ctx, &podList, client.MatchingLabels{"app": policy.Spec.TargetRef.Name}); err != nil {
		return ctrl.Result{}, err
	}

	// Measure latency for each pod
	latencyMap := make(map[string]time.Duration)
	for _, pod := range podList.Items {
		latency := probePod(pod.Status.PodIP)
		latencyMap[pod.Name] = latency
	}

	// Select the pod with the lowest latency
	bestPod := selectBestPod(latencyMap)

	// Update traffic routing
	clientset, err := kubernetes.NewForConfig(ctrl.GetConfigOrDie())
	if err != nil {
		return ctrl.Result{}, err
	}
	err = updateTrafficRouting(ctx, clientset, policy.Namespace, policy.Spec.TargetRef.Name, bestPod)
	if err != nil {
		return ctrl.Result{}, err
	}

	// Requeue reconciliation after PingInterval seconds
	return ctrl.Result{RequeueAfter: time.Duration(policy.Spec.PingInterval) * time.Second}, nil
}

// Probe pod by sending an HTTP request and measuring response time
func probePod(ip string) time.Duration {
	start := time.Now()
	_, err := http.Get(fmt.Sprintf("http://%s:8080", ip))
	if err != nil {
		return time.Duration(999999999)
	}
	return time.Since(start)
}

// Select the pod with the lowest latency
func selectBestPod(latencyMap map[string]time.Duration) string {
	var bestPod string
	var lowestLatency time.Duration = time.Hour
	for pod, latency := range latencyMap {
		if latency < lowestLatency {
			lowestLatency = latency
			bestPod = pod
		}
	}
	return bestPod
}

// Update Kubernetes Service to route traffic to the selected pod by updating the Kubernetes Service Endpoints
// updateTrafficRouting updates the Kubernetes Endpoints object to direct traffic to the best pod
func updateTrafficRouting(ctx context.Context, clientset *kubernetes.Clientset, namespace, serviceName, bestPod string) error {
	// Fetch the best pod's details
	pod, err := clientset.CoreV1().Pods(namespace).Get(ctx, bestPod, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get pod %s: %v", bestPod, err)
	}

	// Get the service's endpoints
	endpoints, err := clientset.CoreV1().Endpoints(namespace).Get(ctx, serviceName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get endpoints for service %s: %v", serviceName, err)
	}

	// Update endpoints to point to only the best pod
	newEndpoints := &corev1.Endpoints{
		ObjectMeta: metav1.ObjectMeta{
			Name:      serviceName,
			Namespace: namespace,
		},
		Subsets: []corev1.EndpointSubset{
			{
				Addresses: []corev1.EndpointAddress{
					{IP: pod.Status.PodIP},
				},
				Ports: endpoints.Subsets[0].Ports, // Retain original ports
			},
		},
	}

	// Apply the updated endpoints
	_, err = clientset.CoreV1().Endpoints(namespace).Update(ctx, newEndpoints, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("failed to update endpoints for service %s: %v", serviceName, err)
	}

	fmt.Printf("Traffic routed to pod: %s with IP: %s\n", bestPod, pod.Status.PodIP)
	return nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *AviatorPolicyReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&aviatorv1alpha1.AviatorPolicy{}).
		Named("aviatorpolicy").
		Complete(r)
}
