# Правила LLM gateway

Дополняют [корневой AGENTS.md](../../AGENTS.md) для `services/llm-gateway`. Поведение и состояние v1 — в [README](README.md); требования — в спецификациях [HTTP](../../documents/llm-gateway-spec.md) и [gRPC](../../documents/llm-gateway-grpc-spec.md). Команды выполняются из `services/llm-gateway`.

## Контракт

- Источники истины по поведению — спецификации. Если меняете очередь, отмену, восстановление, лимиты или ошибки, сначала обновите спецификацию.
- Клиент не выбирает модель: запросы с `model` и `stream: true` отвергайте до постановки в очередь. `complexity` обязательна.
- `openapi.yaml` генерируется из аннотаций командой `bash scripts/generate-openapi.sh`; вручную его не правьте.
- protobuf-контракт меняйте в `services/shared` по его [правилам](../shared/AGENTS.md), а не в gateway.

## Безопасность и логи

- Не пишите в логи содержимое сообщений и ответов модели.
- Не отдавайте клиенту адреса инстансов, upstream-токены и сырые ошибки upstream.
- Dev YAML и prod-шаблон gateway хранятся в Git; заполненная серверная копия с секретами подключается из игнорируемого deployment-каталога. По решению владельца upstream model tokens пока допустимы в `config/dev.yaml`; остальные секреты держите вне Git. Новое поле добавьте в YAML loader, tests, dev YAML и README.

## Сборка и проверки

- Собирайте с `GOWORK=off`: зависимость от `services/shared` подключается через `replace ../shared`.
- Выбирайте объём проверок по [корневым правилам](../../AGENTS.md#локальные-проверки-и-нагрузка).
- Полный локальный набор: `GOWORK=off GOMAXPROCS=2 go test -p 1 -parallel 2 -race ./... -count=1`, `GOWORK=off GOMAXPROCS=2 go vet -p 1 ./...`, `GOWORK=off GOMAXPROCS=2 go build -p 1 ./cmd/llm-gateway`, `bash scripts/generate-openapi.sh` без изменений в `openapi.yaml`.
- Integration-тест очереди запускается только с Redpanda: из корня `docker compose --env-file .env.example up -d --wait redpanda`, затем тесты с `TEST_KAFKA_BROKERS=localhost:19092`. Если не запускали, назовите пропуск в отчёте.
