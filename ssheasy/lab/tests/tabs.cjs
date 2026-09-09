// Real browser, real xterm, mocked SSH and API. No AWS writes or lab creation.
const {chromium}=require('playwright'),{execFileSync}=require('node:child_process');
const {generateKeyPairSync,createHash}=require('node:crypto');
const path=require('node:path'),fs=require('node:fs'),assert=require('node:assert/strict');
(async()=>{
 const ip=execFileSync('dig',['+short','lab.yolomancer.com','A','@1.1.1.1'],{encoding:'utf8'}).trim().split('\n').find(x=>/^\d+\.\d+\.\d+\.\d+$/.test(x));
 const browser=await chromium.launch({args:ip?[`--host-resolver-rules=MAP lab.yolomancer.com ${ip}`]:[]});
 try{
  const context=await browser.newContext({permissions:['clipboard-read','clipboard-write']}),sessions=new Map(),identities=new Map();
  const {authenticate}=await import('../gateway/auth.mjs');
  const page=await context.newPage();
  await context.route('https://lab.yolomancer.com/**',async route=>{
   const req=route.request(),url=new URL(req.url()),name=url.pathname==='/'?'index.html':url.pathname.slice(1);
   if(url.pathname.startsWith('/api/')){
    const tab=req.headers()['x-lab-tab'];assert(tab);let result,status=200;
    if(url.pathname==='/api/provision'){
     const body=req.postDataJSON(),id=createHash('sha256').update(body.code).digest('hex');
     if(body.publicKey){authenticate(body);assert.equal(body.publicKey,identities.get(id).publicKey);}
     if(!identities.has(id)){const pair=generateKeyPairSync('ed25519');identities.set(id,{id,publicKey:pair.publicKey.export({format:'der',type:'spki'}).subarray(-32).toString('base64'),privateKey:pair.privateKey.export({format:'der',type:'pkcs8'}).toString('base64')});}
     sessions.set(tab,{id,generation:'generation-'+id,status:'READY',hostFingerprint:'test',progress:{step:8,total:8,message:'Connecting'}});result={id,identity:identities.get(id)};
    }else if(url.pathname==='/api/duplicate'){const source=sessions.get(tab);assert(source);const target=req.postDataJSON().tab;assert(!sessions.has(target));sessions.set(target,{...source});result={id:source.id};}
    else if(url.pathname==='/api/reset'){sessions.delete(tab);result={ok:true};}
    else{result=sessions.get(tab)||{error:'No session'};if(!sessions.has(tab))status=403;}
    return route.fulfill({status,contentType:'application/json',body:JSON.stringify(result)});
   }
   if(name==='wasm_exec.js')return route.fulfill({contentType:'text/javascript',body:'window.Go=class {constructor(){this.importObject={};}run(){window.initConnection=()=>{window.term.write("MOCK TERMINAL\\r\\n");window.term.onData(t=>{window.testInput=(window.testInput||"")+t;});window.connected();};}};'});
   if(name==='main.wasm')return route.fulfill({contentType:'application/wasm',body:Buffer.from([0,97,115,109,1,0,0,0])});
   const file=path.resolve(__dirname,'../public',name);
   if(fs.existsSync(file)&&fs.statSync(file).isFile())return route.fulfill({path:file,contentType:name.endsWith('.js')?'text/javascript':name.endsWith('.css')?'text/css':name.endsWith('.html')?'text/html':'application/octet-stream'});
   return route.continue();
  });
  await page.goto('https://lab.yolomancer.com');
  const current=async()=>{const handle=await page.locator('iframe:not([hidden])').elementHandle();return handle.contentFrame();};
  let a=await current();await a.locator('#code').fill('ABCDEF0123');await a.locator('#onboard button').click();await a.waitForFunction(()=>document.querySelector('#status')?.textContent==='CONNECTED');
  const first=await a.evaluate(()=>({id:identity.id,key:identity.privateKey,tab:labTabID}));
  await a.evaluate(()=>{window.marker='preserved';});
  await page.locator('#new-tab').click();let b=await current();await b.waitForFunction(()=>document.querySelector('#status')?.textContent==='READY.');
  assert.equal(await b.evaluate(()=>Boolean(identity)),false);assert.equal(await b.locator('#code').inputValue(),'');
  await b.locator('#code').fill('ABCDEF0124');await b.locator('#onboard button').click();await b.waitForFunction(()=>document.querySelector('#status')?.textContent==='CONNECTED');
  const second=await b.evaluate(()=>({id:identity.id,key:identity.privateKey,tab:labTabID}));assert.notEqual(first.id,second.id);assert.notEqual(first.key,second.key);assert.notEqual(first.tab,second.tab);
  await page.getByRole('tab').first().click();assert.equal(await a.evaluate(()=>window.marker),'preserved');assert.equal(await a.evaluate(()=>identity.id),first.id);
  await a.evaluate(()=>new Promise(r=>term.write('CLI OUTPUT SELECTABLE',r)));
  assert.equal(await a.locator('#copy-output, #paste-input, #copy-dialog').count(),0);
  await a.evaluate(()=>{term.select(0,1,21);term.focus();});
  await a.locator('.xterm-helper-textarea').press('Control+Shift+C');assert.match(await a.evaluate(()=>navigator.clipboard.readText()),/CLI OUTPUT SELECTABLE/);
  await a.evaluate(()=>navigator.clipboard.writeText('paste into first tab'));
  await a.locator('.xterm-helper-textarea').press('Control+Shift+V');await a.waitForFunction(()=>window.testInput==='paste into first tab');assert.equal(await b.evaluate(()=>window.testInput||''),'');
  await page.screenshot({path:'/tmp/yolomancer-tabs.png'});
  await page.reload();a=await current();await a.waitForFunction(()=>document.querySelector('#status')?.textContent==='CONNECTED');assert.equal(await a.evaluate(()=>identity.privateKey),first.key);
  await page.getByRole('tab').nth(1).click();b=await current();await b.waitForFunction(()=>document.querySelector('#status')?.textContent==='CONNECTED');assert.equal(await b.evaluate(()=>identity.privateKey),second.key);
  await page.getByRole('tab').first().click({button:'right'});await page.getByRole('menuitem',{name:'Duplicate',exact:true}).click();
  await page.waitForFunction(()=>document.querySelectorAll('[role=tab]').length===3);
  await page.frameLocator('iframe:not([hidden])').locator('#status').filter({hasText:'CONNECTED'}).waitFor();
  const duplicate=await current();await duplicate.waitForFunction(()=>document.querySelector('#status')?.textContent==='CONNECTED');
  assert.equal(await duplicate.evaluate(()=>identity.privateKey),first.key);
  const duplicateTab=await duplicate.evaluate(()=>labTabID),order=await page.evaluate(()=>JSON.parse(localStorage.getItem('yolomancer-tabs')).tabs.map(x=>x.id));
  assert.deepEqual(order,[first.tab,duplicateTab,second.tab]);assert.notEqual(duplicateTab,first.tab);
  await page.locator('.close-tab').nth(1).click();assert.equal(await page.getByRole('tab').count(),2);
  console.log('PASS right-click Duplicate reuses identity/session and inserts immediately after its source');
  await page.locator('#new-tab').click();const c=await current();await c.locator('#code').fill('ABCDEF0123');await c.locator('#onboard button').click();await c.waitForFunction(()=>document.querySelector('#status')?.textContent==='CONNECTED');assert.equal(await c.evaluate(()=>identity.privateKey),first.key);
  await c.evaluate(()=>{connectionPending=true;renderActions();});await c.locator('#reset-lab').click();await c.locator('#confirm-action').click();await c.waitForFunction(()=>document.querySelector('#status')?.textContent==='READY.');
  assert.equal(await b.evaluate(async()=>(await identityStore()).privateKey),second.key);
  assert.equal(sessions.has(second.tab),true);
  const layout=await page.evaluate(()=>({plus:document.querySelector('#new-tab').getBoundingClientRect().left,last:[...document.querySelectorAll('.tab')].at(-1).getBoundingClientRect().right,close:document.querySelector('.close-tab').getBoundingClientRect().left,label:document.querySelector('[role=tab]').getBoundingClientRect().left}));
  assert.equal(Math.round(layout.plus-layout.last),6);assert(layout.close<layout.label);
  await b.evaluate(()=>showReconnect());assert.equal(await page.getByRole('tab').count(),3);
  await a.evaluate(()=>{window.dispatchEvent(new Event('pagehide'));remoteShellClosed();window.dispatchEvent(new Event('pageshow'));});
  assert.equal(await page.getByRole('tab').count(),3);
  await a.evaluate(()=>remoteShellClosed());await page.waitForFunction(()=>document.querySelectorAll('[role=tab]').length===2);
  await page.locator('.close-tab').last().click();assert.equal(await page.getByRole('tab').count(),1);
  await page.reload();b=await current();await b.waitForFunction(()=>document.querySelector('#status')?.textContent==='CONNECTED');
  assert.equal(await page.getByRole('tab').count(),1);assert.equal(await b.evaluate(()=>identity.id),second.id);
  const history=await page.evaluate(()=>JSON.parse(localStorage.getItem('yolomancer-tabs')));
  assert(history.closed.some(x=>x.id===first.tab&&x.reason==='remote-exit'&&x.open===false));assert(history.closed.some(x=>x.reason==='user'));
  console.log('PASS remote/manual close persists, refresh restores only open tabs, disconnect/unload preserves tabs, and tab button layout');
  console.log('PASS isolated tabs, blank new tab, hashed-code key reuse, switching, reload, native clipboard shortcuts without buttons, and per-code Reset');
 }finally{await browser.close();}
})().catch(e=>{console.error(e.stack);process.exitCode=1;});
