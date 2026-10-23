// copyToClipboard — best-effort: Clipboard API нет в небезопасном контексте (дашборд по LAN-адресу), а запись
// может быть отклонена. Результат нужен, чтобы пользователь увидел неудачу до того, как удалит оригинал.
export async function copyToClipboard(text: string): Promise<boolean> {
  try {
    if (navigator.clipboard === undefined) return false
    await navigator.clipboard.writeText(text)
    return true
  } catch {
    return false
  }
}
