VERSION ?= $(shell date +%Y%m%d-%H%M%S)-dev
LDFLAGS := -X remindbot/internal/version.Version=$(VERSION)

# Адрес сервера (user@host) — в deploy.local.mk, он в .gitignore. Шаблон: deploy.local.mk.example.
-include deploy.local.mk
SERVER ?=
SERVER_DIR ?= /opt/remindbot
# На сервере это лежит отдельно и деплоем не перезаписывается (и не удаляется: --delete не трогает исключённое).
DEPLOY_EXCLUDE := --exclude=/.env --exclude=/secrets/ --exclude=/backups/ --exclude=/logs/ --exclude=/bin/ --exclude=/docs/ \
	--exclude=/.git/ --exclude=/.idea/ --exclude=.DS_Store \
	--exclude=/deploy.local.mk --exclude=/PRIVATE.md

need-server:
	@test -n "$(SERVER)" || (echo "SERVER не задан: cp deploy.local.mk.example deploy.local.mk и впиши адрес" && exit 1)

.PHONY: setup secrets db-up db-down db-logs db-psql run build check test-db docs-pdf deploy server-logs server-psql

## setup: первый запуск — .env, секреты, база
setup: .env secrets db-up

.env:
	cp .env.example .env

## secrets: создать папку secrets/ (пароль БД генерируется, токен бота — пустой файл для заполнения)
secrets:
	@mkdir -p secrets && chmod 700 secrets
	@test -s secrets/postgres_password || (openssl rand -hex 24 > secrets/postgres_password && echo "создан secrets/postgres_password")
	@test -e secrets/telegram_token || (touch secrets/telegram_token && echo "создан пустой secrets/telegram_token — впиши туда токен бота")
	@chmod 600 secrets/telegram_token
	@# postgres в контейнере читает файл под своим пользователем, папка secrets/ закрыта правами 700
	@chmod 644 secrets/postgres_password

db-up:
	docker compose up -d --wait db

db-down:
	docker compose down

db-logs:
	docker compose logs -f db

db-psql:
	docker compose exec db psql -U bot remindbot

run:
	go run -ldflags "$(LDFLAGS)" ./cmd/bot

build:
	go build -ldflags "$(LDFLAGS)" -o bin/remindbot ./cmd/bot

check:
	go vet ./...
	go build -o /dev/null ./cmd/bot

## test-db: интеграционные тесты на отдельной базе remindbot_test (Postgres из db-up)
test-db:
	@docker compose exec -T db psql -U bot -d remindbot -tc "SELECT 1 FROM pg_database WHERE datname = 'remindbot_test'" | grep -q 1 \
		|| docker compose exec -T db createdb -U bot remindbot_test
	REMINDBOT_TEST_DB=1 DATABASE_NAME=remindbot_test TELEGRAM_TOKEN=test POSTGRES_PASSWORD_FILE=$(CURDIR)/secrets/postgres_password go test -count=1 ./...

## deploy: выложить код на сервер, пересобрать и перезапустить бота, дождаться healthcheck (/healthz).
## .env и secrets/ на сервере кладутся один раз вручную — см. README, раздел «Сервер».
deploy: need-server
	rsync -az --delete $(DEPLOY_EXCLUDE) ./ $(SERVER):$(SERVER_DIR)/
	ssh $(SERVER) 'cd $(SERVER_DIR) && VERSION=$(VERSION) docker compose up -d --build --wait --wait-timeout 180 bot'

## server-logs: логи бота на сервере (Ctrl+C — выйти)
server-logs: need-server
	ssh -t $(SERVER) 'cd $(SERVER_DIR) && docker compose logs -f --tail=200 bot'

## server-psql: консоль базы на сервере
server-psql: need-server
	ssh -t $(SERVER) 'cd $(SERVER_DIR) && docker compose exec db psql -U bot remindbot'

CHROME ?= /Applications/Google Chrome.app/Contents/MacOS/Google Chrome

## docs-pdf: собрать docs/bot-flow.pdf из docs/diagrams.html (нужны Chrome и интернет для mermaid)
docs-pdf:
	"$(CHROME)" --headless=new --disable-gpu --no-pdf-header-footer --virtual-time-budget=20000 \
		--print-to-pdf="$(CURDIR)/docs/bot-flow.pdf" "file://$(CURDIR)/docs/diagrams.html"
