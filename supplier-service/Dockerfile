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
    -o /out/supplier-service \
    ./cmd/supplier-service

FROM alpine:3.23 AS runtime

RUN apk add --no-cache ca-certificates tzdata \
    && addgroup -S supplier \
    && adduser -S -G supplier supplier

WORKDIR /app
COPY --from=build /out/supplier-service /app/supplier-service

USER supplier
EXPOSE 3002

ENTRYPOINT ["/app/supplier-service"]
