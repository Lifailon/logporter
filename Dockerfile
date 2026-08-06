# Build image
FROM golang:1.23-alpine3.20 AS build
WORKDIR /logporter
COPY go.mod go.sum ./
RUN go mod download
COPY main.go ./
COPY internal ./internal
ARG TARGETARCH TARGETOS
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -o /logporter

# Final image
FROM alpine:3.20
COPY --from=build /logporter /usr/local/bin/
ENTRYPOINT ["logporter"]