import path from 'node:path';
import { readFile } from 'node:fs/promises';

export async function telegramSettings(home, env) {
  const secret = async (name, file) => (env[name]?.trim() || await readFile(path.join(home, '.afm', 'secrets', file), 'utf8').then(text => text.trim(), () => ''));
  const [token, chatId] = await Promise.all([
    secret('TELEGRAM_BOT_TOKEN', 'telegram-bot-token'), secret('TELEGRAM_CHAT_ID', 'telegram-chat-id'),
  ]);
  if (!/^\d+:[\w-]+$/.test(token) || !/^-?\d+$/.test(chatId)) {
    throw Error('Set TELEGRAM_BOT_TOKEN and TELEGRAM_CHAT_ID in Desktop Environment or in ~/.afm/secrets.');
  }
  return { token, chatId };
}

export async function sendTelegram({ token, chatId }, message, request = fetch) {
  const text = Array.from(message);
  const limited = text.length > 4096 ? text.slice(0,4095).join('') + '…' : message;
  const response = await request(`https://api.telegram.org/bot${token}/sendMessage`, {
    method: 'POST', redirect: 'error', signal: AbortSignal.timeout(10_000),
    headers: { 'content-type': 'application/json' }, body: JSON.stringify({ chat_id: chatId, text: limited }),
  }).catch(() => { throw Error('Telegram delivery could not be confirmed.'); });
  if (!response.ok || !(await response.json().catch(() => null))?.ok) throw Error(`Telegram rejected delivery (HTTP ${response.status}).`);
}
