package scraper

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"syscall"
	"time"
)

// MaxScrapeBytes caps how many bytes a scraper will read from any single
// upstream response. Defends against compromised/hos exhaust-the-memory pages.
const MaxScrapeBytes = 8 << 20 // 8 MiB

// NewSafeClient returns an *http.Client hardened against SSRF and unbounded
// responses. It:
//   - refuses to dial private / loopback / link-local / metadata IPs at the
//     transport level (defeats user-supplied internal URLs and DNS rebinding),
//   - follows redirects only to safe hosts (same defense at redirect time),
//   - caps every response body at MaxScrapeBytes.
//
// All outbound scraper traffic should go through a client built here.
func NewSafeClient(timeout time.Duration) *http.Client {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	base := http.DefaultTransport.(*http.Transport).Clone()
	base.DialContext = safeDialContext(base.DialContext)

	rt := &limitBodyTransport{base: base}

	return &http.Client{
		Timeout:       timeout,
		Transport:     rt,
		CheckRedirect: safeCheckRedirect,
	}
}

// safeCheckRedirect rejects any redirect whose URL resolves to an unsafe host.
func safeCheckRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return fmt.Errorf("stopped after 10 redirects")
	}
	if err := assertSafeURL(req.URL); err != nil {
		return fmt.Errorf("redirect to unsafe host %q: %w", req.URL.Host, err)
	}
	return nil
}

// safeDialContext wraps a base dialer and aborts any connection whose resolved
// address is private / loopback / link-local / unspecified. This is the
// authoritative SSRF defense: even if a URL passes parsing, the actual TCP
// dial is refused.
func safeDialContext(next func(ctx context.Context, network, addr string) (net.Conn, error)) func(context.Context, string, string) (net.Conn, error) {
	if next == nil {
		next = (&net.Dialer{Timeout: 15 * time.Second}).DialContext
	}
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		ip := resolveHost(ctx, host)
		if ip == nil {
			return nil, fmt.Errorf("ssrf: could not resolve %q", host)
		}
		if !isPublicIP(ip) {
			return nil, fmt.Errorf("ssrf: refusing to dial private address %s (%s)", ip, host)
		}
		// ponytail: pin the resolved IP so a rebinding attack between resolve
		// and connect cannot redirect to a private address.
		return next(ctx, network, net.JoinHostPort(ip.String(), port))
	}
}

func resolveHost(ctx context.Context, host string) net.IP {
	if ip := net.ParseIP(host); ip != nil {
		return ip
	}
	lookupCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ips, err := net.DefaultResolver.LookupIPAddr(lookupCtx, host)
	if err != nil || len(ips) == 0 {
		return nil
	}
	return ips[0].IP
}

// isPublicIP reports whether ip is safe for outbound scraper traffic.
func isPublicIP(ip net.IP) bool {
	if ip == nil {
		return false
	}
	if ip.IsUnspecified() || ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() {
		return false
	}
	// Block IPv4-mapped IPv6 of private ranges too.
	if v4 := ip.To4(); v4 != nil {
		return isPublicIPv4(v4)
	}
	return true
}

func isPublicIPv4(ip net.IP) bool {
	// CGNAT 100.64.0.0/10
	if ip[0] == 100 && ip[1] >= 64 && ip[1] <= 127 {
		return false
	}
	return true
}

// assertSafeURL validates scheme + host before any dial. Fast-fail for clearly
// internal URLs; the dial-time check is the authoritative gate.
func assertSafeURL(u *url.URL) error {
	if u == nil {
		return fmt.Errorf("nil url")
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return fmt.Errorf("scheme %q not allowed", scheme)
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("empty host")
	}
	if ip := net.ParseIP(host); ip != nil && !isPublicIP(ip) {
		return fmt.Errorf("host %s is not public", host)
	}
	// Block obvious metadata hostnames used by cloud instances.
	lowerHost := strings.ToLower(host)
	for _, bad := range []string{"metadata.google.internal", "metadata", "169.254.169.254"} {
		if lowerHost == bad || strings.HasPrefix(lowerHost, bad+".") {
			return fmt.Errorf("host %s blocked", host)
		}
	}
	return nil
}

// limitBodyTransport wraps a base RoundTripper and replaces each response body
// with one capped at MaxScrapeBytes, so a hostile upstream cannot exhaust
// memory regardless of which scraper consumes the body.
type limitBodyTransport struct{ base http.RoundTripper }

func (t *limitBodyTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := assertSafeURL(req.URL); err != nil {
		return nil, err
	}
	resp, err := t.base.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	resp.Body = &limitedReadCloser{
		Reader:     io.LimitReader(resp.Body, MaxScrapeBytes),
		Closer:     resp.Body,
		exceededAt: MaxScrapeBytes,
	}
	return resp, nil
}

type limitedReadCloser struct {
	io.Reader
	io.Closer
	exceededAt int64
	read       int64
}

func (l *limitedReadCloser) Read(p []byte) (int, error) {
	n, err := l.Reader.Read(p)
	l.read += int64(n)
	if err == io.EOF && l.read >= l.exceededAt {
		// Differentiate "we truncated" from "clean EOF" only if needed; the
		// underlying LimitReader already returns EOF at the cap.
	}
	return n, err
}

// keep syscall import referenced on platforms that need it for dialer control.
var _ = syscall.ECONNREFUSED