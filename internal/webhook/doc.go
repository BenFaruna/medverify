// Package webhook delivers scan events to integrator endpoints.
//
// A payload is signed with HMAC-SHA256 over "<unix timestamp>.<body>" using a
// per-endpoint secret. Receivers reject old timestamps, so a captured request
// cannot be replayed indefinitely.
//
// Endpoints are chosen by customers, so every URL passes an SSRF guard: HTTPS
// only, no literal private or loopback addresses, and the resolved address is
// re-checked at connect time so a DNS answer cannot swing the connection into
// the internal network.
package webhook
