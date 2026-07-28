# syntax=docker/dockerfile:1.7

ARG GO_VERSION=1.25
ARG GOPROXY=https://proxy.golang.org,direct

FROM golang:${GO_VERSION}-alpine AS build

ARG GOPROXY
ENV GOPROXY=${GOPROXY}

ARG TARGETOS=linux
ARG TARGETARCH=amd64

WORKDIR /src

# Keep the build image small while still allowing HTTPS module downloads.
RUN apk add --no-cache ca-certificates

# Copy module metadata first so dependency downloads remain cacheable when Go
# sources change. The API module uses a local replace for the shared contracts.
COPY services/api/go.mod services/api/go.sum services/api/
COPY packages/contracts/go.mod packages/contracts/go.sum packages/contracts/
RUN cd services/api && go mod download

COPY packages/contracts packages/contracts
COPY services/api services/api

RUN cd services/api \
    && CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags='-s -w' -o /out/lingow-api .

FROM alpine:3.22 AS runtime

RUN apk add --no-cache ca-certificates \
    && addgroup -S lingow \
    && adduser -S -G lingow -h /nonexistent -s /sbin/nologin lingow

COPY --from=build /out/lingow-api /usr/local/bin/lingow-api

USER lingow:lingow
WORKDIR /app
ENV API_ADDR=:8080

EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/lingow-api"]
