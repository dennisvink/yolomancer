// Two synthetic access codes and disposable Fargate tasks; never participant labs.
const {chromium}=require('playwright'),{execFileSync}=require('node:child_process');
const {randomInt,createHash}=require('node:crypto'),assert=require('node:assert/strict');
const table='yolomancer-dispenser',S=x=>({S:x}),alphabet='ABCDEF012345678',created=[];
function aws(args,profile='default'){const r=execFileSync('aws',['--profile',profile,'--region','eu-west-1',...args,'--output','json'],{encoding:'utf8'});return r.trim()?JSON.parse(r):null;}
const get=id=>aws(['dynamodb','get-item','--table-name',table,'--key',JSON.stringify({pk:S('lab#'+id)}),'--consistent-read'])?.Item;
let browser;
(async()=>{try{
 const epoch=Math.floor(Date.now()/1000);
 for(let i=0;i<2;i++){
  const code=Array.from({length:10},()=>alphabet[randomInt(alphabet.length)]).join(''),id=createHash('sha256').update(code).digest('hex');
  aws(['dynamodb','put-item','--table-name',table,'--condition-expression','attribute_not_exists(pk)','--item',JSON.stringify({pk:S('code#'+id),lab_test:{BOOL:true},expires_at:{N:String(epoch+1800)},ttl:{N:String(epoch+3600)},account_user:S('prosus-user-100'),credentials:S(JSON.stringify({aws_access_key_id:'AKIA'+'A'.repeat(16),aws_secret_access_key:'x'.repeat(40),aws_region:'eu-west-1'}))})]);created.push({code,id});
 }
 const ip=execFileSync('dig',['+short','lab.yolomancer.com','A','@1.1.1.1'],{encoding:'utf8'}).trim().split('\n').find(x=>/^\d+\.\d+\.\d+\.\d+$/.test(x));
 browser=await chromium.launch({args:ip?[`--host-resolver-rules=MAP lab.yolomancer.com ${ip}`]:[]});const page=await browser.newPage();
 const current=async()=>{const h=await page.locator('iframe:not([hidden])').elementHandle();return h.contentFrame();};
 await page.goto('https://lab.yolomancer.com');let first=await current();
 await first.locator('#code').fill(created[0].code);await first.locator('#onboard button').click();
 await first.waitForFunction(()=>Boolean(state?.generation),null,{timeout:90000});
 await page.locator('#new-tab').click();let second=await current();
 await second.locator('#code').fill(created[1].code);await second.locator('#onboard button').click();
 await second.waitForFunction(()=>Boolean(state?.generation),null,{timeout:90000});
 const keys=[];
 for(const [index,frame] of [first,second].entries()){
  await frame.waitForFunction(()=>document.querySelector('#status')?.textContent==='CONNECTED',null,{timeout:1200000});
  assert.equal(await frame.evaluate(()=>state.id),created[index].id);
  assert.equal(await frame.evaluate(async()=>(await json('/api/status')).id),created[index].id);
  keys.push(await frame.evaluate(()=>identity.privateKey));
 }
 assert.notEqual(keys[0],keys[1]);assert.notEqual(get(created[0].id).task_arn.S,get(created[1].id).task_arn.S);
 console.log('PASS two codes create independent tabs, cookies, SSH identities and Fargate connections');
 console.log('Checking first tab shell input…');
 await page.getByRole('tab').first().click();await first.evaluate(()=>term.paste('export LAB_TAB_TEST=FIRST; echo TAB_""FIRST'));await first.locator('.xterm-helper-textarea').press('Enter');
  await first.waitForFunction(()=>{const b=term.buffer.active;return Array.from({length:b.length},(_,i)=>b.getLine(i).translateToString()).some(x=>x.includes('TAB_FIRST'));});
 console.log('Checking second tab shell input…');
 await page.getByRole('tab').nth(1).click();await second.evaluate(()=>term.paste('test -z "$LAB_TAB_TEST" && echo TAB_""SECOND'));await second.locator('.xterm-helper-textarea').press('Enter');
  await second.waitForFunction(()=>{const b=term.buffer.active;return Array.from({length:b.length},(_,i)=>b.getLine(i).translateToString()).some(x=>x.includes('TAB_SECOND'));});
 await page.getByRole('tab').first().click();assert.equal(await first.evaluate(()=>document.querySelector('#status')?.textContent),'CONNECTED');
 await page.screenshot({path:'/tmp/yolomancer-tabs-live.png'});
 await page.reload();first=await current();await first.waitForFunction(()=>document.querySelector('#status')?.textContent==='CONNECTED',null,{timeout:90000});assert.equal(await first.evaluate(()=>identity.privateKey),keys[0]);
 await page.getByRole('tab').nth(1).click();second=await current();await second.waitForFunction(()=>document.querySelector('#status')?.textContent==='CONNECTED',null,{timeout:90000});assert.equal(await second.evaluate(()=>identity.privateKey),keys[1]);
 console.log('PASS switching keeps shells isolated, and reload restores both saved identities and connections');
 const originalTask=get(created[0].id).task_arn.S;
 await page.getByRole('tab').first().click({button:'right'});await page.getByRole('menuitem',{name:'Duplicate',exact:true}).click();
 await page.waitForFunction(()=>document.querySelectorAll('[role=tab]').length===3);
 const third=await current();await third.waitForFunction(()=>document.querySelector('#status')?.textContent==='CONNECTED',null,{timeout:90000});assert.equal(await third.evaluate(()=>identity.privateKey),keys[0]);
 assert.equal(await third.evaluate(()=>state.id),created[0].id);
 assert.equal(get(created[0].id).task_arn.S,originalTask);
 const duplicateId=await third.evaluate(()=>labTabID),order=await page.evaluate(()=>JSON.parse(localStorage.getItem('yolomancer-tabs')).tabs.map(x=>x.id));
 assert.equal(order[1],duplicateId);
 console.log('PASS right-click Duplicate inserts adjacent tab and connects to the same task/key without code entry');
 const exitingTab=await third.evaluate(()=>labTabID);
 await third.evaluate(()=>term.paste('exit'));await third.locator('.xterm-helper-textarea').press('Enter');
 await page.waitForFunction(()=>document.querySelectorAll('[role=tab]').length===2,null,{timeout:30000});
 const manifest=await page.evaluate(()=>JSON.parse(localStorage.getItem('yolomancer-tabs')));
 assert(manifest.closed.some(x=>x.id===exitingTab&&x.reason==='remote-exit'));
 await page.reload();assert.equal(await page.getByRole('tab').count(),2);
 console.log('PASS real shell exit marks its tab closed and refresh does not reopen it');
}finally{
 if(browser){for(const context of browser.contexts())for(const page of context.pages()){await page.screenshot({path:'/tmp/yolomancer-tabs-live-last.png'}).catch(()=>{});}await browser.close();}
 for(const {id} of created){const item=get(id);if(item?.task_arn?.S)aws(['ecs','stop-task','--cluster','yolomancer-lab','--task',item.task_arn.S,'--reason','Synthetic terminal tab test cleanup'],'prosus-user-000');
  for(const prefix of ['lab#','ssh#'])aws(['dynamodb','delete-item','--table-name',table,'--key',JSON.stringify({pk:S(prefix+id)})]);
  aws(['dynamodb','delete-item','--table-name',table,'--key',JSON.stringify({pk:S('code#'+id)}),'--condition-expression','lab_test = :yes','--expression-attribute-values',JSON.stringify({':yes':{BOOL:true}})]);
 }
 console.log('Stopped only test tasks and removed synthetic records; participant labs unchanged.');
}})().catch(error=>{console.error(error.stack);process.exitCode=1;});
