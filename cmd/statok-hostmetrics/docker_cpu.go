package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	statok "github.com/prostoteam/statokgo"
)

type dockerLabelMode int

const (
	dockerLabelService dockerLabelMode = iota
	dockerLabelContainer
)

type dockerCPUCollector struct {
	client        *http.Client
	baseURL       string
	labelMode     dockerLabelMode
	labelKey      string
	maxContainers int
	concurrency   int
	timeout       time.Duration

	mu         sync.Mutex
	prev       map[string]dockerCPUPrev
	lastErrLog time.Time
}

type dockerCPUPrev struct {
	totalUsage  uint64
	systemUsage uint64
}

func defaultDockerSock() string {
	raw := strings.TrimSpace(os.Getenv("DOCKER_HOST"))
	if raw == "" {
		return "/var/run/docker.sock"
	}
	u, err := url.Parse(raw)
	if err == nil && u.Scheme == "unix" && u.Path != "" {
		return u.Path
	}
	if strings.HasPrefix(raw, "/") {
		return raw
	}
	return "/var/run/docker.sock"
}

func newDockerCPUCollector(endpoint, labelMode string, maxContainers, concurrency int, timeout time.Duration) (*dockerCPUCollector, error) {
	if strings.TrimSpace(endpoint) == "" {
		return nil, errors.New("empty docker endpoint")
	}

	mode, key, err := parseDockerLabelMode(labelMode)
	if err != nil {
		return nil, err
	}
	if maxContainers < 1 {
		maxContainers = 1
	}
	if concurrency < 1 {
		concurrency = 1
	}
	if concurrency > 32 {
		concurrency = 32
	}
	if timeout <= 0 {
		timeout = 5 * time.Second
	}

	client, baseURL, err := dockerHTTPClient(endpoint)
	if err != nil {
		return nil, err
	}

	return &dockerCPUCollector{
		client:        client,
		baseURL:       baseURL,
		labelMode:     mode,
		labelKey:      key,
		maxContainers: maxContainers,
		concurrency:   concurrency,
		timeout:       timeout,
		prev:          make(map[string]dockerCPUPrev),
	}, nil
}

func parseDockerLabelMode(s string) (dockerLabelMode, string, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "service":
		return dockerLabelService, "service", nil
	case "container":
		return dockerLabelContainer, "container", nil
	default:
		return 0, "", fmt.Errorf("invalid --docker-label %q (expected service or container)", s)
	}
}

func dockerHTTPClient(endpoint string) (*http.Client, string, error) {
	endpoint = strings.TrimSpace(endpoint)

	if strings.HasPrefix(endpoint, "/") {
		return dockerUnixClient(endpoint), "http://docker", nil
	}

	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme == "" {
		return nil, "", fmt.Errorf("invalid docker endpoint %q", endpoint)
	}

	switch u.Scheme {
	case "unix":
		if u.Path == "" {
			return nil, "", fmt.Errorf("invalid unix docker endpoint %q", endpoint)
		}
		return dockerUnixClient(u.Path), "http://docker", nil
	case "tcp":
		if u.Host == "" {
			return nil, "", fmt.Errorf("invalid tcp docker endpoint %q", endpoint)
		}
		return dockerTCPClient(), "http://" + u.Host, nil
	case "http", "https":
		if u.Host == "" {
			return nil, "", fmt.Errorf("invalid http docker endpoint %q", endpoint)
		}
		return dockerTCPClient(), u.Scheme + "://" + u.Host, nil
	default:
		return nil, "", fmt.Errorf("unsupported docker endpoint scheme %q", u.Scheme)
	}
}

func dockerUnixClient(sockPath string) *http.Client {
	dialer := &net.Dialer{Timeout: 2 * time.Second}
	transport := &http.Transport{
		Proxy:               nil,
		DisableCompression:  true,
		MaxIdleConns:        8,
		MaxIdleConnsPerHost: 8,
		IdleConnTimeout:     30 * time.Second,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, "unix", sockPath)
		},
	}

	return &http.Client{Transport: transport}
}

func dockerTCPClient() *http.Client {
	transport := &http.Transport{
		Proxy:               http.ProxyFromEnvironment,
		DisableCompression:  true,
		MaxIdleConns:        8,
		MaxIdleConnsPerHost: 8,
		IdleConnTimeout:     30 * time.Second,
	}
	return &http.Client{Transport: transport}
}

func (c *dockerCPUCollector) Collect(parent context.Context, host string) {
	ctx, cancel := context.WithTimeout(parent, c.timeout)
	defer cancel()

	containers, err := c.listContainers(ctx, c.maxContainers)
	if err != nil {
		c.logErr("docker: list containers: %v", err)
		return
	}
	if len(containers) == 0 {
		c.mu.Lock()
		for k := range c.prev {
			delete(c.prev, k)
		}
		c.mu.Unlock()
		return
	}

	hostLabel := statok.Label("host", host)

	active := make(map[string]struct{}, len(containers))
	for _, ctr := range containers {
		active[ctr.ID] = struct{}{}
	}

	workCh := make(chan dockerContainerSummary)
	var wg sync.WaitGroup

	workers := c.concurrency
	if workers > len(containers) {
		workers = len(containers)
	}

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ctr := range workCh {
				c.collectContainer(ctx, hostLabel, ctr)
			}
		}()
	}

