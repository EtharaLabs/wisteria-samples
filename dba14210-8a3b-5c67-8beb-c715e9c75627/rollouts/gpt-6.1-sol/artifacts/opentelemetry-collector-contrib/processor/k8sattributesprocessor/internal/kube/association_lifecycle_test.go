// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package kube

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/component/componenttest"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	metadatafake "k8s.io/client-go/metadata/fake"

	"github.com/open-telemetry/opentelemetry-collector-contrib/internal/k8sconfig"
)

// These tests deliberately observe associations rather than the delete queue:
// implementations may cancel pending deletes or validate them during cleanup.
type associationLifecycleCase struct {
	name       string
	rules      ExtractionRules
	sources    []AssociationSource
	setValue   func(*corev1.Pod, string)
	identifier func(string) PodIdentifier
}

func associationLifecycleCases() []associationLifecycleCase {
	resourceID := func(name string) func(string) PodIdentifier {
		return func(value string) PodIdentifier {
			return PodIdentifier{PodIdentifierAttributeFromResourceAttribute(name, value)}
		}
	}
	cases := []associationLifecycleCase{
		{
			name:       "label",
			rules:      ExtractionRules{Labels: []FieldExtractionRule{{Name: "association.key", Key: "association", From: MetadataFromPod}}},
			sources:    []AssociationSource{{From: ResourceSource, Name: "association.key"}},
			setValue:   func(p *corev1.Pod, value string) { p.Labels = map[string]string{"association": value} },
			identifier: resourceID("association.key"),
		},
		{
			name:       "annotation",
			rules:      ExtractionRules{Annotations: []FieldExtractionRule{{Name: "association.key", Key: "association", From: MetadataFromPod}}},
			sources:    []AssociationSource{{From: ResourceSource, Name: "association.key"}},
			setValue:   func(p *corev1.Pod, value string) { p.Annotations = map[string]string{"association": value} },
			identifier: resourceID("association.key"),
		},
		{
			name:       "pod_ip",
			sources:    []AssociationSource{{From: ResourceSource, Name: "k8s.pod.ip"}},
			setValue:   func(p *corev1.Pod, value string) { p.Status.PodIP = value },
			identifier: resourceID("k8s.pod.ip"),
		},
		{
			name:    "container_id",
			rules:   ExtractionRules{ContainerID: true},
			sources: []AssociationSource{{From: ResourceSource, Name: "container.id"}},
			setValue: func(p *corev1.Pod, value string) {
				p.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "app", ContainerID: "containerd://" + value}}
			},
			identifier: resourceID("container.id"),
		},
	}
	// A mutable source can also be part of a multi-source association.
	compound := cases[0]
	compound.name = "label_and_namespace"
	compound.rules.Namespace = true
	compound.sources = []AssociationSource{
		{From: ResourceSource, Name: "association.key"},
		{From: ResourceSource, Name: "k8s.namespace.name"},
	}
	compound.identifier = func(value string) PodIdentifier {
		return PodIdentifier{
			PodIdentifierAttributeFromResourceAttribute("association.key", value),
			PodIdentifierAttributeFromResourceAttribute("k8s.namespace.name", "default"),
		}
	}
	return append(cases, compound)
}

func newAssociationLifecycleClient(t *testing.T, tc associationLifecycleCase) (*WatchClient, *corev1.Pod) {
	t.Helper()
	provider := func(k8sconfig.APIConfig) (k8sconfig.ClientBundle, error) {
		return k8sconfig.ClientBundle{
			K8s:  fake.NewClientset(),
			Meta: metadatafake.NewSimpleMetadataClient(runtime.NewScheme()),
		}, nil
	}
	client, err := New(componenttest.NewNopTelemetrySettings(), k8sconfig.APIConfig{}, tc.rules,
		Filters{}, []Association{{Sources: tc.sources}}, Excludes{}, provider,
		InformersFactoryList{}, false, time.Second, 0, time.Hour)
	require.NoError(t, err)
	// Do not start informers or the periodic delete loop. Drive events and cleanup
	// synchronously so neither wall-clock scheduling nor a Kubernetes API is needed.
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "default", UID: "pod-uid"},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app"}}},
		Status:     corev1.PodStatus{PodIP: "10.0.0.1", StartTime: &metav1.Time{Time: time.Unix(1, 0)}},
	}
	return client.(*WatchClient), pod
}

