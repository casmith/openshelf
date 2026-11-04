# syntax=docker/dockerfile:1
FROM golang:1.27.3 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
ARG VERSION=dev
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/openshelf ./cmd/openshelf

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/openshelf /openshelf
ENV OPENSHELF_ENV=production OPENSHELF_LISTEN=:8080 OPENSHELF_DATA_DIR=/data
VOLUME /data
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/openshelf"]
CMD ["serve"]
