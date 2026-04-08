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

### 1. Создайте секрет с HMAC-ключом

```bash
kubectl create namespace kms-system

kubectl create secret generic kms-agent-secret \
  --namespace kms-system \
  --from-literal=hmac-key='ваш-секретный-ключ'
```

> **Важно:** Ключ должен совпадать со значением `HEARTBEAT_HMAC_KEY` на KMS-сервере.
> Не используйте `secret.yaml` с placeholder-значением в production.

### 2. Настройте ConfigMap

Отредактируйте `configmap.yaml`:
- `kms-server-url` — URL HTTP API KMS-сервера (порт `4051` по умолчанию)
- `heartbeat-interval` — интервал отправки heartbeat (должен быть меньше
  `HEARTBEAT_TIMEOUT` на сервере)

### 3. Применение манифестов

```bash
kubectl apply -f deploy/kms-agent/serviceaccount.yaml
kubectl apply -f deploy/kms-agent/rbac.yaml
kubectl apply -f deploy/kms-agent/configmap.yaml
kubectl apply -f deploy/kms-agent/daemonset.yaml
```

## NODE_UUID в Talos Linux

В манифесте DaemonSet `NODE_UUID` берётся из `spec.nodeName`. В Talos Linux
имя узла по умолчанию совпадает с machine UUID (из `/sys/class/dmi/id/product_uuid`),
если при регистрации не было задано другое имя.

Если имя узла отличается от machine UUID, замените `fieldRef` в DaemonSet
на подходящий источник (например, annotation или label с machine UUID).

## Проверка работоспособности

```bash
# Статус подов агента
kubectl get pods -n kms-system -l app=kms-agent

# Логи конкретного пода
kubectl logs -n kms-system -l app=kms-agent --tail=20

# Список узлов на KMS-сервере (требует ADMIN_TOKEN)
curl -H "Authorization: Bearer <token>" http://kms-server:4051/admin/nodes
```

## Файлы манифестов

| Файл | Описание |
|---|---|
| `daemonset.yaml` | DaemonSet с tolerations для всех узлов включая control-plane |
| `configmap.yaml` | Конфигурация агента (URL сервера, интервал) |
| `secret.yaml` | Шаблон секрета (placeholder — не использовать в production) |
| `serviceaccount.yaml` | ServiceAccount для агента |
| `rbac.yaml` | Минимальные RBAC-права (пустые — агент не обращается к K8s API) |
