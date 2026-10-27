# WORKS — deployable container (durable execution control plane)
# Go 1.25 builder + distroless runtime.
# golang:1.25-alpine, pinned by digest (OpenSSF Scorecard PinnedDependencies)
FROM golang:1.27-alpine@sha256:8a5910f31396cd4d89662f56c68b3ae31d374308270a1c3bd96672ee5ed43414 AS builder

WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -o /out/works-api ./cmd/works-api

# ── runtime ──
# gcr.io/distroless/static-debian12:latest, pinned by digest
FROM gcr.io/distroless/static-debian12@sha256:d75cdd72874d4790092fcb1b058493ecf6bb5bf2b2b897045b00ff01d91843f2

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