sendLoop:
	for _, ctr := range containers {
		select {
		case <-ctx.Done():
			break sendLoop
		case workCh <- ctr:
		}
	}
	close(workCh)
	wg.Wait()

	c.mu.Lock()
	for id := range c.prev {
		if _, ok := active[id]; !ok {
			delete(c.prev, id)
		}
	}
	c.mu.Unlock()
}

type dockerContainerSummary struct {
	ID     string            `json:"Id"`
	Names  []string          `json:"Names"`
	Labels map[string]string `json:"Labels"`
}

func (c *dockerCPUCollector) listContainers(ctx context.Context, limit int) ([]dockerContainerSummary, error) {
	p := fmt.Sprintf("/containers/json?all=0&limit=%d&size=0", limit)
	var out []dockerContainerSummary
	if err := c.doJSON(ctx, http.MethodGet, p, &out); err != nil {
		return nil, err
	}
	return out, nil
}

type dockerStats struct {
	CPUStats dockerCPUStats `json:"cpu_stats"`
}

type dockerCPUStats struct {
	CPUUsage       dockerCPUUsage `json:"cpu_usage"`
	SystemCPUUsage uint64         `json:"system_cpu_usage"`
	OnlineCPUs     uint32         `json:"online_cpus"`
}

type dockerCPUUsage struct {
	TotalUsage  uint64   `json:"total_usage"`
	PercpuUsage []uint64 `json:"percpu_usage"`
}

func (c *dockerCPUCollector) collectContainer(ctx context.Context, hostLabel string, ctr dockerContainerSummary) {
	if ctr.ID == "" {
		return
	}

	stats, err := c.getStats(ctx, ctr.ID)
	if err != nil {
		return
	}

	total := stats.CPUStats.CPUUsage.TotalUsage
	system := stats.CPUStats.SystemCPUUsage
	if total == 0 || system == 0 {
		return
	}

	online := stats.CPUStats.OnlineCPUs
	if online == 0 {
		online = uint32(len(stats.CPUStats.CPUUsage.PercpuUsage))
	}
	if online == 0 {
		online = 1
	}

	c.mu.Lock()
	prev, ok := c.prev[ctr.ID]
	c.prev[ctr.ID] = dockerCPUPrev{totalUsage: total, systemUsage: system}
	c.mu.Unlock()
	if !ok {
		return
	}

	cpuDelta := diff(prev.totalUsage, total)
	systemDelta := diff(prev.systemUsage, system)
	if cpuDelta == 0 || systemDelta == 0 {
		return
	}

	pct := 100.0 * float64(cpuDelta) / float64(systemDelta) * float64(online)
	if pct < 0 || pct != pct {
		return
	}

	target := c.containerLabelValue(ctr)
	if target == "" {
		return
	}

	statok.Value("docker.container.cpu.usage_pct", pct,
		hostLabel,
		statok.Label(c.labelKey, target),
	)
}

func (c *dockerCPUCollector) containerLabelValue(ctr dockerContainerSummary) string {
	name := ""
	if len(ctr.Names) > 0 {
		name = strings.TrimPrefix(strings.TrimSpace(ctr.Names[0]), "/")
	}

	if c.labelMode == dockerLabelContainer {
		return name
	}

	if v := strings.TrimSpace(ctr.Labels["com.docker.compose.service"]); v != "" {
		return v
	}
	if v := strings.TrimSpace(ctr.Labels["com.docker.swarm.service.name"]); v != "" {
		return v
	}

	return name
}

func (c *dockerCPUCollector) getStats(ctx context.Context, containerID string) (*dockerStats, error) {
	p := fmt.Sprintf("/containers/%s/stats?stream=false", url.PathEscape(containerID))
	var out dockerStats
	if err := c.doJSON(ctx, http.MethodGet, p, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *dockerCPUCollector) doJSON(ctx context.Context, method, path string, dst any) error {
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, nil)
	if err != nil {
		return err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return fmt.Errorf("http %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}

	dec := json.NewDecoder(io.LimitReader(resp.Body, 8<<20))
	return dec.Decode(dst)
}

func (c *dockerCPUCollector) logErr(format string, err error) {
	if err == nil {
		return
	}

	now := time.Now()
	c.mu.Lock()
	allow := c.lastErrLog.IsZero() || now.Sub(c.lastErrLog) >= time.Minute
	if allow {
		c.lastErrLog = now
	}
	c.mu.Unlock()

	if allow {
		log.Printf(format, err)
	}
}
