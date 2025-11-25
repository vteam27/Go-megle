# Multi-stage build for Go server
FROM golang:1.20-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /app/server ./

FROM alpine:3.18
RUN apk add --no-cache ca-certificates
COPY --from=build /app/server /usr/local/bin/server
EXPOSE 8080
USER 1000
ENTRYPOINT ["/usr/local/bin/server"]
