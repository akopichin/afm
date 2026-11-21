import path from 'node:path';
import { writeFile } from 'node:fs/promises';

const MAX_IMAGES = 4;
const MAX_BYTES = 5 * 1024 * 1024;
const png = Buffer.from('89504e470d0a1a0a', 'hex');
const signatures = {
  'image/png': { extension: 'png', matches: bytes => bytes.subarray(0, 8).equals(png) },
  'image/jpeg': { extension: 'jpg', matches: bytes => bytes[0] === 255 && bytes[1] === 216 && bytes[2] === 255 },
  'image/webp': { extension: 'webp', matches: bytes => bytes.toString('ascii', 0, 4) === 'RIFF' && bytes.toString('ascii', 8, 12) === 'WEBP' },
  'image/gif': { extension: 'gif', matches: bytes => ['GIF87a', 'GIF89a'].includes(bytes.toString('ascii', 0, 6)) },
};

export function assistantImages(values = []) {
  if (!Array.isArray(values) || values.length > MAX_IMAGES) throw Error('Attach up to four images per message.');
  return values.map(value => {
    const { type, data } = value ?? {};
    if (typeof type !== 'string' || !Object.hasOwn(signatures, type) || typeof data !== 'string') throw Error('Attach a PNG, JPEG, WebP or GIF image.');
    if (data.length > Math.ceil(MAX_BYTES / 3) * 4) throw Error('Each image must be at most 5 MiB.');
    const bytes = Buffer.from(data, 'base64');
    if (bytes.length > MAX_BYTES) throw Error('Each image must be at most 5 MiB.');
    if (bytes.toString('base64') !== data || !signatures[type].matches(bytes)) throw Error('The image attachment is invalid.');
    return { type, data };
  });
}

export async function writeCodexImages(dir, images) {
  return Promise.all(images.map(async ({ type, data }, index) => {
    const file = path.join(dir, `image-${index + 1}.${signatures[type].extension}`);
    await writeFile(file, Buffer.from(data, 'base64'), { mode: 0o600 });
    return file;
  }));
}

export function claudeImagePrompt(text, images) {
  const content = images.map(({ type, data }) => ({ type: 'image', source: { type: 'base64', media_type: type, data } }));
  return JSON.stringify({ type: 'user', message: { role: 'user', content: [...content, { type: 'text', text }] }, parent_tool_use_id: null }) + '\n';
}

export function endpointImagePrompt(text, images) {
  return [...images.map(({ type, data }) => ({ type: 'image_url', image_url: { url: `data:${type};base64,${data}` } })),
    ...(text ? [{ type: 'text', text }] : [])];
}
