# Правила backend

Дополняют [корневой AGENTS.md](../../AGENTS.md) для `services/backend`. Устройство модулей, env и запуск — в [README](README.md) и [архитектуре](../../documents/backend-architecture.md). Команды выполняются из `services/backend`.

## Слои

- Feature состоит из `application` (сценарии и порты), `infrastructure` (адаптеры портов) и `transport/http` (DTO и handler). Зависимости связывает только `internal/app`.
- `application` и `entities` не импортируют `net/http`, pgx и адаптеры своего feature.
- Features не импортируют друг друга; общие доменные типы кладите в `internal/entities`.
- `internal/shared` не содержит учебных правил и не импортирует features и entities.
- Правила экзамена (обход, A–D, статусы, пробелы, прогресс) пишите только в `internal/entities/exam` — чистым кодом без IO.

## Данные

- SQL пишите в `internal/shared/postgres/queries`, затем запускайте `bash scripts/generate-sql.sh`. `sqlcgen` вручную не правьте.
- Миграции лежат в `internal/shared/postgres/migrations`. Применённые не меняйте: новая получает следующий номер, свободный в момент мержа.
- Новая таблица или миграция — обновите раздел «БД и импорт» в README.

## HTTP и ошибки

- Ошибки сценариев возвращайте через `internal/shared/fault`: машинный код в `UPPER_SNAKE_CASE` и сообщение на русском.
- Причину сбоя сохраняйте через `fault.WithCause`: она попадает в лог запроса и не уходит клиенту. Текст ответа модели и ответ студента в причину не кладите: ошибка разбора может их цитировать, поэтому используйте `llmgateway.SafeReason`.
- Логгер берите из контекста через `internal/shared/logx`; фоновая задача начинает свой trace через `logx.StartTrace`.
- Владельца ресурса берите из principal, а не из тела запроса. Middleware только аутентифицирует; роль admin проверяйте в `application` (`FORBIDDEN`). Чужой ресурс — 404.
- Изменили DTO или Swag-аннотации — запустите `bash scripts/generate-openapi.sh` и обновите контрактные тесты. Новую ручку сначала добавьте в `api/openapi.yaml` со статусом `implemented`.
- Запрос, который вызывает модель, не меняет состояние до успешного ответа модели. Сбой модели — 503, повтор с тем же `Idempotency-Key` должен пройти.

## Конфигурация

Новые настройки приложения добавляйте в YAML loader, его tests, `config/dev.yaml`, серверный шаблон `config/prod.yaml` и README. Заполненная серверная копия хранится вне Git в `deployment/backend/prod.yaml`. Env остаётся для секретов/инфраструктуры и не переопределяет поля YAML.

## Тесты

- Тесты не ходят в настоящие модели, S3 и LLM gateway: подменяйте их локальными HTTP- или gRPC-серверами, как в существующих тестах.
- Правило экзамена изменили — добавьте случай в `internal/entities/exam/exam_test.go`.
- DB-тесты запускаются только с `TEST_DATABASE_URL`, каждый в своей schema. Если переменная не задана, назовите пропуск в отчёте.
- Выбирайте объём проверок по [корневым правилам](../../AGENTS.md#локальные-проверки-и-нагрузка).
- Полный локальный набор: `GOMAXPROCS=2 go test -p 1 -parallel 2 -race ./... -count=1`, `GOMAXPROCS=2 go vet -p 1 ./...`, `GOMAXPROCS=2 go build -p 1 -o bin/api ./cmd/api`.
