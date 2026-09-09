import {createHash,createPublicKey,verify,timingSafeEqual} from 'node:crypto';
export const hash = x => createHash('sha256').update(x).digest('hex');
export const now = () => Math.floor(Date.now()/1000);
export function authenticate(r, time=now()) {
  if (!r || !/^[ABCDEF012345678]{10}$/.test(r.code||'') || !Number.isSafeInteger(r.timestamp) || Math.abs(time-r.timestamp)>60 || !/^[a-f0-9]{32}$/.test(r.nonce||'')) throw Error('Invalid request');
  const pub=Buffer.from(r.publicKey||'','base64'),sig=Buffer.from(r.signature||'','base64');
  if(pub.length!==32 || sig.length!==64 || pub.toString('base64')!==r.publicKey) throw Error('Invalid identity');
  const key=createPublicKey({key:Buffer.concat([Buffer.from('302a300506032b6570032100','hex'),pub]),format:'der',type:'spki'});
  if(!verify(null,Buffer.from(JSON.stringify(['yolomancer-lab-v1',r.code,r.publicKey,r.timestamp,r.nonce])),key,sig)) throw Error('Invalid signature');
  return hash(r.code);
}
export function equal(a,b){return typeof a==='string'&&typeof b==='string'&&a.length===b.length&&timingSafeEqual(Buffer.from(a),Buffer.from(b));}
export function sshPublic(raw) {
  const algorithm=Buffer.from('ssh-ed25519'),pub=Buffer.from(raw,'base64');
  const size=n=>{const b=Buffer.alloc(4);b.writeUInt32BE(n);return b;};
  return 'ssh-ed25519 '+Buffer.concat([size(algorithm.length),algorithm,size(pub.length),pub]).toString('base64');
}
