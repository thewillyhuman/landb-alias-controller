ARG TARGETARCH

# Build stage
FROM --platform=${TARGETARCH} registry.cern.ch/docker.io/golang:1.24.4-alpine AS build
WORKDIR /app

# Copy go.mod and go.sum first to leverage Docker layer caching.
# These files don't change often, so this layer will be cached
# unless the dependencies are updated.
COPY go.mod go.sum ./
RUN go mod download

# Copy the rest of the application source code.
COPY . .

# CGO_ENABLED=0 creates a statically linked binary
# -ldflags="-w -s" strips debug information
RUN CGO_ENABLED=0 GOOS=linux GOARCH=${TARGETARCH} go build -a -ldflags="-w -s" -o /landb-alias-controller .

# Final distroless image
FROM scratch
COPY --from=build /landb-alias-controller /landb-alias-controller
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt

ENTRYPOINT ["/landb-alias-controller"]
