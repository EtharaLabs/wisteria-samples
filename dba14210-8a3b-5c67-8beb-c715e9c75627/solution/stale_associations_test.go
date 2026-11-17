// Tests for stale pod identifiers in the WatchClient cache: cleanup after the grace period and reactivation.

package kube

import (
	goruntime "runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/component/componenttest"
	api_v1 "k8s.io/api/core/v1"
	meta_v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	clientmetafake "k8s.io/client-go/metadata/fake"

	"github.com/open-telemetry/opentelemetry-collector-contrib/internal/k8sconfig"
)

// staleAssocGracePeriod is any grace period long enough that no queued cleanup
// can have expired yet, so a sweep with it must be a no-op.
const staleAssocGracePeriod = time.Hour

// staleAssocNewWatchClient builds a WatchClient through the exported constructor
// with a zero-valued informer factory list, so no informer is ever needed.
func staleAssocNewWatchClient(t *testing.T, rules ExtractionRules, assocs []Association) *WatchClient {
	t.Helper()
	provider := func(_ k8sconfig.APIConfig) (k8sconfig.ClientBundle, error) {
		return k8sconfig.ClientBundle{
			K8s:  fake.NewClientset(),
			Meta: clientmetafake.NewSimpleMetadataClient(runtime.NewScheme()),
		}, nil
	}
	client, err := New(
		componenttest.NewNopTelemetrySettings(),
		k8sconfig.APIConfig{},
		rules,
		Filters{},
		assocs,
		Excludes{},
		provider,
		InformersFactoryList{},
		false,
		10*time.Second,
		0,
		120*time.Second,
	)
	require.NoError(t, err)
	watchClient, ok := client.(*WatchClient)
	require.True(t, ok)
	return watchClient
}

// staleAssocKey builds the cache key for one association source and value.
func staleAssocKey(from, name, value string) PodIdentifier {
	if from == ConnectionSource {
		name = ""
	}
	return PodIdentifier{{Source: AssociationSource{From: from, Name: name}, Value: value}}
}

func staleAssocUIDKey(uid string) PodIdentifier {
	return staleAssocKey(ResourceSource, "k8s.pod.uid", uid)
}

// staleAssocBuildPod builds an api pod. startTime orders concurrent updates.
func staleAssocBuildPod(uid, ip string, startTime int64, labels, annotations map[string]string, containerIDs ...string) *api_v1.Pod {
	started := meta_v1.NewTime(time.Unix(startTime, 0))
	pod := &api_v1.Pod{
		ObjectMeta: meta_v1.ObjectMeta{
			Name:        "pod-" + uid,
			Namespace:   "ns-stale-assoc",
			UID:         types.UID(uid),
			Labels:      labels,
			Annotations: annotations,
		},
		Status: api_v1.PodStatus{PodIP: ip, StartTime: &started},
	}
	for _, containerID := range containerIDs {
		pod.Status.ContainerStatuses = append(pod.Status.ContainerStatuses, api_v1.ContainerStatus{
			Name:        "container-" + containerID,
			ContainerID: "containerd://" + containerID,
		})
	}
	return pod
}

// staleAssocResolves reports whether the key currently resolves to a pod.
func staleAssocResolves(client *WatchClient, key PodIdentifier) bool {
	_, ok := client.GetPod(key)
	return ok
}

func staleAssocLabelRules() ExtractionRules {
	return ExtractionRules{
		Labels: []FieldExtractionRule{{Name: "team", Key: "team", From: MetadataFromPod}},
	}
}

func staleAssocLabelAssociations() []Association {
	return []Association{{Sources: []AssociationSource{{From: ResourceSource, Name: "team"}}}}
}

