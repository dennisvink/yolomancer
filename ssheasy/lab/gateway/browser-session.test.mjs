import test from 'node:test';import assert from 'node:assert/strict';
import {cookieName,readCookie,tabId,duplicateDestination} from './browser-session.mjs';
const a='t-11111111-1111-1111-1111-111111111111',b='t-22222222-2222-2222-2222-222222222222';
test('duplicate destination cannot overwrite source or existing session cookies',()=>{
 const req={url:'/api/duplicate',headers:{'x-lab-tab':a,cookie:`lab_${a}=one.token`}};
 assert.equal(cookieName(duplicateDestination(req,b)),'lab_'+b);
 for(const target of [undefined,'bad',a])assert.throws(()=>duplicateDestination(req,target));
 assert.throws(()=>duplicateDestination({...req,headers:{...req.headers,cookie:req.headers.cookie+`; lab_${b}=two.token`}},b));
 assert.equal(req.headers['x-lab-tab'],a);
});
test('HTTP and WebSocket tab selectors isolate session cookies',()=>{
 const cookie=`lab_${a}=one.token1; lab_${b}=two.token2; lab=legacy.token`;
 assert.deepEqual(readCookie({url:'/api/status',headers:{cookie,'x-lab-tab':a}}),['one','token1']);
 assert.deepEqual(readCookie({url:'/p?tab='+b,headers:{cookie}}),['two','token2']);
 assert.equal(cookieName({url:'/api/reset',headers:{'x-lab-tab':a}}),'lab_'+a);
 assert.throws(()=>tabId({url:'/p?tab=bad',headers:{}}));
});
test('new tabs cannot silently inherit the old global session',()=>{
 const req={url:'/api/status',headers:{cookie:'lab=legacy.token','x-lab-tab':a}};
 assert.deepEqual(readCookie(req),['']);
 assert.deepEqual(readCookie({...req,headers:{...req.headers,'x-lab-migrate':'1'}}),['legacy','token']);
});
