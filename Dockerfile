# Build stage. Pin the toolchain in one place: go.mod and CI must match.
FROM golang:1.26 AS build

WORKDIR /src

# Cache module downloads separately from source changes.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG VERSION=dev
ENV CGO_ENABLED=0
RUN set -eux; \
    for cmd in api worker admin eval; do \
        go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o "/out/${cmd}" "./cmd/${cmd}"; \
    done

# Runtime stage. Alpine gives us a shell for healthchecks and keeps the image
# small; all binaries are static.
FROM alpine:3.22

RUN apk add --no-cache ca-certificates wget \
    && addgroup -g 10001 medverify \
    && adduser -D -u 10001 -G medverify medverify

WORKDIR /app
COPY --from=build /out/ /app/
# WEB_DIR defaults to "web"; without these files the API refuses to start.
COPY --chown=medverify:medverify --from=build /src/web /app/web

USER medverify
EXPOSE 8080

# CMD, not ENTRYPOINT: docker compose overrides the command to run the worker
# or admin binary from the same image.
CMD ["/app/api"]
