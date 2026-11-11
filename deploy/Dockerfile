FROM node:24-bookworm-slim AS frontend
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run check && npm run build

FROM golang:1.26-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY migrations ./migrations
COPY configs/categories.go configs/categories.yaml ./configs/
# modernc SQLite 可在关闭 CGO 时编译。
RUN CGO_ENABLED=0 go build -trimpath -o /out/radar ./cmd/radar

FROM debian:bookworm-slim
WORKDIR /app
RUN mkdir -m 700 /app/data && chown 10001:10001 /app/data
COPY --from=build /out/radar /usr/local/bin/radar
COPY --from=frontend /src/web/dist /app/web/dist
# GitHub API 和 Trending 页面使用系统根证书验证 HTTPS。
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
# 实际部署配置由启动时只读挂载，避免将管理员凭据写入镜像。
USER 10001:10001
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/radar"]
CMD ["server", "-config", "/app/config.yaml"]
