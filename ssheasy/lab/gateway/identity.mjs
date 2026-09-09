import {createPrivateKey,createPublicKey,randomBytes} from 'node:crypto';
import {sshPublic} from './auth.mjs';

const uint=n=>{const b=Buffer.alloc(4);b.writeUInt32BE(n);return b;};
const field=b=>Buffer.concat([uint(b.length),b]);
export function identityFromSeed(seed) {
  if(seed.length!==32)throw Error('Invalid identity seed');
  const der=Buffer.concat([Buffer.from('302e020100300506032b657004220420','hex'),seed]);
  const privateKey=createPrivateKey({key:der,format:'der',type:'pkcs8'});
  const pub=createPublicKey(privateKey).export({format:'der',type:'spki'}).subarray(-32);
  const algorithm=Buffer.from('ssh-ed25519'),wire=Buffer.concat([field(algorithm),field(pub)]);
  const check=randomBytes(4);
  let block=Buffer.concat([check,check,field(algorithm),field(pub),field(Buffer.concat([seed,pub])),field(Buffer.from('yolomancer-lab'))]);
  const padding=8-block.length%8;block=Buffer.concat([block,Buffer.from(Array.from({length:padding},(_,i)=>i+1))]);
  const key=Buffer.concat([Buffer.from('openssh-key-v1\0'),field(Buffer.from('none')),field(Buffer.from('none')),field(Buffer.alloc(0)),uint(1),field(wire),field(block)]).toString('base64');
  return {publicKey:pub.toString('base64'),privateKey:der.toString('base64'),authorizedKey:sshPublic(pub.toString('base64')),privatePem:'-----BEGIN OPENSSH PRIVATE KEY-----\n'+key.match(/.{1,70}/g).join('\n')+'\n-----END OPENSSH PRIVATE KEY-----\n'};
}
