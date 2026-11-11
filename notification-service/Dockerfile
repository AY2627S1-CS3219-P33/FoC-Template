# AI Assistance Disclosure:
# Tool: Claude, date: 2026-11-11
# Scope: container image build configuration for notification-service.
# Author review: I validated correctness, edited for style, and added boundary checks.

FROM golang:1.27.1-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY migrations ./migrations
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/notification-service ./cmd/notification-service

FROM alpine:3.23 AS runtime
RUN apk add --no-cache ca-certificates tzdata \
    && wget -q -O /etc/ssl/certs/rds-global-bundle.pem https://truststore.pki.rds.amazonaws.com/global/global-bundle.pem \
    && addgroup -S notif && adduser -S -G notif notif
WORKDIR /app
COPY --from=build /out/notification-service /app/notification-service
USER notif
EXPOSE 3007
ENTRYPOINT ["/app/notification-service"]
