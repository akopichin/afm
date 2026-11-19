// Keep project and run objects stable on idle polls so drafts, focus and the active browser survive.
function reconcileInventory(projects, rows, makeId) {
  const byPath=new Map(projects.map(p=>[p.path,p]));
  const next=new Map();
  const changed=new Set();
  let added=false;
  for(const row of rows){
    if(!row.project_root||!row.run_id||!row.instance_id||!['attached','managed'].includes(row.ownership)
       ||!['active','active_orphaned'].includes(row.liveness))continue;
    let p=byPath.get(row.project_root);
    if(!p){
      p={id:makeId(),name:row.project_root.split(/[\\/]/).filter(Boolean).at(-1)||row.project_root,
        path:row.project_root,tone:'var(--violet)',git:false,runs:[]};
      byPath.set(p.path,p);projects.push(p);added=true;
    }
    const list=next.get(p)||[];
    const i=list.length,prev=p.runs[i];
    const state=row.run_status==='running'?'running':'stopped';
    const source=row.ownership==='managed'?'Desktop':'Terminal';
    const flow=row.run_name||row.run_id;
    if(prev&&prev.rawId===row.run_id&&prev.instance===row.instance_id&&prev.mode===row.mode&&prev.flow===flow&&prev.state===state&&prev.source===source){
      list.push(prev);
    }else{
      changed.add(p.id);
      list.push({id:encodeURIComponent(JSON.stringify([row.instance_id,row.run_id])).replaceAll("'","%27"),
        instance:row.instance_id,rawId:row.run_id,flow,source,mode:row.mode,state});
    }
    next.set(p,list);
  }
  for(const p of projects){
    const list=next.get(p)||[];
    if(list.length!==p.runs.length)changed.add(p.id);
    if(changed.has(p.id))p.runs=list;
  }
  return {changed,added};
}
