# Build stage
FROM golang:1.26-alpine AS builder

ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_DATE=unknown
ARG TARGETOS
ARG TARGETARCH

WORKDIR /workspace

# Copy go module files
COPY go.mod go.sum ./
RUN go mod download

# Copy source
COPY cmd/ cmd/
COPY internal/ internal/

# Build static binary
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64} \
    go build \
    -ldflags "-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.buildDate=${BUILD_DATE}" \
    -o /workspace/goma-k8s-provider \
    ./cmd

# Runtime stage
FROM gcr.io/distroless/static:nonroot

WORKDIR /
COPY --from=builder /workspace/goma-k8s-provider /usr/local/bin/goma-k8s-provider

USER 65532:65532

ENTRYPOINT ["/usr/local/bin/goma-k8s-provider"]
