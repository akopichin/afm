#!/bin/sh
set -e

# afm Docker-mode entrypoint. Контейнер стартует под root; здесь мы дропаем
# привилегии до хостового пользователя (через gosu), чтобы все записи в
# примонтированные тома (~/.claude, ~/.afm, каталог проекта, extra_mounts)
# принадлежали пользователю хоста, а не root. См. CLAUDE.md → Docker Mode.

# Хостовый uid/gid передаёт afm (ReExec) через -e AFM_HOST_UID/GID. Если скрипт
# запускают без них (например, образ вслепую), по умолчанию берём 1000:1000.
: "${AFM_HOST_UID:=1000}"
: "${AFM_HOST_GID:=1000}"

# Дом для non-root пользователя. Маунты ~/.claude и ~/.afm накладываются поверх
# /home/afm/.claude и /home/afm/.afm. Chown только сам каталог (без -R): в рекурсию
# попали бы примонтированные подкаталоги хоста — медленно и лишние записи.
mkdir -p /home/afm
chown "$AFM_HOST_UID:$AFM_HOST_GID" /home/afm

# gosu сбрасывает HOME по /etc/passwd; для uid без записи там он ставит HOME=/.
# Поэтому HOME задаём явно ПОСЛЕ gosu (через env) — иначе afm и его субпроцессы
# (агенты) видят HOME=/ и не находят ~/файлы (токены ~/.ai-free, конфиг ~/.claude).
# /home/afm — туда же примонтированы ~/.claude, ~/.afm и extra_mounts.
exec gosu "$AFM_HOST_UID:$AFM_HOST_GID" env HOME=/home/afm PATH="/home/afm/.local/bin:$PATH" /usr/local/bin/afm "$@"
