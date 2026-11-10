# syntax=docker/dockerfile:1.6

FROM golang:1.26.1-alpine3.23 AS builder

RUN apk add --no-cache \
    build-base \
    pkgconfig \
    zeromq-dev \
    protobuf-dev

WORKDIR /src

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

RUN --mount=type=cache,target=/go/pkg/mod \
    go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.28.1 \
    && GOBIN=/out go install github.com/rubenv/sql-migrate/sql-migrate@v1.8.1

COPY protocol/proto ./protocol/proto
RUN protoc -I . -I /usr/include --go_out=. --go_opt=module=timesync-analyzer protocol/proto/metrics.proto
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


FROM alpine:3.23

RUN apk add --no-cache \
    zeromq \
    ca-certificates \
    tzdata \
    postgresql-client \
    && addgroup -S analyzer \
    && adduser -S -G analyzer analyzer \
    && mkdir -p /reports \
    && chown analyzer:analyzer /reports

WORKDIR /app

COPY --from=builder /out/analyzer /app/analyzer
COPY --from=builder /out/report /app/report
COPY --from=builder /out/reportd /app/reportd
COPY --from=builder /out/sql-migrate /usr/local/bin/sql-migrate
COPY config/config.yaml /app/config/config.yaml
COPY grafana /app/grafana
COPY migrations /app/migrations
COPY deploy/migrate.sh deploy/bootstrap.sql deploy/migrations.yaml /app/deploy/

USER analyzer

EXPOSE 10000 8080

ENTRYPOINT ["/app/analyzer"]
CMD ["-config", "/app/config/config.yaml"]
