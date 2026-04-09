# KMS Client

Сервер управления ключами шифрования дисков (LUKS) для Talos Linux
с механизмом Dead Man's Switch для автоматической блокировки узлов.

## Компоненты

| Компонент | Описание | Расположение |
|-----------|----------|-------------|
| **kms-server** | gRPC-сервер Seal/Unseal + HTTP API heartbeat/admin | `cmd/kms-server/` |
| **kms-agent** | HTTP heartbeat-агент (Kubernetes DaemonSet) | `cmd/kms-agent/` |

## Архитектура

```
┌─────────────┐    gRPC (Unseal)     ┌───────────┐
│  Talos Node  │ ──────────────────> │  nginx    │
│  (boot)      │                     │  (TLS)    │
└─────────────┘                      └─────┬─────┘
                                           │ grpc://127.0.0.1:4443
┌─────────────┐   POST /heartbeat    ┌─────▼─────┐
│  kms-agent   │ ──────────────────> │           │
│  (DaemonSet) │   HMAC-SHA256       │ kms-server│
└─────────────┘                      └───────────┘
```

### Порядок работы

1. **Нода загружается** — Talos обращается к KMS серверу за расшифровкой LUKS (`Unseal`).
   Сервер регистрирует ноду (`node_uuid` + `node_ip`) и выдаёт ключ.
2. **Kubernetes стартует** — kms-agent (DaemonSet) обращается на `POST /node/identify`
   со своим IP. Сервер находит ноду по IP и возвращает `node_uuid`.
3. Каждые `HEARTBEAT_INTERVAL` секунд агент отправляет подписанный HMAC-SHA256
   запрос на `POST /heartbeat`.
