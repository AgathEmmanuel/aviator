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
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	aviatorv1alpha1 "aviator/api/v1alpha1"
	"aviator/internal/endpointslice"
	"aviator/internal/latency"
)

// mockLatencySource is a test double for the latency.Source interface.
type mockLatencySource struct {
	latencies map[string]latency.Stats
	err       error
}

func (m *mockLatencySource) GetLatencies(_ context.Context, podIPs []string) (map[string]latency.Stats, error) {
	if m.err != nil {
		return nil, m.err
	}
	if m.latencies == nil {
		return map[string]latency.Stats{}, nil
	}
	result := make(map[string]latency.Stats, len(podIPs))
	for _, ip := range podIPs {
		if s, ok := m.latencies[ip]; ok {
			result[ip] = s
		}
	}
	return result, nil
}

func (m *mockLatencySource) Name() string            { return "mock" }
func (m *mockLatencySource) Ready(_ context.Context) bool { return true }

var _ = Describe("AviatorPolicy Controller", func() {
	const (
		policyName    = "test-policy"
		serviceName   = "test-service"
		testNamespace = "default"
		timeout       = time.Second * 10
		interval      = time.Millisecond * 250
	)

	ctx := context.Background()

	Context("When reconciling a resource", func() {
		var (
			policy    *aviatorv1alpha1.AviatorPolicy
			service   *corev1.Service
			mockSrc   *mockLatencySource
		)

		BeforeEach(func() {
			// Create test Service.
			service = &corev1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Name:      serviceName,
					Namespace: testNamespace,
				},
				Spec: corev1.ServiceSpec{
					Selector: map[string]string{"app": "test"},
					Ports: []corev1.ServicePort{
						{
							Name:     "http",
							Port:     80,
							Protocol: corev1.ProtocolTCP,
						},
					},
				},
			}
			err := k8sClient.Get(ctx, types.NamespacedName{Name: serviceName, Namespace: testNamespace}, service)
			if errors.IsNotFound(err) {
				Expect(k8sClient.Create(ctx, service)).To(Succeed())
			}

			// Create test AviatorPolicy.
			pct := int32(50)
			policy = &aviatorv1alpha1.AviatorPolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      policyName,
					Namespace: testNamespace,
				},
				Spec: aviatorv1alpha1.AviatorPolicySpec{
					TargetRef: aviatorv1alpha1.TargetRef{
						APIVersion: "v1",
						Kind:       "Service",
						Name:       serviceName,
					},
					LatencyThreshold:   metav1.Duration{Duration: 100 * time.Millisecond},
					EvaluationInterval: metav1.Duration{Duration: 5 * time.Second},
					Selection: aviatorv1alpha1.SelectionPolicy{
						Mode:       aviatorv1alpha1.SelectionModePercentage,
						Percentage: &pct,
					},
					LatencySource: aviatorv1alpha1.LatencySourceProbe,
				},
			}
			err = k8sClient.Get(ctx, types.NamespacedName{Name: policyName, Namespace: testNamespace}, &aviatorv1alpha1.AviatorPolicy{})
			if errors.IsNotFound(err) {
				Expect(k8sClient.Create(ctx, policy)).To(Succeed())
			}

			mockSrc = &mockLatencySource{
				latencies: map[string]latency.Stats{
					"10.0.0.1": {P50: 5 * time.Millisecond, P99: 10 * time.Millisecond, SampleCount: 100},
					"10.0.0.2": {P50: 50 * time.Millisecond, P99: 80 * time.Millisecond, SampleCount: 100},
					"10.0.0.3": {P50: 200 * time.Millisecond, P99: 500 * time.Millisecond, SampleCount: 100},
				},
			}
		})

		AfterEach(func() {
			// Cleanup.
			p := &aviatorv1alpha1.AviatorPolicy{}
			err := k8sClient.Get(ctx, types.NamespacedName{Name: policyName, Namespace: testNamespace}, p)
			if err == nil {
				// Remove finalizer to allow deletion.
				p.Finalizers = nil
				_ = k8sClient.Update(ctx, p)
				_ = k8sClient.Delete(ctx, p)
			}

			s := &corev1.Service{}
			err = k8sClient.Get(ctx, types.NamespacedName{Name: serviceName, Namespace: testNamespace}, s)
			if err == nil {
				_ = k8sClient.Delete(ctx, s)
			}
		})

		It("should successfully reconcile and add a finalizer", func() {
			esManager := endpointslice.NewManager(k8sClient, ctrl.Log)
			reconciler := NewReconciler(k8sClient, k8sClient.Scheme(), mockSrc, esManager)

			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: policyName, Namespace: testNamespace},
			})
			Expect(err).NotTo(HaveOccurred())

			// Verify finalizer was added.
			updated := &aviatorv1alpha1.AviatorPolicy{}
			Eventually(func() bool {
				err := k8sClient.Get(ctx, types.NamespacedName{Name: policyName, Namespace: testNamespace}, updated)
				if err != nil {
					return false
				}
				for _, f := range updated.Finalizers {
					if f == finalizerName {
						return true
					}
				}
				return false
			}, timeout, interval).Should(BeTrue())
		})

		It("should handle missing Service gracefully", func() {
			// Create policy pointing to non-existent service.
			badPolicy := &aviatorv1alpha1.AviatorPolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "bad-policy",
					Namespace: testNamespace,
				},
				Spec: aviatorv1alpha1.AviatorPolicySpec{
					TargetRef: aviatorv1alpha1.TargetRef{
						Name: "nonexistent-service",
					},
					LatencyThreshold:   metav1.Duration{Duration: 100 * time.Millisecond},
					EvaluationInterval: metav1.Duration{Duration: 5 * time.Second},
					LatencySource:      aviatorv1alpha1.LatencySourceProbe,
				},
			}
			Expect(k8sClient.Create(ctx, badPolicy)).To(Succeed())

			esManager := endpointslice.NewManager(k8sClient, ctrl.Log)
			reconciler := NewReconciler(k8sClient, k8sClient.Scheme(), mockSrc, esManager)

			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: "bad-policy", Namespace: testNamespace},
			})
			// Should not return error for missing service (ignores not found).
			Expect(err).NotTo(HaveOccurred())

			// Cleanup.
			badPolicy.Finalizers = nil
			_ = k8sClient.Update(ctx, badPolicy)
			_ = k8sClient.Delete(ctx, badPolicy)
		})
	})
})
