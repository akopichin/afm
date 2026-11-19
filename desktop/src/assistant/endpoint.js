const MAX_LINE = 1 << 20;

// Chat with an OpenAI-compatible endpoint: streamed text only, no tools and no file access.
export function runEndpoint({ endpoint, apiKey, model, messages, onEvent, fetchImpl = fetch }) {
  const controller = new AbortController();
  let cancelled = false;
  const done = (async () => {
    try {
      const response = await fetchImpl(endpoint + '/chat/completions', {
        method: 'POST', redirect: 'error', signal: controller.signal,
        headers: { 'content-type': 'application/json', authorization: 'Bearer ' + apiKey },
        body: JSON.stringify({ model, messages, stream: true }),
      });
      if (!response.ok) return { code: 1, error: Error(`The endpoint answered ${response.status}.`) };
      const decoder = new TextDecoder();
      let buffer = '';
      for await (const chunk of response.body) {
        buffer += decoder.decode(chunk, { stream: true });
        if (buffer.length > MAX_LINE) return { code: 1, error: Error('The endpoint sent an oversized event.') };
        let end;
        while ((end = buffer.indexOf('\n')) >= 0) {
          const line = buffer.slice(0, end).trim();
          buffer = buffer.slice(end + 1);
          if (!line.startsWith('data:') || line.slice(5).trim() === '[DONE]') continue;
          try {
            const text = JSON.parse(line.slice(5)).choices?.[0]?.delta?.content;
            if (text) onEvent({ type: 'delta', text });
          } catch { /* Keep-alive or a format this adapter does not read. */ }
        }
      }
      onEvent({ type: 'result', ok: true, text: '', error: null });
      return { code: 0, cancelled };
    } catch (error) {
      return cancelled ? { code: null, cancelled } : { code: 1, error };
    }
  })();
  return { done, cancel() { cancelled = true; controller.abort(); } };
}
