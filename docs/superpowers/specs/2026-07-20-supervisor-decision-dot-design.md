# Supervisor Decision: точка-индикатор + поповер по клику

**Дата:** 2026-07-20
**Ветка:** ux-improvements
**Область:** `pkg/web/dashboard` (фронтенд дашборда)

## Проблема

В заголовке выбранного стейджа решение супервизора рендерится инлайн-бейджем
`supervisor: autonomous — <длинная причина>` рядом со статус-бейджем (RUNNING).
Причина выводится видимым текстом и занимает много места в строке заголовка,
растягивая её и вытесняя остальные элементы.

## Цель

Заменить инлайн-бейдж на компактную **точку** в правом-верхнем углу статус-бейджа.
Причину показывать во всплывающем **фиолетовом поповере по клику** — не в потоке.

## Дизайн

### Разметка (`src/app/App.tsx`)

Сейчас `status-badge` и `SupervisorDecision` — соседние элементы в строке
`stageHeader`. Оборачиваем статус-бейдж и индикатор в контейнер
`position: relative`, чтобы точку можно было спозиционировать в угол бейджа
независимо от потока:

```tsx
<span className="status-badge-wrap">
  <span id="detail-status" className="status-badge" data-status={selectedStage.status}>
    {STAGE_STATUS_LABELS[selectedStage.status]}
  </span>
  {selectedStageId != null && <SupervisorDecision stageId={selectedStageId} />}
</span>
```

Инлайн-текст `supervisor: <decision> — <reason>` и элемент `.supervisor-reason`
удаляются полностью.

### Компонент (`src/components/supervisor-decision/SupervisorDecision.tsx`)

Логика загрузки данных не меняется: тот же `useEffect` с поллингом
`/api/stages/<id>/supervisor` каждые 3 c, тот же тип `Decision`.

Меняется только рендер:

- `decision == null` → возвращаем `null` (точки нет, как сейчас).
- Иначе рендерим:
  - `<button class="supervisor-dot autonomous|standard">` — круглая точка ~8px,
    абсолютно спозиционированная в верхний-правый угол бейджа (`top:-3px; right:-3px`).
    Класс трека (`autonomous`/`standard`) задаёт цвет.
  - При открытом состоянии — `<div class="supervisor-popover">` под точкой:
    заголовок `supervisor: <decision>` + причина текстом (перенос строк,
    `max-width` ~280px, без обрезки многоточием). Если `reason === ''` —
    только заголовок.

Состояние и взаимодействие:

- Локальный `const [open, setOpen] = useState(false)`.
- Клик по точке — тоггл `open`.
- Закрытие: повторный клик по точке; клик вне (слушатель `mousedown` на
  `document`, вешается только при `open`); нажатие `Esc`.
- Смена `stageId` сбрасывает `open` в `false`.

Доступность:

- Точка — `<button>` с `aria-label` (напр. `supervisor decision: autonomous`),
  `aria-expanded={open}`.
- Поповер — `role="dialog"` (или `tooltip`).

### CSS (`public/style.css`)

- Удалить `.supervisor-reason`; переработать `.supervisor-badge` в:
  - `.status-badge-wrap { position: relative; display: inline-flex; }`
  - `.supervisor-dot` — круг (`width/height: 8px; border-radius: 50%;
    background: currentColor;`), `position: absolute; top:-3px; right:-3px;`,
    `cursor: pointer;`, `border: none;`, доступный размер клика (padding/box).
  - `.supervisor-popover` — абсолютный блок под точкой: фиолетовая рамка/фон в
    духе текущего `#c084fc`, тень, `z-index` поверх соседей, `max-width: 280px`,
    перенос строк.
- Классы цвета точки:
  - `.supervisor-dot.standard { color: var(--amber); }`
  - `.supervisor-dot.autonomous { color: #c084fc; }`

### Совместимость с темами

Дашборд имеет две визуальные темы: `theme-novacorps` (дефолт, `public/style.css`)
и `theme-goga` (`public/style-goga.css`, оверрайды через префикс `.theme-goga`).
Текущий `.supervisor-badge` не имеет goga-оверрайдов и работает корректно в обеих
темах — сохраняем ту же схему:

| Трек | Цвет | novacorps | goga |
|------|------|-----------|------|
| standard | `var(--amber)` (токен темы) | `#e5d442` (жёлтый) | `#3882F6` (синий) |
| autonomous | `#c084fc` (хардкод) | фиолетовый | фиолетовый |

- **standard** берёт `var(--amber)` → адаптируется под тему автоматически.
- **autonomous** — хардкод-фиолетовый в обеих темах (совпадает с текущим
  поведением и требованием «фиолетовый тултип»).
- Поповер стилизуется на `#c084fc` — единый вид в обеих темах, отдельный
  `.theme-goga`-оверрайд не требуется.

### Сборка

Корневой `style.css` — build-копия `public/style.css` (Vite копирует `public/`
+ артефакт коммитится / встраивается через `go:embed`). Правки вносятся в
`public/style.css`, затем дашборд пересобирается, чтобы обновить корневой/
встроенный ассет.

## Тесты (`src/components/supervisor-decision/SupervisorDecision.test.tsx`, новый)

- точка рендерится при наличии решения; ничего не рендерится при `decision == null`;
- класс цвета соответствует треку (`autonomous`/`standard`);
- клик по точке открывает поповер с текстом `supervisor: <decision>` и причиной;
- поповер закрывается по повторному клику, клику вне и нажатию `Esc`;
- при `reason === ''` поповер показывает только заголовок.

## Вне области

- API/бэкенд (`/api/stages/<id>/supervisor`, `supervisor.jsonl`) не меняется.
- Другие компоненты заголовка (`h2`, `ornament`) не трогаем.
- Логика поллинга и типы данных не меняются.
