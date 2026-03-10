/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package endpointslice

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	aviatorv1alpha1 "aviator/api/v1alpha1"
)

const (
	// ManagedByLabel identifies EndpointSlices managed by Aviator.
	ManagedByLabel = "endpointslice.kubernetes.io/managed-by"
	// ManagedByValue is the field manager identity for Aviator.
	ManagedByValue = "aviator-controller"
	// ServiceNameLabel links EndpointSlice to its Service.
	ServiceNameLabel = "kubernetes.io/service-name"
	// PolicyNameLabel links EndpointSlice to its AviatorPolicy.
	PolicyNameLabel = "aviator.io/policy-name"
	// AviatorManagedAnnotation marks a Service as managed by Aviator.
	AviatorManagedAnnotation = "aviator.io/managed"
)

// Manager handles creation and updates of Aviator-owned EndpointSlices.
type Manager struct {
	client client.Client
	log    logr.Logger
}

// NewManager creates a new EndpointSlice manager.
func NewManager(c client.Client, log logr.Logger) *Manager {
	return &Manager{
		client: c,
		log:    log.WithName("endpointslice-manager"),
	}
}

// PodEndpoint represents a pod that should be included in the EndpointSlice.
type PodEndpoint struct {
	PodName string
	PodIP   string
	NodeName string
	Ready   bool
}

// Reconcile creates or updates an Aviator-owned EndpointSlice for the given Service.
func (m *Manager) Reconcile(
	ctx context.Context,
	policy *aviatorv1alpha1.AviatorPolicy,
	service *corev1.Service,
	selectedPods []PodEndpoint,
) error {
	sliceName := fmt.Sprintf("aviator-%s", service.Name)

	// Build the desired EndpointSlice.
	desired := m.buildEndpointSlice(sliceName, policy, service, selectedPods)

	// Try to get the existing slice.
	existing := &discoveryv1.EndpointSlice{}
	err := m.client.Get(ctx, types.NamespacedName{
		Name:      sliceName,
		Namespace: service.Namespace,
	}, existing)

	if errors.IsNotFound(err) {
		m.log.Info("creating EndpointSlice", "name", sliceName, "endpoints", len(selectedPods))
		return m.client.Create(ctx, desired)
	}
	if err != nil {
		return fmt.Errorf("getting EndpointSlice: %w", err)
	}

	// Update existing slice.
	existing.Endpoints = desired.Endpoints
	existing.Ports = desired.Ports
	existing.Labels = desired.Labels

	m.log.Info("updating EndpointSlice", "name", sliceName, "endpoints", len(selectedPods))
	return m.client.Update(ctx, existing)
}

// Cleanup removes the Aviator-owned EndpointSlice for a Service.
func (m *Manager) Cleanup(ctx context.Context, namespace, serviceName string) error {
	sliceName := fmt.Sprintf("aviator-%s", serviceName)
	slice := &discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name:      sliceName,
			Namespace: namespace,
		},
	}

	err := m.client.Delete(ctx, slice)
	if errors.IsNotFound(err) {
		return nil
	}
	return err
}

func (m *Manager) buildEndpointSlice(
	name string,
	policy *aviatorv1alpha1.AviatorPolicy,
	service *corev1.Service,
	pods []PodEndpoint,
) *discoveryv1.EndpointSlice {
	addressType := discoveryv1.AddressTypeIPv4

	endpoints := make([]discoveryv1.Endpoint, 0, len(pods))
	for _, pod := range pods {
		ready := pod.Ready
		ep := discoveryv1.Endpoint{
			Addresses: []string{pod.PodIP},
			Conditions: discoveryv1.EndpointConditions{
				Ready: &ready,
			},
			TargetRef: &corev1.ObjectReference{
				Kind:      "Pod",
				Name:      pod.PodName,
				Namespace: service.Namespace,
			},
		}
		if pod.NodeName != "" {
			ep.NodeName = &pod.NodeName
		}
		endpoints = append(endpoints, ep)
	}

	// Copy ports from the Service spec.
	ports := make([]discoveryv1.EndpointPort, 0, len(service.Spec.Ports))
	for _, sp := range service.Spec.Ports {
		port := sp.TargetPort.IntVal
		if port == 0 {
			port = sp.Port
		}
		name := sp.Name
		protocol := sp.Protocol
		ports = append(ports, discoveryv1.EndpointPort{
			Name:     &name,
			Port:     &port,
			Protocol: &protocol,
		})
	}

	return &discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: service.Namespace,
			Labels: map[string]string{
				ManagedByLabel:   ManagedByValue,
				ServiceNameLabel: service.Name,
				PolicyNameLabel:  policy.Name,
			},
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: policy.APIVersion,
					Kind:       policy.Kind,
					Name:       policy.Name,
					UID:        policy.UID,
				},
			},
		},
		AddressType: addressType,
		Endpoints:   endpoints,
		Ports:       ports,
	}
}
