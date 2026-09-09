import http from 'node:http';
import net from 'node:net';
import fs from 'node:fs';
import path from 'node:path';
import {UpdateItemCommand} from "@aws-sdk/client-dynamodb";
import {WebSocketServer,createWebSocketStream} from "ws";
import {now,equal} from "./auth.mjs";
import {validStage} from './progress.mjs';
import {metadataFence} from './lifecycle.mjs';
import {cookieName,readCookie,duplicateDestination} from './browser-session.mjs';
import {db,table,s,session,live,provision,status,recoverIdentity,requestStop,reinstall} from "./provision.mjs";
const origin="https://lab.yolomancer.com";
async function body(req){let raw='';for await(const chunk of req){raw+=chunk;if(raw.length>8192)throw Error('Request too large');}return JSON.parse(raw);}
function reply(res,status,value){res.writeHead(status,{'content-type':'application/json','cache-control':'no-store','x-content-type-options':'nosniff'});res.end(JSON.stringify(value));}
async function browserSession(req){const [id,token]=readCookie(req);const r=live(await session(id));if(!equal(token,r.browserToken))throw Error('Unauthorized');return r;}
function sessionCookie(req,res,record){res.setHeader('set-cookie',`${cookieName(req)}=${record.id}.${record.browserToken}; Secure; HttpOnly; SameSite=Strict; Path=/; Max-Age=${record.expires-now()}`);}

