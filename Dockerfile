# Multi-stage build for Go backend

# --- Build stage ---
FROM golang:1.24.4-alpine AS builder
WORKDIR /src

# System deps
RUN apk add --no-cache git

# Go modules first (better cache)
COPY backend/go.mod backend/go.sum ./backend/
WORKDIR /src/backend
RUN go mod download

# Copy backend source
COPY backend/. .

# Build static-ish binary
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /out/server ./cmd/main.go

# --- Runtime stage ---
FROM alpine:3.20
WORKDIR /app
RUN apk add --no-cache ca-certificates tzdata

# Copy binary
COPY --from=builder /out/server /usr/local/bin/server

# Default envs (can be overridden at runtime)
ENV MATCH_LIMIT=10

# Example: docker run -e RIOT_API_KEY=... -v $(pwd)/backend:/data -w /data <image>
ENTRYPOINT ["/usr/local/bin/server"]
