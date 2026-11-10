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

RUN sqlc generate

RUN CGO_ENABLED=0 GOOS=linux go build \
    -trimpath \
    -ldflags="-s -w" \
    -o /out/admin-service \
    ./cmd/admin-service

# The one-off job that loads event history (README, "Backfill").
RUN CGO_ENABLED=0 GOOS=linux go build \
    -trimpath \
    -ldflags="-s -w" \
    -o /out/admin-backfill \
    ./cmd/backfill

FROM alpine:3.23 AS runtime

# tzdata: day boundaries are computed in Asia/Singapore.
RUN apk add --no-cache ca-certificates tzdata \
    && wget -q -O /etc/ssl/certs/rds-global-bundle.pem https://truststore.pki.rds.amazonaws.com/global/global-bundle.pem \
    && addgroup -S admin \
    && adduser -S -G admin admin

WORKDIR /app
COPY --from=build /out/admin-service /app/admin-service
COPY --from=build /out/admin-backfill /app/admin-backfill

USER admin
EXPOSE 3005

ENTRYPOINT ["/app/admin-service"]
