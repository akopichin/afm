# Lifecycle hooks

Конфигурация и payload: [docs/lifecycle-hooks.md](../../docs/lifecycle-hooks.md).

- Lifecycle-хуки — observers: ошибка после retry даёт live+durable warning,
  но не меняет FSM. Не смешивай их с блокирующими script_before/script_after.
- Слои global → project → flow → stage заменяют hook с тем же ID целиком на
  прежней позиции. ID — безопасный компонент пути: им строится имя hook log.
- FSM-события эмитятся только после успешного CAS с committed seq. Event ID
  должен оставаться стабильным для одной операции. Каталог берётся из `AllEvents`.
- Очередь доставки best-effort, не durable outbox. У каждого hook свой
  последовательный worker; переполнение/ошибки видны через OnError.
- Dispatcher имеет собственный context: `flow_interrupted` доставляется после
  отмены run context. Сохраняй Flush/Stop на всех выходах и защиту Emit/Stop.
- Базовый env минимален. Даже `inherit_env: true` удаляет транспортные секреты
  хуков, агентов и browser; поверх добавляются AFM metadata и ResolvedEnv.
- Env резолвится fail-fast до `flow_started`. Общий secrets resolver выбирает
  project secrets.env > global secrets.env > process env. `AFM_` зарезервирован
  регистронезависимо; имена env проверяются на коллизии после uppercase.
- Host/container используют один combined-hooks список до Docker re-exec.
  In-container резолв завершает `UnsetTransportVars` ДО запуска дочерних процессов.
- Редактируй секреты и в streaming stdout/stderr, и в возвращаемой ошибке/notice.
  Маркер редактирования сам не должен содержать или восстанавливать секрет.
- Таймаут завершает process group, не только shell. Payload/env пути абсолютные;
  command CWD — эффективный project root.
