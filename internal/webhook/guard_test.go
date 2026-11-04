package webhook

import (
	"context"
	"errors"
	"net/netip"
	"testing"
)

func TestValidateURLRejectsUnsafeTargets(t *testing.T) {
	t.Parallel()

	unsafe := []string{
		"http://example.test/hook",         // not HTTPS
		"https://localhost/hook",           // loopback by name
		"https://127.0.0.1/hook",           // loopback
		"https://10.1.2.3/hook",            // private
		"https://192.168.0.10/hook",        // private
		"https://169.254.169.254/latest",   // cloud metadata
		"https://[::1]/hook",               // IPv6 loopback
		"https://[fd00::1]/hook",           // IPv6 unique local
		"https://user:pass@example.test/h", // credentials
		"https:///hook",                    // no host
		"://missing-scheme",                // unparsable
	}
	for _, raw := range unsafe {
		if _, err := ValidateURL(raw); !errors.Is(err, ErrUnsafeURL) {
			t.Fatalf("ValidateURL(%q) error = %v, want ErrUnsafeURL", raw, err)
		}
	}
}

func TestValidateURLAllowsPublicHTTPS(t *testing.T) {
	t.Parallel()

	if _, err := ValidateURL("https://hooks.example.test/medverify"); err != nil {
		t.Fatalf("ValidateURL: %v", err)
	}
	if _, err := ValidateURL("https://203.0.114.10/hook"); err != nil {
		t.Fatalf("ValidateURL(public ipv4): %v", err)
	}
}

func TestIsPublicAddr(t *testing.T) {
	t.Parallel()

	public := []string{"8.8.8.8", "1.1.1.1", "203.0.114.10", "2606:4700:4700::1111"}
	for _, raw := range public {
		if !IsPublicAddr(netip.MustParseAddr(raw)) {
			t.Fatalf("%s must be public", raw)
		}
	}

	internal := []string{"127.0.0.1", "10.0.0.1", "192.168.1.1", "172.16.0.1", "169.254.169.254", "0.0.0.0", "::1", "fc00::1", "fe80::1"}
	for _, raw := range internal {
		if IsPublicAddr(netip.MustParseAddr(raw)) {
			t.Fatalf("%s must not be public", raw)
		}
	}

	// An IPv4 address mapped into IPv6 is still the IPv4 address.
	if IsPublicAddr(netip.MustParseAddr("::ffff:127.0.0.1")) {
		t.Fatal("an IPv4-mapped loopback must stay private")
	}
}

type staticResolver map[string][]string

func (r staticResolver) LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error) {
	answers, ok := r[host]
	if !ok {
		return nil, errors.New("no such host")
	}
	addrs := make([]netip.Addr, 0, len(answers))
	for _, answer := range answers {
		addrs = append(addrs, netip.MustParseAddr(answer))
	}
	return addrs, nil
}

func TestCheckResolutionRejectsAnyInternalAnswer(t *testing.T) {
	t.Parallel()

	// A host that answers with one public and one private address must be
	// rejected: the client could be sent to either.
	resolver := staticResolver{"mixed.example.test": {"93.184.216.34", "10.0.0.5"}}
	if err := CheckResolution(context.Background(), resolver, "mixed.example.test"); !errors.Is(err, ErrUnsafeURL) {
		t.Fatalf("CheckResolution error = %v, want ErrUnsafeURL", err)
	}
}

func TestCheckResolutionAllowsFullyPublicHost(t *testing.T) {
	t.Parallel()

	resolver := staticResolver{"hooks.example.test": {"93.184.216.34"}}
	if err := CheckResolution(context.Background(), resolver, "hooks.example.test"); err != nil {
		t.Fatalf("CheckResolution: %v", err)
	}
}

func TestCheckResolutionRejectsAnUnknownHost(t *testing.T) {
	t.Parallel()

	if err := CheckResolution(context.Background(), staticResolver{}, "missing.example.test"); !errors.Is(err, ErrUnsafeURL) {
		t.Fatalf("CheckResolution error = %v, want ErrUnsafeURL", err)
	}
}
