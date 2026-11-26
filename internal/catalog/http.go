// Copyright 2026 Daniela Petruzalek
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package catalog

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	maxRedirects      = 10
	maxRetryAfterWait = 10 * time.Second
	maxDrainBodyBytes = 16 * 1024
)

// safeTransport marks HTTP transports created by NewSafeHTTPClient so URL validation
// distinguishes hardened production clients from injected httptest.Server clients.
type safeTransport struct {
	base          *http.Transport
	allowLoopback bool
}

func (s *safeTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return s.base.RoundTrip(req)
}

// AllowLoopbackHTTP reports whether client is allowed to talk to loopback HTTP servers.
func AllowLoopbackHTTP(client *http.Client) bool {
	if client == nil {
		return false
	}
	if st, ok := client.Transport.(*safeTransport); ok {
		return st.allowLoopback
	}
	return true
}

// IsLoopbackHost reports whether host is localhost or a loopback IP literal.
func IsLoopbackHost(host string) bool {
	h := strings.TrimSpace(strings.ToLower(host))
	if h == "localhost" {
		return true
	}
	addr, err := netip.ParseAddr(h)
	return err == nil && addr.IsLoopback()
}

// IsLoopbackURL reports whether rawURL targets a loopback host over http or https.
func IsLoopbackURL(rawURL string) bool {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || u.Host == "" {
		return false
	}
	scheme := strings.ToLower(u.Scheme)
	return (scheme == "http" || scheme == "https") && IsLoopbackHost(u.Hostname())
}

func isDisallowedIP(ip net.IP, allowLoopback bool) bool {
	addr, ok := netip.AddrFromSlice(ip)
	if !ok {
		return true
	}
	addr = addr.Unmap()
	if addr.IsLoopback() {
		return !allowLoopback
	}
	return addr.IsPrivate() ||
		addr.IsLinkLocalUnicast() ||
		addr.IsLinkLocalMulticast() ||
		addr.IsMulticast() ||
		addr.IsUnspecified()
}

// ValidateRemoteURL parses rawURL and enforces HTTPS (or loopback HTTP only when allowLoopbackHTTP is true).
func ValidateRemoteURL(rawURL string, allowLoopbackHTTP bool) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInsecureURL, err)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("%w: missing host in %q", ErrInsecureURL, rawURL)
	}
	scheme := strings.ToLower(u.Scheme)
	switch scheme {
	case "https":
		return u, nil
	case "http":
		if allowLoopbackHTTP && IsLoopbackHost(u.Hostname()) {
			return u, nil
		}
		return nil, fmt.Errorf("%w: %q", ErrInsecureURL, rawURL)
	default:
		return nil, fmt.Errorf("%w: scheme %q in %q", ErrInsecureURL, u.Scheme, rawURL)
	}
}

// NewSafeHTTPClient creates an HTTP client hardened against SSRF, redirect downgrades,
// cross-host credential leakage, and unbounded timeouts.
func NewSafeHTTPClient(timeout time.Duration) *http.Client {
	return NewSafeHTTPClientWithLoopback(timeout, false)
}