// T1a: a label used as an association source is removed; the key must still
// resolve inside the grace period.
func TestStaleLabelKeyResolvesInsideGracePeriod(t *testing.T) {
	client := staleAssocNewWatchClient(t, staleAssocLabelRules(), staleAssocLabelAssociations())
	labelKey := staleAssocKey(ResourceSource, "team", "alpha")

	client.handlePodAdd(staleAssocBuildPod("uid-t1a", "", 10, map[string]string{"team": "alpha"}, nil))
	require.True(t, staleAssocResolves(client, labelKey))

	client.handlePodUpdate(nil, staleAssocBuildPod("uid-t1a", "", 10, nil, nil))
	assert.True(t, staleAssocResolves(client, labelKey),
		"the removed label key must still resolve right after the update")

	client.deleteLoopProcessing(staleAssocGracePeriod)
	assert.True(t, staleAssocResolves(client, labelKey),
		"the removed label key must still resolve while the grace period has not elapsed")
}

// T1b: two label association sources are configured and only the second one's
// label is removed; its key is evicted once the grace period elapses, while the
// key of the first association, whose label is still present, keeps resolving.
func TestStaleLabelKeyEvictedAfterGracePeriod(t *testing.T) {
	rules := ExtractionRules{
		Labels: []FieldExtractionRule{
			{Name: "team", Key: "team", From: MetadataFromPod},
			{Name: "tier", Key: "tier", From: MetadataFromPod},
		},
	}
	assocs := []Association{
		{Sources: []AssociationSource{{From: ResourceSource, Name: "team"}}},
		{Sources: []AssociationSource{{From: ResourceSource, Name: "tier"}}},
	}
	client := staleAssocNewWatchClient(t, rules, assocs)
	keptKey := staleAssocKey(ResourceSource, "team", "alpha")
	removedKey := staleAssocKey(ResourceSource, "tier", "backend")

	client.handlePodAdd(staleAssocBuildPod("uid-t1b", "", 10,
		map[string]string{"team": "alpha", "tier": "backend"}, nil))
	require.True(t, staleAssocResolves(client, keptKey))
	require.True(t, staleAssocResolves(client, removedKey))

	client.handlePodUpdate(nil, staleAssocBuildPod("uid-t1b", "", 10,
		map[string]string{"team": "alpha"}, nil))
	client.deleteLoopProcessing(0)

	assert.False(t, staleAssocResolves(client, removedKey),
		"the removed label key of the second association must be evicted once the grace period elapsed")
	assert.True(t, staleAssocResolves(client, keptKey),
		"the label key of the first association must keep resolving")
}

// T1c: after that eviction the pod UID key and every live key still resolve.
func TestStaleLabelEvictionKeepsLiveKeys(t *testing.T) {
	client := staleAssocNewWatchClient(t, staleAssocLabelRules(), []Association{
		{Sources: []AssociationSource{{From: ResourceSource, Name: "team"}}},
		{Sources: []AssociationSource{{From: ConnectionSource}}},
	})

	client.handlePodAdd(staleAssocBuildPod("uid-t1c", "10.1.0.7", 10, map[string]string{"team": "alpha"}, nil))
	require.True(t, staleAssocResolves(client, staleAssocKey(ResourceSource, "team", "alpha")))

	client.handlePodUpdate(nil, staleAssocBuildPod("uid-t1c", "10.1.0.7", 10, nil, nil))
	client.deleteLoopProcessing(0)

	assert.True(t, staleAssocResolves(client, staleAssocUIDKey("uid-t1c")),
		"the pod UID key must survive the eviction of a stale label key")
	assert.True(t, staleAssocResolves(client, staleAssocKey(ConnectionSource, "", "10.1.0.7")),
		"the live connection key must survive the eviction of a stale label key")
	assert.True(t, staleAssocResolves(client, staleAssocKey(ResourceSource, "k8s.pod.ip", "10.1.0.7")),
		"the live k8s.pod.ip key must survive the eviction of a stale label key")
}

