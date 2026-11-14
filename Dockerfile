# Сборка: статический бинарник без CGO. Кэш модулей и сборки переживает пересборки
# (на сервере 1 vCPU — без кэша каждая сборка с нуля).
FROM golang:1.27-alpine3.24 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
ARG VERSION=dev
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X remindbot/internal/version.Version=${VERSION}" \
    -o /out/remindbot ./cmd/bot

# Запуск: alpine ради wget для healthcheck. Процесс — от пользователя bot (uid 10001), не root:
# файл с токеном в secrets/ должен быть доступен этому uid.
FROM alpine:3.24
RUN adduser -D -H -u 10001 bot
COPY --from=build /out/remindbot /usr/local/bin/remindbot
USER bot
ENTRYPOINT ["remindbot"]
