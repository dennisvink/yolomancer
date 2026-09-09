import test from 'node:test';
import assert from 'node:assert/strict';
import {generateKeyPairSync,sign} from 'node:crypto';
import {authenticate,hash,equal,sshPublic} from './auth.mjs';
test('signed browser identity binds code, public key and timestamp',()=>{
  const pair=generateKeyPairSync('ed25519'),pub=pair.publicKey.export({format:'der',type:'spki'}).subarray(-32).toString('base64');
  const request={code:'ABCDEF0123',publicKey:pub,timestamp:1000,nonce:'1'.repeat(32)};
  request.signature=sign(null,Buffer.from(JSON.stringify(['yolomancer-lab-v1',request.code,pub,request.timestamp,request.nonce])),pair.privateKey).toString('base64');
  assert.equal(authenticate(request,1000),hash(request.code));
  assert.throws(()=>authenticate({...request,code:'ABCDEF0124'},1000));
  assert.throws(()=>authenticate(request,1061));
  assert.throws(()=>authenticate({...request,signature:''},1000));
  assert.throws(()=>authenticate({...request,publicKey:'bad'},1000));
  assert.match(sshPublic(pub),/^ssh-ed25519 AAAAC3NzaC1lZDI1NTE5/);
  assert.equal(equal('a','b'),false);assert.equal(equal('a','a'),true);
});