// T2: a label value changes from alpha to beta; alpha is evicted after the
// grace period and beta resolves.
func TestStaleLabelValueChangeEvictsOldValue(t *testing.T) {
	client := staleAssocNewWatchClient(t, staleAssocLabelRules(), staleAssocLabelAssociations())
	oldKey := staleAssocKey(ResourceSource, "team", "alpha")
	newKey := staleAssocKey(ResourceSource, "team", "beta")

	client.handlePodAdd(staleAssocBuildPod("uid-t2", "", 10, map[string]string{"team": "alpha"}, nil))
	require.True(t, staleAssocResolves(client, oldKey))

	client.handlePodUpdate(nil, staleAssocBuildPod("uid-t2", "", 10, map[string]string{"team": "beta"}, nil))
	client.deleteLoopProcessing(0)

	assert.False(t, staleAssocResolves(client, oldKey), "the superseded label value must be evicted")
	assert.True(t, staleAssocResolves(client, newKey), "the current label value must resolve")
}

// T3: an annotation used as an association source is removed; its key is
// evicted after the grace period.
func TestStaleAnnotationKeyEvictedAfterGracePeriod(t *testing.T) {
	rules := ExtractionRules{
		Annotations: []FieldExtractionRule{{Name: "owner", Key: "owner", From: MetadataFromPod}},
	}
	assocs := []Association{{Sources: []AssociationSource{{From: ResourceSource, Name: "owner"}}}}
	client := staleAssocNewWatchClient(t, rules, assocs)
	annotationKey := staleAssocKey(ResourceSource, "owner", "team-x")

	client.handlePodAdd(staleAssocBuildPod("uid-t3", "", 10, nil, map[string]string{"owner": "team-x"}))
	require.True(t, staleAssocResolves(client, annotationKey))

	client.handlePodUpdate(nil, staleAssocBuildPod("uid-t3", "", 10, nil, nil))
	client.deleteLoopProcessing(0)

	assert.False(t, staleAssocResolves(client, annotationKey),
		"the removed annotation key must be evicted once the grace period elapsed")
}

// T4: a label value changes and then the pod is deleted; after the sweeps no
// key of that pod remains in the cache.
func TestStaleLabelChangeThenDeleteLeavesNoKey(t *testing.T) {
	client := staleAssocNewWatchClient(t, staleAssocLabelRules(), []Association{
		{Sources: []AssociationSource{{From: ResourceSource, Name: "team"}}},
		{Sources: []AssociationSource{{From: ConnectionSource}}},
	})

	client.handlePodAdd(staleAssocBuildPod("uid-t4", "10.2.0.3", 10, map[string]string{"team": "alpha"}, nil))
	client.handlePodUpdate(nil, staleAssocBuildPod("uid-t4", "10.2.0.3", 10, map[string]string{"team": "beta"}, nil))
	client.handlePodDelete(staleAssocBuildPod("uid-t4", "10.2.0.3", 10, map[string]string{"team": "beta"}, nil))
	client.deleteLoopProcessing(0)
	client.deleteLoopProcessing(0)

	assert.Empty(t, client.Pods,
		"no key of a deleted pod may remain in the cache, including keys of superseded label values")
}

// T5: the pod IP changes; the old connection and k8s.pod.ip keys are evicted
// after the grace period.
func TestStalePodIPKeysEvictedAfterGracePeriod(t *testing.T) {
	client := staleAssocNewWatchClient(t, ExtractionRules{}, []Association{
		{Sources: []AssociationSource{{From: ConnectionSource}}},
	})

	client.handlePodAdd(staleAssocBuildPod("uid-t5", "10.3.0.1", 10, nil, nil))
	require.True(t, staleAssocResolves(client, staleAssocKey(ConnectionSource, "", "10.3.0.1")))

	client.handlePodUpdate(nil, staleAssocBuildPod("uid-t5", "10.3.0.2", 10, nil, nil))
	client.deleteLoopProcessing(0)

	assert.False(t, staleAssocResolves(client, staleAssocKey(ConnectionSource, "", "10.3.0.1")),
		"the superseded connection key must be evicted")
	assert.False(t, staleAssocResolves(client, staleAssocKey(ResourceSource, "k8s.pod.ip", "10.3.0.1")),
		"the superseded k8s.pod.ip key must be evicted")
	assert.True(t, staleAssocResolves(client, staleAssocKey(ConnectionSource, "", "10.3.0.2")),
		"the current connection key must resolve")
}

