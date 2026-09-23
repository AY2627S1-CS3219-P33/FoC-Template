FROM golang:1.26.7-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -o /out/user-service ./cmd/api

FROM scratch
COPY --from=build /out/user-service /user-service
USER 65532:65532
ENTRYPOINT ["/user-service"]
