// Package launcher starts and stops a JetStream cluster from a server binary.
//
// It never imports server packages: every node is a separate process started
// from the binary under test, configured through a generated config file with
// free loopback ports, and observed through its client and monitoring ports.
package launcher

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Node is one server process of the cluster.
type Node struct {
	Index       int
	Name        string
	Tag         string
	ClientPort  int
	ClusterPort int
	HTTPPort    int
	Dir         string
	ConfPath    string
	LogPath     string

	mu   sync.Mutex
	cmd  *exec.Cmd
	done chan struct{}
	err  error
}

// Cluster is a set of nodes started from one binary under one temp root.
type Cluster struct {
	Binary string
	Root   string
	Name   string
	Nodes  []*Node
}

// freePorts reserves n distinct free loopback ports. The listeners are held
// until all ports are known so the same port is never handed out twice.
func freePorts(n int) ([]int, error) {
	listeners := make([]net.Listener, 0, n)
	defer func() {
		for _, l := range listeners {
			_ = l.Close()
		}
	}()
	ports := make([]int, 0, n)
	for range n {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return nil, fmt.Errorf("reserve port: %w", err)
		}
		listeners = append(listeners, l)
		addr, ok := l.Addr().(*net.TCPAddr)
		if !ok {
			return nil, errors.New("reserve port: unexpected address type")
		}
		ports = append(ports, addr.Port)
	}
	return ports, nil
}

// New writes per-node configs for an n-node cluster under root. A single
// node is configured without a cluster block.
func New(binary string, n int, root string) (*Cluster, error) {
	if n < 1 {
		return nil, errors.New("launcher: need at least one node")
	}
	info, err := os.Stat(binary)
	if err != nil {
		return nil, fmt.Errorf("launcher: server binary: %w", err)
	}
	if info.IsDir() || info.Mode()&0o111 == 0 {
		return nil, fmt.Errorf("launcher: server binary %s is not executable", binary)
	}
	ports, err := freePorts(3 * n)
	if err != nil {
		return nil, err
	}
	c := &Cluster{Binary: binary, Root: root, Name: "wb"}
	for i := range n {
		name := fmt.Sprintf("n%d", i+1)
		dir := filepath.Join(root, name)
		node := &Node{
			Index:       i,
			Name:        name,
			Tag:         "node:" + name,
			ClientPort:  ports[3*i],
			ClusterPort: ports[3*i+1],
			HTTPPort:    ports[3*i+2],
			Dir:         dir,
			ConfPath:    filepath.Join(dir, "server.conf"),
			LogPath:     filepath.Join(dir, "server.log"),
		}
		c.Nodes = append(c.Nodes, node)
	}
	for _, node := range c.Nodes {
		if err := os.MkdirAll(filepath.Join(node.Dir, "js"), 0o755); err != nil {
			return nil, fmt.Errorf("launcher: %w", err)
		}
		if err := os.WriteFile(node.ConfPath, []byte(c.config(node)), 0o644); err != nil {
			return nil, fmt.Errorf("launcher: %w", err)
		}
	}
	return c, nil
}

func (c *Cluster) config(node *Node) string {
	var b strings.Builder
	fmt.Fprintf(&b, "server_name: %s\n", node.Name)
	fmt.Fprintf(&b, "listen: 127.0.0.1:%d\n", node.ClientPort)
	fmt.Fprintf(&b, "http: 127.0.0.1:%d\n", node.HTTPPort)
	fmt.Fprintf(&b, "server_tags: [%q]\n", node.Tag)
	b.WriteString("jetstream {\n")
	fmt.Fprintf(&b, "  store_dir: %q\n", filepath.Join(node.Dir, "js"))
	b.WriteString("  max_memory_store: 268435456\n")
	b.WriteString("  max_file_store: 1073741824\n")
	b.WriteString("}\n")
	if len(c.Nodes) > 1 {
		b.WriteString("cluster {\n")
		fmt.Fprintf(&b, "  name: %s\n", c.Name)
		fmt.Fprintf(&b, "  listen: 127.0.0.1:%d\n", node.ClusterPort)
		b.WriteString("  routes: [\n")
		for _, other := range c.Nodes {
			fmt.Fprintf(&b, "    nats-route://127.0.0.1:%d\n", other.ClusterPort)
		}
		b.WriteString("  ]\n}\n")
	}
	return b.String()
}

// URL is the client URL of node i.
func (c *Cluster) URL(i int) string {
	return fmt.Sprintf("nats://127.0.0.1:%d", c.Nodes[i].ClientPort)
}

// URLs lists the client URLs of the given nodes, or of all when none given.
func (c *Cluster) URLs(indexes ...int) []string {
	if len(indexes) == 0 {
		for i := range c.Nodes {
			indexes = append(indexes, i)
		}
	}
	urls := make([]string, 0, len(indexes))
	for _, i := range indexes {
		urls = append(urls, c.URL(i))
	}
	return urls
}

// NodeByName returns the node with the given server name.
func (c *Cluster) NodeByName(name string) (*Node, bool) {
	for _, n := range c.Nodes {
		if n.Name == name {
			return n, true
		}
	}
	return nil, false
}

