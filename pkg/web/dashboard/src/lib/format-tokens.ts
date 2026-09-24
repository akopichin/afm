// Компактный формат количества токенов для тесных мест дашборда (тайл
// «Est. tokens» в шапке): 1_234_567 → «1.2M», 345_000 → «345K», меньше 1000 —
// как есть. Отдельно от полного formatTokens в CostPanel (там таблица показывает
// точные значения через toLocaleString) — расхождение намеренное: тайлу нужна
// краткость, таблице — точность.
export function formatTokensCompact(n: number): string {
  if (n < 1000) return String(n)
  if (n < 1_000_000) return `${trimTrailingZero(n / 1000)}K`
  return `${trimTrailingZero(n / 1_000_000)}M`
}

// Одна значащая цифра после запятой, но без хвостового «.0» (12.0 → «12»).
function trimTrailingZero(value: number): string {
  const rounded = Math.round(value * 10) / 10
  return Number.isInteger(rounded) ? String(rounded) : rounded.toFixed(1)
}
