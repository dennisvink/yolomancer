const statusLine=document.querySelector('#status'),form=document.querySelector('#onboard');
const pageParams=new URLSearchParams(location.search);
window.labTabID=pageParams.get('tab')||'';
let migrate=pageParams.get('migrate')==='1';
let identityKey=pageParams.get('identity')||(!window.labTabID||migrate?'ssh':null),tabLabel;
function announceIdentity(id,label){
  const url=new URL(location.href);if(id)url.searchParams.set('identity',id);else url.searchParams.delete('identity');url.searchParams.delete('migrate');history.replaceState(null,'',url);
  if(parent!==window)parent.postMessage({type:'lab-identity',identityId:id,...(label?{label}:{})},location.origin);
}
let identity,state,fit,pollVersion=0,actionBusy=false,pendingAction,connectionPending=false;
let unloading=false,remotelyClosed=false;
window.addEventListener('pagehide',()=>{unloading=true;});window.addEventListener('pageshow',()=>{unloading=false;});
function markTabClosed(reason){
  if(unloading||actionBusy||remotelyClosed)return;remotelyClosed=true;
  statusLine.textContent='Terminal closed.';
  if(parent!==window)parent.postMessage({type:'lab-tab-closed',reason},location.origin);
}
window.remoteShellClosed=()=>markTabClosed('remote-exit');
const dialog=document.querySelector('#lab-action-dialog'),confirmButton=document.querySelector('#confirm-action'),cancelButton=document.querySelector('#cancel-action');
const deleteButton=document.querySelector('#delete-lab'),reinstallButton=document.querySelector('#reinstall-lab');
const resetButton=document.createElement('button');resetButton.id='reset-lab';resetButton.type='button';resetButton.textContent='Reset';resetButton.hidden=true;document.querySelector('#lab-actions').append(resetButton);
async function copyText(text){try{await navigator.clipboard.writeText(text);}catch{showErr('Clipboard access denied. Use the browser copy shortcut on your selection.');}}
async function pasteClipboard(){try{const text=await navigator.clipboard.readText();if(/[\r\n]/.test(text)&&!window.confirm('Paste multiple lines? Newlines may execute commands in the shell.'))return;term.paste(text);term.focus();}catch{showErr('Clipboard access denied. Focus the terminal and use Ctrl+V / Cmd+V.');}}
const progressPanel=document.createElement('section');
progressPanel.id='lab-progress';progressPanel.hidden=true;
progressPanel.innerHTML='<p id="progress-message" role="status"></p><progress aria-label="Lab setup progress" max="8"></progress><p id="progress-step"></p>';
document.querySelector('#terminal').before(progressPanel);
function showProgress(progress){
  if(!progress)return;
  progressPanel.hidden=false;
  progressPanel.querySelector('#progress-message').textContent=progress.message;
  const bar=progressPanel.querySelector('progress');
  if(progress.step){bar.max=progress.total;bar.value=progress.step;}else bar.removeAttribute('value');
  bar.hidden=Boolean(progress.failed);
  progressPanel.querySelector('#progress-step').textContent=progress.step?`STEP ${progress.step} OF ${progress.total}`:'';
  statusLine.textContent=progress.message;
}
async function identityStore(value,key=identityKey){
  const db=await new Promise((resolve,reject)=>{const r=indexedDB.open('yolomancer-lab',1);r.onupgradeneeded=()=>r.result.createObjectStore('identity');r.onsuccess=()=>resolve(r.result);r.onerror=()=>reject(r.error);});
  try{
    if(value!==undefined){await new Promise((resolve,reject)=>{const t=db.transaction('identity','readwrite');if(value===null){if(key)t.objectStore('identity').delete(key);}else{t.objectStore('identity').put(value,value.id);if(key==='ssh')t.objectStore('identity').delete('ssh');}t.oncomplete=resolve;t.onabort=()=>reject(t.error);});identityKey=value?.id||null;announceIdentity(identityKey,tabLabel);return value;}
    if(!key)return undefined;
    return await new Promise((resolve,reject)=>{const r=db.transaction('identity').objectStore('identity').get(key);r.onsuccess=()=>resolve(r.result);r.onerror=()=>reject(r.error);});
  }finally{db.close();}
}
async function json(url,options={}){const headers=new Headers(options.headers);if(window.labTabID)headers.set('x-lab-tab',window.labTabID);if(migrate)headers.set('x-lab-migrate','1');const response=await fetch(url,{...options,headers});const result=await response.json();if(!response.ok)throw Error(result.error||'Request failed');if(url==='/api/status')migrate=false;return result;}
async function provisionRequest(code){
  const id=Array.from(new Uint8Array(await crypto.subtle.digest('SHA-256',new TextEncoder().encode(code))),b=>b.toString(16).padStart(2,'0')).join('');
  const saved=await identityStore(undefined,id);
  identity=saved||(identity?.id===id?identity:undefined);
  if(!identity?.privateKey)return {code};
  const publicKey=identity.publicKey,timestamp=Math.floor(Date.now()/1000);
  const nonce=Array.from(crypto.getRandomValues(new Uint8Array(16)),b=>b.toString(16).padStart(2,'0')).join('');
  const key=await crypto.subtle.importKey('pkcs8',Uint8Array.from(atob(identity.privateKey),c=>c.charCodeAt(0)),{name:'Ed25519'},false,['sign']);
  const signature=btoa(String.fromCharCode(...new Uint8Array(await crypto.subtle.sign('Ed25519',key,new TextEncoder().encode(JSON.stringify(['yolomancer-lab-v1',code,publicKey,timestamp,nonce]))))));
  return {code,publicKey,timestamp,nonce,signature};
}
window.showErr=message=>{statusLine.textContent=message;};
window.showReconnect=()=>{if(!actionBusy&&!remotelyClosed&&!unloading)statusLine.textContent='Connection closed. Reload to reconnect.';};
window.showServerKey=fingerprint=>{const matches=fingerprint===state.hostFingerprint;if(!matches)showErr('SSH host key mismatch. Connection refused.');setTimeout(()=>acceptFingerprint(matches),0);};
window.connected=()=>{connectionPending=false;renderActions();progressPanel.hidden=true;statusLine.textContent='CONNECTED';term.focus();};
function connect(){connectionPending=true;renderActions();form.hidden=true;document.querySelector('#terminal').style.display='block';fit.fit();const pem='-----BEGIN PRIVATE KEY-----\n'+identity.privateKey.match(/.{1,64}/g).join('\n')+'\n-----END PRIVATE KEY-----\n';initConnection(term.rows,term.cols,state.id,22,'participant','',pem,false,false,false);}
async function poll(){const version=++pollVersion;for(;;){const result=await json('/api/status');if(version!==pollVersion)return;state=result;renderActions();
  connectionPending=state.status==='READY';renderActions();
  showProgress(state.progress);
  if(state.operation==='reinstall'){pendingAction={operation:'reinstall',generation:state.generation};openAction('reinstall');await performAction();return;}
  if(state.operation==='delete'){showProgress({message:'Lab deleted. Enter the same code to create a new lab with your saved SSH identity.',failed:true});form.hidden=false;form.querySelector('button').disabled=false;document.querySelector('#terminal').style.display='none';connectionPending=true;renderActions();return;}
  if(state.status==='READY'){showProgress({step:8,total:8,message:'Connecting to your lab'});connect();return;}
  if(state.status==='FAILED'){form.hidden=false;form.querySelector('button').disabled=false;return;}
  if(state.status==='STOPPED'){markTabClosed('workspace-stopped');showProgress({message:'Workspace stopped. Reinstall or enter the same code to reuse your saved SSH identity.',failed:true});form.hidden=false;form.querySelector('button').disabled=false;return;}
  form.hidden=true;await new Promise(r=>setTimeout(r,2000));
}}
function renderActions(){const provisioning=state?.status==='PROVISIONING'||state?.status==='STOPPING';for(const button of [deleteButton,reinstallButton]){button.hidden=connectionPending||!state?.generation;button.disabled=actionBusy||provisioning;}resetButton.hidden=!connectionPending;resetButton.disabled=actionBusy;}
function openAction(operation){
  if((operation!=='reset'&&!state?.generation)||actionBusy)return;
  if(operation!=='reset'&&(state?.status==='PROVISIONING'||state?.status==='STOPPING'))return;
  pendingAction={operation,generation:state?.generation};
  document.querySelector('#action-title').textContent=operation==='reset'?'Reset credentials?':operation==='delete'?'Delete Lab?':'Reinstall Lab?';
  document.querySelector('#action-warning').textContent=operation==='reset'?'This removes the previously registered SSH identity and saved lab session from this browser. You will need your access code to connect again. Your running lab and its AWS credentials will not be deleted.':operation==='delete'?'All files and changes in this lab will be permanently lost. This cannot be undone.':'All files and changes in this lab will be permanently lost. A new lab will be provisioned using your existing access code. This cannot be undone.';
  document.querySelector('#action-error').textContent='';
  confirmButton.textContent=operation==='reset'?'Reset':operation==='delete'?'Delete Lab':'Reinstall';
  if(!dialog.open)dialog.showModal();cancelButton.focus();
}
async function performAction(){
  if(actionBusy||!pendingAction)return;
  const {operation,generation}=pendingAction;actionBusy=true;++pollVersion;
  confirmButton.disabled=true;cancelButton.disabled=true;renderActions();
  statusLine.textContent=operation==='delete'?'DELETING LAB':'REINSTALLING LAB';
  dialog.close();form.hidden=true;document.querySelector('#terminal').style.display='none';
  showProgress({message:operation==='reset'?'Resetting browser credentials':operation==='delete'?'Stopping lab':'Stopping old lab before reinstalling',indeterminate:true});
  try{
    let result;
    do{result=await json('/api/'+operation,{method:'POST',headers:{'content-type':'application/json'},body:JSON.stringify({generation,confirm:true})});if(result.pending)await new Promise(r=>setTimeout(r,2000));}while(result.pending);
    if(operation==='reset'){await identityStore(null);location.reload();return;}
    if(operation==='delete'){location.reload();return;}
    identity=await identityStore(result.identity);
    // Reset the old WASM SSH connection. Startup immediately resumes progress
    // from the replacement's persisted status using the saved session.
    location.reload();
  }catch(error){document.querySelector('#action-error').textContent=error.message;actionBusy=false;confirmButton.disabled=false;cancelButton.disabled=false;renderActions();if(!dialog.open)dialog.showModal();}
}
deleteButton.addEventListener('click',()=>openAction('delete'));
resetButton.addEventListener('click',()=>openAction('reset'));
reinstallButton.addEventListener('click',()=>openAction('reinstall'));
cancelButton.addEventListener('click',()=>{if(!actionBusy)dialog.close();});
confirmButton.addEventListener('click',performAction);
dialog.addEventListener('cancel',event=>{if(actionBusy)event.preventDefault();});
form.addEventListener('submit',async event=>{event.preventDefault();const button=form.querySelector('button');button.disabled=true;showProgress({message:'Validating access code and preparing your lab',indeterminate:true});try{const code=form.querySelector('input').value.trim().toUpperCase();const result=await json('/api/provision',{method:'POST',headers:{'content-type':'application/json'},body:JSON.stringify(await provisionRequest(code))});tabLabel='Lab ·'+code.slice(-4);identity=await identityStore(result.identity);migrate=false;form.reset();await poll();}catch(error){progressPanel.hidden=true;form.hidden=false;showErr(error.message);button.disabled=false;}});
form.querySelector('input').addEventListener('input',event=>{event.target.value=event.target.value.toUpperCase();});
form.querySelector('button').disabled=true;
(async()=>{try{
  identity=await identityStore();
  if(identity&&identityKey==='ssh')identity=await identityStore(identity);
  if(identity?.id){connectionPending=true;renderActions();showProgress({message:'Connecting to your lab',indeterminate:true});}
  else form.hidden=false;
  await document.fonts.load("16px C64");
  window.term=new Terminal({cursorBlink:true,macOptionClickForcesSelection:true,fontFamily:"C64, monospace",fontSize:16,theme:{background:'#40318d',foreground:'#7869c4'}});
  term.attachCustomKeyEventHandler(event=>{
    if(event.type!=='keydown')return true;
    if((event.ctrlKey&&event.shiftKey||event.metaKey)&&event.key.toLowerCase()==='c'&&term.hasSelection()){event.preventDefault();copyText(term.getSelection());return false;}
    if((event.ctrlKey&&event.shiftKey||event.metaKey)&&event.key.toLowerCase()==='v'){event.preventDefault();pasteClipboard();return false;}
    return true;
  });
  fit=new FitAddon.FitAddon();term.loadAddon(fit);term.open(document.querySelector('#terminal'));
  const resize=()=>{if(document.querySelector('#terminal').clientWidth){fit.fit();if(window.changeWindowSize)changeWindowSize(term.rows,term.cols);}};
  window.addEventListener('resize',resize);
  window.addEventListener('message',event=>{if(event.origin===location.origin&&event.source===parent&&event.data?.type==='lab-tab-focus'){resize();if(state?.status==='READY')term.focus();else if(!form.hidden)form.querySelector('input').focus();}});
  const go=new Go();const wasm=await WebAssembly.instantiateStreaming(fetch('/main.wasm'),go.importObject);go.run(wasm.instance);
  while(!window.initConnection)await new Promise(r=>setTimeout(r,30));
  form.querySelector('button').disabled=false;
  if(identity?.id){
    try{state=await json('/api/status');}catch{state=null;}
    if(state?.id===identity.id){try{await poll();}catch(error){showErr(error.message);}return;}
    showProgress({message:'Lab unavailable. Enter the same access code to reuse your saved SSH identity.',failed:true});form.hidden=false;return;
  }
  statusLine.textContent='READY.';
}catch(error){showErr(error.message);form.querySelector('button').disabled=true;}})();
