// Метрики потребления ресурсов (переключатель панели потребления).
export type UsageMetric = 'tokens' | 'cost' | 'kb'

// Точка временного ряда потребления из GET /api/usage?metric=...&stage=...
//   timestamp — бакет времени в RFC 3339 (поле timeBucket сервера), подпись оси X;
//   metric    — метрика запроса (одна на весь ряд, проставляется хуком);
//   value     — значение метрики в точке.
export type UsagePoint = {
  timestamp: string
  metric: UsageMetric
  value: number
}
