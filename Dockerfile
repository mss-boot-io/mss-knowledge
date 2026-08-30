# syntax=docker/dockerfile:1.7

ARG GO_VERSION=1.26.6
FROM golang:${GO_VERSION}-alpine AS build

ARG TARGET=gateway
ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_DATE=unknown

WORKDIR /src
COPY go.mod ./
RUN go mod download
COPY . .

RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags "-s -w \
      -X github.com/mss-boot-io/mss-knowledge/internal/buildinfo.Version=${VERSION} \
      -X github.com/mss-boot-io/mss-knowledge/internal/buildinfo.Commit=${COMMIT} \
      -X github.com/mss-boot-io/mss-knowledge/internal/buildinfo.Date=${BUILD_DATE}" \
    -o /out/mss-knowledge ./cmd/${TARGET}

FROM alpine:3.22
RUN apk add --no-cache ca-certificates tzdata \
    && addgroup -S -g 10001 mss \
    && adduser -S -D -H -u 10001 -G mss mss

COPY --from=build /out/mss-knowledge /usr/local/bin/mss-knowledge

USER 10001:10001
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/mss-knowledge"]
