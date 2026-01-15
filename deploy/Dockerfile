FROM golang:1.25.1 AS builder

WORKDIR /app

# Copy go mod files
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build the application
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o dinq_message .

# Final stage
FROM alpine:latest

# Install dependencies
RUN apk --no-cache add ca-certificates curl postgresql-client tzdata

# Create non-root user
RUN adduser -D -s /bin/sh appuser

WORKDIR /app

# Copy binary from builder
COPY --from=builder /app/dinq_message .

# Change ownership
RUN chown -R appuser:appuser /app

USER appuser

# Expose port
EXPOSE 8080

# Health check
HEALTHCHECK --interval=30s --timeout=10s --start-period=40s --retries=3 \
    CMD curl -f http://localhost:8080/health || exit 1

# Run the application
CMD ["./dinq_message"]
