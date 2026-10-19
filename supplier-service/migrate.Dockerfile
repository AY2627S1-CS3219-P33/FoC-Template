FROM golang:1.27.1-alpine AS build
RUN go install github.com/pressly/goose/v3/cmd/goose@v3.28.0

FROM alpine:3.23
RUN apk add --no-cache ca-certificates
COPY --from=build /go/bin/goose /usr/local/bin/goose
ENTRYPOINT ["goose"]
