import { createHash } from 'node:crypto';
import path from 'node:path';
import { desktopDir, Mutex, readJSON, writeJSON } from '../files.js';

export const backlogTool = {
  name: 'desktop_backlog_add',
  description: 'Save a plan in the current backlog when the user asks. Preserve the supplied title. Include the plan and acceptance criteria when available. A request with only a title can create a draft.',
  inputSchema: { type: 'object', required: ['title'], properties: {
    title: { type: 'string', maxLength: 160 },
    plan: { type: 'string', maxLength: 9000 },
    acceptance: { type: 'string', maxLength: 2000 },
  } },
};

export const BACKLOG_COMMAND = /^\/backlog (?:list(?: done)?|done \d+|delete \d+|done delete \d+|done clear)$/;

export class AssistantBacklog {
  #lock = new Mutex();
  constructor(home) { this.root = path.join(desktopDir(home), 'assistant', 'memory', 'backlogs'); }

  withBacklog(scope, action) {
    const key = scope.projectRoot ? createHash('sha256').update(scope.projectRoot).digest('hex') : 'all-projects';
    const file = path.join(this.root, key, 'backlog.json');
    return this.#lock.run(async () => {
      const stored = await readJSON(file, []);
      const store = Array.isArray(stored) ? { plans: stored, lastNumber: stored.at(-1)?.number ?? 0 } : stored;
      return action(store, () => writeJSON(file, store));
    });
  }

  add(scope, args, source, authorized) {
    return this.withBacklog(scope, async (store, save) => {
      const rows = store.plans;
      if (!authorized()) throw Error('This assistant reply has ended.');
      const title = String(args.title ?? '').trim().slice(0, 160);
      const plan = String(args.plan ?? '').trim().slice(0, 9000);
      const acceptance = String(args.acceptance ?? '').trim().slice(0, 2000);
      if (!title) throw Error('A title is required.');
      const previous = rows.find(row => row.source.messageId === source.messageId && row.title === title);
      if (previous) return previous;
      const row = { number: ++store.lastNumber, title, plan, acceptance, source, createdAt: new Date().toISOString() };
      rows.push(row);
      await save();
      return row;
    });
  }

  plans(scope, completed = false) {
    return this.withBacklog(scope, ({ plans }) => plans.filter(row => Boolean(row.doneAt) === completed).map(({ number, title }) => ({ number, title })));
  }

  command(scope, command) {
    return this.withBacklog(scope, async (store, save) => {
      const rows = store.plans;
      if (command === '/backlog done clear') {
        const count = rows.filter(row => row.doneAt).length;
        if (!count) return 'No completed plans to clear.';
        store.plans = rows.filter(row => !row.doneAt);
        await save();
        return `Removed ${count} completed ${count === 1 ? 'plan' : 'plans'}.`;
      }
      const deletion = /^\/backlog (done )?delete (\d+)$/.exec(command);
      if (deletion) {
        const index = rows.findIndex(row => row.number === Number(deletion[2]) && Boolean(row.doneAt) === Boolean(deletion[1]));
        if (index < 0) return `No ${deletion[1] ? 'completed' : 'open'} plan #${deletion[2]}.`;
        const [row] = rows.splice(index, 1);
        await save();
        return `Deleted #${row.number} · ${row.title}.`;
      }
      const done = /^\/backlog done (\d+)$/.exec(command);
      if (done) {
        const row = rows.find(item => item.number === Number(done[1]));
        if (!row) return `Plan #${done[1]} was not found.`;
        if (row.doneAt) return `Already completed #${row.number} · ${row.title}.`;
        row.doneAt = new Date().toISOString();
        await save();
        return `Marked complete #${row.number} · ${row.title}.`;
      }
      const complete = command === '/backlog list done';
      const matches = rows.filter(row => Boolean(row.doneAt) === complete);
      return `${complete ? 'Completed' : 'Open'} plans\n\n${matches.map(row => `#${row.number} · ${row.title}`).join('\n') || 'No plans yet.'}`;
    });
  }
}
