package ingest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

type FetchResult struct {
	Body        []byte
	URL         string
	Headers     http.Header
	NotModified bool
}

type Fetcher interface {
	Fetch(context.Context, string, http.Header) (FetchResult, error)
}

type FetchError struct {
	Status int
	URL    string
}

func (e *FetchError) Error() string {
	if e.Blocked() {
		return fmt.Sprintf("source access unavailable (HTTP %d); public content could not be refreshed", e.Status)
	}
	return fmt.Sprintf("source returned HTTP %d", e.Status)
}

func (e *FetchError) Blocked() bool {
	return e.Status == 401 || e.Status == 403 || e.Status == 429 || e.Status == 999
}

func AccessBlocked(err error) bool {
	var fetchError *FetchError
	var xError *XAPIError
	return errors.As(err, &fetchError) && fetchError.Blocked() || errors.Is(err, ErrNoPublicPost) || errors.Is(err, ErrXNotConfigured) || errors.As(err, &xError) && (xError.Status == 401 || xError.Status == 402 || xError.Status == 403 || xError.Status == 429)
}

type HTTPFetcher struct {
	client *http.Client
	delay  time.Duration
	mu     sync.Mutex
	next   map[string]time.Time
}

func NewHTTPFetcher(timeout, delay time.Duration) *HTTPFetcher {
	transport := &http.Transport{
		DialContext: publicDial, ForceAttemptHTTP2: true, MaxIdleConns: 20,
		MaxConnsPerHost: 2, IdleConnTimeout: 90 * time.Second, TLSHandshakeTimeout: 10 * time.Second,
		ResponseHeaderTimeout: timeout,
	}
	client := &http.Client{Timeout: timeout, Transport: transport}
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("too many redirects")
		}
		_, err := ValidateURL(req.URL.String())
		return err
	}
	return &HTTPFetcher{client: client, delay: delay, next: map[string]time.Time{}}
}

func (f *HTTPFetcher) Fetch(ctx context.Context, raw string, headers http.Header) (FetchResult, error) {
	u, err := ValidateURL(raw)
	if err != nil {
		return FetchResult{}, err
	}
	f.mu.Lock()
	start := time.Now()
	if f.next[u.Hostname()].After(start) {
		start = f.next[u.Hostname()]
	}
	f.next[u.Hostname()] = start.Add(f.delay)
	f.mu.Unlock()
	if wait := time.Until(start); wait > 0 {
		timer := time.NewTimer(wait)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			return FetchResult{}, ctx.Err()
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return FetchResult{}, err
	}
	req.Header.Set("User-Agent", "Radwave/0.2 (+https://github.com/YeandAmen/radwave)")
	req.Header.Set("Accept", "text/html,application/rss+xml,application/atom+xml,application/xml;q=0.9,*/*;q=0.5")
	req.Header.Set("Accept-Language", "en")
	for key, values := range headers {
		if key == "If-None-Match" || key == "If-Modified-Since" {
			req.Header[key] = values
		}
	}
	response, err := f.client.Do(req)
	if err != nil {
		return FetchResult{}, fmt.Errorf("fetch failed: %w", err)
	}
	defer response.Body.Close()
	result := FetchResult{URL: response.Request.URL.String(), Headers: response.Header}
	if response.StatusCode == http.StatusNotModified {
		result.NotModified = true
		return result, nil
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return FetchResult{}, &FetchError{Status: response.StatusCode, URL: raw}
	}
	const maxBody = 5 << 20
	result.Body, err = io.ReadAll(io.LimitReader(response.Body, maxBody+1))
	if err != nil {
		return FetchResult{}, err
	}
	if len(result.Body) > maxBody {
		return FetchResult{}, errors.New("source document exceeds the 5 MB limit")
	}
	return result, nil
}

func publicDial(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", strings.Trim(host, "[]"))
	if err != nil {
		return nil, err
	}
	if len(ips) == 0 {
		return nil, errors.New("source hostname has no public address")
	}
	for _, ip := range ips {
		if !PublicIP(ip) {
			return nil, errors.New("source resolved to a private or non-public address")
		}
	}
	dialer := net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	var lastError error
	for _, ip := range ips {
		conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		if err == nil {
			return conn, nil
		}
		lastError = err
	}
	return nil, lastError
}
