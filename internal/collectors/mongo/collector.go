package mongo

import (
	"context"
	"fmt"
	"log"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"

	statok "github.com/prostoteam/statokgo"
)

const defaultOpTimeout = 2 * time.Second

type Instance struct {
	URI   string
	Label string
}

type instanceState struct {
	Instance
	client      *mongo.Client
	nextAttempt time.Time
}

type Collector struct {
	every      time.Duration
	retryEvery time.Duration
	opTimeout  time.Duration
	instances  []instanceState
}

func NewCollector(instances []Instance, every time.Duration, retryEvery time.Duration) *Collector {
	state := make([]instanceState, len(instances))
	for i, inst := range instances {
		state[i] = instanceState{Instance: inst}
	}
	if retryEvery <= 0 {
		retryEvery = time.Minute
	}
	return &Collector{
		every:      every,
		retryEvery: retryEvery,
		opTimeout:  defaultOpTimeout,
		instances:  state,
	}
}

func (c *Collector) ID() string { return "mongo" }

func (c *Collector) Every() time.Duration { return c.every }

func (c *Collector) Collect(ctx context.Context) error {
	now := time.Now()
	for i := range c.instances {
		inst := &c.instances[i]
		if now.Before(inst.nextAttempt) {
			continue
		}
		if ctx.Err() != nil {
			return nil
		}
		if err := c.collectInstance(ctx, inst); err != nil {
			log.Printf("mongo: instance %s: %v", inst.Label, err)
			inst.nextAttempt = now.Add(c.retryEvery)
		}
	}
	return nil
}

func (c *Collector) Close(ctx context.Context) error {
	for i := range c.instances {
		c.resetClient(ctx, &c.instances[i])
	}
	return nil
}

type serverStatus struct {
	Connections struct {
		Current   int64 `bson:"current"`
		Available int64 `bson:"available"`
	} `bson:"connections"`
}

func (c *Collector) collectInstance(ctx context.Context, inst *instanceState) error {
	timeout := c.opTimeout
	if timeout <= 0 {
		timeout = defaultOpTimeout
	}
	opCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	client, err := c.ensureClient(opCtx, inst, timeout)
	if err != nil {
		return err
	}

	var status serverStatus
	if err := client.Database("admin").RunCommand(opCtx, bson.D{{Key: "serverStatus", Value: 1}}).Decode(&status); err != nil {
		c.resetClient(context.Background(), inst)
		return fmt.Errorf("serverStatus: %w", err)
	}

	emit := func(typ string, v int64) {
		if v < 0 {
			return
		}
		statok.Value("mongo.connections", float64(v),
			statok.Label("instance", inst.Label),
			statok.Label("type", typ),
		)
	}
	emit("current", status.Connections.Current)
	emit("available", status.Connections.Available)
	return nil
}

func (c *Collector) ensureClient(ctx context.Context, inst *instanceState, timeout time.Duration) (*mongo.Client, error) {
	if inst.client != nil {
		return inst.client, nil
	}
	opts := options.Client().ApplyURI(inst.URI)
	opts.SetConnectTimeout(timeout)
	opts.SetServerSelectionTimeout(timeout)
	client, err := mongo.Connect(ctx, opts)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	if err := client.Ping(ctx, nil); err != nil {
		_ = client.Disconnect(ctx)
		return nil, fmt.Errorf("ping: %w", err)
	}
	inst.client = client
	return client, nil
}

func (c *Collector) resetClient(ctx context.Context, inst *instanceState) {
	if inst.client == nil {
		return
	}
	disconnectCtx := ctx
	if disconnectCtx == nil {
		disconnectCtx = context.Background()
	}
	if _, ok := disconnectCtx.Deadline(); !ok {
		var cancel context.CancelFunc
		disconnectCtx, cancel = context.WithTimeout(disconnectCtx, time.Second)
		defer cancel()
	}
	_ = inst.client.Disconnect(disconnectCtx)
	inst.client = nil
}
