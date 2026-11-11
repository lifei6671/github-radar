FROM golang:1.26-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY migrations ./migrations
COPY configs ./configs
# modernc SQLite 可在关闭 CGO 时编译。
RUN CGO_ENABLED=0 go build -trimpath -o /out/radar ./cmd/radar

FROM debian:bookworm-slim
WORKDIR /app
RUN mkdir -m 700 /app/data && chown 10001:10001 /app/data
COPY --from=build /out/radar /usr/local/bin/radar
# GitHub API 和 Trending 页面使用系统根证书验证 HTTPS。
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY deploy/container.yaml /app/config.yaml
USER 10001:10001
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/radar"]
CMD ["server", "-config", "/app/config.yaml"]
