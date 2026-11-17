The following text contains a user issue (in <issue/> brackets) posted at a repository. Your task is to identify the issue and implement a test case that verifies a proposed solution to this issue. More details at the end of this text.
<issue>
[processor/k8sattributes] Fix memory leak and incorrect deletion for active-stale-active identifiers

### Component(s)

processor/k8sattributes

## Is your feature request related to a problem? Please describe.

Recently, we observed an unbounded memory growth issue. For example, during container restarts, entries are created for the new `container.id`, but the old, invalid ones are never cleared from memory. We are currently addressing this primary issue in PR #48398.

We still have two related issues:

### 1. Memory leak
The above PR only solves the leak for container.id, but we likely have the same issue for custom association sources (such as a label or an annotation).

### 2. Identifiers that go from active -> stale -> active
While working on that fix, we identified another edge case that we'll hit when we fix the memory leak for other identifiers:

When an update temporarily drops an identifier, it queues a delayed delete. However, if a later update restores the same identifier for the same pod UID, that pending request is never removed. The `deleteLoopProcessing` only checks that the cached pod still has the same UID before deleting. As a result, after the grace period expires, it will remove an active association (for example, a `container.id` or pod IP that disappeared in one informer update and reappeared before cleanup). This breaks lookups until another update repopulates it.

For `container.id`, this isn't really expected to happen, because when a container restarts, it gets a completely different ID than it had before. However, this can definitely happen for custom association sources. If someone configures one against a mutable pod field (like a label or an annotation), the same value can theoretically come and go over a pod's lifetime.

After discussing with @TylerHelmuth and @Gandem , we decided to scope the PR to the container.id case, but we should definitely follow up on this.

### Describe the solution you'd like

We need to rethink the current design to handle this properly.
I would be happy to implement this and open a PR to fix it. Let me know if this approach works for you, or if you have a better way to handle it in mind! I'm totally open to feedback.

### Describe alternatives you've considered

_No response_

### Additional context

_No response_

### Tip

<sub>React with 👍 to help prioritize this issue. Please use comments to provide useful context, avoiding `+1` or `me too`, to help us triage it.</sub>
</issue>
Please generate test cases that check whether an implemented solution resolves the issue of the user (at the top, within <issue/> brackets).
You may apply changes to several files.
Apply as much reasoning as you please and see necessary.
Make sure to implement only test cases and don't try to fix the issue itself.
You are not allowed to read git history.

The repository is checked out at /app/opentelemetry-collector-contrib in the state the issue describes. Write your tests as Go test files in /app/opentelemetry-collector-contrib/processor/k8sattributesprocessor/internal/kube/: they must consist of *_test.go files directly in that directory, plus any fixtures under /app/opentelemetry-collector-contrib/processor/k8sattributesprocessor/internal/kube/testdata/, and must not rely on changes to any other file. Besides the package's exported API, your tests may use only these unexported members of *WatchClient, with these signatures: the field m sync.RWMutex, and the methods handlePodAdd(obj any), handlePodUpdate(_, newPod any), handlePodDelete(obj any), and deleteLoopProcessing(gracePeriod time.Duration). That directory must not contain repository metadata (such as a .git entry), prebuilt or generated binaries, coverage profiles, archives or compressed files, or files with an executable bit set, and your tests must not write files outside the repository and their own temporary directories. One `go test -count=1 ./internal/kube/...` run from /app/opentelemetry-collector-contrib/processor/k8sattributesprocessor must finish within 120 seconds, and every test must give the same result each time it runs. The module builds and tests offline with its vendored dependencies.
