FROM golang:1.24-bookworm AS go-build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/manager-server ./cmd/manager-server && \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/pics-cli ./cmd/cli && \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/verify-scan ./cmd/verify-scan

FROM debian:bookworm-slim

RUN apt-get update && \
    apt-get install -y --no-install-recommends wget ca-certificates && \
    rm -rf /var/lib/apt/lists/* && \
    useradd --system --uid 10001 --create-home --home-dir /var/lib/pics-manager pics-manager
WORKDIR /app
COPY --from=go-build /out/manager-server /usr/local/bin/manager-server
COPY --from=go-build /out/pics-cli /usr/local/bin/pics-cli
COPY --from=go-build /out/verify-scan /usr/local/bin/verify-scan
COPY config.yaml /app/config.yaml

USER pics-manager
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/manager-server"]
