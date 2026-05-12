# ── Stage 1: Builder ────────────────────────────────────────────────────────
FROM golang:1.26-alpine AS builder

# gcc + musl-dev needed for go-sqlite3 (CGO)
RUN apk add --no-cache gcc musl-dev

WORKDIR /build

# Cache dependencies layer
COPY go.mod go.sum ./
RUN go mod download

# Copy source
COPY . .

# Compile: static binary, strip debug info for minimal size
# Inject version via ldflags (defaults to 'dev' if not provided)
ARG VERSION=dev
ARG VERSION_SUFFIX=""
RUN CGO_ENABLED=1 GOOS=linux \
  go build \
  -ldflags="-s -w -extldflags '-static' -X main.AppVersion=${VERSION}${VERSION_SUFFIX}" \
  -trimpath \
  -o e-conomato \
  ./cmd/server

# ── Stage 2: Runtime ─────────────────────────────────────────────────────────
# alpine gives us CA certs (needed for LDAPS) and a minimal libc
FROM alpine:3.19

RUN apk add --no-cache ca-certificates tzdata \
  && adduser -D -u 1001 econom \
  && mkdir -p /data \
  && chown -R econom:econom /data

WORKDIR /app

# Default DB path inside the container
ENV DB_PATH=/data/magazzino.db

# Copy compiled binary
COPY --from=builder /build/e-conomato .

# Copy web assets (templates + static files)
COPY --chown=econom:econom web/ ./web/

# Data volume: file SQLite del database (immagini prodotto incluse come BLOB)
VOLUME ["/data"]

# Run as non-root
USER econom

EXPOSE 8080

ENTRYPOINT ["/app/e-conomato"]
