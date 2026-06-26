# MULTI-STAGE DOCKERFILE FOR OFFLINEPAY

# Build stage
FROM golang:1.23-alpine AS builder

# Install build dependencies
RUN apk add --no-cache git gcc musl-dev

WORKDIR /app

# Copy dependency files and download
COPY go.mod go.sum ./
RUN go mod download

# Copy the entire codebase
COPY . .

# Build the main server binary
RUN CGO_ENABLED=0 GOOS=linux go build -o offlinepay-server ./cmd/server/main.go

# Build the simulation binary (useful for running inside container if needed)
RUN CGO_ENABLED=0 GOOS=linux go build -o offlinepay-simulation ./cmd/simulation/main.go

# Final release stage
FROM alpine:3.19

RUN apk add --no-cache ca-certificates tzdata

WORKDIR /app

# Copy compiled binaries from builder
COPY --from=builder /app/offlinepay-server .
COPY --from=builder /app/offlinepay-simulation .

# Expose ports: API (8080) and Prometheus metrics (9090)
EXPOSE 8080
EXPOSE 9090

# Default run command
ENTRYPOINT ["./offlinepay-server"]
