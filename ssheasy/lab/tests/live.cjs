// End-to-end test: creates a test-only dispenser row, never a participant claim.
// Uses non-working AWS credentials. Cleans up only its own task and records.
const {chromium}=require('playwright');
const {execFileSync}=require('node:child_process');
const {randomInt,createHash}=require('node:crypto');
const assert=require('node:assert/strict');
const table='yolomancer-dispenser',alphabet='ABCDEF012345678';
const code=Array.from({length:10},()=>alphabet[randomInt(alphabet.length)]).join('');
const id=createHash('sha256').update(code).digest('hex'),pk='code#'+id;
const S=x=>({S:x}),N=x=>({N:String(x)});
function aws(args,profile='default'){const value=execFileSync('aws',[...args,'--profile',profile,'--region','eu-west-1','--output','json'],{encoding:'utf8'});return value.trim()?JSON.parse(value):null;}
function get(key){return aws(['dynamodb','get-item','--table-name',table,'--key',JSON.stringify({pk:S(key)}),'--consistent-read'])?.Item;}
let browser,created=false;
(async()=>{
 try{
  const epoch=Math.floor(Date.now()/1000);
  aws(['dynamodb','put-item','--table-name',table,'--condition-expression','attribute_not_exists(pk)','--item',JSON.stringify({pk:S(pk),lab_test:{BOOL:true},expires_at:N(epoch-1),ttl:N(epoch+3600),account_user:S('prosus-user-100'),credentials:S(JSON.stringify({aws_access_key_id:'AKIA'+'A'.repeat(16),aws_secret_access_key:'x'.repeat(40),aws_region:'eu-west-1'}))})]);created=true;
  const addresses=execFileSync('dig',['+short','lab.yolomancer.com','A','@1.1.1.1'],{encoding:'utf8'}).trim().split('\n');
  const ip=addresses.find(x=>/^\d+\.\d+\.\d+\.\d+$/.test(x));
  browser=await chromium.launch({args:ip?[`--host-resolver-rules=MAP lab.yolomancer.com ${ip}`]:[]});const context=await browser.newContext();const page=await context.newPage();
  page.on('pageerror',error=>console.log('Browser error:',error.message));
  page.on('console',message=>{if(message.type()==='error')console.log('Browser console:',message.text());});
  await page.goto('https://lab.yolomancer.com/terminal.html');await page.waitForFunction(()=>typeof initConnection==='function',null,{timeout:90000});
  const request=async(target=page)=>target.evaluate(async code=>{const response=await fetch('/api/provision',{method:'POST',headers:{'content-type':'application/json'},body:JSON.stringify({code})});const body=await response.json();if(response.ok)identity=await identityStore(body.identity);return {status:response.status};},code);
  assert.equal((await request()).status,403);assert(!get('lab#'+id));console.log('PASS expired code rejected without provisioning');
  aws(['dynamodb','update-item','--table-name',table,'--key',JSON.stringify({pk:S(pk)}),'--update-expression','SET expires_at=:expiry, dispensed=:yes, claimed_fingerprint=:fp','--expression-attribute-values',JSON.stringify({':expiry':N(epoch+1800),':yes':{BOOL:true},':fp':S('already-used')})]);
  const provisionStarted=Date.now();
  const results=await Promise.all([request(),request()]);assert(results.every(r=>r.status===200),'Valid used code recovery failed');
  const task=get('lab#'+id).task_arn.S;assert(task);console.log('PASS concurrent recovery of used code provisions one task');
  const again=await request();assert.equal(again.status,200);assert.equal(get('lab#'+id).task_arn.S,task);
  let ready=false;
  for(let i=0;i<240;i++){
    const status=await page.evaluate(async()=>{const r=await fetch('/api/status');return r.json()});
    if(status.status==='READY'){ready=true;break;}if(status.status==='STOPPED')throw Error('Workspace stopped during bootstrap');
    if(i%6===0)console.log('Waiting for workspace bootstrap…');await new Promise(r=>setTimeout(r,5000));
  }
  assert(ready,'Workspace did not become ready');console.log(`PASS source update, cached Go build and registration bootstrap (${Math.round((Date.now()-provisionStarted)/1000)}s including Fargate startup)`);
  await page.reload();
  await page.waitForFunction(()=>typeof initConnection==='function',null,{timeout:90000});
  await page.evaluate(()=>{const show=window.showErr;window.showErr=message=>{console.error('SSH:',message);show(message);};});
  try { await page.waitForFunction(()=>document.querySelector('#status').textContent==='CONNECTED',null,{timeout:60000}); }
  catch(error) { console.log('SSH status:',await page.locator('#status').textContent());await page.screenshot({path:'/tmp/yolomancer-lab-failed.png'});throw error; }
  console.log('PASS browser SSH connected with pinned host key');
  const files=await page.evaluate(()=>new Promise((resolve,reject)=>sftpListFiles('/home/participant/.aws',(files,status)=>status===200?resolve(files):reject(Error('SFTP listing failed')))));
  assert.equal(files.find(file=>file.name==='credentials')?.rights,'-rw-------');
  console.log('PASS AWS credentials exist with mode 0600 (contents not read)');
  const keys=await page.evaluate(()=>new Promise((resolve,reject)=>sftpListFiles('/home/participant/.ssh',(files,status)=>status===200?resolve(files):reject(Error('SFTP listing failed')))));
  assert.equal(keys.find(file=>file.name==='id_ed25519')?.rights,'-rw-------');
  assert.equal(keys.find(file=>file.name==='id_ed25519.pub')?.rights,'-rw-r--r--');
  console.log('PASS provisioned private and public SSH key permissions');
  await page.evaluate(()=>term.paste("sudo -n sh -c 'test $(id -u) = 0' && test -d /workspace/.git && test -x /workspace/yolomancer && test \"$(readlink /usr/bin/yolomancer)\" = /workspace/yolomancer && test \"$(stat -c %a /workspace/yolomancer)\" = 755 && test \"$(pwd)\" = /workspace && printf 'LAB_%s\\n' VERIFIED\r"));
  await page.keyboard.press('Enter');
  await page.waitForFunction(()=>{const b=term.buffer.active;return Array.from({length:b.length},(_,i)=>b.getLine(i).translateToString()).some(line=>line.trim()==='LAB_VERIFIED');},null,{timeout:30000});
  console.log('PASS passwordless sudo, shell directory and source build in /workspace');
  await page.screenshot({path:'/tmp/yolomancer-lab-live.png'});
  const item=get(pk),record=JSON.parse(get('lab#'+id).data.S);
  assert(item.dispensed.BOOL);assert(item.claimed_public_key.S);assert.equal(record.publicKey,await page.evaluate(()=>identity.publicKey));
  console.log('PASS code is bound to the browser identity and claimed');
  const context2=await browser.newContext(),page2=await context2.newPage();
  await page2.goto('https://lab.yolomancer.com/terminal.html');await page2.waitForFunction(()=>typeof initConnection==='function',null,{timeout:90000});
  assert.equal((await request(page2)).status,200);assert.equal(get('lab#'+id).task_arn.S,task);
  assert.equal(await page.evaluate(()=>identity.privateKey),await page2.evaluate(()=>identity.privateKey));
  await page2.reload();await page2.waitForFunction(()=>document.querySelector('#status').textContent==='CONNECTED',null,{timeout:90000});
  console.log('PASS clean browser recovers the same key and reconnects to the existing workspace');
 }finally{
  if(browser)await browser.close();
  if(created){const lab=get('lab#'+id);if(lab?.task_arn?.S)aws(['ecs','stop-task','--cluster','yolomancer-lab','--task',lab.task_arn.S,'--reason','Lab end-to-end test complete'],'prosus-user-000');
    if(lab)aws(['dynamodb','delete-item','--table-name',table,'--key',JSON.stringify({pk:S('lab#'+id)})]);
    aws(['dynamodb','delete-item','--table-name',table,'--key',JSON.stringify({pk:S('ssh#'+id)})]);
    aws(['dynamodb','delete-item','--table-name',table,'--key',JSON.stringify({pk:S(pk)}),'--condition-expression','lab_test = :yes','--expression-attribute-values',JSON.stringify({':yes':{BOOL:true}})]);
    console.log('Removed test-only records and stopped test workspace. Participant codes unchanged.');
  }
 }
})().catch(error=>{console.error(error.message);process.exitCode=1;});
