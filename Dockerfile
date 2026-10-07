# syntax=docker/dockerfile:1

# ---------------------------------------------------------------------------
# 1) Build the Next.js + shadcn static export.
# ---------------------------------------------------------------------------
FROM node:24-alpine AS web
WORKDIR /web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
# Produces /web/out and copies the export to /internal/site/web for go:embed.
RUN npm run build

# ---------------------------------------------------------------------------
# 2) Build the Go server and CLI with the embedded frontend.
# ---------------------------------------------------------------------------
FROM golang:1.27-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /internal/site/web ./internal/site/web
RUN CGO_ENABLED=1 go build -tags with_quic,with_utls -trimpath -ldflags="-s -w" -o /out/xgift ./cmd/xgift \
 && CGO_ENABLED=1 go build -tags with_quic,with_utls -trimpath -ldflags="-s -w" -o /out/xgift-web ./cmd/xgift-web

# ---------------------------------------------------------------------------
# 3) Minimal runtime. The entrypoint fixes /data ownership, then drops root.
# ---------------------------------------------------------------------------
FROM debian:bookworm-slim
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates tzdata gosu curl \
 && rm -rf /var/lib/apt/lists/* \
 && useradd --system --uid 10001 --home-dir /data --shell /usr/sbin/nologin xgift \
 && mkdir -p /data && chown xgift:xgift /data
COPY --from=build /out/xgift /out/xgift-web /usr/local/bin/
COPY docker/entrypoint.sh /usr/local/bin/docker-entrypoint.sh
RUN chmod 0755 /usr/local/bin/docker-entrypoint.sh
WORKDIR /data
EXPOSE 8787
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD curl -fsS http://127.0.0.1:8787/livez || exit 1
ENTRYPOINT ["/usr/local/bin/docker-entrypoint.sh"]
CMD ["xgift-web"]
