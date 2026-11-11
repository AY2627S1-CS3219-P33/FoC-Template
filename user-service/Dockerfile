# AI Assistance Disclosure:
# Tool: ChatGPT (model: GPT-6 Luna), date: 2026-11-11
# Scope: Container build configuration for user-service.
# Author review: I validated correctness, edited for style, and added boundary checks.

FROM golang:1.26.7-alpine AS build
WORKDIR /src
RUN apk add --no-cache ca-certificates
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -o /out/user-service ./cmd/api

FROM scratch
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=build /out/user-service /user-service
USER 65532:65532
ENTRYPOINT ["/user-service"]
