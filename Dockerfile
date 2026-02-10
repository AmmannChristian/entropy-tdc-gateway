# Multi-stage build for the entropy-tdc-gateway service (pure Go).

# Builder: compile the gateway binary
FROM golang:1.25-alpine AS build

WORKDIR /src
RUN apk add --no-cache git
ENV CGO_ENABLED=0 \
    GOOS=linux

# Go module files first for better layer caching
COPY go.mod go.sum ./
RUN go mod download

# Copy the remaining source
COPY . .

# Build static binary
RUN go build -trimpath -ldflags="-s -w" -o /out/entropy-tdc-gateway ./cmd/entropy-tdc-gateway

# Runtime image
FROM alpine:3.18

LABEL maintainer="Christian Ammann" \
      description="entropy-tdc-gateway edge entropy collection service"

# Minimal runtime deps and non-root user
RUN apk add --no-cache ca-certificates libc6-compat && \
    addgroup -g 1000 gateway && \
    adduser -D -u 1000 -G gateway gateway

# Copy binary
COPY --from=build /out/entropy-tdc-gateway /usr/local/bin/entropy-tdc-gateway

# Expose default ports (entropy HTTP + metrics)
EXPOSE 9797 8080

USER gateway

ENTRYPOINT ["/usr/local/bin/entropy-tdc-gateway"]
