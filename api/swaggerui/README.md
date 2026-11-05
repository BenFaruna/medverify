# Vendored Swagger UI

These files are the static Swagger UI distribution used by `GET /docs`.

- Package: [`swagger-ui-dist`](https://www.npmjs.com/package/swagger-ui-dist)
- Version: 5.33.0
- Upstream: <https://github.com/swagger-api/swagger-ui>
- License: Apache-2.0 (see `LICENSE` and `NOTICE`)

Only the files the page needs are vendored: the bundle, the standalone preset,
the stylesheet, the two favicons, and the license notices. The source maps,
ES-module bundles, and test fixtures from the npm package are intentionally not
included.

To upgrade, replace these files with the same paths from the new
`swagger-ui-dist` release, update the version above, and run `go test ./api/...`
and `go test ./cmd/api/...`.
