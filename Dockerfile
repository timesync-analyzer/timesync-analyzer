# syntax=docker/dockerfile:1.6

FROM golang:1.25-alpine AS builder

RUN apk add --no-cache \
    build-base \
    pkgconfig \
    zeromq-dev

WORKDIR /src

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY protocol ./protocol
COPY src ./src

ENV CGO_ENABLED=1
ENV GOOS=linux

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go build -trimpath -ldflags="-s -w" -o /out/analyzer ./src/cmd


FROM alpine:3.20

RUN apk add --no-cache \
    zeromq \
    ca-certificates \
    tzdata \
    && addgroup -S analyzer \
    && adduser -S -G analyzer analyzer

WORKDIR /app

COPY --from=builder /out/analyzer /app/analyzer
COPY config /app/config

USER analyzer

EXPOSE 10000

ENTRYPOINT ["/app/analyzer"]
CMD ["-config", "/app/config/config.yaml"]
