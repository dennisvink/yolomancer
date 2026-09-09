// Browser-only synthetic identity: never creates or modifies a participant lab.
const {chromium}=require('playwright');
const {execFileSync}=require('node:child_process');
const assert=require('node:assert/strict');
(async()=>{
 const ip=execFileSync('dig',['+short','lab.yolomancer.com','A','@1.1.1.1'],{encoding:'utf8'}).trim().split('\n').find(x=>/^\d+\.\d+\.\d+\.\d+$/.test(x));
 const browser=await chromium.launch({args:ip?[`--host-resolver-rules=MAP lab.yolomancer.com ${ip}`]:[]});
 try{
  const context=await browser.newContext(),page=await context.newPage();
  await page.goto('https://lab.yolomancer.com/terminal.html');
  await page.waitForFunction(()=>typeof identityStore==='function');
  await page.evaluate(()=>identityStore({id:'f'.repeat(64),publicKey:'synthetic',privateKey:'synthetic'}));
  await context.addCookies([{name:'lab',value:'synthetic.session',domain:'lab.yolomancer.com',path:'/',secure:true,httpOnly:true,sameSite:'Strict'}]);
  await page.route('**/api/status',async route=>{await new Promise(r=>setTimeout(r,3000));await route.fulfill({status:403,contentType:'application/json',body:'{"error":"Synthetic expired session"}'});});
  await page.reload();
  await page.locator('#reset-lab').waitFor({state:'visible'});
  assert.equal(await page.locator('#onboard').isVisible(),false);
  assert.match(await page.locator('#progress-message').textContent(),/Connecting/);
  assert.equal(await page.locator('#delete-lab').isVisible(),false);
  await page.locator('#reset-lab').click();
  assert.match(await page.locator('#action-warning').textContent(),/from this browser/);
  await page.screenshot({path:'/tmp/yolomancer-reset-modal.png'});
  await page.locator('#cancel-action').click();
  assert.equal(await page.evaluate(async()=>Boolean(await identityStore())),true);
  assert((await context.cookies()).some(c=>c.name==='lab'));
  console.log('PASS saved identity hides code input and Reset cancellation preserves credentials');
  assert.equal(await page.evaluate(async()=>{const r=await fetch('/api/reset',{method:'POST',headers:{'content-type':'application/json'},body:'{"confirm":false}'});return r.status;}),403);
  await page.locator('#reset-lab').click();await page.locator('#confirm-action').click();
  await page.waitForFunction(()=>document.querySelector('#onboard').hidden===false&&document.querySelector('#status').textContent==='READY.',null,{timeout:90000});
  assert.equal(await page.evaluate(async()=>Boolean(await identityStore())),false);
  assert(!(await context.cookies()).some(c=>c.name==='lab'));
  console.log('PASS confirmed Reset clears only browser identity/session and restores code form');
 }finally{await browser.close();}
})().catch(error=>{console.error(error.message);process.exitCode=1;});
