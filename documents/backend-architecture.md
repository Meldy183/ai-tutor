# Архитектура backend

Код находится в `services/backend`, Go-модуль — `github.com/AskarKasimov/ai-tutor/services/backend`. Geek — модуль `features/geek`; legacy обучение восстановлено в собственных `features/learning`, `features/diagnostic`, `features/training`, `features/taskgen`, `features/variantgen`, `features/competency`, `features/taskbank`, `features/taskaudio` и assessment/feedback slices. Режимы работают в одном router и PostgreSQL, структура карт и банк заданий находятся в нейтральных `course_map_*`, subject state, учебные snapshots и очередь task audio — в `learning_*`. Источник структуры задаёт `course_map_competencies.source_mode`, режим задания — `course_map_tasks.mode`. Geek использует собственные exam sessions/graph. LLM вызываются через отдельный LLM gateway (`services/llm-gateway`) с HTTP-контрактом в `openapi.yaml` и gRPC-контрактом в `services/shared/proto/llmgateway/v1/gateway.proto`; обучение, как и Geek, указывает только сложность, а модель выбирает gateway. Этапы обработки course-graph выполняет Python-сервис `services/course-graph`. Видение — в [README](../README.md).

## Реализованные части

| Путь | Ответственность |
| --- | --- |
| `cmd/api` | Запуск, миграции, healthcheck, фоновая проверка S3-бакета, shutdown. |
| `internal/app` | Конфигурация, зависимости, router, auth middleware и HTTP logging. |
| `features/auth` | Регистрация/вход, refresh/logout/me, общий аккаунт админки, PostgreSQL и Argon2id. |
| `features/voice` | STT/TTS, проверка входа, сохранение расшифровки владельца; gRPC-адаптеры к voice gateway. |
| `features/subject` | Каталог предметов (курсов) и создание предмета admin. |
| `features/{diagnostic,assessment,diagnosticfeedback,learning,training}` | Учебная диагностика с общим exam-обходом и шкалой 0/1/2, feedback, план и тренировка; собственные сессии и snapshots. |
| `features/{competency,variantgen,taskbank,taskgen,taskaudio}` | Разбор второго листа единого импорта, генерация/выдача вариантов по составляющим, чтение diagnostic-банка общей ревизии, подготовка задач через gateway и audio worker. Отдельной ручки учебного импорта нет. |
| `features/materials` | Загрузка материалов курса, фоновые OCR через course-graph, перевод через LLM gateway и очистка с темами, хранение в S3 и PostgreSQL, список, справочник видов, неделя/тип, правка метаданных, замена PDF, удаление и выдача исходника. Метаданные сохраняют готовые задания; замена отменяет старые leases и запускает обработку нового файла. |
| `features/coursegraph` | Сборка графа из материалов (очередь с lease в PostgreSQL, вызов `/graph/build`) или карты компетенций. Оба листа КК используют общий отбор основного и двух базовых; авторские связи не используются. Полные ОР и задания первого листа сохраняются независимо от отбора. Публикация и чтение снимка графа для админки. |
| `features/geek` | Диагностика Geek: сценарии старта, ответа, итога и озвучки реплик; порты графа, хранилища, расшифровок, модели (Examiner), TTS и кэша аудио. Адаптеры: PostgreSQL, опубликованный граф из БД (материалы или карта компетенций), gRPC-клиент LLM gateway, кэш WAV в S3. |
| `entities/exam` | Граф курса (проверка ацикличности, верхний уровень, замыкание пререквизитов), шкала темы 0/1/2 и 0/1, A–D курса, зачёт составляющей, общий обход диагностики и экзамена, прогресс, пробелы и итог. Чистый код без IO. |
| `entities` | Пользователь, auth-сессия, аудио (проверка, разбиение и склейка WAV), расшифровка, предмет, материал и экзамен. |
| `shared` | Fault, security, HTTP helpers, S3-клиент, миграции и SQLC. |

Каждый feature содержит `application`, `infrastructure`, `transport/http`. Application определяет сценарии и порты; инфраструктура реализует порты; HTTP переводит запросы/ответы. `app` связывает зависимости. Сценарии и entities не импортируют HTTP, pgx или собственные адаптеры. Features не импортируют сценарии друг друга; общие доменные типы находятся в entities.

SQL находится в `shared/postgres/queries`; PostgreSQL-адаптеры вызывают сгенерированные SQLC-методы. Генератор ID — в `shared/security`. S3-клиент `shared/s3` реализует общие операции с объектами; адаптер материалов в `app` передаёт ему бакет.

Авторизация выполняется middleware: auth возвращает principal, HTTP handler передаёт его сценарию. Курс имеет общий `subject_id` без признака режима; активная редакция учебной карты принадлежит `learning_subject_state`, а готовность обучения и экзамена определяется независимо. `shared` не содержит учебных правил и не зависит от features/entities.

## API и модели

Единственный контракт — [api/openapi.yaml](../api/openapi.yaml). `x-implementation` отличает работающие операции от планируемых; наличие planned в Swagger не означает наличие handler. Внутренний API Python-сервиса — [api/course-graph.yaml](../api/course-graph.yaml).

Frontend вызывает общий HTTPS API `/api/v1`; Caddy удаляет префикс. Learning operations находятся под `/learn`, Geek под `/exam`, auth и voice общие. STT/TTS вызываются backend через два gRPC-адаптера к отдельному `services/voice-gateway`; тот ведёт независимые STT/TTS-очереди и вызывает HTTP API моделей, OCR — через сервис course-graph; настройки моделей находятся только на сервере. `/health` проверяет backend/БД, не модели.

## Импорт и независимая готовность

`app.pairedCourseGraphRepository` сохраняет оба листа одной транзакцией: учебная структура и задания через competency-адаптер, полный exam-банк и job через coursegraph-адаптер. Содержимое листов не обязано совпадать. Учебная активная ревизия меняется при commit, прежний Geek работает до успешной подготовки новой версии. Snapshot Geek содержит выбранные темы, зависимости алгоритма и полный банк ОР; независимой операции редактирования структуры снимка нет.

Остальные ОР составляющей хранятся для обучения без придуманных рёбер. `entities/exam` владеет общим обходом и правилом зачёта: основное на максимум или сумма заданных заданий от 90%. Незачтённая составляющая отправляет в обучение все ОР, кроме отвеченных на максимум. План и тренировка читают данные той же ревизии; результаты разных составляющих не перезаписывают друг друга. Публикация изменённого Geek-графа отменяет активные экзаменационные сессии. Завершённая история сохраняется. Запуск обучения после Geek — следующий шаг, текущий UI его не добавляет.