// NewSafeHTTPClientWithLoopback creates a hardened HTTP client that optionally permits
// explicit loopback (127.0.0.0/8, ::1, localhost) targets for local development and testing,
// while still blocking private LAN, link-local/metadata, multicast, unspecified IPs, and
// non-loopback HTTP or HTTPS-to-HTTP downgrades.
func NewSafeHTTPClientWithLoopback(timeout time.Duration, allowLoopback bool) *http.Client {
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	dialer := &net.Dialer{
		Timeout:   10 * time.Second,
		KeepAlive: 30 * time.Second,
	}
	tr := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          32,
		IdleConnTimeout:       60 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
			if err != nil {
				return nil, err
			}
			for _, ipAddr := range ips {
				if isDisallowedIP(ipAddr.IP, allowLoopback) {
					return nil, fmt.Errorf("%w: blocked connection to non-public address %s (%s)", ErrInsecureURL, host, ipAddr.IP.String())
				}
			}
			var lastErr error
			for _, ipAddr := range ips {
				conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(ipAddr.IP.String(), port))
				if err == nil {
					return conn, nil
				}
				lastErr = err
			}
			if lastErr != nil {
				return nil, lastErr
			}
			return nil, fmt.Errorf("no routable address for %s", host)
		},
	}

	return &http.Client{
		Timeout:   timeout,
		Transport: &safeTransport{base: tr, allowLoopback: allowLoopback},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= maxRedirects {
				return fmt.Errorf("stopped after %d redirects", maxRedirects)
			}
			scheme := strings.ToLower(req.URL.Scheme)
			prevHTTPS := len(via) > 0 && strings.EqualFold(via[len(via)-1].URL.Scheme, "https")
			if scheme != "https" {
				if prevHTTPS || !allowLoopback || scheme != "http" || !IsLoopbackHost(req.URL.Hostname()) {
					return fmt.Errorf("%w: redirect to %q blocked", ErrInsecureURL, req.URL.String())
				}
			}
			if len(via) > 0 && !strings.EqualFold(via[len(via)-1].URL.Host, req.URL.Host) {
				req.Header.Del("Authorization")
				req.Header.Del("Cookie")
			}
			return nil
		},
	}
}

func parseRetryAfter(val string) (time.Duration, bool) {
	val = strings.TrimSpace(val)
	if val == "" {
		return 0, false
	}
	if secs, err := strconv.Atoi(val); err == nil && secs >= 0 {
		d := min(time.Duration(secs)*time.Second, maxRetryAfterWait)
		return d, true
	}
	if t, err := http.ParseTime(val); err == nil {
		d := time.Until(t)
		if d < 0 {
			return 0, true
		}
		if d > maxRetryAfterWait {
			d = maxRetryAfterWait
		}
		return d, true
	}
	return 0, false
}

// FetchWithRetryAndHeaders executes an HTTP GET request with URL validation, custom headers,
// jittered exponential backoff, Retry-After support, and keep-alive body draining.
func FetchWithRetryAndHeaders(ctx context.Context, client *http.Client, rawURL string, userAgent string, headers map[string]string, maxRetries int, initialBackoff time.Duration) (*http.Response, error) {
	allowLoopback := AllowLoopbackHTTP(client)
	if client == nil {
		client = NewSafeHTTPClient(15 * time.Second)
	}
	if _, err := ValidateRemoteURL(rawURL, allowLoopback); err != nil {
		return nil, err
	}
	if maxRetries < 0 {
		maxRetries = 0
	}
	if initialBackoff <= 0 {
		initialBackoff = 100 * time.Millisecond
	}

	backoff := initialBackoff
	var lastErr error
	var nextDelay time.Duration

	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			wait := nextDelay
			if wait <= 0 {
				// Apply +/-25% jitter
				jitterFactor := 0.75 + rand.Float64()*0.5
				wait = time.Duration(float64(backoff) * jitterFactor)
			}
			nextDelay = 0

			timer := time.NewTimer(wait)
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil, ctx.Err()
			case <-timer.C:
			}
			backoff *= 2
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
		if err != nil {
			return nil, err
		}
		if userAgent != "" {
			req.Header.Set("User-Agent", userAgent)
		}
		for k, v := range headers {
			req.Header.Set(k, v)
		}

		resp, err := client.Do(req)
		if err != nil {
			if errors.Is(err, ErrInsecureURL) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return nil, err
			}
			lastErr = err
			continue
		}
		if resp == nil {
			lastErr = fmt.Errorf("nil HTTP response from %s", rawURL)
			continue
		}

		if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusNotModified {
			return resp, nil
		}

		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			if ra, ok := parseRetryAfter(resp.Header.Get("Retry-After")); ok {
				nextDelay = ra
			}
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxDrainBodyBytes))
			_ = resp.Body.Close()
			lastErr = &HTTPStatusError{StatusCode: resp.StatusCode, URL: rawURL}
			continue
		}

		return resp, nil
	}

	return nil, lastErr
}
