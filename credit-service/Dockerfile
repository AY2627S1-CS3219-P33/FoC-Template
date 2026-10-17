FROM golang:1.27.1-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY migrations ./migrations
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/credit-service ./cmd/credit-service

FROM alpine:3.23 AS runtime
RUN apk add --no-cache ca-certificates tzdata \
    && addgroup -S credit && adduser -S -G credit credit
WORKDIR /app
COPY --from=build /out/credit-service /app/credit-service
USER credit
EXPOSE 3004
ENTRYPOINT ["/app/credit-service"]
