FROM golang:1.27.1-alpine AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY cmd ./cmd
COPY internal ./internal

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
EXPOSE 3005

ENTRYPOINT ["/app/chat-service"]