// StartNode starts the process of node i.
func (c *Cluster) StartNode(i int) error {
	node := c.Nodes[i]
	node.mu.Lock()
	defer node.mu.Unlock()
	if node.cmd != nil {
		select {
		case <-node.done:
		default:
			return fmt.Errorf("launcher: node %s already running", node.Name)
		}
	}
	logf, err := os.OpenFile(node.LogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("launcher: %w", err)
	}
	cmd := exec.Command(c.Binary, "-c", node.ConfPath)
	cmd.Stdout = logf
	cmd.Stderr = logf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		_ = logf.Close()
		return fmt.Errorf("launcher: start %s: %w", node.Name, err)
	}
	done := make(chan struct{})
	node.cmd, node.done, node.err = cmd, done, nil
	go func() {
		werr := cmd.Wait()
		_ = logf.Close()
		node.mu.Lock()
		node.err = werr
		node.mu.Unlock()
		close(done)
	}()
	return nil
}

// Start starts every node.
func (c *Cluster) Start() error {
	for i := range c.Nodes {
		if err := c.StartNode(i); err != nil {
			c.Shutdown()
			return err
		}
	}
	return nil
}

// Running reports whether node i's process is alive.
func (c *Cluster) Running(i int) bool {
	node := c.Nodes[i]
	node.mu.Lock()
	defer node.mu.Unlock()
	if node.cmd == nil {
		return false
	}
	select {
	case <-node.done:
		return false
	default:
		return true
	}
}

// StopNode asks node i to shut down gracefully and kills it if it has not
// exited within the grace period.
func (c *Cluster) StopNode(i int, grace time.Duration) error {
	node := c.Nodes[i]
	node.mu.Lock()
	cmd, done := node.cmd, node.done
	node.mu.Unlock()
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	select {
	case <-done:
		return nil
	default:
	}
	_ = cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-done:
		return nil
	case <-time.After(grace):
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	select {
	case <-done:
		return fmt.Errorf("launcher: node %s did not stop within %s and was killed", node.Name, grace)
	case <-time.After(5 * time.Second):
		return fmt.Errorf("launcher: node %s could not be stopped", node.Name)
	}
}

// Shutdown stops every node.
func (c *Cluster) Shutdown() {
	var wg sync.WaitGroup
	for i := range c.Nodes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = c.StopNode(i, 10*time.Second)
		}()
	}
	wg.Wait()
}

// Cleanup removes the temp root.
func (c *Cluster) Cleanup() error {
	return os.RemoveAll(c.Root)
}

func httpGet(ctx context.Context, url string) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, nil, err
	}
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	return resp.StatusCode, body, err
}

// Monitor fetches path from node i's monitoring port.
func (c *Cluster) Monitor(ctx context.Context, i int, path string) ([]byte, error) {
	url := fmt.Sprintf("http://127.0.0.1:%d%s", c.Nodes[i].HTTPPort, path)
	code, body, err := httpGet(ctx, url)
	if err != nil {
		return nil, err
	}
	if code != http.StatusOK {
		return body, fmt.Errorf("monitor %s: status %d", path, code)
	}
	return body, nil
}

// WaitNodeHealthy waits until node i reports healthy, JetStream included.
func (c *Cluster) WaitNodeHealthy(ctx context.Context, i int) error {
	for {
		if !c.Running(i) {
			node := c.Nodes[i]
			node.mu.Lock()
			err := node.err
			node.mu.Unlock()
			return fmt.Errorf("launcher: node %s exited: %v", node.Name, err)
		}
		if _, err := c.Monitor(ctx, i, "/healthz"); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("launcher: node %s not healthy: %w", c.Nodes[i].Name, ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// MetaLeader returns the meta leader as seen by node i.
func (c *Cluster) MetaLeader(ctx context.Context, i int) (string, error) {
	if len(c.Nodes) == 1 {
		return c.Nodes[0].Name, nil
	}
	body, err := c.Monitor(ctx, i, "/jsz")
	if err != nil {
		return "", err
	}
	var jsz struct {
		Meta *struct {
			Leader string `json:"leader"`
		} `json:"meta_cluster"`
	}
	if err := json.Unmarshal(body, &jsz); err != nil {
		return "", err
	}
	if jsz.Meta == nil || jsz.Meta.Leader == "" {
		return "", errors.New("no meta leader")
	}
	return jsz.Meta.Leader, nil
}

// WaitReady waits until every running node is healthy and all of them agree
// on one meta leader.
func (c *Cluster) WaitReady(ctx context.Context) (string, error) {
	for i := range c.Nodes {
		if !c.Running(i) {
			continue
		}
		if err := c.WaitNodeHealthy(ctx, i); err != nil {
			return "", err
		}
	}
	for {
		leaders := map[string]bool{}
		var lastErr error
		for i := range c.Nodes {
			if !c.Running(i) {
				continue
			}
			leader, err := c.MetaLeader(ctx, i)
			if err != nil {
				lastErr = err
				continue
			}
			leaders[leader] = true
		}
		if lastErr == nil && len(leaders) == 1 {
			for leader := range leaders {
				return leader, nil
			}
		}
		select {
		case <-ctx.Done():
			return "", fmt.Errorf("launcher: no agreed meta leader: %v (%w)", lastErr, ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// SubDetail is one subscription reported by a node's /subsz endpoint.
type SubDetail struct {
	Account string `json:"account"`
	Subject string `json:"subject"`
	Sid     string `json:"sid"`
	Msgs    int64  `json:"msgs"`
	Cid     uint64 `json:"cid"`
}

// LocalSubs lists node i's local subscriptions a publish on subject would match.
func (c *Cluster) LocalSubs(ctx context.Context, i int, subject string) ([]SubDetail, error) {
	body, err := c.Monitor(ctx, i, "/subsz?subs=1&limit=4096&test="+subject)
	if err != nil {
		return nil, err
	}
	var subsz struct {
		Subs []SubDetail `json:"subscriptions_list"`
	}
	if err := json.Unmarshal(body, &subsz); err != nil {
		return nil, err
	}
	return subsz.Subs, nil
}
