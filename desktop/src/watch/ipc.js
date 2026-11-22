import { taskView } from './index.js';

// Renderer-facing watch calls. The page names a task by id only; every change is stored before the call returns.
export function registerWatchIpc(handle, watch, publish) {
  const changed = async change => { const task = taskView(change()); await watch.flush(); return task; };
  handle('watch:list', () => watch.list().map(taskView));
  handle('watch:pause', id => changed(() => watch.pause(String(id))));
  handle('watch:resume', id => changed(() => watch.resume(String(id))));
  handle('watch:stop', id => changed(() => watch.stop(String(id))));
  handle('watch:remove', id => watch.remove(String(id)));
  handle('watch:read', id => changed(() => watch.markRead(String(id))));
  return () => publish(watch.list().map(taskView));
}
