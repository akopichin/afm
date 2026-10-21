import { render, screen } from '@testing-library/react'
import { describe, expect, test } from 'vitest'
import { MessageTimeZoneProvider, useMessageTimeZone } from './message-time-zone'

// Потребитель выводит значение контекста, чтобы тесты читали его из DOM.
function ZoneProbe() {
  const { timeZone, utcOffsetMinutes } = useMessageTimeZone()
  return <div data-testid="zone">{`${timeZone}|${utcOffsetMinutes}`}</div>
}

const browserZone = Intl.DateTimeFormat().resolvedOptions().timeZone
// Хост-зона, гарантированно отличная от зоны браузера, в которой идёт тест.
const hostZone = browserZone === 'Asia/Tokyo' ? 'America/New_York' : 'Asia/Tokyo'
const hostOffset = hostZone === 'Asia/Tokyo' ? 540 : -300

describe('MessageTimeZoneProvider', () => {
  test('host zone from /api/status wins over a different browser zone', () => {
    expect(hostZone).not.toBe(browserZone)
    render(
      <MessageTimeZoneProvider timeZone={hostZone} utcOffsetMinutes={hostOffset}>
        <ZoneProbe />
      </MessageTimeZoneProvider>,
    )
    expect(screen.getByTestId('zone').textContent).toBe(`${hostZone}|${hostOffset}`)
  })

  test('keeps an empty zone name with a valid signed offset (unidentified host zone)', () => {
    render(
      <MessageTimeZoneProvider timeZone="" utcOffsetMinutes={-210}>
        <ZoneProbe />
      </MessageTimeZoneProvider>,
    )
    // Не подменяем browser-зоной: offset пришёл с хоста, именно он должен задавать часы.
    expect(screen.getByTestId('zone').textContent).toBe('|-210')
  })

  test('falls back to the browser zone only when an older server omits both fields', () => {
    render(
      <MessageTimeZoneProvider timeZone="" utcOffsetMinutes={null}>
        <ZoneProbe />
      </MessageTimeZoneProvider>,
    )
    const expectedOffset = -new Date().getTimezoneOffset()
    expect(screen.getByTestId('zone').textContent).toBe(`${browserZone}|${expectedOffset}`)
  })

  test('consumer without a provider gets the safe browser-local default', () => {
    render(<ZoneProbe />)
    const expectedOffset = -new Date().getTimezoneOffset()
    expect(screen.getByTestId('zone').textContent).toBe(`${browserZone}|${expectedOffset}`)
  })
})
