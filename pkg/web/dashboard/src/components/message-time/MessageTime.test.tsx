import { describe, expect, test } from 'vitest'
import { render } from '@testing-library/react'
import { MessageTimeZoneProvider } from '../../lib/message-time-zone'
import { MessageTime } from './MessageTime'

function renderTime(timestamp: string | number, gap: string, timeZone = 'Asia/Tokyo', utcOffsetMinutes = 540, focusable = false, firstEvent = false) {
  const view = render(
    <MessageTimeZoneProvider timeZone={timeZone} utcOffsetMinutes={utcOffsetMinutes}>
      <MessageTime timestamp={timestamp} gap={gap} focusable={focusable} firstEvent={firstEvent} />
    </MessageTimeZoneProvider>,
  )
  return { ...view, time: view.container.querySelector('time') }
}

describe('MessageTime', () => {
  test('shows host-local HH:MM and the gap with one accessible name', () => {
    const { time } = renderTime('2026-10-21T05:31:09Z', '7s', 'Asia/Tokyo', 0, true)
    expect(time).not.toBeNull()
    expect(time).toHaveAttribute('dateTime', '2026-10-21T05:31:09.000Z')
    expect(time).toHaveAttribute('tabindex', '0')
    expect(time?.querySelector('.message-time-clock')).toHaveTextContent('14:31')
    expect(time?.querySelector('.message-time-gap')).toHaveTextContent('+7s')
    expect(time).toHaveAttribute('title', 'Oct 21, 2026, 14:31:09 (host time) · +7s after previous')
    expect(time).toHaveAttribute('aria-label', time?.getAttribute('title'))
    expect(time?.querySelectorAll('span[aria-hidden="true"]')).toHaveLength(2)
  })

  test('uses +0s for the first event and leaves a non-focusable time out of the tab order', () => {
    const { time } = renderTime(Date.parse('2026-10-21T05:31:09Z'), '0s', 'Asia/Tokyo', 540, false, true)
    expect(time?.querySelector('.message-time-gap')).toHaveTextContent('+0s')
    expect(time).toHaveAttribute('title', 'Oct 21, 2026, 14:31:09 (host time) · first event (+0s)')
    expect(time).not.toHaveAttribute('tabindex')
  })

  test('a later event in the same second is not described as the first', () => {
    const { time } = renderTime('2026-10-21T05:31:09Z', '0s')
    expect(time).toHaveAttribute('title', 'Oct 21, 2026, 14:31:09 (host time) · +0s after previous')
  })

  test.each([
    ['', 'host has no IANA name'],
    ['Mars/Phobos', 'browser does not know host IANA name'],
  ])('uses signed host offset when %s (%s)', (timeZone) => {
    const { time } = renderTime('2026-10-21T05:31:09Z', '7s', timeZone, 540)
    expect(time?.querySelector('.message-time-clock')).toHaveTextContent('14:31')
    expect(time).toHaveAttribute('title', 'Oct 21, 2026, 14:31:09 (host time) · +7s after previous')
  })

  test('accepts a feed epoch in milliseconds', () => {
    const { time } = renderTime(Date.parse('2026-10-21T05:31:09Z'), '2m')
    expect(time?.querySelector('.message-time-clock')).toHaveTextContent('14:31')
    expect(time?.querySelector('.message-time-gap')).toHaveTextContent('+2m')
  })

  test('uses historical DST from the IANA zone instead of the current offset snapshot', () => {
    const { time } = renderTime('2026-07-21T12:00:00Z', '7s', 'America/New_York', -300)
    expect(time?.querySelector('.message-time-clock')).toHaveTextContent('08:00')
  })

  test('keeps the sign of a negative fallback offset', () => {
    const { time } = renderTime('2026-10-21T12:00:00Z', '7s', '', -210)
    expect(time?.querySelector('.message-time-clock')).toHaveTextContent('08:30')
  })

  test.each(['invalid', '', Number.NaN, Number.POSITIVE_INFINITY])('renders no label for invalid timestamp %s', (timestamp) => {
    const { time, container } = renderTime(timestamp, '7s')
    expect(time).toBeNull()
    expect(container).toBeEmptyDOMElement()
  })
})
