package httpx

import (
	"net/http"
	"net/url"
	"strings"
)

// SameOrigin reports whether a browser sent the request from this origin.
//
// Cookies travel with any request a browser makes to this host, including one
// started by another site, so a cookie-authenticated write needs a check that a
// form on attacker.example cannot satisfy. The check reads the headers browsers
// attach themselves; a non-browser client sends neither, and is allowed through
// because it cannot have been tricked into carrying a cookie it does not own.
func SameOrigin(r *http.Request) bool {
	switch site := r.Header.Get("Sec-Fetch-Site"); site {
	case "", "same-origin", "none":
		// "none" is a direct navigation, such as typing the address.
	default:
		return false
	}

	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	parsed, err := url.Parse(origin)
	if err != nil {
		return false
	}
	return strings.EqualFold(parsed.Host, r.Host)
}
