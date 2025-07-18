# Build stage
FROM golang:1.24-alpine AS builder

# Install git for go modules
RUN apk add --no-cache git

# Set working directory
WORKDIR /app

# Copy go mod files
COPY go.mod go.sum ./

# Download dependencies
RUN go mod download

# Copy source code
COPY . .

# Build the binary
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o omet .

# Final stage
FROM alpine:latest

# Install ca-certificates for HTTPS requests (if needed)
RUN apk --no-cache add ca-certificates

# Create non-root user
RUN adduser -D -s /bin/sh omet

# Set working directory
WORKDIR /home/omet

# Copy binary from builder stage
COPY --from=builder /app/omet .

# Change ownership
RUN chown omet:omet omet

# Switch to non-root user
USER omet

# Set entrypoint
ENTRYPOINT ["./omet"]
