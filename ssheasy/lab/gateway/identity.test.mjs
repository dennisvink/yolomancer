import test from 'node:test';
import assert from 'node:assert/strict';
import {randomBytes,createPrivateKey,createPublicKey} from 'node:crypto';
import {identityFromSeed} from './identity.mjs';
import {mkdtempSync,writeFileSync,rmSync} from 'node:fs';
import {tmpdir} from 'node:os';
import {join} from 'node:path';
import {execFileSync} from 'node:child_process';
test('recovery deterministically restores the same browser and OpenSSH key',()=>{
  const seed=randomBytes(32),a=identityFromSeed(seed),b=identityFromSeed(seed);
  assert.equal(a.publicKey,b.publicKey);assert.equal(a.privateKey,b.privateKey);
  const key=createPrivateKey({key:Buffer.from(a.privateKey,'base64'),format:'der',type:'pkcs8'});
  assert.equal(createPublicKey(key).export({format:'der',type:'spki'}).subarray(-32).toString('base64'),a.publicKey);
  assert(a.privatePem.startsWith('-----BEGIN OPENSSH PRIVATE KEY-----'));
  assert.throws(()=>identityFromSeed(Buffer.alloc(31)));
});
test('OpenSSH reads the generated private key and derives the same public key',()=>{
  const identity=identityFromSeed(randomBytes(32)),dir=mkdtempSync(join(tmpdir(),'lab-key-test-'));
  try{const file=join(dir,'id_ed25519');writeFileSync(file,identity.privatePem,{mode:0o600});
    const pub=execFileSync('ssh-keygen',['-y','-f',file],{encoding:'utf8'}).trim().split(' ').slice(0,2).join(' ');
    assert.equal(pub,identity.authorizedKey);
  }finally{rmSync(dir,{recursive:true});}
});