// T6: a container.id goes active, stale and active again inside the grace
// period; it resolves after the sweep.
func TestReactivatedContainerIDSurvivesSweep(t *testing.T) {
	client := staleAssocNewWatchClient(t, ExtractionRules{ContainerID: true}, []Association{
		{Sources: []AssociationSource{{From: ResourceSource, Name: "container.id"}}},
	})
	containerKey := staleAssocKey(ResourceSource, "container.id", "cid-t6")

	client.handlePodAdd(staleAssocBuildPod("uid-t6", "", 10, nil, nil, "cid-t6"))
	require.True(t, staleAssocResolves(client, containerKey))

	client.handlePodUpdate(nil, staleAssocBuildPod("uid-t6", "", 10, nil, nil))
	client.handlePodUpdate(nil, staleAssocBuildPod("uid-t6", "", 10, nil, nil, "cid-t6"))
	client.deleteLoopProcessing(0)

	assert.True(t, staleAssocResolves(client, containerKey),
		"a container.id that became active again inside the grace period must not be evicted")
}

// T7: a label goes active, stale and active again inside the grace period; it
// resolves after the sweep, and when removed again it is evicted after grace.
func TestReactivatedLabelSurvivesSweepThenEvictedWhenRemovedAgain(t *testing.T) {
	client := staleAssocNewWatchClient(t, staleAssocLabelRules(), staleAssocLabelAssociations())
	labelKey := staleAssocKey(ResourceSource, "team", "alpha")

	client.handlePodAdd(staleAssocBuildPod("uid-t7", "", 10, map[string]string{"team": "alpha"}, nil))
	require.True(t, staleAssocResolves(client, labelKey))

	client.handlePodUpdate(nil, staleAssocBuildPod("uid-t7", "", 10, nil, nil))
	client.handlePodUpdate(nil, staleAssocBuildPod("uid-t7", "", 10, map[string]string{"team": "alpha"}, nil))
	client.deleteLoopProcessing(0)
	assert.True(t, staleAssocResolves(client, labelKey),
		"a label that became active again inside the grace period must not be evicted")

	client.handlePodUpdate(nil, staleAssocBuildPod("uid-t7", "", 10, nil, nil))
	client.deleteLoopProcessing(0)
	assert.False(t, staleAssocResolves(client, labelKey),
		"a label removed again after a re-activation must still be evicted after the grace period")
}

// T8: a label value shared with another pod is dropped by one pod; the key
// still resolves, to the other pod, after the sweep.
func TestSharedLabelValueKeptForOtherPod(t *testing.T) {
	client := staleAssocNewWatchClient(t, staleAssocLabelRules(), staleAssocLabelAssociations())
	sharedKey := staleAssocKey(ResourceSource, "team", "alpha")

	client.handlePodAdd(staleAssocBuildPod("uid-t8-first", "", 10, map[string]string{"team": "alpha"}, nil))
	client.handlePodAdd(staleAssocBuildPod("uid-t8-second", "", 20, map[string]string{"team": "alpha"}, nil))
	owner, ok := client.GetPod(sharedKey)
	require.True(t, ok)
	require.Equal(t, "uid-t8-second", owner.PodUID)

	client.handlePodUpdate(nil, staleAssocBuildPod("uid-t8-first", "", 10, nil, nil))
	client.deleteLoopProcessing(0)

	owner, ok = client.GetPod(sharedKey)
	require.True(t, ok, "a shared label key owned by another pod must not be evicted")
	assert.Equal(t, "uid-t8-second", owner.PodUID,
		"the shared label key must still resolve to the pod that still carries the label")
}

