'use strict';
const isDemo=new URLSearchParams(location.search).get('demo')==='1';
const storeKey=isDemo?'afm.desktop.sample':'afm.desktop.workspace';
const seed = {
  gitDefault:'worktree', setup: false, profile: 'codex', chat: [],
  models: [
    {id:'codex', name:'Codex', model:'CLI default', auth:'Existing CLI login'},
    {id:'claude', name:'Claude Code', model:'CLI default', auth:'Existing CLI login'}
  ],
  sources: [{id:'starter', name:'Starter library', items:[
    {name:'feature-delivery', kind:'Flow', path:'flows/feature-delivery.yaml'},
    {name:'code-review', git:'branch', kind:'Flow', path:'flows/code-review.yaml'},
    {name:'architecture-workshop', kind:'Pipeline', path:'pipelines/architecture-workshop'},
    {name:'refactor', git:'worktree', kind:'Pipeline', path:'pipelines/refactor'}
  ]}],
  projects: [
    {id:'spider', name:'spider', path:'~/work/spider', git:true, tone:'var(--violet)', runs:[{id:'checkout',flow:'checkout-flow',source:'Desktop',state:'running',live:true}]},
    {id:'afm', name:'afm', path:'~/work/personal/afm', git:true, tone:'var(--attention)', runs:[{id:'review',flow:'release-review',source:'Terminal',state:'review',live:false}]},
    {id:'spider-ui',name:'spider-ui',path:'~/work/spider-ui',git:true, tone:'var(--success)',runs:[]}
  ]
};
function readStore(){
  try {return JSON.parse(localStorage.getItem(storeKey)) || seed}
  catch {return seed}
}
const state = readStore();
// Projects and library sources belong to the main process; only the demo keeps them in the page.
if(!isDemo){state.projects=[];state.sources=[];state.chat=[]}
// The real assistant's providers and conversation belong to the main process; these stay until it answers.
if(!isDemo)state.models=[{id:'claude',name:'Claude Code'},{id:'codex',name:'Codex'}].map(m=>({...m,model:'CLI default',auth:'Checking…',permission:'ask',levels:[],permissions:['ask','read','edit','yolo'],modes:{}}));
const projects = state.projects;
function save(){try{localStorage.setItem(storeKey,JSON.stringify(isDemo?state:{...state,projects:[],sources:[],chat:[],models:[],profile:''}))}catch{}}
const uid = () => crypto.randomUUID();

if(isDemo&&!state.attentionSeeded){
  const now=Date.now();
  const spider=projects.find(p=>p.id==='spider');
  if(spider)spider.runs.unshift({id:'delivery-check',flow:'delivery-check',state:'question',source:'Desktop',scenario:'question',live:false,noticeAt:now-120000});
  const review=projects.find(p=>p.id==='afm')?.runs.find(r=>r.id==='review');
  if(review)review.noticeAt=now-180000;
  const checkout=spider?.runs.find(r=>r.id==='checkout'&&r.live);
  if(checkout){checkout.liveNotice=true;checkout.noticeAt=now-60000}
  state.attentionSeeded=true;save();
}
// Notices the main process found in running stages; the demo derives its own from sample runs.
let attentionItems=[];
