package webhook

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"syscall"
	"time"
)

// ErrUnsafeURL means a webhook target could reach an internal address.
var ErrUnsafeURL = errors.New("webhook: unsafe destination")

// privatePrefixes are ranges that must never receive a webhook: loopback,
// link-local (including cloud metadata), private, and the CGNAT plus
// documentation ranges that hosting providers sometimes route internally.
var privatePrefixes = mustPrefixes(
	"0.0.0.0/8",
	"10.0.0.0/8",
	"100.64.0.0/10",
	"127.0.0.0/8",
	"169.254.0.0/16",
	"172.16.0.0/12",
	"192.0.0.0/24",
	"192.0.2.0/24",
	"192.168.0.0/16",
	"198.18.0.0/15",
	"198.51.100.0/24",
	"203.0.113.0/24",
	"224.0.0.0/4",
	"240.0.0.0/4",
	"::/128",
	"::1/128",
	"fc00::/7",
	"fe80::/10",
	"ff00::/8",
	"2001:db8::/32",
)

func mustPrefixes(cidrs ...string) []netip.Prefix {
	prefixes := make([]netip.Prefix, 0, len(cidrs))
	for _, cidr := range cidrs {
		prefix, err := netip.ParsePrefix(cidr)
		if err != nil {
			panic("webhook: bad private CIDR " + cidr + ": " + err.Error())
		}
		prefixes = append(prefixes, prefix)
	}
	return prefixes
}

// IsPublicAddr reports whether an address is safe to connect to.
func IsPublicAddr(addr netip.Addr) bool {
	if !addr.IsValid() {
		return false
	}
	if addr.IsUnspecified() || addr.IsLoopback() || addr.IsMulticast() ||
		addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast() || addr.IsPrivate() {
		return false
	}
	// An IPv4 address embedded in IPv6 must be checked as IPv4 too.
	if addr.Is4In6() {
		return IsPublicAddr(addr.Unmap())
	}
	for _, prefix := range privatePrefixes {
		if prefix.Contains(addr) {
			return false
		}
	}
	return true
}

// ValidateURL checks the scheme, shape, and any literal IP host of a webhook
// URL. It cannot judge a hostname: that happens at connect time, because DNS
// can answer differently later.
func ValidateURL(raw string) (*url.URL, error) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("%w: not a URL", ErrUnsafeURL)
	}
	if parsed.Scheme != "https" {
		return nil, fmt.Errorf("%w: webhook URLs must use https", ErrUnsafeURL)
	}
	if parsed.User != nil {
		return nil, fmt.Errorf("%w: credentials are not allowed in the URL", ErrUnsafeURL)
	}
	host := parsed.Hostname()
	if host == "" {
		return nil, fmt.Errorf("%w: the URL has no host", ErrUnsafeURL)
	}
	if addr, err := netip.ParseAddr(host); err == nil && !IsPublicAddr(addr) {
		return nil, fmt.Errorf("%w: %s is not a public address", ErrUnsafeURL, host)
	}
	if host == "localhost" || host == "localhost.localdomain" {
		return nil, fmt.Errorf("%w: localhost is not a public address", ErrUnsafeURL)
	}
	return parsed, nil
}

// Resolver is the subset of net.Resolver the guard needs, so tests can supply
// answers without touching DNS.
type Resolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

// CheckResolution resolves a host and rejects it if any answer is internal.
// Checking every answer, not just the first, closes the case where a hostname
// returns one public and one private address.
func CheckResolution(ctx context.Context, resolver Resolver, host string) error {
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	addrs, err := resolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return fmt.Errorf("%w: cannot resolve %s", ErrUnsafeURL, host)
	}
	if len(addrs) == 0 {
		return fmt.Errorf("%w: %s resolved to no addresses", ErrUnsafeURL, host)
	}
	for _, addr := range addrs {
		if !IsPublicAddr(addr) {
			return fmt.Errorf("%w: %s resolves to %s", ErrUnsafeURL, host, addr)
		}
	}
	return nil
}

// guardedDialControl re-checks the address the socket is actually connecting to.
// Resolving a name and then dialing by name would leave a window for a second
// DNS answer to point inward; this closes it at connect time.
func guardedDialControl(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("%w: bad address", ErrUnsafeURL)
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return fmt.Errorf("%w: bad address", ErrUnsafeURL)
	}
	if !IsPublicAddr(addr) {
		return fmt.Errorf("%w: %s is not a public address", ErrUnsafeURL, addr)
	}
	return nil
}

// dialer returns a dialer that refuses internal addresses.
func dialer(timeout time.Duration) *net.Dialer {
	return &net.Dialer{
		Timeout:   timeout,
		KeepAlive: 30 * time.Second,
		Control:   guardedDialControl,
	}
}
