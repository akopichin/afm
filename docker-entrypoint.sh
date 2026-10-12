#!/bin/sh
set -e

# Entrypoint drops root to host UID/GID so mounted files retain host ownership.

# Хостовый uid/gid передаёт afm (ReExec) через -e AFM_HOST_UID/GID. Если скрипт
# запускают без них (например, образ вслепую), по умолчанию берём 1000:1000.
: "${AFM_HOST_UID:=1000}"
: "${AFM_HOST_GID:=1000}"

# Дом для non-root пользователя. Chown только сам каталог (без -R): в рекурсию попали бы примонтированные
# подкаталоги хоста — медленно и лишние записи.
mkdir -p /home/afm
chown "$AFM_HOST_UID:$AFM_HOST_GID" /home/afm

# Codex-состояние: смонтировано read-only в /tmp/host-codex, здесь копируется в $HOME/.codex — codex может
# обновлять auth.json внутри контейнера, не задевая хостовый ~/.codex.
#
# Копируем ТОЛЬКО нужное для авторизации и работы + пропускаем «мёртвый груз». Не копируем sessions/,
# session_index.jsonl и runtime sqlite (thread_history/logs/state/goals/memories/queue): они хранят
# host-абсолютные пути rollout'ов и рвутся, если хостовый codex-демон пишет в них во время копии —
# контейнерный `codex exec` на старте падает с "state db returned stale rollout path" (exit 1), роняя
# verify-стадию. Свежая verify-сессия этой истории не требует. packages/ (~2.8 ГБ нативных бинарей
# app-server) тоже не нужны — в образе свой npm-codex.
if [ -d /tmp/host-codex ]; then
  mkdir -p /home/afm/.codex
  for f in auth.json config.toml config.toml.bak installation_id version.json \
           models_cache.json .sandbox_migration; do
    [ -f "/tmp/host-codex/$f" ] && cp -a "/tmp/host-codex/$f" /home/afm/.codex/
  done
  for d in cache rules skills plugins; do
    [ -d "/tmp/host-codex/$d" ] && cp -a "/tmp/host-codex/$d" /home/afm/.codex/
  done
  chown -R "$AFM_HOST_UID:$AFM_HOST_GID" /home/afm/.codex
fi

# Задаём HOME после gosu: для UID вне /etc/passwd gosu иначе установит HOME=/.
exec gosu "$AFM_HOST_UID:$AFM_HOST_GID" env HOME=/home/afm PATH="/home/afm/.local/bin:$PATH" /usr/local/bin/afm "$@"
