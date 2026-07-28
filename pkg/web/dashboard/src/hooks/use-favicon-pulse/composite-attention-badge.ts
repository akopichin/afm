const BADGE_SIZE = 32
const BADGE_RADIUS = 6
const BADGE_RING_RADIUS = 8
const BADGE_CENTER = BADGE_SIZE - BADGE_RING_RADIUS - 1
const FALLBACK_AMBER = '#e5d442'

const badgeCache = new Map<string, Promise<string>>()

function readAmberColor(): string {
  const value = getComputedStyle(document.documentElement).getPropertyValue('--amber').trim()
  return value !== '' ? value : FALLBACK_AMBER
}

function loadImage(href: string): Promise<HTMLImageElement> {
  return new Promise((resolve, reject) => {
    const img = new Image()
    img.onload = () => resolve(img)
    img.onerror = () => reject(new Error(`favicon-pulse: failed to load icon "${href}"`))
    img.src = href
  })
}

// Рисует амберный (по факту — цвет var(--amber) активного скина) бейдж-кружок
// поверх текущей иконки вкладки, какой бы она ни была (дефолтная/скиновая) —
// не заменяет иконку отдельным ассетом, а компонует поверх неё через canvas.
// Кешируется по iconHref: повторная активация пульса на той же иконке не
// перерисовывает canvas заново.
export async function compositeAttentionBadge(iconHref: string): Promise<string> {
  const cached = badgeCache.get(iconHref)
  if (cached !== undefined) return cached

  const promise = (async () => {
    const img = await loadImage(iconHref)
    const canvas = document.createElement('canvas')
    canvas.width = BADGE_SIZE
    canvas.height = BADGE_SIZE
    const ctx = canvas.getContext('2d')
    if (ctx === null) {
      throw new Error('favicon-pulse: 2d canvas context unavailable')
    }

    ctx.drawImage(img, 0, 0, BADGE_SIZE, BADGE_SIZE)

    ctx.beginPath()
    ctx.arc(BADGE_CENTER, BADGE_CENTER, BADGE_RING_RADIUS, 0, Math.PI * 2)
    ctx.fillStyle = 'rgba(0, 0, 0, 0.55)'
    ctx.fill()

    ctx.beginPath()
    ctx.arc(BADGE_CENTER, BADGE_CENTER, BADGE_RADIUS, 0, Math.PI * 2)
    ctx.fillStyle = readAmberColor()
    ctx.fill()

    return canvas.toDataURL()
  })()

  badgeCache.set(iconHref, promise)
  promise.catch(() => badgeCache.delete(iconHref))
  return promise
}
