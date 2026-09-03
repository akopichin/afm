// Тонкая обёртка над highlight.js/lib/core с ЯВНЫМ списком из 4 грамматик —
// go/typescript/javascript/python (см. бриф Task 13). core (не index.js)
// нужен, чтобы не тянуть в бандл все ~190 грамматик highlight.js — дашборд
// показывает только код проекта afm, для которого этих четырёх достаточно.
//
// highlight() — единственная функция, чей вывод разрешено класть в
// dangerouslySetInnerHTML (см. FileViewer.tsx): hljs.highlight() сам
// экранирует спецсимволы найденного текста, оборачивая только распознанные
// токены в <span class="hljs-...">; escapeHtml — тот же контракт для языка
// 'plain' или нераспознанной грамматики, чтобы обе ветки одинаково безопасны
// класть в HTML.
import hljs from 'highlight.js/lib/core'
import go from 'highlight.js/lib/languages/go'
import javascript from 'highlight.js/lib/languages/javascript'
import python from 'highlight.js/lib/languages/python'
import typescript from 'highlight.js/lib/languages/typescript'

hljs.registerLanguage('go', go)
hljs.registerLanguage('typescript', typescript)
hljs.registerLanguage('javascript', javascript)
hljs.registerLanguage('python', python)

export function escapeHtml(source: string): string {
  return source.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
}

// highlight возвращает HTML-строку: подсвеченную (hljs сам экранирует текст
// внутри span'ов) для языка из зарегистрированных четырёх, иначе — просто
// экранированный исходник как есть (язык 'plain' — явный сигнал "не пытайся
// подсвечивать", как и любой язык, которого нет в hljs.getLanguage).
export function highlight(language: string, source: string): string {
  if (language === 'plain' || !hljs.getLanguage(language)) {
    return escapeHtml(source)
  }
  return hljs.highlight(source, { language }).value
}
