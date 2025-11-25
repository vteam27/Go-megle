# Multi-stage build for Go server
FROM node:18-alpine AS frontend
WORKDIR /src
COPY web/package*.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

FROM golang:1.21-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /app/server ./

FROM alpine:3.18
RUN apk add --no-cache ca-certificates
COPY --from=build /app/server /usr/local/bin/server
COPY --from=frontend /src/dist /web/dist
EXPOSE 8080
USER 1000
ENTRYPOINT ["/usr/local/bin/server"]
