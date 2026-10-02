# Image for vrok-relay, the public endpoint for vrok's own tunnel.
#
# The CLI is not containerised: it exists to serve files from the machine it
# runs on, and a container would only get in the way of that.

FROM golang:1.27-alpine AS build

WORKDIR /src

# Dependencies are copied first so a source-only change does not refetch them.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG VERSION=dev
ARG COMMIT=none

# CGO is off so the result is a static binary that runs on scratch.
RUN CGO_ENABLED=0 go build -trimpath -buildvcs=false \
    -ldflags "-s -w -X main.version=${VERSION} -X main.commit=${COMMIT}" \
    -o /out/vrok-relay ./cmd/vrok-relay

FROM scratch

# The relay makes no outbound TLS connections of its own, but carrying the
# CA bundle keeps it usable behind a proxy that requires one.
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=build /out/vrok-relay /vrok-relay

# The relay holds no state and needs no filesystem access, so it runs as a
# non-root user with nothing writable.
USER 65534:65534

EXPOSE 8787

ENV VROK_RELAY_ADDR=:8787

ENTRYPOINT ["/vrok-relay"]
