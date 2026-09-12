# syntax=docker/dockerfile:1.7

FROM golang:1.26-bookworm AS builder
ARG GOPROXY=https://proxy.golang.org,direct
WORKDIR /src
COPY --from=weknora . /weknora
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    GOPROXY="$GOPROXY" go mod edit -replace github.com/Tencent/WeKnora=/weknora && \
    GOPROXY="$GOPROXY" go mod tidy && \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/plugin .

FROM scratch
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=builder /out/plugin /plugin
EXPOSE 9000
USER 65532:65532
ENTRYPOINT ["/plugin"]
