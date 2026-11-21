'use strict';
const skippedDirectories=new Set(['.git','.worktrees','node_modules','vendor','dist','build','runs','cache']);
const basename=path=>path.split(/[\\/]/).filter(Boolean).at(-1)||path;
const yamlFile=path=>/\.ya?ml$/i.test(path);
async function collectFiles(handle, prefix='', files=new Map()){
  for await(const [name,entry] of handle.entries()){
    const path=prefix+name;
    if(entry.kind==='directory'){
      if(!skippedDirectories.has(name))await collectFiles(entry,path+'/',files);
    }else if(yamlFile(name))files.set(path,async()=>(await entry.getFile()).text());
  }
  return files;
}
function inputFiles(list){
  const files=new Map();
  for(const file of list){
    const parts=file.webkitRelativePath.split('/').slice(1);
    if(yamlFile(file.name)&&!parts.some(p=>skippedDirectories.has(p)))files.set(parts.join('/'),()=>file.text());
  }
  return files;
}
// Only the manifest fields displayed in the preview are read here.
function scalar(text,key){
  const value=text.match(new RegExp('^'+key+':\\s*([^\\r\\n#]+)','m'))?.[1]?.trim();
  return value?.replace(/^(['"])(.*)\1$/,'$2');
}
function gitMode(text){
  // A branch-only declaration is a Desktop prototype extension to the AFM flow format.
  const block=text.match(/^git:[^\S\n]*(?:#.*)?\r?\n((?:[ \t]+[^\n]*\n?)+)/m)?.[1];
  const mode=block?.match(/^[ \t]+mode:[ \t]*(branch|worktree|current)[ \t]*(?:#.*)?$/m)?.[1];
  if(mode)return mode;
  const worktrees=scalar(text,'worktrees');
  if(worktrees==='true')return 'worktree';
  if(worktrees==='false')return 'current';
  return undefined;
}
async function catalog(files,includeFlow=()=>true){
  const items=[], entries=new Set();
  for(const [path,load] of files){
    if(!/(^|\/)pipeline\.ya?ml$/i.test(path))continue;
    const text=await load(), root=path.slice(0,path.lastIndexOf('/')+1);
    const entry=scalar(text,'entrypoint');
    if(!entry)continue;
    const entryPath=root+entry.replace(/^\.\//,'');
    entries.add(entryPath);
    const flow=files.get(entryPath);
    items.push({kind:'Pipeline',path,name:scalar(text,'name')||basename(root)||'pipeline',git:flow?gitMode(await flow()):undefined});
  }
  for(const [path,load] of files){
    if(!includeFlow(path)||entries.has(path)||!yamlFile(path)||/(^|\/)pipeline\.ya?ml$/i.test(path))continue;
    const text=await load();
    if(!/^stages:\s*(?:#.*)?$/m.test(text)&&!/^stages:\s*\[/m.test(text))continue;
    items.push({kind:'Flow',path,name:scalar(text,'name')||basename(path).replace(/\.ya?ml$/i,''),git:gitMode(text)});
  }
  return items.sort((a,b)=>a.name.localeCompare(b.name));
}
async function gitHubSource(input){
  const match=input.trim().replace(/\/$/,'').match(/^(?:https:\/\/github\.com\/)?([\w.-]+)\/([\w.-]+)$/);
  if(!match)throw Error('Enter a public GitHub repository as owner/repository.');
  const repo=match[1]+'/'+match[2].replace(/\.git$/,'');
  async function get(url,headers={}){
    const res=await fetch(url,{credentials:'omit',headers,signal:AbortSignal.timeout(20000)});
    if(!res.ok)throw Error('GitHub returned '+res.status+'. Check the repository or import a local folder.');
    return res;
  }
  const api='https://api.github.com/repos/'+repo;
  const tree=await (await get(api+'/git/trees/HEAD?recursive=1')).json();
  if(tree.truncated)throw Error('Repository tree is too large. Import a local workflow folder instead.');
  const files=new Map();
  const isWorkflowPath=path=>/(^|\/)(flow|pipeline)\.ya?ml$|(^|\/)flows\/[^/]+\.ya?ml$/i.test(path);
  for(const node of tree.tree){
    if(node.type!=='blob'||!yamlFile(node.path)||node.path.split('/').some(p=>skippedDirectories.has(p)))continue;
    files.set(node.path,()=>get(api+'/git/blobs/'+node.sha,{Accept:'application/vnd.github.raw+json'}).then(r=>r.text()));
  }
  if([...files.keys()].filter(isWorkflowPath).length>120)throw Error('Use a local workflow folder for libraries with more than 120 manifests.');
  return {id:uid(),name:repo,items:await catalog(files,isWorkflowPath)};
}
async function projectFromFolder(handle){
  let git=false,flows=[];
  try{await handle.getDirectoryHandle('.git');git=true}catch{try{await handle.getFileHandle('.git');git=true}catch{}}
  try{
    const afm=await handle.getDirectoryHandle('.afm');
    const dir=await afm.getDirectoryHandle('flows');
    flows=await catalog(await collectFiles(dir));
  }catch(error){if(error.name!=='NotFoundError')throw error}
  addProject(handle.name,git,flows);
}
function addProject(path,git,flows=[]){
  const existing=projects.find(p=>p.path===path&&(/[\\/]/.test(path)));
  const project=existing||{id:uid(),name:basename(path),path,git,tone:'var(--violet)',runs:[],flows};
  if(!existing)projects.push(project);
  save();$('open-project').close();openProject(project);
}

let libraryLink=null, installingLibraryLink=false;
function showLibraryLink(link){
  if(!link||libraryLink?.id===link.id)return;
  libraryLink=link;
  openSettings(false,'library');
}
function libraryLinkHTML(){
  if(!libraryLink)return '';
  const {repository,subdir,ref,error}=libraryLink;
  return `<section class="library-link" aria-labelledby="library-link-title">
    <h3 id="library-link-title">Add pipeline to Library</h3>
    ${repository?`<dl><dt>Repository</dt><dd>${esc(repository)}</dd>${subdir?`<dt>Directory</dt><dd>${esc(subdir)}</dd>`:''}
      <dt>Git ref</dt><dd>${esc(ref||'Default branch')}</dd></dl>`:''}
    ${error?`<p class="hint error" role="alert">${esc(error)}</p>`:''}
    <div class="actions"><button type="button" id="library-link-dismiss"${installingLibraryLink?' disabled':''}>${repository?'Cancel':'Dismiss'}</button>
    ${repository?`<button class="primary" type="button" id="library-link-confirm"${installingLibraryLink?' disabled':''}>${installingLibraryLink?'Reading repository…':error?'Retry':'Add to Library'}</button>`:''}</div>
  </section>`;
}
async function dismissLibraryLink(){
  const link=libraryLink;
  if(!link||installingLibraryLink)return;
  try{
    await nativeAFM.dismissLibraryLink(link.id);
    if(libraryLink===link){libraryLink=null;drawSettings()}
  }catch(error){showNativeError(error)}
}
async function confirmLibraryLink(){
  const link=libraryLink;
  if(!link?.repository||installingLibraryLink)return;
  installingLibraryLink=true;drawSettings();
  try{
    const source=await nativeAFM.confirmLibraryLink(link.id);
    if(libraryLink===link)libraryLink=source.error?{...link,error:source.error}:null;
    if(!source.error)sourceMessage=`${source.entries.length} workflows available in New run.`;
    await loadSettingsData();
  }catch(error){
    if(libraryLink===link)libraryLink={...link,error:error.message};
    else showNativeError(error);
  }finally{
    installingLibraryLink=false;
    if($('settings').open&&settingsTab==='library')drawSettings();
  }
}
