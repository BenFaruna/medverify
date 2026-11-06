package main

import (
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// webPages maps a URL path to the page file served there. Each page is its own
// document rather than a client-side route: a failed sign-in still has to work
// with JavaScript half-loaded, and the app page stays exactly as it was.
var webPages = map[string]string{
	"/":         "index.html",
	"/login":    "login.html",
	"/register": "register.html",
	"/settings": "settings.html",
}

// webClient serves the static client at "/", the account pages, and "/assets/".
// Leaving the directory unset disables it, which is what a deployment that
// hosts the client elsewhere wants.
//
// The client is a plain static bundle: it calls only the documented public API
// and never an internal endpoint.
func webClient(dir string) (http.Handler, error) {
	if dir == "" {
		return nil, nil
	}

	info, err := os.Stat(dir)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, &fs.PathError{Op: "open", Path: dir, Err: errors.New("not a directory")}
	}

	pages := make(map[string]string, len(webPages))
	for route, name := range webPages {
		path := filepath.Join(dir, name)
		if _, err := os.Stat(path); err != nil {
			return nil, err
		}
		pages[route] = path
	}

	assets := filepath.Join(dir, "assets")

	router := http.NewServeMux()
	router.Handle("/assets/", cacheable(http.StripPrefix("/assets/", http.FileServer(http.Dir(assets)))))
	router.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// Only a known page is served. Anything else stays a problem-JSON 404
		// from the API router rather than an HTML page.
		page, ok := pages[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		http.ServeFile(w, r, page)
	})

	// Reject a path with a `.` or `..` segment outright. http.ServeMux would
	// redirect it to the cleaned form, which is safe but turns a probe into a
	// 301; a plain 404 keeps the answer uniform.
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isCleanPath(r.URL.Path) {
			http.NotFound(w, r)
			return
		}
		router.ServeHTTP(w, r)
	}), nil
}

// isCleanPath reports whether a URL path has no `.` or `..` segment.
func isCleanPath(raw string) bool {
	for _, segment := range strings.Split(raw, "/") {
		if segment == "." || segment == ".." {
			return false
		}
	}
	return true
}

// cacheable marks hashed-agnostic static assets as revalidated. The bundle is
// small and changes with deploys, so a short max-age plus revalidation is a
// better trade than a hard cache.
func cacheable(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=300")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		next.ServeHTTP(w, r)
	})
}
