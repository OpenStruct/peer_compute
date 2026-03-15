FROM golang:1.23-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /bin/registry ./cmd/registry
RUN CGO_ENABLED=0 go build -o /bin/agent ./cmd/agent
RUN CGO_ENABLED=0 go build -o /bin/peerctl ./cmd/peerctl

FROM alpine:3.20 AS registry
COPY --from=builder /bin/registry /usr/local/bin/registry
ENTRYPOINT ["registry"]

FROM alpine:3.20 AS agent
COPY --from=builder /bin/agent /usr/local/bin/agent
ENTRYPOINT ["agent"]

FROM alpine:3.20 AS peerctl
COPY --from=builder /bin/peerctl /usr/local/bin/peerctl
ENTRYPOINT ["peerctl"]
