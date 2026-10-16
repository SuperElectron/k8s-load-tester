# Multi-arch: build natively on the build host, cross-compile for the target.
#   docker buildx build --platform linux/arm64,linux/amd64 -t <repo>/mock-llm:<tag> --push .
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY *.go ./
ARG TARGETOS
ARG TARGETARCH
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /out/mock-llm .

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/mock-llm /mock-llm
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/mock-llm"]