const requests=new Map();
setInterval(()=>requests.clear(),60000).unref();
const server=http.createServer(async(req,res)=>{
  try{
    const url=new URL(req.url,origin);
    if(req.method==='GET'&&url.pathname==='/health'){res.end('ok');return;}
    if(req.method==='POST'&&url.pathname==='/api/duplicate'){
      if(req.headers.origin!==origin)throw Error('Origin denied');
      const record=await browserSession(req),request=await body(req);
      if(record.operation)throw Error('Lab is stopping');
      const destination=duplicateDestination(req,request.tab);
      // Copy only the authenticated browser session into a fresh tab cookie.
      // Do not claim a code, rotate a key, or start/reinstall a task.
      sessionCookie(destination,res,record);reply(res,200,{id:record.id});return;
    }
    if(req.method==='POST'&&url.pathname==='/api/reset'){
      if(req.headers.origin!==origin)throw Error('Origin denied');
      const request=await body(req);if(request.confirm!==true)throw Error('Confirmation required');
      res.setHeader('set-cookie',`${cookieName(req)}=; Secure; HttpOnly; SameSite=Strict; Path=/; Max-Age=0`);
      reply(res,200,{ok:true});return;
    }
    if(req.method==='POST'&&['/api/delete','/api/reinstall'].includes(url.pathname)){
      if(req.headers.origin!==origin)throw Error('Origin denied');
      const record=await browserSession(req),request=await body(req);
      if(url.pathname==='/api/delete'){
        await requestStop(record,request,'delete');
        for(const client of wss.clients)if(client.labId===record.id&&client.generation===record.generation)client.close(1000,'Lab deleted');
        // Keep the browser identity and session so the same code/key can be
        // reused. Only explicit Reset clears browser credentials.
        reply(res,200,{ok:true});return;
      }
      const result=await reinstall(record,request);
      if(!result){reply(res,202,{pending:true});return;}
      sessionCookie(req,res,result.record);reply(res,200,{id:result.record.id,identity:result.identity});return;
    }
    if(req.method==='POST'&&url.pathname==='/api/provision'){
      if(req.headers.origin!==origin)throw Error('Origin denied');
      const ip=String(req.headers['x-forwarded-for']||req.socket.remoteAddress).split(',').at(-1).trim();
      const count=(requests.get(ip)||0)+1;
      if(count>10||requests.size>10000){reply(res,429,{error:'Too many attempts. Wait a minute.'});return;}
      requests.set(ip,count);
      const {record,identity}=await provision(await body(req));
      sessionCookie(req,res,record);
      reply(res,200,{id:record.id,status:'PROVISIONING',expires:record.expires,identity});return;
    }
    if(req.method==='GET'&&url.pathname==='/api/status'){
      const record=await browserSession(req),r=await status(record);if(req.headers['x-lab-migrate']==='1')sessionCookie(req,res,record);delete r.ip;reply(res,200,r);return;
    }
    if(req.method==='POST'&&['/bootstrap','/ready','/progress'].includes(url.pathname)){
      const b=await body(req),r=live(await session(b.id));if(r.operation||!equal(b.token,r.bootstrapToken))throw Error('Unauthorized');
      if(url.pathname==='/bootstrap'){const identity=await recoverIdentity(r.code);reply(res,200,{code:r.code,registration:r.registration,authorizedKey:identity.authorizedKey,privatePem:identity.privatePem,expires:r.expires});return;}
      if(url.pathname==='/progress'){
        if(!validStage(b.stage))throw Error('Invalid progress stage');
        // Match the immutable generation payload as well as the bootstrap token:
        // a replaced task cannot update a new lab's progress.
        const fence=metadataFence(r);
        await db.send(new UpdateItemCommand({TableName:table,Key:{pk:s('lab#'+r.id)},...fence,UpdateExpression:'SET progress_stage=:stage',ExpressionAttributeValues:{...fence.ExpressionAttributeValues,':stage':s(b.stage)}}));
        reply(res,200,{ok:true});return;
      }
      if(!/^([a-f0-9]{2}:){15}[a-f0-9]{2}$/.test(b.fingerprint||''))throw Error('Invalid host key');
      const fence=metadataFence(r);
      await db.send(new UpdateItemCommand({TableName:table,Key:{pk:s('lab#'+r.id)},...fence,UpdateExpression:'SET host_fingerprint=:fp',ConditionExpression:fence.ConditionExpression+' AND (attribute_not_exists(host_fingerprint) OR host_fingerprint=:fp)',ExpressionAttributeValues:{...fence.ExpressionAttributeValues,':fp':s(b.fingerprint)}}));reply(res,200,{ok:true});return;
    }
    if(req.method!=='GET'){reply(res,404,{error:'Not found'});return;}
    const root=path.resolve('/app/public'),file=path.resolve(root,'.'+(url.pathname==='/'?'/index.html':url.pathname));
    if(!file.startsWith(root+'/')||!fs.existsSync(file)||!fs.statSync(file).isFile()){reply(res,404,{error:'Not found'});return;}
    res.writeHead(200,{'content-type':{'.html':'text/html; charset=utf-8','.js':'text/javascript','.css':'text/css','.wasm':'application/wasm'}[path.extname(file)]||'application/octet-stream','cache-control':'no-store','x-content-type-options':'nosniff','referrer-policy':'no-referrer','content-security-policy':"default-src 'self'; script-src 'self' 'wasm-unsafe-eval'; style-src 'self' 'unsafe-inline'; connect-src 'self'; object-src 'none'; frame-ancestors 'self'; base-uri 'none'"});fs.createReadStream(file).pipe(res);
  }catch(error){console.error('Request failed:',error.name);reply(res,403,{error:'Code invalid, expired, or workspace unavailable. Retry with your access code.'});}
});
const wss=new WebSocketServer({noServer:true,maxPayload:1024*1024});
server.on('upgrade',async(req,socket,head)=>{
  try{
    if(new URL(req.url,origin).pathname!=='/p'||req.headers.origin!==origin)throw Error();
    const record=await browserSession(req),state=await status(record);
    if(state.status!=='READY'||!state.ip)throw Error();
    wss.handleUpgrade(req,socket,head,ws=>{
      ws.labId=record.id;ws.generation=record.generation;
      const firstTimer=setTimeout(()=>ws.close(),5000);
      ws.once('message',message=>{
        clearTimeout(firstTimer);
        try{const target=JSON.parse(message);if(target.Host!==record.id||target.Port!==22)throw Error();}catch{ws.close();return;}
        const tcp=net.connect({host:state.ip,port:22});tcp.setTimeout(30000);
        tcp.once('connect',()=>{tcp.setTimeout(0);ws.send(Buffer.from(JSON.stringify({status:'ok'})));const stream=createWebSocketStream(ws);stream.on('error',()=>tcp.destroy());tcp.on('error',()=>stream.destroy());stream.pipe(tcp).pipe(stream);});
        tcp.on('timeout',()=>tcp.destroy());tcp.on('error',()=>ws.close());ws.on('close',()=>tcp.destroy());
      });
      const heartbeat=setInterval(()=>ws.ping(),20000),deadline=setTimeout(()=>ws.close(),Math.max(1,record.expires-now())*1000);
      ws.on('close',()=>{clearInterval(heartbeat);clearTimeout(deadline);clearTimeout(firstTimer);});ws.on('error',()=>ws.close());
    });
  }catch{socket.end('HTTP/1.1 403 Forbidden\r\nConnection: close\r\n\r\n');}
});
server.listen(8080,'0.0.0.0');
process.on('SIGTERM',()=>{server.close();for(const client of wss.clients)client.close(1012);setTimeout(()=>process.exit(),5000).unref();});
