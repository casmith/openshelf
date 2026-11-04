# syntax=docker/dockerfile:1
FROM golang:1.27.3 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
ARG TARGETARCH
ARG TAILWIND_VERSION=4.3.4
RUN arch=$([ "$TARGETARCH" = "arm64" ] && echo arm64 || echo x64) && \
    curl -fsSL -o /usr/local/bin/tailwindcss \
      "https://github.com/tailwindlabs/tailwindcss/releases/download/v${TAILWIND_VERSION}/tailwindcss-linux-${arch}" && \
    chmod +x /usr/local/bin/tailwindcss && \
    tailwindcss -i internal/web/styles/app.css -o internal/web/static/css/app.css --minify
ARG VERSION=dev
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/openshelf ./cmd/openshelf
RUN mkdir -p /out/data

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/openshelf /openshelf
COPY --from=build --chown=65532:65532 /out/data /data
ENV OPENSHELF_ENV=production OPENSHELF_LISTEN=:8080 OPENSHELF_DATA_DIR=/data
VOLUME /data
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=5s --start-period=30s CMD ["/openshelf", "healthcheck"]
USER nonroot:nonroot
ENTRYPOINT ["/openshelf"]
CMD ["serve"]