4. Фоновая горутина (Dead Man's Switch) каждые `HEARTBEAT_CHECK_INTERVAL`
   проверяет все активные узлы.
5. Если узел не отправил heartbeat дольше `HEARTBEAT_TIMEOUT` — статус
   меняется на `blocked`.
6. Заблокированный узел **не может** получить ключ через `Unseal`.
7. Разблокировка — только через admin API.
8. Если узел уходит на обслуживание, оператор может включить **maintenance mode** —
   Dead Man's Switch не заблокирует узел при отсутствии heartbeat.

### Nginx Reverse Proxy

KMS-сервер работает за nginx, который терминирует TLS и проксирует gRPC.
Nginx передаёт заголовок `X-Real-IP`, а сервер извлекает реальный IP узла
из gRPC metadata (с fallback на peer address).

Конфигурация nginx разворачивается через Ansible-роль [`talos-kms`](https://gl.pypypy.py/devops/iac/-/tree/main/ansible/roles/talos-kms).

## Конфигурация

### kms-server

Переменные окружения:

| Переменная | Описание | Обязательно |
|---|---|---|
| `HEARTBEAT_HMAC_KEY` | Pre-shared ключ HMAC-SHA256 | Да (при heartbeat) |
| `ADMIN_TOKEN` | Токен для admin API | Да (при heartbeat) |

Секреты зашифрованы через ansible-vault и хранятся в [`devops/iac/ansible/roles/talos-kms`](https://gl.pypypy.py/devops/iac/-/tree/main/ansible/roles/talos-kms) (`vars/vault.yml`).
SSH-ключ деплоя и пароль ansible-vault продублированы в Hashicorp Vault.

### Бэкап master.key

Master key (`/etc/kms-server/master.key`) — критический секрет. Без него LUKS-диски нерасшифровуемы.

Бэкапы:
- **Ansible-vault** — `talos_kms_master_key_backup` в `devops/iac/ansible/roles/talos-kms/vars/vault.yml`
- **Hashicorp Vault** — `infra/talos-kms`

При восстановлении — декодировать из base64 и записать в `/etc/kms-server/master.key` (владелец `kms-server:kms-server`, права `0600`).

Флаги командной строки:

| Флаг | Описание | По умолчанию |
|---|---|---|
| `--kms-api-endpoint` | Адрес gRPC API | `:4050` |
| `--key-path` | Путь к master key | — (обязательно) |
| `--tls-enable` | Включить TLS на gRPC | `false` |
| `--tls-cert-path` | Путь к TLS-сертификату | — |
| `--tls-key-path` | Путь к TLS-ключу | — |
| `--heartbeat-enable` | Включить Dead Man's Switch | `false` |
| `--heartbeat-interval` | Ожидаемый интервал heartbeat | `30s` |
| `--heartbeat-timeout` | Время без heartbeat до блокировки | `120s` |
| `--heartbeat-check-interval` | Интервал проверки таймаутов | `30s` |
| `--lease-duration` | Длительность lease | — (обязательно при heartbeat) |
| `--lease-store-path` | Путь к файлу хранилища lease | — (обязательно при heartbeat) |
| `--http-endpoint` | Адрес HTTP API (heartbeat + admin) | `:4051` |
| `--metrics-endpoint` | Адрес Prometheus метрик | `:2112` |

### kms-agent

| Переменная | Описание | По умолчанию |
|---|---|---|
| `NODE_UUID` | UUID узла (автоопределение через `/node/identify`) | автоопределение |
| `NODE_IP` | IP узла (`status.podIP` в K8s) | — (обязательно) |
| `NODE_HOSTNAME` | Имя узла (отображается в admin API). Fallback на `os.Hostname()` | автоопределение |
| `KMS_SERVER_URL` | URL HTTP API KMS-сервера | — (обязательно) |
| `HEARTBEAT_HMAC_KEY` | Pre-shared HMAC ключ | — (обязательно) |
| `HEARTBEAT_INTERVAL` | Интервал отправки heartbeat | `30s` |
| `HEARTBEAT_TIMEOUT` | Таймаут HTTP-запроса | `5s` |

## HTTP API

### POST /node/identify

Получение `node_uuid` по IP-адресу. Используется агентом при старте.

```bash
curl -X POST http://kms-server:4051/node/identify \
  -H "Content-Type: application/json" \
  -H "X-HMAC-Signature: <hmac_hex>" \
  -d '{"node_ip": "10.0.0.1", "timestamp": 1700000000}'
```

Ответ `200`:
```json
{"node_uuid": "abc-123", "status": "active"}
```

Ответ `404` — нода ещё не прошла первый Unseal:
```json
{"error": "node_not_found"}
```

### POST /heartbeat

Отправка heartbeat от агента.

```bash
curl -X POST http://kms-server:4051/heartbeat \
  -H "Content-Type: application/json" \
  -H "X-HMAC-Signature: <hmac_hex>" \
  -d '{"node_uuid": "node-1", "node_ip": "10.0.0.1", "hostname": "worker-1", "timestamp": 1700000000}'
```

Ответ `200`:
```json
{"status": "ok", "next_heartbeat_in": 30}
```

Ответ `403` — узел заблокирован:
```json
{"error": "node_blocked", "reason": "heartbeat_timeout"}
```

### GET /admin/nodes

Список всех узлов. Требует `Authorization: Bearer <ADMIN_TOKEN>`.

```bash
curl -H "Authorization: Bearer <token>" http://kms-server:4051/admin/nodes
```

### POST /admin/nodes/{uuid}/unblock

Разблокировка узла. Требует `Authorization: Bearer <ADMIN_TOKEN>`.

```bash
curl -X POST -H "Authorization: Bearer <token>" \
  http://kms-server:4051/admin/nodes/node-1/unblock
```

### POST /admin/nodes/{uuid}/maintenance

Включение/выключение режима обслуживания. Узел в maintenance mode не блокируется
Dead Man's Switch при отсутствии heartbeat. Требует `Authorization: Bearer <ADMIN_TOKEN>`.

Включить:
```bash
curl -X POST -H "Authorization: Bearer <token>" \
  -H "Content-Type: application/json" \
  -d '{"enabled": true}' \
  http://kms-server:4051/admin/nodes/node-1/maintenance
```

Выключить (узел получает grace period для возобновления heartbeat):
```bash
curl -X POST -H "Authorization: Bearer <token>" \
  -H "Content-Type: application/json" \
  -d '{"enabled": false}' \
  http://kms-server:4051/admin/nodes/node-1/maintenance
```

## CI/CD

### Пайплайн devops-utils (kms-client)

Запускается при пуше с изменениями в `kms-client/`. Ручной запуск через GitLab UI — только с переменной `job`.

Режимы:

- `push` с изменениями в `kms-client/**` — автоматический pipeline (`lint/test/build/...`)
- `web` pipeline с переменной `job` — только адресная KMS admin job
- `web` pipeline без переменной `job` — ни одна джоба не запустится

| Стадия | Job | Описание |
|--------|-----|----------|
| verify | `lint` | golangci-lint |
| verify | `test` | go test + coverage |
| build | `build:kms-server` | Бинарник kms-server (linux/amd64) |
| build | `build:kms-agent-image` | Docker-образ kms-agent → Container Registry (Kaniko) |
| publish | `publish:kms-server` | Бинарник → Generic Package Registry (только main) |
| deploy | `deploy:kms-server` | Trigger пайплайна devops/iac (только main) |
| sync | `kms_list_nodes` | Ручной вывод всех KMS lease через admin API |
| sync | `kms_list_blocked_nodes` | Ручной вывод только заблокированных узлов |
| sync | `kms_unblock_node` | Разблокировка узла по UUID или hostname |
| sync | `kms_maintenance_on` | Включение maintenance mode для узла |
| sync | `kms_maintenance_off` | Выключение maintenance mode для узла |

### Пайплайн devops/iac

Запускается по trigger из devops-utils:
1. Получает переменную `KMS_VERSION` из trigger
2. Скачивает бинарник `kms-server` из Generic Package Registry devops-utils
3. Запускает `ansible-playbook talos-kms.yml` — копирует бинарник на сервер и перезапускает сервис

Образ: `cytopia/ansible:latest-tools`. Раннер: `talos-k8s` (Kubernetes).
SSH-подключение к `kms.mgmt.pypypy.py` под пользователем `ansible` (sudo, NOPASSWD).

### Версионирование

Версия формируется автоматически: `{KMS_VERSION_PREFIX}.{CI_PIPELINE_IID}`.

- `KMS_VERSION_PREFIX` — переменная в `.gitlab-ci.yml` (по умолчанию `1.0`)
- `CI_PIPELINE_IID` — авто-инкремент GitLab (уникальный номер пайплайна в проекте)

Пример: `1.0.42`, `1.0.43`, ...

Для смены major/minor — обновить `KMS_VERSION_PREFIX` в `.gitlab-ci.yml`.

### CI-переменные

**devops-utils:**

| Переменная | Описание |
|---|---|
| `IAC_TRIGGER_TOKEN` | Trigger token для запуска пайплайна devops/iac |
| `IAC_PROJECT_ID` | ID проекта devops/iac (захардкожен: `6`) |
| `KMS_ADMIN_TOKEN` | Токен admin API для ручных KMS jobs; хранится в CI/CD variables проекта |
| `KMS_BASE_URL` | Базовый URL admin API, по умолчанию `https://kms.mgmt.pypypy.py` |
| `KMS_NODE` | UUID или hostname узла для jobs `kms_unblock_node`, `kms_maintenance_on`, `kms_maintenance_off` |

**devops/iac:**

| Переменная | Описание |
|---|---|
| `DEVOPS_UTILS_PROJECT_ID` | ID проекта devops-utils (захардкожен: `3`) |
| `ANSIBLE_SSH_KEY` | SSH-ключ (base64) для пользователя `ansible` на KMS-сервере |
| `ANSIBLE_VAULT_PASSWORD` | Пароль ansible-vault |

### Кросс-проектный доступ

Для скачивания бинарника из Package Registry в iac pipeline используется `CI_JOB_TOKEN`.
В проекте **devops-utils** необходимо разрешить доступ:
Settings → CI/CD → Job token permissions → добавить `devops/iac`.

## Сборка

```bash
# Бинарники
CGO_ENABLED=0 go build -ldflags="-s -w" -o _out/kms-server-linux-amd64 ./cmd/kms-server
CGO_ENABLED=0 go build -ldflags="-s -w" -o _out/kms-agent-linux-amd64 ./cmd/kms-agent

# Docker-образ kms-agent
docker build -f Dockerfile.kms-agent -t kms-agent:latest .

# Тесты
go test ./...

# Линтер
golangci-lint run --config .golangci.yml
```

## Деплой

### kms-server

Автоматический деплой при пуше в main:
1. CI собирает бинарник и публикует в Package Registry
2. Trigger запускает пайплайн в devops/iac
3. Ansible скачивает бинарник из Registry и деплоит на `kms.mgmt.pypypy.py`

Ручной деплой через Ansible:
```bash
cd devops/iac/ansible
ansible-playbook talos-kms.yml
```

### kms-agent

Docker-образ автоматически публикуется в Container Registry при пуше.
Деплой в Kubernetes — Helm-чарт в репозитории `devops/helm-infra/kms-agent/`.

### KMS admin jobs

В `kms-client/.gitlab-ci.yml` есть ручные operational jobs по тому же паттерну,
что и в `keycloak`: через `Run pipeline` и переменную `job`.

Если задана переменная `job`, запускается только выбранная KMS admin job без
`lint/test/build/publish/deploy`.

Поддерживаются:

- `job=kms_list_nodes`
- `job=kms_list_blocked_nodes`
- `job=kms_unblock_node`
- `job=kms_maintenance_on`
- `job=kms_maintenance_off`

Для `kms_unblock_node`, `kms_maintenance_on`, `kms_maintenance_off` дополнительно нужна переменная:

- `KMS_NODE=<uuid или hostname>`

Во всех случаях должен быть задан `KMS_ADMIN_TOKEN`.

Если `KMS_ADMIN_TOKEN` уже сохранен в секретах GitLab CI/CD variables проекта,
его не нужно передавать вручную при запуске pipeline.

Примеры:

```text
job=kms_list_nodes
```

```text
job=kms_list_blocked_nodes
```

```text
job=kms_unblock_node
KMS_NODE=worker-1
```

```text
job=kms_maintenance_on
KMS_NODE=worker-1
```

```text
job=kms_maintenance_off
KMS_NODE=worker-1
```
