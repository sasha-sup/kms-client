# KMS client

KMS client defines API for network based disk encryption for Talos Linux.

This repository contains:

- `kms-server`: production KMS server for Seal/Unseal and heartbeat lease enforcement
- `kms-agent`: HTTP heartbeat agent for Kubernetes DaemonSet deployment with HMAC authentication

## Dead Man's Switch Heartbeat

### Описание механизма

Dead Man's Switch — механизм автоматической блокировки узлов, которые перестали
отправлять heartbeat-сигналы. Это защищает от сценариев, когда скомпрометированный
узел может запрашивать ключи LUKS-шифрования.

### Как это работает

1. **Нода загружается** — Talos обращается к KMS серверу за расшифровкой LUKS (`Unseal`).
   Сервер регистрирует новую ноду (`node_uuid` + `node_ip`) и выдаёт ключ
2. **Kubernetes стартует** — kms-agent (DaemonSet) обращается на `POST /node/identify`
   со своим IP. Сервер находит ноду по IP и возвращает `node_uuid`
3. Каждые `HEARTBEAT_INTERVAL` секунд агент отправляет подписанный HMAC-SHA256
   запрос на `POST /heartbeat` KMS-сервера
4. Фоновая горутина (Dead Man's Switch) на сервере каждые `HEARTBEAT_CHECK_INTERVAL`
   проверяет все активные узлы
5. Если узел не отправил heartbeat дольше `HEARTBEAT_TIMEOUT` — его статус
   меняется на `blocked`
6. Заблокированный узел **не может** получить ключ через `Unseal` или отправить heartbeat
7. Разблокировка возможна только через admin API

### Переменные окружения сервера

| Переменная | Описание | По умолчанию |
| --- | --- | --- |
| `HEARTBEAT_HMAC_KEY` | Pre-shared ключ для HMAC-SHA256 аутентификации heartbeat | **обязательно** |
| `ADMIN_TOKEN` | Токен для admin API | **обязательно** |

Флаги командной строки:

| Флаг | Описание | По умолчанию |
| --- | --- | --- |
| `--heartbeat-enable` | Включить dead-man's switch | `false` |
| `--heartbeat-interval` | Ожидаемый интервал heartbeat от агентов | `30s` |
| `--heartbeat-timeout` | Время без heartbeat до блокировки узла | `120s` |
| `--heartbeat-check-interval` | Интервал проверки таймаутов | `30s` |
| `--http-endpoint` | Адрес HTTP API (heartbeat + admin) | `:4051` |
| `--lease-store-path` | Путь к файлу хранилища лизов | обязательно при `--heartbeat-enable` |

### HTTP API

#### POST /node/identify

Получение `node_uuid` по IP адресу. Используется агентом при старте.

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

Ответ `404` (нода ещё не прошла первый Unseal):

```json
{"error": "node_not_found"}
```

#### POST /heartbeat

Отправка heartbeat от агента.

```bash
curl -X POST http://kms-server:4051/heartbeat \
  -H "Content-Type: application/json" \
  -H "X-HMAC-Signature: <hmac_hex>" \
  -d '{"node_uuid": "node-1", "node_ip": "10.0.0.1", "timestamp": 1700000000}'
```

Ответ `200`:

```json
{"status": "ok", "next_heartbeat_in": 30}
```

Ответ `403` (узел заблокирован):

```json
{"error": "node_blocked", "reason": "heartbeat_timeout"}
```

#### GET /admin/nodes

Список всех узлов. Требует `Authorization: Bearer <ADMIN_TOKEN>`.

```bash
curl -H "Authorization: Bearer <token>" http://kms-server:4051/admin/nodes
```

#### POST /admin/nodes/{uuid}/unblock

Разблокировка узла. Требует `Authorization: Bearer <ADMIN_TOKEN>`.

```bash
curl -X POST -H "Authorization: Bearer <token>" \
  http://kms-server:4051/admin/nodes/node-1/unblock
```

### Переменные окружения агента

| Переменная | Описание | По умолчанию |
| --- | --- | --- |
| `NODE_UUID` | UUID узла (опционально — автоопределение через `/node/identify`) | автоопределение |
| `NODE_IP` | IP узла (из `status.podIP` в K8s) | **обязательно** |
| `KMS_SERVER_URL` | URL HTTP API KMS-сервера | **обязательно** |
| `HEARTBEAT_HMAC_KEY` | Pre-shared HMAC ключ | **обязательно** |
| `HEARTBEAT_INTERVAL` | Интервал отправки heartbeat | `30s` |
| `HEARTBEAT_TIMEOUT` | Таймаут HTTP запроса | `5s` |
