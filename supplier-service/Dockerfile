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