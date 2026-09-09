const bar=document.querySelector('#tabs'),panels=document.querySelector('#panels'),entries=new Map();
const menu=document.createElement('div');menu.id='tab-menu';menu.role='menu';menu.hidden=true;
const duplicateButton=document.createElement('button');duplicateButton.type='button';duplicateButton.role='menuitem';duplicateButton.textContent='Duplicate';menu.append(duplicateButton);
const menuStatus=document.createElement('div');menuStatus.id='tab-menu-status';menuStatus.role='status';menu.append(menuStatus);document.body.append(menu);
let menuTarget,duplicating=false;
function hideMenu(){menu.hidden=true;}
function showMenu(event,id){
  event.preventDefault();menuTarget=id;menuStatus.textContent='';duplicateButton.disabled=duplicating;menu.hidden=false;
  const rect=entries.get(id).button.getBoundingClientRect();
  menu.style.left=Math.max(0,Math.min(event.clientX||rect.left,innerWidth-menu.offsetWidth))+'px';
  menu.style.top=Math.max(0,Math.min(event.clientY||rect.bottom,innerHeight-menu.offsetHeight))+'px';duplicateButton.focus();
}
document.addEventListener('pointerdown',event=>{if(!menu.contains(event.target))hideMenu();});
document.addEventListener('keydown',event=>{if(event.key==='Escape'&&!menu.hidden){hideMenu();entries.get(menuTarget)?.button.focus();}});
window.addEventListener('blur',hideMenu);window.addEventListener('resize',hideMenu);document.querySelector('#tab-strip').addEventListener('scroll',hideMenu);
duplicateButton.addEventListener('click',async()=>{
  const source=entries.get(menuTarget);if(!source||duplicating)return;
  duplicating=true;duplicateButton.disabled=true;menuStatus.textContent='Opening…';
  try{
    const id='t-'+crypto.randomUUID();let identityId=source.identityId;
    if(identityId){
      const response=await fetch('/api/duplicate',{method:'POST',headers:{'content-type':'application/json','x-lab-tab':source.id},body:JSON.stringify({tab:id})});
      const result=await response.json();if(!response.ok)throw Error(result.error||'Unable to duplicate this tab.');identityId=result.id;
    }
    if(entries.has(source.id)){select(add({id,identityId,label:source.label},source.id));hideMenu();}
  }catch(error){menuStatus.textContent=error.message;}
  finally{duplicating=false;duplicateButton.disabled=false;}
});
let saved;try{saved=JSON.parse(localStorage.getItem('yolomancer-tabs')||'null');}catch{}
let active=saved?.active;
const validId=id=>typeof id==='string'&&/^t-[a-f0-9-]{36}$/.test(id);
const closed=new Map((Array.isArray(saved?.closed)?saved.closed:[]).filter(x=>validId(x?.id)).map(x=>[x.id,x]));
let unloading=false;window.addEventListener('pagehide',()=>{unloading=true;});window.addEventListener('pageshow',()=>{unloading=false;});
function persist(){localStorage.setItem('yolomancer-tabs',JSON.stringify({version:2,active,tabs:[...entries.values()].map(({id,identityId,label,migrate})=>({id,identityId,label,migrate,open:true})),closed:[...closed.values()]}));}
function closeTab(id,reason){
  if(unloading)return;const entry=entries.get(id);if(!entry)return;
  const ids=[...entries.keys()],index=ids.indexOf(id);
  closed.set(id,{id,identityId:entry.identityId,label:entry.label,open:false,reason,closedAt:Date.now()});
  entries.delete(id);persist();entry.frame.remove();entry.wrap.remove();
  if(!entries.size)add();else if(active===id)select(ids[index+1]||ids[index-1]);persist();
}
function select(id){
  active=id;
  for(const entry of entries.values()){
    const selected=entry.id===id;entry.frame.hidden=!selected;entry.button.setAttribute('aria-selected',String(selected));entry.button.tabIndex=selected?0:-1;
    if(selected)entry.frame.contentWindow.postMessage({type:'lab-tab-focus'},location.origin);
  }
  persist();
}
function add(data={},afterId){
  const id=validId(data.id)?data.id:'t-'+crypto.randomUUID();if(entries.has(id))return;
  const identityId=/^[a-f0-9]{64}$/.test(data.identityId||'')?data.identityId:null;
  const label=typeof data.label==='string'?data.label.slice(0,32):'New lab';
  const wrap=document.createElement('div');wrap.className='tab';
  const button=document.createElement('button');button.type='button';button.role='tab';button.textContent=label;button.id='button-'+id;button.setAttribute('aria-controls','panel-'+id);
  const close=document.createElement('button');close.type='button';close.className='close-tab';close.textContent='×';close.title='Close tab (lab keeps running)';close.setAttribute('aria-label','Close '+label);
  const frame=document.createElement('iframe');frame.id='panel-'+id;frame.title=label;frame.setAttribute('role','tabpanel');frame.setAttribute('aria-labelledby',button.id);frame.hidden=true;
  const query=new URLSearchParams({tab:id});if(identityId)query.set('identity',identityId);if(data.migrate)query.set('migrate','1');
  frame.allow='clipboard-read; clipboard-write';frame.src='/terminal.html?'+query;
  const entry={id,identityId,label,migrate:Boolean(data.migrate),button,frame,wrap};entries.set(id,entry);
  if(afterId&&entries.has(afterId)){
    const ordered=[...entries].filter(([key])=>key!==id),index=ordered.findIndex(([key])=>key===afterId);ordered.splice(index+1,0,[id,entry]);entries.clear();for(const [key,value] of ordered)entries.set(key,value);
  }
  button.addEventListener('click',()=>select(id));
  button.addEventListener('keydown',event=>{const ids=[...entries.keys()],index=ids.indexOf(id);let next;if(event.key==='ArrowRight')next=ids[(index+1)%ids.length];if(event.key==='ArrowLeft')next=ids[(index+ids.length-1)%ids.length];if(next){event.preventDefault();select(next);entries.get(next).button.focus();}});
  close.addEventListener('click',()=>closeTab(id,'user'));
  wrap.addEventListener('contextmenu',event=>showMenu(event,id));
  wrap.append(close,button);const preceding=entries.get(afterId);if(preceding){preceding.wrap.after(wrap);preceding.frame.after(frame);}else{bar.append(wrap);panels.append(frame);}
  if(!active||!entries.has(active))select(id);else select(active);
  persist();return id;
}
window.addEventListener('message',event=>{
  if(event.origin!==location.origin)return;
  const entry=[...entries.values()].find(e=>e.frame.contentWindow===event.source);if(!entry)return;
  if(event.data?.type==='lab-tab-closed'&&['remote-exit','workspace-stopped'].includes(event.data.reason)){closeTab(entry.id,event.data.reason);return;}
  if(event.data?.type==='lab-identity'){
    const id=event.data.identityId;if(id!==null&&!/^[a-f0-9]{64}$/.test(id||''))return;
    entry.identityId=id;entry.migrate=false;
    entry.label=id?(typeof event.data.label==='string'?event.data.label.slice(0,32):entry.label==='New lab'?'Saved lab':entry.label):'New lab';
    entry.button.textContent=entry.label;entry.frame.title=entry.label;persist();
  }
});
document.querySelector('#new-tab').addEventListener('click',()=>select(add()));
const restore=Array.isArray(saved?.tabs)?saved.tabs.filter(x=>validId(x?.id)&&x.open!==false&&!closed.has(x.id)):[];
for(const tab of restore)add(tab);
if(!entries.size)add({migrate:!saved});
if(saved?.active&&entries.has(saved.active))select(saved.active);
