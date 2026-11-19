// One CDP message is a JSON text. Chrome's pipe separates them with NUL, the Desktop broker with a newline;
// neither byte can occur inside serialized JSON.
export const MAX_MESSAGE = 8 << 20;

export class Framer {
  #buffer = '';
  constructor(separator) { this.separator = separator; }

  // Returns the complete messages in the chunk; a partial tail waits for the next one.
  push(chunk) {
    this.#buffer += chunk;
    const parts = this.#buffer.split(this.separator);
    this.#buffer = parts.pop();
    if (this.#buffer.length > MAX_MESSAGE) throw Error('CDP message exceeds the size limit.');
    return parts.filter(Boolean);
  }
}

export const pipeFramer = () => new Framer('\0');
export const lineFramer = () => new Framer('\n');
