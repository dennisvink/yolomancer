// Exercise UI states only: no provisioning or mutation requests.
const {chromium}=require('playwright');
const {execFileSync}=require('node:child_process');
const assert=require('node:assert/strict');
(async()=>{
 const ip=execFileSync('dig',['+short','lab.yolomancer.com','A','@1.1.1.1'],{encoding:'utf8'}).trim().split('\n').find(x=>/^\d+\.\d+\.\d+\.\d+$/.test(x));
 const browser=await chromium.launch({args:ip?[`--host-resolver-rules=MAP lab.yolomancer.com ${ip}`]:[]});
 try{
  const page=await browser.newPage();
  if(process.env.CONTROLS_LOCAL)await page.route('**/lab.js',route=>route.fulfill({path:require('node:path').resolve(__dirname,'../public/lab.js'),contentType:'text/javascript'}));
  await page.goto('https://lab.yolomancer.com/terminal.html');
  await page.waitForFunction(()=>document.querySelector('#status').textContent==='READY.',null,{timeout:90000});
  for(const status of ['PROVISIONING','STOPPING','READY','FAILED','STOPPED']){
   await page.evaluate(status=>{state={generation:'synthetic',status};connectionPending=false;actionBusy=false;renderActions();},status);
   const disabled=['PROVISIONING','STOPPING'].includes(status);
   for(const id of ['delete-lab','reinstall-lab']){
    assert.equal(await page.locator('#'+id).isVisible(),true);
    assert.equal(await page.locator('#'+id).isDisabled(),disabled);
   }
   if(disabled){await page.evaluate(()=>openAction('delete'));assert.equal(await page.locator('#lab-action-dialog').evaluate(el=>el.open),false);}
  }
  await page.evaluate(()=>{actionBusy=true;renderActions();});
  assert.equal(await page.locator('#reinstall-lab').isDisabled(),true);
  console.log('PASS provisioning/stopping disable both controls; ready/failure/stopped restore them; busy remains disabled');
 }finally{await browser.close();}
})().catch(error=>{console.error(error.message);process.exitCode=1;});
