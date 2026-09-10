ARG TAILSCALE_IMAGE=tailscale/tailscale:stable
FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/dstore ./cmd/dstore

FROM ${TAILSCALE_IMAGE}
COPY --from=build /out/dstore /usr/local/bin/dstore
COPY docker/peer/entrypoint.sh /usr/local/bin/dstore-peer
COPY docker/peer/healthcheck.sh /usr/local/bin/dstore-peer-health
RUN chmod 0755 /usr/local/bin/dstore-peer /usr/local/bin/dstore-peer-health
ENTRYPOINT ["/usr/local/bin/dstore-peer"]
