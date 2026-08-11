# Build image
FROM golang:1.23-alpine3.20 AS build
WORKDIR /src
# Installing dependencies for build caching
COPY go.mod go.sum ./
RUN go mod download
# Copy main.go and internal packages
COPY main.go ./
COPY internal ./internal
# Build for different architectures
ARG TARGETARCH TARGETOS
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -o /logporter
# CA certificates for Loki over TLS
RUN apk add --no-cache ca-certificates

# Final scratch image
FROM scratch
COPY --from=build /logporter /logporter
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
ENTRYPOINT ["/logporter"]