# syntax=docker/dockerfile:1

ARG GO_VERSION

# ---- Build ----------------------------------------------------------------
FROM golang:${GO_VERSION}-alpine AS builder
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
ARG REVISION=dev

# upx (build stage only) compresses the final binary to shrink the image.
RUN apk add --no-cache upx

WORKDIR /workspace
COPY go.mod go.sum ./
RUN go mod download

# Only the runtime binary is built and shipped; cmd/schema is a dev/CI-only
# generator (see cmd/schema/main.go), so keep it out of the build context.
COPY cmd/chaski/ cmd/chaski/
COPY internal/ internal/

RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} \
    go build -trimpath \
    -ldflags "-s -w -X main.version=${VERSION} -X main.commit=${REVISION}" \
    -o chaski ./cmd/chaski

RUN upx --best --lzma chaski

# ---- Runtime --------------------------------------------------------------
# distroless/static:nonroot — the fleet-standard runtime base for our static
# (CGO_ENABLED=0) Go binaries: it bundles CA certs, /etc/passwd, and a nonroot
# user (uid/gid 65532), so no files need carrying over and no USER is needed.
FROM gcr.io/distroless/static:nonroot
COPY --from=builder /workspace/chaski /chaski
ENTRYPOINT ["/chaski"]
