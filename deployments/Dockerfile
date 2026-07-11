# Multi-stage build for game-search-backend.
FROM golang:1.25-alpine AS builder
WORKDIR /src

# Cache deps first.
COPY go.mod go.sum ./
RUN go mod download

# Build the rest. CGO disabled for a static alpine binary.
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o /out/server .

# Runtime image: minimal, non-root, healthcheck on /health.
FROM alpine:3.20
RUN apk add --no-cache ca-certificates wget && \
    adduser -D -u 10001 app
COPY --from=builder /out/server /app/server
USER app
EXPOSE 3011
ENV PORT=3011
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD wget -qO- http://127.0.0.1:${PORT}/health || exit 1
ENTRYPOINT ["/app/server"]