package api

import (
	"bytes"
	"embed"
	"io/fs"
	"net/http"
	"time"
)

// The document and the interactive UI are embedded so a binary built from a
// clean checkout can serve API documentation without a second file, a Node
// build, or a network call.
//
//go:embed openapi.yaml
var specYAML []byte

//go:embed swaggerui
var uiFS embed.FS

// Spec returns a copy of the OpenAPI document served at /openapi.yaml.
func Spec() []byte {
	return append([]byte(nil), specYAML...)
}

// DocsHandler serves the interactive API documentation and the raw contract:
//
//	GET /docs             redirects to /docs/
//	GET /docs/            Swagger UI
//	GET /docs/<asset>     vendored Swagger UI assets
//	GET /openapi.yaml     the OpenAPI document
//
// It is deliberately unauthenticated: it exposes no data, only the public
// contract. The routes are mounted by cmd/api before the static web client.
func DocsHandler() http.Handler {
	assets, err := fs.Sub(uiFS, "swaggerui")
	if err != nil {
		// The embed directive above makes this unreachable; fail loudly at
		// startup rather than serving a half-broken docs page.
		panic("api: embedded swagger UI assets are missing: " + err.Error())
	}

	files := http.FileServer(http.FS(assets))

	mux := http.NewServeMux()
	mux.HandleFunc("GET /openapi.yaml", serveSpec)
	mux.HandleFunc("GET /docs", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/docs/", http.StatusMovedPermanently)
	})
	mux.HandleFunc("GET /docs/{$}", serveDocs)
	mux.Handle("GET /docs/", http.StripPrefix("/docs/", cacheableDocsAsset(files)))
	return mux
}

func serveSpec(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition", `inline; filename="openapi.yaml"`)
	http.ServeContent(w, r, "openapi.yaml", time.Time{}, bytes.NewReader(specYAML))
}

func serveDocs(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	_, _ = w.Write([]byte(docsHTML))
}

// cacheableDocsAsset lets a browser reuse the vendored bundle for a short time.
// The bundle changes only with a deploy, but it is not content-hashed, so a
// short max-age plus revalidation is safer than an immutable cache.
func cacheableDocsAsset(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=300")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		next.ServeHTTP(w, r)
	})
}

// docsHTML is kept inline so the docs page has no template or build step. The
// spec URL is relative to the serving origin. Authorization is not persisted in
// browser storage; an operator must re-enter an API key after a reload.
const docsHTML = `<!doctype html>
<html lang="en">
  <head>
    <meta charset="utf-8" />
    <meta name="viewport" content="width=device-width, initial-scale=1" />
    <meta name="color-scheme" content="light dark" />
    <title>MedVerify NG API documentation</title>
    <link rel="icon" type="image/png" sizes="32x32" href="/docs/favicon-32x32.png" />
    <link rel="icon" type="image/png" sizes="16x16" href="/docs/favicon-16x16.png" />
    <link rel="stylesheet" href="/docs/swagger-ui.css" />
    <style>
      body { margin: 0; background: #fafafa; }
      .medverify-hint {
        margin: 0;
        padding: 0.65rem 1rem;
        border-bottom: 1px solid #d8dee4;
        background: #fff8e1;
        color: #3d3210;
        font: 14px/1.45 system-ui, -apple-system, "Segoe UI", sans-serif;
      }
      .medverify-hint a { color: #0b5cad; }
    </style>
  </head>
  <body>
    <p class="medverify-hint">
      Public contract for MedVerify NG. Use <strong>Authorize</strong> with a
      test key to try the endpoints. Keys are kept in memory only and are not
      written to browser storage. The integrator guide covers scopes, quotas,
      and webhook behaviour.
    </p>
    <div id="swagger-ui"></div>
    <script src="/docs/swagger-ui-bundle.js" charset="UTF-8"></script>
    <script src="/docs/swagger-ui-standalone-preset.js" charset="UTF-8"></script>
    <script>
      window.onload = function () {
        window.ui = SwaggerUIBundle({
          url: "/openapi.yaml",
          dom_id: "#swagger-ui",
          deepLinking: true,
          displayRequestDuration: true,
          tryItOutEnabled: true,
          persistAuthorization: false,
          withCredentials: false,
          validatorUrl: null,
          presets: [SwaggerUIBundle.presets.apis, SwaggerUIStandalonePreset],
          plugins: [SwaggerUIBundle.plugins.DownloadUrl],
          layout: "StandaloneLayout",
        });
      };
    </script>
  </body>
</html>
`