// T9: an out-of-order update with an older start time that lacks the label
// triggers no cleanup.
func TestOutOfOrderUpdateTriggersNoCleanup(t *testing.T) {
	client := staleAssocNewWatchClient(t, staleAssocLabelRules(), staleAssocLabelAssociations())
	labelKey := staleAssocKey(ResourceSource, "team", "alpha")

	client.handlePodAdd(staleAssocBuildPod("uid-t9", "", 50, map[string]string{"team": "alpha"}, nil))
	require.True(t, staleAssocResolves(client, labelKey))

	client.handlePodUpdate(nil, staleAssocBuildPod("uid-t9", "", 10, nil, nil))
	client.deleteLoopProcessing(0)

	assert.True(t, staleAssocResolves(client, labelKey),
		"an update that was not applied because it is older must not evict the current label key")
	assert.True(t, staleAssocResolves(client, staleAssocUIDKey("uid-t9")),
		"the pod UID key must survive an out-of-order update")
}

// T10: an identical resync after the removal does not rescue the stale key.
func TestResyncAfterRemovalDoesNotRescueStaleKey(t *testing.T) {
	client := staleAssocNewWatchClient(t, staleAssocLabelRules(), staleAssocLabelAssociations())
	labelKey := staleAssocKey(ResourceSource, "team", "alpha")

	client.handlePodAdd(staleAssocBuildPod("uid-t10", "", 10, map[string]string{"team": "alpha"}, nil))
	require.True(t, staleAssocResolves(client, labelKey))

	client.handlePodUpdate(nil, staleAssocBuildPod("uid-t10", "", 10, nil, nil))
	client.handlePodUpdate(nil, staleAssocBuildPod("uid-t10", "", 10, nil, nil))
	client.deleteLoopProcessing(0)

	assert.False(t, staleAssocResolves(client, labelKey),
		"an identical resync must not keep a stale label key alive")
}

// staleAssocLockWaitBound bounds each wait for an expected goroutine state. It
// is generous because the suite also runs under emulation. When it passes, the
// test goes on without forcing the interleaving and logs forced=false.
const staleAssocLockWaitBound = 10 * time.Second

// staleAssocWatchdog bounds the wait for the racing calls to return once the
// cache lock is released, so a deadlock fails the test instead of hanging it.
const staleAssocWatchdog = 30 * time.Second

// staleAssocGoroutineID returns the id of the calling goroutine, read from the
// header of its own stack trace ("goroutine 42 [running]:").
func staleAssocGoroutineID() string {
	buf := make([]byte, 64)
	fields := strings.Fields(string(buf[:goruntime.Stack(buf, false)]))
	if len(fields) < 2 {
		return ""
	}
	return fields[1]
}

// staleAssocInLockWait reports whether goroutine id is parked acquiring a
// sync.Mutex or sync.RWMutex while (*WatchClient).fn is on its stack.
func staleAssocInLockWait(id, fn string) bool {
	buf := make([]byte, 64<<10)
	for {
		n := goruntime.Stack(buf, true)
		if n < len(buf) {
			buf = buf[:n]
			break
		}
		buf = make([]byte, 2*len(buf))
	}
	for _, trace := range strings.Split(string(buf), "\n\n") {
		header, frames, _ := strings.Cut(trace, "\n")
		if !strings.HasPrefix(header, "goroutine "+id+" ") {
			continue
		}
		_, state, _ := strings.Cut(header, "[")
		parked := strings.HasPrefix(state, "sync.Mutex.Lock") ||
			strings.HasPrefix(state, "sync.RWMutex.Lock") ||
			strings.HasPrefix(state, "sync.RWMutex.RLock") ||
			strings.HasPrefix(state, "semacquire")
		acquiring := strings.Contains(frames, "sync.(*Mutex).Lock(") ||
			strings.Contains(frames, "sync.(*RWMutex).Lock(") ||
			strings.Contains(frames, "sync.(*RWMutex).RLock(")
		return parked && acquiring && strings.Contains(frames, "(*WatchClient)."+fn+"(")
	}
	return false
}

