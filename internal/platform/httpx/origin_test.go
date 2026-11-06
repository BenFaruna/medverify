package httpx

import (
	"net/http"
	"testing"
)

func TestSameOrigin(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		headers map[string]string
		want    bool
	}{
		{
			name: "a non-browser client sends neither header",
			want: true,
		},
		{
			name:    "same-origin fetch",
			headers: map[string]string{"Sec-Fetch-Site": "same-origin"},
			want:    true,
		},
		{
			name:    "direct navigation",
			headers: map[string]string{"Sec-Fetch-Site": "none"},
			want:    true,
		},
		{
			name:    "link from another site",
			headers: map[string]string{"Sec-Fetch-Site": "cross-site"},
			want:    false,
		},
		{
			name:    "sibling subdomain",
			headers: map[string]string{"Sec-Fetch-Site": "same-site"},
			want:    false,
		},
		{
			name:    "origin matches the request host",
			headers: map[string]string{"Origin": "http://example.com"},
			want:    true,
		},
		{
			name:    "origin host differs",
			headers: map[string]string{"Origin": "https://attacker.example"},
			want:    false,
		},
		{
			name:    "opaque origin from a sandboxed frame",
			headers: map[string]string{"Origin": "null"},
			want:    false,
		},
		{
			name:    "origin is not a URL",
			headers: map[string]string{"Origin": "::not a url::"},
			want:    false,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			request, err := http.NewRequest(http.MethodPost, "http://example.com/v1/scans", nil)
			if err != nil {
				t.Fatalf("new request: %v", err)
			}
			request.Host = "example.com"
			for name, value := range testCase.headers {
				request.Header.Set(name, value)
			}

			if got := SameOrigin(request); got != testCase.want {
				t.Fatalf("SameOrigin() = %v, want %v", got, testCase.want)
			}
		})
	}
}
