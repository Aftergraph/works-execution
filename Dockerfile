# WORKS — deployable container (durable execution control plane)
# Go 1.23 builder + distroless runtime.
FROM golang:1.23-alpine AS builder

WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -o /out/works-api ./cmd/works-api

# ── runtime ──
FROM gcr.io/distroless/static-debian12

COPY --from=builder /out/works-api /works-api
COPY policies/ /policies/

ENV WORKS_ADDR=0.0.0.0:8080
ENV WORKS_DB=/data/works.db
EXPOSE 8080

# Fail-closed: enrollment disabled unless WORKS_ENROLL_SECRET is set.
HEALTHCHECK --interval=30s --timeout=5s --retries=3 \
  CMD ["/works-api", "-healthz-only"]

# distroless runs as non-root by default.
VOLUME ["/data"]

ENTRYPOINT ["/works-api"]
