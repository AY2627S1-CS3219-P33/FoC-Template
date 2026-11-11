# AI Assistance Disclosure:
# Tool: Claude Code (model: Claude Opus 5.5), date: 2026-10-19
# Scope: Boilerplate generation for build/container/tooling configuration.
# Author review: Reviewed, validated, and tested by the authors.

FROM golang:1.27.1-alpine AS build
RUN go install github.com/pressly/goose/v3/cmd/goose@v3.28.0

FROM alpine:3.23
RUN apk add --no-cache ca-certificates
COPY --from=build /go/bin/goose /usr/local/bin/goose
ENTRYPOINT ["goose"]
