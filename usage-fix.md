# Учёт usage/cost для claude не работает

**Дата:** 2026-07-08
**Прогон:** `.afm/runs/preprompt-20260708-045656` (Docker, `akopichin/afm:latest`)

## TL;DR

Usage для claude **вообще не парсится** — не из-за бага парсинга, а потому что прокси
(единственный механизм захвата usage) намеренно **отключён** для команды `claude`. После
перехода с glm на claude запись usage навсегда остановилась. Реальный расход денег при этом
идёт (и уже упёрся в лимит Anthropic), просто он нигде не учитывается.

## Доказательства

1. **`usage.jsonl` замёрз в `05:53:46 UTC`** — 54 записи, все `glm-5.2`, файл с тех пор не
   трогался (mtime `08:53:46` по Москве = те же 05:53 UTC).

2. **afm был перезапущен в `06:07 UTC` (09:07 по Москве)** — docker-контейнер `b9c0c5aba4e6`
   стартовал тогда, и события `manual_retry` всех стадий идут ровно с `06:07:42`. Все
   glm-записи — от **предыдущего** инстанса; текущий (claude) не написал ни одной.

3. **Живой env агента claude внутри контейнера** (`/proc/<pid>/environ`) содержал **только**
   `CLAUDE_CODE_OAUTH_TOKEN`. Ни `ANTHROPIC_BASE_URL`, ни `AFM_PROXY_URL`, ни shim в PATH.
   То есть прокси агенту не передан.

4. **В логах контейнера видно, что claude ходит в настоящий Anthropic, мимо прокси**:
   ```
   proxy: http://127.0.0.1:42367 → https://api.z.ai/api/anthropic   ← прокси поднят, но на него никто не идёт
   ...
   "You've hit your org's monthly spend limit · ask your admin to raise it at claude.ai/settings/usage"
   api_error_status: 429
   modelUsage: { claude-haiku-4-5:..., claude-sonnet-5: {costUSD: 3.41} }
   total_cost_usd: 3.41
   ```
   `claude.ai/settings/usage` + реальные модели с `costUSD` = платформа Anthropic через OAuth,
   не z.ai.

## Корневая причина

`pkg/orchestrator/orchestrator.go:222` — `proxyForCmd`:

```go
// Команда claude использует OAuth-токен (CLAUDE_CODE_OAUTH_TOKEN) и ходит
// напрямую на api.anthropic.com — инжектировать прокси z.ai ей не нужно
// (z.ai не принимает OAuth-токены, только API-ключи).
func proxyForCmd(cmd, proxyURL, shimDir string) (string, string) {
	if cmd == "claude" {
		return "", ""          // ← прокси выключен для claude
	}
	return proxyURL, shimDir
}
```

Это **намеренное** решение (z.ai не ест OAuth). Но `AppendUsageRecord` вызывается **только**
из прокси (`pkg/proxy/proxy.go:110`) — executor сам usage из вывода claude не достаёт.
Поэтому:

- glm-стадии шли через не-claude команду → прокси вкл → z.ai → usage писался;
- claude-стадии (OAuth) → `proxyForCmd` возвращает `""` → прокси выкл → **usage не пишется вообще**.

Совпадает с наблюдением: «размер застопорился при переходе glm→claude».

## По токенам/деньгам

- **Расход сильно занижен**: только design-review сжёг ≥ **$3.41** (видно в `total_cost_usd`
  ошибки), а в `usage.jsonl` по нему $0. Учёт по факту не работает для всех claude-стадий.
- **Парсинг-баг (вторичный, пока неактуальный)**: `config.GetModelPricing` — **точное**
  совпадение по имени (`p.Models[model]`). В конфиге цены есть для `claude-sonnet-5` и
  `claude-haiku-4-5`, но реальная модель из API приходит как `claude-haiku-4-5-20251001`
  (с суффиксом даты) — haiku-часть ценится бы в 0. Но это не важно, пока usage не
  захватывается.
- **Отдельно, не про учёт**: прогон упирается в **org spend limit Anthropic** (429) —
  design-review упал по этой причине (`exit status 1`, 6m34s). Это причина падений стадий,
  не accounting.

## Варианты фикса

| Вариант | Суть | Плюс/минус |
|---|---|---|
| **A. Захват из вывода claude** | executor парсит финальную `result`-строку stream-json (`usage`/`modelUsage`/`total_cost_usd`) и пишет в usage.jsonl. Работает независимо от OAuth/прокси. | Самый надёжный для claude; устраняет зависимость от прокси как единственного источника. |
| **B. Прокси на реальный Anthropic при OAuth** | если определён OAuth-токен, направлять прокси на `api.anthropic.com` вместо z.ai и **не** отключать его в `proxyForCmd` для claude. Прокси умеет захватывать любой upstream. | Меньше нового кода, но завязывает захват на прокси. |

Рекомендуется **вариант A**: он закрывает дыру в учёте claude и не ломает z.ai-путь для
glm-обёрток.
