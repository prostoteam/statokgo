package nginx

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/prostoteam/statokgo/internal/agent"
)

type Probe struct {
	endpoint string
	every    time.Duration
}

func NewProbe(endpoint string, every time.Duration) *Probe {
	return &Probe{endpoint: endpoint, every: every}
}

func (p *Probe) ID() string { return "nginx" }

func (p *Probe) Detect(ctx context.Context) (bool, string) {
	endpoint := strings.TrimSpace(p.endpoint)
	if endpoint == "" {
		return false, "no endpoint configured"
	}
	_, err := fetchStubStatus(ctx, &http.Client{Timeout: defaultTimeout}, endpoint)
	if err != nil {
		return false, err.Error()
	}
	return true, fmt.Sprintf("stub_status reachable at %s", endpoint)
}

func (p *Probe) New() agent.Collector {
	return NewCollector(p.endpoint, p.every)
}
