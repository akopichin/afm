'use strict';
(() => {
  const params=new URLSearchParams(location.search);
  const isReview=params.get('source')==='Terminal'||params.get('scenario')==='review';
  const asking=params.get('scenario')==='question';
  const question={id:'shipping',phase:'planning',question:'Which delivery option should the checkout test cover?',options:['Standard','Express'],allow_custom:true,answer:params.get('answer')||null};
  const stopped=params.get('state')==='stopped';
  const hasBrowser=params.get('live')!=='no';
  const start=Date.now()-12*60000;
  const stages=(asking?['inspect','delivery','verify']:isReview?['inspect','review-plan','verify','publish']:(hasBrowser?['plan','implement','live-run','verify','report']:['plan','implement','verify','report'])).map((id,i)=>{
    const current=isReview||!hasBrowser?1:2;
    const waiting=isReview&&params.get('state')==='review';
    const status=i<current?'done':i>current?'pending':asking&&params.get('state')==='question'?'awaiting_user_input':stopped?'paused':waiting?'awaiting_approval':'running';
    return {id,name:id,status,command:params.get('agent')||'codex',interactive:asking&&i===current,autonomous:false,auto_approve:false,show_plan:i<=current,show_dialog:asking&&i===current,has_dialog:asking&&i===current,is_script:false};
  });
  const status={flow_name:params.get('flow')||'checkout-flow',run_id:params.get('id')||'desktop-preview',version:'Desktop preview',
    description:[params.get('project')||'spider',params.get('workspace')].filter(Boolean).join(' · '),started_at:new Date(start).toISOString(),run_status:stopped?'interrupted':'running',
    elapsed_accumulated_ms:720000,idle_accumulated_ms:18000,stages,capabilities:{}};
  const plans={
    'live-run':'## Verify the checkout flow\n\n1. Open the checkout page in the stage browser.\n2. Switch between standard and express delivery.\n3. Check the total and capture the receipt.',
    'review-plan':'## Release review\n\n1. Inspect the changes since the last release.\n2. Review compatibility and test coverage.\n3. Publish a summary of the findings.'
  };
  const messages={
    'live-run':[['text','The checkout is ready for a browser pass. I’ll verify both delivery options and check the order total.'],['navigate_page','/checkout'],['take_snapshot','Order summary · Trail notebook'],['text','Standard delivery shows **$24.00**. The page is open in the Live tab. Next I’ll check the express rate.']],
    'review-plan':[['text','I reviewed the release changes and prepared a plan. The compatibility checks are ready for approval.']],
    plan:[['text','The implementation plan is ready.']],implement:[['text','Added the delivery selector and order total.']],inspect:[['text','Collected changes for the release review.']]
  };
  const events=Object.entries(messages).flatMap(([stage_id,items])=>items.map(([tool,detail],i)=>({stage_id,type:'agent_action',timestamp:new Date(start+480000+i*14000).toISOString(),data:{tool,detail}}))).map((item,i)=>({...item,seq:i+1}));
  function resolved(answer){
    const notice={type:'notice-resolved',runId:params.get('id'),answer};
    if(parent!==window)parent.postMessage(notice,'*');
    else localStorage.setItem('afm.desktop.reply',JSON.stringify({...notice,time:Date.now()}));
  }
  const json=(value,code=200)=>new Response(JSON.stringify(value),{status:code,headers:{'Content-Type':'application/json'}});
  window.fetch=async(input,options={})=>{
    const url=new URL(typeof input==='string'?input:input.url,'http://desktop.invalid');
    const path=url.pathname;
    if(path==='/api/status')return json(status);
    if(path==='/api/events')return json(events.filter(item=>!url.searchParams.has('stage')||item.stage_id===url.searchParams.get('stage')));
    const match=path.match(/^\/api\/stages\/([^/]+)\/(.+)$/);
    if(match){
      const stage=stages.find(item=>item.id===match[1]);
      if(match[2]==='plan')return new Response(plans[stage.id]||'## '+stage.name);
      if(match[2]==='dialog')return json(asking&&stage?.id==='delivery'?[question]:[]);
      if(options.method==='POST'&&match[2]==='dialog/answer'&&stage?.status==='awaiting_user_input'){
        question.answer=JSON.parse(options.body).answer;stage.status='running';
        resolved(question.answer);return json({});
      }
      if(options.method==='POST'&&stage&&['approve','pause','continue'].includes(match[2])){
        stage.status=match[2]==='pause'?'paused':'running';
        if(match[2]==='approve')resolved();
        return json({});
      }
    }
    return json({},404);
  };
  window.WebSocket=class {
    constructor(){this.timer=setTimeout(()=>this.onopen?.(),10)}
    close(){clearTimeout(this.timer)}
  };
  if(parent!==window){
    let focus=false, ready=false;
    function sync(){
      const next=!!document.querySelector('.dashboard-body--feed-focus');
      if(next!==focus){focus=next;parent.postMessage({type:'stage-focus',active:focus},'*')}
      if(!ready&&document.querySelector('.stage-row')){ready=true;parent.postMessage('stage-ready','*')}
    }
    new MutationObserver(sync).observe(document.getElementById('root'),{attributes:true,attributeFilter:['class'],childList:true,subtree:true});
    window.addEventListener('message',e=>{
      if(e.source!==parent)return;
      if(e.data==='toggle-stage-focus'||(e.data==='exit-stage-focus'&&focus))document.querySelector('.feed-focus-toggle')?.click();
      if(e.data?.type==='open-stage'&&ready){
        const row=document.querySelector('[data-stage-id="'+CSS.escape(e.data.stage)+'"] .stage-row');
        if(!row)return;
        row.click();
        requestAnimationFrame(()=>{
          document.querySelector('[data-tab-id="detail"]')?.click();
          parent.postMessage('stage-opened','*');
        });
      }
    });
  }
  document.addEventListener('keydown',e=>{
    if(e.isComposing||e.repeat||document.querySelector('dialog[open]'))return;
    if(parent!==window&&e.altKey&&!e.ctrlKey&&!e.metaKey&&!e.shiftKey){
      const navigate={KeyH:'home',KeyJ:'next',KeyK:'previous',KeyM:'model',KeyA:'assistant'}[e.code];
      if(navigate){e.preventDefault();parent.postMessage({navigate},'*')}
      return;
    }
    if(parent===window||!(e.metaKey||e.ctrlKey))return;
    const key=e.key.toLowerCase();
    if(key==='k'||(key==='enter'&&e.shiftKey)){e.preventDefault();parent.postMessage(key==='k'?'switch-project':'new-run','*')}
  });
})();
