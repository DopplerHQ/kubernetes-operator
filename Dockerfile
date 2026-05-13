# Build the manager and CSI provider binaries
FROM golang:1.25 AS builder

ARG CONTROLLER_VERSION
ARG TARGETOS
ARG TARGETARCH

WORKDIR /workspace
# Copy the Go Modules manifests
COPY go.mod go.mod
COPY go.sum go.sum
# cache deps before building and copying source so that we don't need to re-download as much
# and so that source changes don't invalidate our downloaded layer
RUN go mod download

# Copy the go source
COPY main.go main.go
COPY api/ api/
COPY cmd/ cmd/
COPY controllers/ controllers/
COPY pkg/ pkg/

# Build
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} GO111MODULE=on go build -a -ldflags="-X 'github.com/DopplerHQ/kubernetes-operator/pkg/version.ControllerVersion=${CONTROLLER_VERSION}'" -o manager main.go
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} GO111MODULE=on go build -a -ldflags="-X 'github.com/DopplerHQ/kubernetes-operator/pkg/version.ControllerVersion=${CONTROLLER_VERSION}'" -o csi-provider ./cmd/csi-provider

# Use distroless as minimal base image to package the manager binary
# Refer to https://github.com/GoogleContainerTools/distroless for more details
FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY --from=builder /workspace/manager .
COPY --from=builder /workspace/csi-provider .
USER 65532:65532

ENTRYPOINT ["/manager"]
