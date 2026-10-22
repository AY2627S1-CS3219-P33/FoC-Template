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
    -o /out/order-service \
    ./cmd/order-service

FROM alpine:3.23 AS runtime

# tzdata: supplier opening hours are checked in Asia/Singapore.
RUN apk add --no-cache ca-certificates tzdata \
    && addgroup -S order \
    && adduser -S -G order order

WORKDIR /app
COPY --from=build /out/order-service /app/order-service

USER order
EXPOSE 3003

ENTRYPOINT ["/app/order-service"]
