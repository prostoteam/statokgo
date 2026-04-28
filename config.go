package statok

import (
	"fmt"
	"log"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
)

const (
	defaultQueueSize         = 64 * 1024
	defaultMaxBatchSize      = 512
	defaultMaxSeriesPerBatch = 2048
	defaultMaxTotalSeries    = defaultMaxSeriesPerBatch
	defaultFlushInterval     = 500 * time.Millisecond
	defaultFlushTimeout      = 5 * time.Second
	defaultEndpointHost      = "statok.dev0101.xyz"
	defaultIngestPath        = "/api/i/batch"
	defaultStopStatusCode    = 401
	defaultStopResponseCode  = "unauthorized"
	workloadMaxLen           = 100
)

// Config contains the public settings needed to connect the client.
type Config struct {
	Endpoint  string
	APIKey    string
	Transport Transport
	Logger    Logger
	Verbose   bool
}

// Logger is the minimal logging interface used by the library. The default logger
// writes to stderr using log.Printf semantics.
type Logger interface {
	Printf(format string, args ...any)
}

type noopLogger struct{}

func (noopLogger) Printf(string, ...any) {}

func (c *Config) applyDefaults() error {
	if c.Endpoint == "" && c.Transport == nil {
		c.Endpoint = EndpointFromHost(defaultEndpointHost)
	}
	if c.Endpoint != "" {
		c.Endpoint = ensureIngestPath(c.Endpoint)
	}
	if c.Logger == nil {
		c.Logger = log.Default()
	}
	if c.Transport == nil && c.Endpoint != "" {
		c.Transport = &HTTPTransport{
			Endpoint: c.Endpoint,
			APIKey:   c.APIKey,
			Logger:   c.Logger,
		}
	}
	if ht, ok := c.Transport.(*HTTPTransport); ok {
		if ht.Logger == nil {
			ht.Logger = c.Logger
		}
		if c.APIKey != "" && ht.APIKey == "" {
			ht.APIKey = c.APIKey
		}
		transportAPIKey := ht.APIKey
		if hasAuthorizationHeader(ht.Header) && transportAPIKey != "" {
			return ErrAPIKeyAuthorizationConflict
		}
		if transportAPIKey == "" {
			return ErrMissingAPIKey
		}
		if err := validateAPIKey(transportAPIKey); err != nil {
			return err
		}
		ht.APIKey = transportAPIKey
		c.APIKey = transportAPIKey
	}
	if c.Logger == nil {
		c.Logger = noopLogger{}
	}
	return nil
}

func validateWorkload(workload string) error {
	if workload == "" {
		return ErrInvalidWorkload
	}
	if len(workload) > workloadMaxLen {
		return ErrInvalidWorkload
	}
	for i := 0; i < len(workload); i++ {
		ch := workload[i]
		switch {
		case ch >= 'a' && ch <= 'z':
		case ch >= 'A' && ch <= 'Z':
		case ch >= '0' && ch <= '9':
		case ch == '.' || ch == '-' || ch == '_' || ch == '/':
		default:
			return ErrInvalidWorkload
		}
	}
	return nil
}

// EndpointFromHost builds the ingest endpoint from a host (with or without scheme)
// and appends the default ingest path when missing.
func EndpointFromHost(host string) string {
	host = strings.TrimSpace(host)
	if host == "" {
		return ""
	}
	if strings.HasPrefix(host, "http://") || strings.HasPrefix(host, "https://") {
		return ensureIngestPath(host)
	}
	return ensureIngestPath("https://" + host)
}

// ensureIngestPath appends the ingest path when the provided endpoint has no path.
// On parse errors, the original string is returned unchanged.
func ensureIngestPath(endpoint string) string {
	if endpoint == "" {
		return ""
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		return endpoint
	}
	if u.Path == "" || u.Path == "/" {
		u.Path = defaultIngestPath
	}
	return u.String()
}

func validateAPIKey(raw string) error {
	if raw == "" {
		return ErrMissingAPIKey
	}
	lowerRaw := strings.ToLower(raw)
	if strings.HasPrefix(lowerRaw, "bearer ") || strings.HasPrefix(lowerRaw, "bearer\t") {
		return fmt.Errorf("%w: token must be raw without Bearer prefix", ErrInvalidAPIKey)
	}
	if strings.IndexFunc(raw, unicode.IsSpace) >= 0 {
		return fmt.Errorf("%w: token must not contain spaces", ErrInvalidAPIKey)
	}
	parts := strings.SplitN(raw, "_", 2)
	if len(parts) != 2 {
		return fmt.Errorf("%w: expected <client_id>_<secret>", ErrInvalidAPIKey)
	}
	clientIDPart := parts[0]
	secretPart := parts[1]
	if clientIDPart == "" || secretPart == "" {
		return fmt.Errorf("%w: expected non-empty client id and secret", ErrInvalidAPIKey)
	}
	clientID, err := strconv.ParseUint(clientIDPart, 10, 64)
	if err != nil || clientID == 0 {
		return fmt.Errorf("%w: client id must be a positive integer", ErrInvalidAPIKey)
	}
	return nil
}

func hasAuthorizationHeader(h map[string][]string) bool {
	if len(h) == 0 {
		return false
	}
	for k, values := range h {
		if !strings.EqualFold(k, "Authorization") {
			continue
		}
		for _, v := range values {
			if strings.TrimSpace(v) != "" {
				return true
			}
		}
		return false
	}
	return false
}
