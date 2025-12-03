package statok

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"
)

var (
	ErrNoTransport  = errors.New("statok: no transport configured")
	ErrClientClosed = errors.New("statok: client closed")
)

// Client implements the non-blocking Statok metrics API.
type Client struct {
	cfg     Config
	queue   chan *event
	cancel  context.CancelFunc
	done    chan struct{}
	dropped atomic.Uint64
	logger  Logger
	once    sync.Once
}

// NewClient builds a client with the provided configuration and starts the
// background flushing goroutine.
func NewClient(cfg Config) (*Client, error) {
	cfg.applyDefaults()
	if cfg.Transport == nil {
		return nil, ErrNoTransport
	}
	if cfg.MaxBatchSize > cfg.QueueSize {
		cfg.MaxBatchSize = cfg.QueueSize
	}
	ctx, cancel := context.WithCancel(context.Background())
	c := &Client{
		cfg:    cfg,
		queue:  make(chan *event, cfg.QueueSize),
		cancel: cancel,
		done:   make(chan struct{}),
		logger: cfg.Logger,
	}
	go c.run(ctx)
	return c, nil
}

// Default client handling -----------------------------------------------------

var (
	defaultMu     sync.RWMutex
	defaultClient *Client
)

// Init replaces the package-level Client returned by Count/Value helpers.
func Init(cfg Config) (*Client, error) {
	client, err := NewClient(cfg)
	if err != nil {
		return nil, err
	}
	defaultMu.Lock()
	defer defaultMu.Unlock()
	if defaultClient != nil {
		_ = defaultClient.Close(context.Background())
	}
	defaultClient = client
	return client, nil
}

// Default returns the package-level Client, or nil if Init was never called.
func Default() *Client {
	defaultMu.RLock()
	defer defaultMu.RUnlock()
	return defaultClient
}

// Count records a counter delta using the default client.
func Count(metric string, delta float64, labels ...string) {
	if c := Default(); c != nil {
		c.Count(metric, delta, labels...)
	}
}

// Value records a value sample using the default client.
func Value(metric string, value float64, labels ...string) {
	if c := Default(); c != nil {
		c.Value(metric, value, labels...)
	}
}

// Client methods --------------------------------------------------------------

// Count records a counter delta. Calls never block; on overflow the event is dropped.
func (c *Client) Count(metric string, delta float64, labels ...string) {
	c.enqueue(metricTypeCounter, metric, delta, labels)
}

// Value records a measurement sample.
func (c *Client) Value(metric string, value float64, labels ...string) {
	c.enqueue(metricTypeValue, metric, value, labels)
}

func (c *Client) enqueue(typ metricType, metric string, value float64, labels []string) {
	if metric == "" {
		return
	}
	select {
	case <-c.done:
		return
	default:
	}
	ev := borrowEvent(typ, metric, value, labels)
	select {
	case c.queue <- ev:
	default:
		c.dropped.Add(1)
		releaseEvent(ev)
	}
}

// Dropped returns how many events were rejected because the queue was full.
func (c *Client) Dropped() uint64 {
	return c.dropped.Load()
}

// Close flushes pending events and stops the background worker.
func (c *Client) Close(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	var err error
	c.once.Do(func() {
		c.cancel()
		select {
		case <-c.done:
		case <-ctx.Done():
			err = ctx.Err()
			return
		}
	})
	return err
}

func (c *Client) run(ctx context.Context) {
	defer close(c.done)
	ticker := time.NewTicker(c.cfg.FlushInterval)
	defer ticker.Stop()
	batch := make([]*event, 0, c.cfg.MaxBatchSize)
	flush := func() {
		if len(batch) == 0 {
			return
		}
		c.flush(batch)
		for i := range batch {
			releaseEvent(batch[i])
			batch[i] = nil
		}
		batch = batch[:0]
	}
	for {
		select {
		case <-ticker.C:
			flush()
		case ev := <-c.queue:
			if ev == nil {
				continue
			}
			batch = append(batch, ev)
			if len(batch) >= c.cfg.MaxBatchSize {
				flush()
			}
		case <-ctx.Done():
			// drain queue without blocking
			for {
				select {
				case ev := <-c.queue:
					if ev != nil {
						batch = append(batch, ev)
						if len(batch) >= c.cfg.MaxBatchSize {
							flush()
						}
					}
				default:
					flush()
					return
				}
			}
		}
	}
}

func (c *Client) flush(events []*event) {
	builder := newBatchBuilder(&c.cfg, len(events))
	for _, ev := range events {
		builder.add(ev)
	}
	payload := builder.build()
	if payload == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), c.cfg.FlushTimeout)
	defer cancel()
	if err := c.cfg.Transport.Send(ctx, payload); err != nil {
		c.logger.Printf("statok: flush failed: %v", err)
	}
}
