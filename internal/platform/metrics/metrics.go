// Package metrics exposes a small, dependency-free Prometheus text endpoint.
//
// Only aggregate HTTP counters live here. Requests are labelled by route
// pattern, never by path, so a scan ID cannot multiply series or leak into a
// metric. No user text, image data, coordinates, or API key is ever recorded.
package metrics

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Metrics is a concurrency-safe counter and duration registry.
type Metrics struct {
	mu        sync.Mutex
	requests  map[requestKey]uint64
	durations map[requestKey]duration
	events    map[eventKey]uint64
}

type requestKey struct {
	method string
	route  string
	status int
}

type duration struct {
	total time.Duration
	count uint64
}

type eventKey struct {
	name   string
	labels string
}

// New returns an empty registry.
func New() *Metrics {
	return &Metrics{
		requests:  make(map[requestKey]uint64),
		durations: make(map[requestKey]duration),
		events:    make(map[eventKey]uint64),
	}
}

// ObserveRequest records one completed request. route must be the router
// pattern (for example "/v1/scans/{id}"), not the concrete path.
func (m *Metrics) ObserveRequest(method, route string, status int, elapsed time.Duration) {
	if m == nil {
		return
	}
	if route == "" {
		route = "unmatched"
	}
	key := requestKey{method: method, route: route, status: status}

	m.mu.Lock()
	defer m.mu.Unlock()
	m.requests[key]++
	current := m.durations[key]
	current.total += elapsed
	current.count++
	m.durations[key] = current
}

// Inc increments a named counter with optional labels. Labels must be a fixed
// set of values, never user input.
func (m *Metrics) Inc(name string, labels ...string) {
	if m == nil || name == "" {
		return
	}
	if len(labels)%2 != 0 {
		labels = labels[:len(labels)-1]
	}
	parts := make([]string, 0, len(labels)/2)
	for i := 0; i < len(labels); i += 2 {
		parts = append(parts, labels[i]+`="`+escape(labels[i+1])+`"`)
	}
	sort.Strings(parts)

	m.mu.Lock()
	defer m.mu.Unlock()
	m.events[eventKey{name: name, labels: strings.Join(parts, ",")}]++
}

// Handler serves the Prometheus text exposition format.
func (m *Metrics) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "text/plain; version=0.0.4; charset=utf-8")
		w.Header().Set("cache-control", "no-store")
		m.write(w)
	})
}

func (m *Metrics) write(w http.ResponseWriter) {
	type requestEntry struct {
		key      requestKey
		count    uint64
		duration duration
	}
	type eventEntry struct {
		key   eventKey
		count uint64
	}

	m.mu.Lock()
	requests := make([]requestEntry, 0, len(m.requests))
	for key, count := range m.requests {
		requests = append(requests, requestEntry{key: key, count: count, duration: m.durations[key]})
	}
	events := make([]eventEntry, 0, len(m.events))
	for key, count := range m.events {
		events = append(events, eventEntry{key: key, count: count})
	}
	m.mu.Unlock()

	sort.Slice(requests, func(i, j int) bool { return lessRequest(requests[i].key, requests[j].key) })
	sort.Slice(events, func(i, j int) bool {
		if events[i].key.name != events[j].key.name {
			return events[i].key.name < events[j].key.name
		}
		return events[i].key.labels < events[j].key.labels
	})

	_, _ = fmt.Fprintln(w, "# HELP medverify_http_requests_total Total HTTP requests handled.")
	_, _ = fmt.Fprintln(w, "# TYPE medverify_http_requests_total counter")
	for _, entry := range requests {
		_, _ = fmt.Fprintf(w, "medverify_http_requests_total{method=%q,route=%q,status=%q} %d\n",
			entry.key.method, entry.key.route, strconv.Itoa(entry.key.status), entry.count)
	}

	_, _ = fmt.Fprintln(w, "# HELP medverify_http_request_duration_seconds Request duration in seconds.")
	_, _ = fmt.Fprintln(w, "# TYPE medverify_http_request_duration_seconds summary")
	for _, entry := range requests {
		labels := fmt.Sprintf("method=%q,route=%q,status=%q", entry.key.method, entry.key.route, strconv.Itoa(entry.key.status))
		_, _ = fmt.Fprintf(w, "medverify_http_request_duration_seconds_sum{%s} %g\n", labels, entry.duration.total.Seconds())
		_, _ = fmt.Fprintf(w, "medverify_http_request_duration_seconds_count{%s} %d\n", labels, entry.duration.count)
	}

	_, _ = fmt.Fprintln(w, "# HELP medverify_events_total Named application events.")
	_, _ = fmt.Fprintln(w, "# TYPE medverify_events_total counter")
	for _, entry := range events {
		if entry.key.labels == "" {
			_, _ = fmt.Fprintf(w, "%s %d\n", entry.key.name, entry.count)
			continue
		}
		_, _ = fmt.Fprintf(w, "%s{%s} %d\n", entry.key.name, entry.key.labels, entry.count)
	}
}

func lessRequest(a, b requestKey) bool {
	if a.route != b.route {
		return a.route < b.route
	}
	if a.method != b.method {
		return a.method < b.method
	}
	return a.status < b.status
}

func escape(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `"`, `\"`)
	return strings.ReplaceAll(value, "\n", `\n`)
}