// staleAssocAwaitLockWait waits until the goroutine whose id arrives on ids is
// in a lock wait inside (*WatchClient).fn. It reports false when that state is
// not seen within staleAssocLockWaitBound or the goroutine returned first.
func staleAssocAwaitLockWait(ids <-chan string, done <-chan struct{}, fn string) bool {
	timeout := time.After(staleAssocLockWaitBound)
	var id string
	select {
	case id = <-ids:
	case <-timeout:
		return false
	}
	for !staleAssocInLockWait(id, fn) {
		select {
		case <-done:
			return false
		case <-timeout:
			return false
		case <-time.After(time.Millisecond):
		}
	}
	return true
}

// staleAssocRaceUpdateAndSweep runs handlePodUpdate(nil, update) and
// deleteLoopProcessing(0) concurrently and forces, when it can, the real
// interleaving in which the update takes the cache lock before the sweep.
// The test holds c.m for reading only: the update's own read locks still
// succeed, so it parks on c.m as the pending writer; the sweep, started next,
// parks behind that writer (a sweep that dequeues expired requests before it
// takes c.m has dequeued them by then); releasing the read lock hands c.m to
// the update first. The test never touches c.Pods or any queue itself.
func staleAssocRaceUpdateAndSweep(t *testing.T, client *WatchClient, update *api_v1.Pod) {
	t.Helper()
	client.m.RLock()
	held := true
	defer func() {
		if held {
			client.m.RUnlock()
		}
	}()

	updateIDs, updateDone := make(chan string, 1), make(chan struct{})
	go func() {
		defer close(updateDone)
		updateIDs <- staleAssocGoroutineID()
		client.handlePodUpdate(nil, update)
	}()
	forced := staleAssocAwaitLockWait(updateIDs, updateDone, "handlePodUpdate")

	sweepIDs, sweepDone := make(chan string, 1), make(chan struct{})
	go func() {
		defer close(sweepDone)
		sweepIDs <- staleAssocGoroutineID()
		client.deleteLoopProcessing(0)
	}()
	forced = staleAssocAwaitLockWait(sweepIDs, sweepDone, "deleteLoopProcessing") && forced

	client.m.RUnlock()
	held = false
	t.Logf("forced=%t", forced)

	watchdog := time.After(staleAssocWatchdog)
	for _, call := range []struct {
		name string
		done chan struct{}
	}{{"handlePodUpdate", updateDone}, {"deleteLoopProcessing", sweepDone}} {
		select {
		case <-call.done:
		case <-watchdog:
			t.Fatalf("%s did not return within %v of the cache lock being released: "+
				"the re-activating update and the concurrent sweep deadlock", call.name, staleAssocWatchdog)
		}
	}
}

// T11: an annotation goes active, stale and active again while a sweep for
// which the grace period has elapsed runs concurrently with the restoring
// update, and the update takes the cache lock first; the restored key still
// resolves once both calls have returned.
func TestStaleAnnotationKeyReactivatedDuringConcurrentSweepSurvives(t *testing.T) {
	rules := ExtractionRules{
		Annotations: []FieldExtractionRule{{Name: "owner", Key: "owner", From: MetadataFromPod}},
	}
	assocs := []Association{{Sources: []AssociationSource{{From: ResourceSource, Name: "owner"}}}}
	client := staleAssocNewWatchClient(t, rules, assocs)
	annotationKey := staleAssocKey(ResourceSource, "owner", "team-x")

	client.handlePodAdd(staleAssocBuildPod("uid-t11", "", 10, nil, map[string]string{"owner": "team-x"}))
	require.True(t, staleAssocResolves(client, annotationKey))
	client.handlePodUpdate(nil, staleAssocBuildPod("uid-t11", "", 10, nil, nil))

	staleAssocRaceUpdateAndSweep(t, client,
		staleAssocBuildPod("uid-t11", "", 10, nil, map[string]string{"owner": "team-x"}))

	assert.True(t, staleAssocResolves(client, annotationKey),
		"an annotation that became active again while a concurrent sweep waited for the cache lock must not be evicted by that sweep")
}
