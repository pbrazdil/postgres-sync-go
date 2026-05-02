FROM golang:1.26-alpine AS builder

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY cmd ./cmd
COPY internal ./internal
COPY pkg ./pkg

RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/pulsesync ./cmd/pulsesync

FROM alpine:3.22

RUN apk add --no-cache ca-certificates tzdata wget

WORKDIR /app

COPY --from=builder /out/pulsesync /usr/local/bin/pulsesync

RUN mkdir -p /var/lib/pulsesync

ENV SYNC_PORT=3000
ENV SYNC_STORAGE_MODE=memory
ENV SYNC_STORAGE_DIR=/var/lib/pulsesync

EXPOSE 3000

HEALTHCHECK --interval=10s --timeout=3s --start-period=10s --retries=5 \
  CMD sh -ec 'wget -qO- "http://127.0.0.1:${SYNC_PORT}/v1/health" | grep -q "\"status\":\"active\""'

ENTRYPOINT ["/usr/local/bin/pulsesync"]
