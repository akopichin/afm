import { editor } from 'monaco-editor/editor/editor.api.js';
import 'monaco-editor/languages/definitions/yaml/register.js';
import 'monaco-editor/editor/contrib/find/browser/findController.js';

const workerURL = URL.createObjectURL(new Blob([AFM_EDITOR_WORKER], {type: 'text/javascript'}));
globalThis.MonacoEnvironment = {
  getWorker: () => new Worker(workerURL, {name: 'afm-code-editor'}),
};

const dark = {
  background: '0B111B', foreground: 'F5F7FB', muted: '97A1B4', gutter: '111A27',
  selection: '273A70', key: 'A9B5FF', string: '72D6AD', value: 'F2B84B',
};
const light = {
  background: 'FFFFFF', foreground: '172033', muted: '536179', gutter: 'F3F5F9',
  selection: 'DCE4FD', key: '3D48A3', string: '17664A', value: '8D450B',
};
function theme(base, colors) {
  return {
    base, inherit: true,
    rules: [
      { token: 'comment', foreground: colors.muted },
      { token: 'type', foreground: colors.key },
      { token: 'string', foreground: colors.string },
      { token: 'number', foreground: colors.value },
      { token: 'keyword', foreground: colors.value },
    ],
    colors: {
      'editor.background': '#' + colors.background,
      'editor.foreground': '#' + colors.foreground,
      'editorLineNumber.foreground': '#' + colors.muted,
      'editorGutter.background': '#' + colors.gutter,
      'editor.selectionBackground': '#' + colors.selection,
      'editor.inactiveSelectionBackground': '#' + colors.selection,
    },
  };
}
editor.defineTheme('afm-dark', theme('vs-dark', dark));
editor.defineTheme('afm-light', theme('vs', light));

export function createCodeEditor(parent, {text, language, label, readOnly = false}) {
  const syncTheme = () => editor.setTheme(document.documentElement.dataset.theme === 'light' ? 'afm-light' : 'afm-dark');
  syncTheme();
  const observer = new MutationObserver(syncTheme);
  observer.observe(document.documentElement, {attributes: true, attributeFilter: ['data-theme']});
  const model = editor.createModel(text, language);
  const view = editor.create(parent, {
    model, readOnly, ariaLabel: label, automaticLayout: true, fontSize: 13, tabSize: 2,
    insertSpaces: true, minimap: {enabled: false}, scrollBeyondLastLine: false,
    wordWrap: 'on', wordBasedSuggestions: 'off', quickSuggestions: false,
  });
  return {
    getValue: () => model.getValue(),
    setReadOnly: value => view.updateOptions({readOnly: value}),
    onChange: callback => model.onDidChangeContent(callback),
    dispose() { observer.disconnect(); view.dispose(); model.dispose(); },
  };
}
