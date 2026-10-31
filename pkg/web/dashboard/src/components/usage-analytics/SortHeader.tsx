import type { ReactElement } from 'react'
import type { UsageSortState } from '../../hooks/use-usage-analytics'
import type { UsageSort } from '../../types/usage-analytics'

type SortHeaderProps = {
  label: string
  sortKey: UsageSort
  current: UsageSortState | null
  onSort: (key: UsageSort) => void
  numeric?: boolean
}

// SortHeader — заголовок колонки со своей сортировкой: aria-sort у активной, none у остальных. Повторный
// клик меняет направление (это решает controller.sortBy).
export function SortHeader({ label, sortKey, current, onSort, numeric = false }: SortHeaderProps): ReactElement {
  const active = current?.key === sortKey
  const ariaSort = active ? (current.dir === 'asc' ? 'ascending' : 'descending') : 'none'
  return (
    <th scope="col" aria-sort={ariaSort} className={numeric ? 'usage-numeric' : undefined}>
      <button type="button" className="usage-sort-button" onClick={() => onSort(sortKey)}>
        {label}
        <span className={`usage-sort-arrow${active ? ` is-active ${current.dir}` : ''}`} aria-hidden="true" />
      </button>
    </th>
  )
}
