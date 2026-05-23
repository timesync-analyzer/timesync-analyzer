# syntax=docker/dockerfile:1.6

FROM golang:1.26-alpine AS builder

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
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go build -trimpath -ldflags="-s -w" -o /out/report ./src/cmd/report
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go build -trimpath -ldflags="-s -w" -o /out/reportd ./src/cmd/reportd


FROM alpine:3.20

RUN apk add --no-cache \
    zeromq \
    ca-certificates \
    tzdata \
    && addgroup -S analyzer \
    && adduser -S -G analyzer analyzer \
    && mkdir -p /reports \
    && chown analyzer:analyzer /reports

WORKDIR /app

COPY --from=builder /out/analyzer /app/analyzer
COPY --from=builder /out/report /app/report
COPY --from=builder /out/reportd /app/reportd
COPY config /app/config
COPY grafana /app/grafana

USER analyzer

EXPOSE 10000 8080

ENTRYPOINT ["/app/analyzer"]
CMD ["-config", "/app/config/config.yaml"]
