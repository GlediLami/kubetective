# Multi-stage distroless image (roadmap v0.9): non-root, no shell, no
# package manager. Kubetective only needs its two binaries and the
# container reads the cluster via kubeconfig/in-cluster config.
#
#   docker build -t ghcr.io/gledilami/kubetective:latest .
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X github.com/GlediLami/kubetective/internal/engine.Version=v0.9.0-dev" \
    -o /out/kubetective ./cmd/kubetective

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/kubetective /usr/local/bin/kubetective
COPY LICENSE /usr/share/licenses/kubetective/LICENSE
COPY NOTICE /usr/share/licenses/kubetective/NOTICE
# Incident store + config live on the default state dir; keep it writable
# by the non-root user (65532).
ENV KUBETECTIVE_HOME=/data/kubetective
USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/kubetective"]
CMD ["serve", "--listen", ":8080"]