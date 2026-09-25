FROM node:22-bookworm-slim AS web-build
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

FROM golang:1.25.6-bookworm AS go-build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ ./cmd/
COPY internal/ ./internal/
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/server ./cmd/server && \
    CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/bookctl ./cmd/bookctl

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates webp && \
    rm -rf /var/lib/apt/lists/* && \
    mkdir -p /app/web /data/books /data/editor && \
    chown -R 10001:10001 /data
WORKDIR /app
COPY --from=go-build /out/server /app/server
COPY --from=go-build /out/bookctl /app/bookctl
COPY --from=web-build /src/web/dist/ /app/web/
USER 10001:10001
EXPOSE 8080
CMD ["/app/server"]
