FROM golang:1.26-alpine AS builder

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY cmd ./cmd
COPY internal ./internal
COPY pkg ./pkg

RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/postgres-sync ./cmd/postgres-sync

FROM alpine:3.23

RUN apk add --no-cache ca-certificates tzdata wget

WORKDIR /app

COPY --from=builder /out/postgres-sync /usr/local/bin/postgres-sync

RUN mkdir -p /var/lib/postgres-sync-go

ENV SYNC_PORT=3000
ENV SYNC_STORAGE_MODE=memory
ENV SYNC_STORAGE_DIR=/var/lib/postgres-sync-go

EXPOSE 3000

HEALTHCHECK --interval=10s --timeout=3s --start-period=10s --retries=5 \
  CMD sh -ec 'wget -qO- "http://127.0.0.1:${SYNC_PORT}/v1/health" | grep -q "\"status\":\"active\""'

ENTRYPOINT ["/usr/local/bin/postgres-sync"]