func assertLifecycleAssociation(t *testing.T, c *WatchClient, id PodIdentifier, present bool) {
	t.Helper()
	pod, ok := c.GetPod(id)
	if !assert.Equal(t, present, ok, "association %v", id) || !present {
		return
	}
	require.NotNil(t, pod)
	assert.Equal(t, "pod-uid", pod.PodUID)
}

func TestAssociationLifecycleStaleIdentifiersAreReclaimed(t *testing.T) {
	for _, tc := range associationLifecycleCases() {
		t.Run(tc.name, func(t *testing.T) {
			c, pod := newAssociationLifecycleClient(t, tc)
			tc.setValue(pod, "10.1.0.1")
			c.handlePodAdd(pod)
			initialSize := len(c.Pods)
			// Repeated mutations must not leave one cache entry per historical value.
			for i := 2; i <= 8; i++ {
				previous := pod
				pod = pod.DeepCopy()
				oldValue, newValue := fmt.Sprintf("10.1.0.%d", i-1), fmt.Sprintf("10.1.0.%d", i)
				tc.setValue(pod, newValue)
				c.handlePodUpdate(previous, pod)
				c.deleteLoopProcessing(time.Hour)
				assertLifecycleAssociation(t, c, tc.identifier(oldValue), true)
				assertLifecycleAssociation(t, c, tc.identifier(newValue), true)
				// A zero grace period expires requests without sleeping or changing internals.
				c.deleteLoopProcessing(0)
				assertLifecycleAssociation(t, c, tc.identifier(oldValue), false)
				assertLifecycleAssociation(t, c, tc.identifier(newValue), true)
				assert.Len(t, c.Pods, initialSize, "cache must remain bounded after cleanup")
			}
			c.handlePodDelete(pod)
			c.deleteLoopProcessing(0)
			assert.Empty(t, c.Pods, "deleting the pod must also release historical associations")
		})
	}
}

func TestAssociationLifecycleRestoredIdentifierSurvivesCleanup(t *testing.T) {
	for _, tc := range associationLifecycleCases() {
		t.Run(tc.name, func(t *testing.T) {
			c, original := newAssociationLifecycleClient(t, tc)
			tc.setValue(original, "10.1.0.1")
			c.handlePodAdd(original)
			changed := original.DeepCopy()
			tc.setValue(changed, "10.1.0.2")
			c.handlePodUpdate(original, changed)
			c.deleteLoopProcessing(time.Hour)
			assertLifecycleAssociation(t, c, tc.identifier("10.1.0.1"), true)

			restored := original.DeepCopy()
			c.handlePodUpdate(changed, restored)
			c.deleteLoopProcessing(0)
			assertLifecycleAssociation(t, c, tc.identifier("10.1.0.1"), true)
			assertLifecycleAssociation(t, c, tc.identifier("10.1.0.2"), false)
			assertLifecycleAssociation(t, c, PodIdentifier{PodIdentifierAttributeFromResourceAttribute("k8s.pod.uid", "pod-uid")}, true)

			// Restoration must not permanently exempt the identifier from future cleanup.
			c.handlePodUpdate(restored, changed)
			c.deleteLoopProcessing(0)
			assertLifecycleAssociation(t, c, tc.identifier("10.1.0.1"), false)
			assertLifecycleAssociation(t, c, tc.identifier("10.1.0.2"), true)
			c.handlePodDelete(changed)
			c.deleteLoopProcessing(0)
			assert.Empty(t, c.Pods)
		})
	}
}
