package resolver

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/miekg/dns"
)

// export timeNow for testing
func SetTimeNow(f func() time.Time) {
	timeNow = f
}

type cacheEntry struct {
	ips     []string
	expires time.Time
}

// timeNow allows mocking time in tests.
var timeNow = time.Now

// CachingResolver resolves hostnames using a pool of UDP DNS clients and
// caches the results based on the TTL returned by the authoritative server,
// bounded between 60s and 300s.
type CachingResolver struct {
	cache      sync.Map
	clientPool chan *dns.Client
	config     *dns.ClientConfig
}

// NewCachingResolver creates a new resolver with a DNS client pool of size concurrency.
func NewCachingResolver(concurrency int) (*CachingResolver, error) {
	if concurrency <= 0 {
		return nil, fmt.Errorf("concurrency must be positive")
	}

	config, err := dns.ClientConfigFromFile("/etc/resolv.conf")
	if err != nil {
		// Fallback to Cloudflare/Google if no local resolv.conf
		config = &dns.ClientConfig{
			Servers: []string{"1.1.1.1", "8.8.8.8"},
			Port:    "53",
		}
	}

	pool := make(chan *dns.Client, concurrency)
	for i := 0; i < concurrency; i++ {
		pool <- new(dns.Client)
	}

	return &CachingResolver{
		clientPool: pool,
		config:     config,
	}, nil
}

// LookupHost resolves a hostname to a list of IPv4 addresses.
// Returns a cached response immediately if available and not expired.
func (r *CachingResolver) LookupHost(ctx context.Context, host string) ([]string, error) {
	// First check cache
	if v, ok := r.cache.Load(host); ok {
		entry := v.(cacheEntry)
		if timeNow().Before(entry.expires) {
			return entry.ips, nil
		}
		// Expired — remove it and proceed to fetch
		r.cache.Delete(host)
	}

	// Prepare DNS query
	m := new(dns.Msg)
	m.SetQuestion(dns.Fqdn(host), dns.TypeA)
	m.RecursionDesired = true

	// Borrow a client from the pool
	var client *dns.Client
	select {
	case client = <-r.clientPool:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() {
		r.clientPool <- client
	}()

	// Try servers
	var resp *dns.Msg
	var err error
	for _, server := range r.config.Servers {
		address := net.JoinHostPort(server, r.config.Port)
		// We use a local timeout to prevent hanging, but respect context
		client.Timeout = 5 * time.Second

		var rtt time.Duration
		resp, rtt, err = client.ExchangeContext(ctx, m, address)
		_ = rtt
		if err == nil && resp != nil && resp.Rcode == dns.RcodeSuccess {
			break
		}
	}

	if err != nil {
		return nil, fmt.Errorf("dns exchange failed for %s: %w", host, err)
	}
	if resp == nil || resp.Rcode != dns.RcodeSuccess {
		return nil, fmt.Errorf("no such host: %s", host)
	}

	var ips []string
	var minTTL uint32 = 0xFFFFFFFF

	for _, answer := range resp.Answer {
		if a, ok := answer.(*dns.A); ok {
			ips = append(ips, a.A.String())
			if a.Hdr.Ttl < minTTL {
				minTTL = a.Hdr.Ttl
			}
		}
	}

	if len(ips) == 0 {
		return nil, fmt.Errorf("no A records found for %s", host)
	}

	// Clamp TTL between 60s and 300s
	ttl := int(minTTL)
	if ttl < 60 {
		ttl = 60
	} else if ttl > 300 {
		ttl = 300
	}

	expires := timeNow().Add(time.Duration(ttl) * time.Second)
	r.cache.Store(host, cacheEntry{
		ips:     ips,
		expires: expires,
	})

	return ips, nil
}
