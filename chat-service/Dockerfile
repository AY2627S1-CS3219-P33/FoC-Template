# AI Assistance Disclosure:
# Tool: Claude Code (model: Claude Opus 5.5), date: 2026-09-03
# Scope: Boilerplate generation for build/container/tooling configuration.
# Author review: Reviewed, validated, and tested by the authors.

FROM golang:1.27.1-alpine AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download
RUN go install github.com/sqlc-dev/sqlc/cmd/sqlc@v1.31.1

COPY sqlc.yaml ./
COPY migrations ./migrations
COPY queries ./queries
COPY cmd ./cmd
COPY internal ./internal

# internal/database/chatgen is git-ignored; generate it from the schema and
# queries so the image never builds against stale code.
RUN sqlc generate

RUN CGO_ENABLED=0 GOOS=linux go build \
    -trimpath \
    -ldflags="-s -w" \
    -o /out/chat-service \
    ./cmd/chat-service

FROM alpine:3.23 AS runtime

# tzdata: admin text exports print times in Asia/Singapore.
RUN apk add --no-cache ca-certificates tzdata \
    && wget -q -O /etc/ssl/certs/rds-global-bundle.pem https://truststore.pki.rds.amazonaws.com/global/global-bundle.pem \
    && addgroup -S chat \
    && adduser -S -G chat chat

WORKDIR /app
COPY --from=build /out/chat-service /app/chat-service

USER chat
EXPOSE 3006

ENTRYPOINT ["/app/chat-service"]
