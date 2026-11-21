'use strict';
// The summary dialog and the small markdown subset reports use. Everything is escaped first, so report text can never add markup.
const runLink=/^afm:\/\/run\/([\w.-]+)\/([\w.-]+)(?:\/([\w.-]+))?$/;

// Inline text: `code`, **bold**, and links to runs or stages (afm://run/<instance>/<run>[/<stage>]).
// Any other link shows its address as plain text, because a report must not open arbitrary pages.
function inlineMarkdown(text){
  return text.split(/(`[^`\n]+`)/).map(part=>{
    if(/^`[^`\n]+`$/.test(part))return `<code>${esc(part.slice(1,-1))}</code>`;
    return esc(part).replace(/\*\*([^*\n]+)\*\*/g,'<strong>$1</strong>').replace(/\[([^\]\n]+)\]\(([^)\s]+)\)/g,(_all,label,url)=>{
      const target=runLink.exec(url.replace(/&amp;/g,'&'));
      return target?`<button type="button" class="inline-link" data-report-run="${target[2]}" data-report-instance="${target[1]}" data-report-stage="${target[3]||''}">${label} ↗</button>`:`${label} (${url})`;
    });
  }).join('');
}
// Block structure: headings, bullet lists, fenced code (a diagram is shown as its text) and <details> on lines of their own.
function renderMarkdown(source){
  const lines=String(source).replace(/\r\n?/g,'\n').split('\n'),out=[];
  let list=false,fence=null,paragraph=[];
  const endParagraph=()=>{if(paragraph.length)out.push(`<p>${paragraph.map(inlineMarkdown).join('<br>')}</p>`);paragraph=[]};
  const endList=()=>{if(list)out.push('</ul>');list=false};
  for(const line of lines){
    if(fence){
      if(line.trim().startsWith('```')){out.push(`${fence.diagram?'<p class="diagram-note">Diagram (shown as text)</p>':''}<pre><code>${esc(fence.lines.join('\n'))}</code></pre>`);fence=null}
      else fence.lines.push(line);
      continue;
    }
    const open=/^\s*```\s*(\w*)/.exec(line);
    if(open){endParagraph();endList();fence={lines:[],diagram:/^(mermaid|plantuml|dot|graphviz)$/i.test(open[1])};continue}
    const summary=/^\s*<summary>(.*)<\/summary>\s*$/i.exec(line);
    if(/^\s*<details>\s*$/i.test(line)){endParagraph();endList();out.push('<details>');continue}
    if(/^\s*<\/details>\s*$/i.test(line)){endParagraph();endList();out.push('</details>');continue}
    if(summary){endParagraph();endList();out.push(`<summary>${inlineMarkdown(summary[1])}</summary>`);continue}
    const heading=/^(#{1,4})\s+(.*)$/.exec(line),item=/^\s*[-*]\s+(.*)$/.exec(line);
    if(heading){endParagraph();endList();const level=Math.min(heading[1].length+1,5);out.push(`<h${level}>${inlineMarkdown(heading[2])}</h${level}>`)}
    else if(item){endParagraph();if(!list)out.push('<ul>');list=true;out.push(`<li>${inlineMarkdown(item[1])}</li>`)}
    else if(!line.trim()){endParagraph();endList()}
    else{endList();paragraph.push(line)}
  }
  if(fence)out.push(`<pre><code>${esc(fence.lines.join('\n'))}</code></pre>`);
  endParagraph();endList();
  // An unclosed <details> must not swallow the rest of the dialog.
  const opened=(out.join('').match(/<details>/g)||[]).length-(out.join('').match(/<\/details>/g)||[]).length;
  return out.join('\n')+'</details>'.repeat(Math.max(0,opened));
}

// The report text and the events Desktop added at `eventsOffset`, cut before line endings are cleaned up so the cut lands
// where it was stored. A report without a valid offset, as older ones are, stays one part.
function reportParts({markdown,eventsOffset}){
  const valid=typeof markdown==='string'&&Number.isInteger(eventsOffset)&&eventsOffset>=0&&eventsOffset<=markdown.length;
  return valid?[markdown.slice(0,eventsOffset),markdown.slice(eventsOffset)]:[markdown];
}
// Each part is parsed into an element of its own, so a block the model text left open cannot hide the event links.
function renderReport(report){
  return reportParts(report).map(part=>{const block=document.createElement('div');block.innerHTML=renderMarkdown(part);return block});
}

let reportOpener=null,reportNavigating=false;
const reportDialog=()=>$('watch-report');
// Opens a stored report. The opener gets the focus back when the dialog closes, unless the user navigated away.
// Registered after showModal so it runs after the shell's generic restore and has the last word on focus.
function restoreReportFocus(){
  const opener=reportOpener;reportOpener=null;
  if(reportNavigating){reportNavigating=false;return}
  const target=(opener&&findControl(opener))||$('assistant-toggle');
  target.focus({preventScroll:true});
}
function openReport(task,report){
  const dialog=reportDialog();
  reportOpener=rememberControl(document.activeElement instanceof HTMLElement?document.activeElement:null);
  $('watch-report-scope').textContent='Watch · '+watchScopeText(task);
  $('watch-report-title').textContent=report.title||task.title;
  $('watch-report-time').textContent=new Date(report.at).toLocaleString();
  $('watch-report-body').replaceChildren(...renderReport(report));
  reportNavigating=false;
  dialog.showModal();
  dialog.addEventListener('close',restoreReportFocus,{once:true});
  $('watch-report-body').scrollTop=0;
  $('watch-report-title').focus({preventScroll:true});
  nativeAFM.watch.markRead(task.id).catch(showNativeError);
}
// Closes the report for a navigation shortcut, which then runs normally.
function closeReportForNavigation(){
  if(!reportDialog().open)return false;
  reportNavigating=true;
  reportDialog().close();
  return true;
}
reportDialog().addEventListener('click',e=>{
  if(e.target.closest('[data-report-close]')){reportDialog().close();return}
  const link=e.target.closest('[data-report-run]');
  if(!link)return;
  closeReportForNavigation();
  openRunLink(link.dataset.reportInstance,link.dataset.reportRun,link.dataset.reportStage);
});
reportDialog().addEventListener('keydown',e=>{
  const command={KeyH:'home',KeyJ:'next',KeyK:'previous'}[e.code];
  if(!command||!primary(e)||e.altKey||e.shiftKey||e.repeat||e.isComposing)return;
  e.preventDefault();e.stopPropagation();
  closeReportForNavigation();
  navigateApp(command);
});
