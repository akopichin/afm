# Managed browser

Настройка: [docs/browser.md](../../docs/browser.md).

- Manager/helper и Chrome принадлежат хосту; контейнер получает MCP-транспорт.
  Chrome стартует лениво на реальный tool call, через remote-debugging-pipe.
  Initialize/tools-list не запускают браузер, открытого CDP TCP-порта нет.
- `browser.enabled` tri-state; разыменование и env override централизованы.
  Stage opt-out и поддержка команды гейтят одновременно MCP-инъекцию и prompt.
  Verify/helper агенты browser не получают: read-only ФС не защищает внешнее app.
- Токен передаётся через bare Docker env, затем приватный per-invocation
  descriptor. В argv/logs/MCP config допускается путь, не токен. После чтения
  транспорта снимай env; все spawn-пути фильтруют `IsTransportEnv`.
- CDP request IDs ремапятся per-connection в глобальные IDs. Неизвестный/поздний
  response отбрасывается; broadcast разрешён только событиям без ID. Медленный
  клиент должен получить disconnect вместо вечного ожидания потерянного ответа.
- Сессии ключуются process-epoch + stage ID; launch singleflight, ошибки не
  кэшируются. Close ждёт завершения launch до teardown; stale-профили удаляются
  по owner-маркерам после аварийного выхода.
- Stage ID безопасен и для URL, и для имени профиля; не допускай коллизии после
  sanitization. MCP filesystem roots включают project root и stage artifacts.
- Host-пин Chrome DevTools MCP совпадает с Dockerfile; stdout launcher содержит
  только JSON-RPC. Codex TOML-overrides строятся с корректным escaping.
- Агент сервит приложение на `0.0.0.0:$AFM_BROWSER_APP_PORT`, а открывает
  `AFM_BROWSER_APP_URL`: Docker host port не равен container localhost.
