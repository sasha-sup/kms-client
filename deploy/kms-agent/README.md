# KMS Agent — Деплой в Kubernetes

## Описание

KMS Agent запускается как DaemonSet на каждом узле кластера и периодически
отправляет подписанные HMAC-SHA256 heartbeat-запросы на KMS-сервер.

Если узел перестаёт отправлять heartbeat — KMS-сервер автоматически блокирует
его и отказывает в выдаче ключей шифрования LUKS.

## Предварительные требования

1. KMS-сервер запущен с флагом `--heartbeat-enable` и переменными окружения
   `HEARTBEAT_HMAC_KEY` и `ADMIN_TOKEN`
2. Namespace `kms-system` создан
3. Образ `kms-agent` доступен в container registry

## Установка

Helm-чарт kms-agent расположен в отдельном репозитории:
`devops/helm-infra/kms-agent/`

### Конфигурация (values)

| Параметр | Описание | По умолчанию |
|---|---|---|
| `kmsServerUrl` | URL HTTP API KMS-сервера | — |
| `heartbeatInterval` | Интервал отправки heartbeat | `30s` |
| `secret.name` | Имя Secret с HMAC-ключом | `kms-agent-secret` |
| `secret.externalSecret.enabled` | Использовать ExternalSecret (Vault) | `true` |

> **Важно:** HMAC-ключ должен совпадать со значением `HEARTBEAT_HMAC_KEY` на KMS-сервере.

## NODE_UUID в Talos Linux

В манифесте DaemonSet `NODE_UUID` берётся из `spec.nodeName`. В Talos Linux
имя узла по умолчанию совпадает с machine UUID (из `/sys/class/dmi/id/product_uuid`),
если при регистрации не было задано другое имя.

Если имя узла отличается от machine UUID, замените `fieldRef` в DaemonSet
на подходящий источник (например, annotation или label с machine UUID).

## NODE_HOSTNAME

`NODE_HOSTNAME` берётся из `spec.nodeName` и передаётся на KMS-сервер вместе
с каждым heartbeat. Отображается в admin API (`GET /admin/nodes`) и позволяет
управлять нодами по hostname вместо UUID через скрипт `kms-admin.sh`.

Если переменная не задана, агент использует `os.Hostname()` как fallback.

## Maintenance mode

Перед плановым обслуживанием узла включите maintenance mode через admin API —
это предотвратит блокировку узла Dead Man's Switch при отсутствии heartbeat:

```bash
# Включить maintenance (через скрипт — по hostname)
KMS_ADMIN_TOKEN=... ./scripts/kms-admin.sh maintenance worker-1 on

# Выключить maintenance после завершения обслуживания
KMS_ADMIN_TOKEN=... ./scripts/kms-admin.sh maintenance worker-1 off
```

## Проверка работоспособности

```bash
# Статус подов агента
kubectl get pods -n kms-system -l app=kms-agent

# Логи конкретного пода
kubectl logs -n kms-system -l app=kms-agent --tail=20

# Список узлов на KMS-сервере (требует ADMIN_TOKEN)
KMS_ADMIN_TOKEN=... ./scripts/kms-admin.sh list
```

## Helm-чарт

Манифесты Kubernetes находятся в `devops/helm-infra/kms-agent/`:

| Файл | Описание |
|---|---|
| `templates/daemonset.yaml` | DaemonSet с tolerations, env: NODE_IP, NODE_HOSTNAME, HMAC_KEY |
| `templates/configmap.yaml` | Конфигурация агента (URL сервера, интервал) |
| `templates/externalsecret.yaml` | ExternalSecret для HMAC-ключа из Vault |
| `templates/serviceaccount.yaml` | ServiceAccount для агента |
| `values-dev.yaml` | Значения для dev-окружения |
