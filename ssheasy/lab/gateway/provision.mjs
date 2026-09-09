import {randomBytes,randomUUID,createPrivateKey,createPublicKey} from 'node:crypto';
import {DynamoDBClient,GetItemCommand,TransactWriteItemsCommand,UpdateItemCommand} from '@aws-sdk/client-dynamodb';
import {ECSClient,RunTaskCommand,DescribeTasksCommand,ListTasksCommand,StopTaskCommand} from '@aws-sdk/client-ecs';
import {fromTemporaryCredentials,fromNodeProviderChain} from '@aws-sdk/credential-providers';
import {SignatureV4} from '@smithy/signature-v4';
import {HttpRequest} from '@smithy/protocol-http';
import {Sha256} from '@aws-crypto/sha256-js';
import {hash,now,authenticate} from './auth.mjs';
import {workspaceDeadline,confirmAction,replaceAfterStopAcknowledged} from './lifecycle.mjs';
import {progressFor} from './progress.mjs';

const env=process.env;
export const table=env.DISPENSER_TABLE;
export const db=new DynamoDBClient({region:'eu-west-1',credentials:fromTemporaryCredentials({params:{RoleArn:env.DISPENSER_ROLE,RoleSessionName:'yolomancer-lab'},clientConfig:{region:'eu-west-1'}})});
const ecs=new ECSClient({region:'eu-west-1'});
const signer=new SignatureV4({credentials:fromNodeProviderChain(),region:'eu-west-1',service:'execute-api',sha256:Sha256});
export const s=x=>({S:String(x)}),n=x=>({N:String(x)});
export async function get(pk){return (await db.send(new GetItemCommand({TableName:table,Key:{pk:s(pk)},ConsistentRead:true}))).Item;}
export async function session(id){if(!/^[a-f0-9]{64}$/.test(id||''))throw Error('Invalid session');const item=await get('lab#'+id);if(!item)return null;const r=JSON.parse(item.data.S);if(item.task_arn)r.taskArn=item.task_arn.S;if(item.operation)r.operation=item.operation.S;return r;}
export function live(r){if(!r||r.expires<=now())throw Error('Session expired');return r;}
export async function recoverIdentity(code){
  if(typeof code!=='string'||!/^[ABCDEF012345678]{10}$/.test(code))throw Error('Invalid code');
  const url=new URL(env.IDENTITY_API),body=JSON.stringify({code});
  const request=await signer.sign(new HttpRequest({method:'POST',protocol:url.protocol,hostname:url.hostname,path:url.pathname,headers:{host:url.hostname,'content-type':'application/json'},body}));
  const response=await fetch(url,{method:'POST',headers:request.headers,body,signal:AbortSignal.timeout(30000)});
  if(!response.ok)throw Error('Code unavailable');return response.json();
}
export async function status(r){
  const item=await get('lab#'+r.id),arn=item?.task_arn?.S;
  const meta={id:r.id,generation:r.generation,expires:r.expires,operation:item?.operation?.S};
  if(!arn)return {...meta,status:meta.operation?'STOPPING':'PROVISIONING',progress:progressFor(null,item?.progress_stage?.S,meta.operation)};
  const task=(await ecs.send(new DescribeTasksCommand({cluster:env.CLUSTER,tasks:[arn]}))).tasks?.[0];
  const ip=task?.attachments?.flatMap(x=>x.details||[]).find(x=>x.name==='privateIPv4Address')?.value;
  return {...meta,progress:progressFor(task,item?.progress_stage?.S,meta.operation),status:!task||task.lastStatus==='STOPPED'?'STOPPED':meta.operation?'STOPPING':item?.progress_stage?.S==='failed'?'FAILED':item.host_fingerprint?.S&&task?.lastStatus==='RUNNING'?'READY':'PROVISIONING',hostFingerprint:item.host_fingerprint?.S,ip};
}
async function generationTasks(r){
  const listed=await ecs.send(new ListTasksCommand({cluster:env.CLUSTER,startedBy:r.generation}));
  const arns=[...new Set([...(listed.taskArns||[]),...(r.taskArn?[r.taskArn]:[])])];
  return arns.length?(await ecs.send(new DescribeTasksCommand({cluster:env.CLUSTER,tasks:arns}))).tasks||[]:[];
}
export async function requestStop(r,request,operation){
  confirmAction(r,request);
  if(!['delete','reinstall'].includes(operation))throw Error('Invalid operation');
  const item=await get('lab#'+r.id);
  if(!item||JSON.parse(item.data.S).generation!==r.generation)throw Error('Lab changed; reload before continuing');
  await db.send(new UpdateItemCommand({TableName:table,Key:{pk:s('lab#'+r.id)},UpdateExpression:'SET #operation=:operation',ConditionExpression:'#data=:data AND (attribute_not_exists(#operation) OR #operation=:operation)',ExpressionAttributeNames:{'#data':'data','#operation':'operation'},ExpressionAttributeValues:{':data':item.data,':operation':s(operation)}}));
  const current=await session(r.id);
  if(current?.generation!==r.generation)return;
  for(const task of await generationTasks(current))if(task.lastStatus!=='STOPPED')await ecs.send(new StopTaskCommand({cluster:env.CLUSTER,task:task.taskArn,reason:operation==='delete'?'Participant confirmed Delete Lab':'Participant confirmed Reinstall Lab'}));
}
export async function reinstall(r,request){
  if(request?.confirm===true&&r.reinstalledFrom===request.generation&&!r.operation){const key=await recoverIdentity(r.code);return {record:r,identity:{id:r.id,publicKey:key.publicKey,privateKey:key.privateKey}};}
  // requestStop fences the old generation before StopTask. Its acknowledgement
  // is sufficient: ECS can finish shutting down while the replacement starts.
  return replaceAfterStopAcknowledged(()=>requestStop(r,request,'reinstall'),()=>provision({code:r.code},r.generation));
}
export async function provision(request,reinstallGeneration){
  if(Object.hasOwn(request,'publicKey'))authenticate(request);
  const identity=await recoverIdentity(request.code),id=hash(request.code);
  if(request.publicKey&&request.publicKey!==identity.publicKey)throw Error('Saved identity does not match this code');
  const previous=await get('lab#'+id);
  let r=await session(id);
  const stopped=r?.taskArn&&(await status(r)).status==='STOPPED';
  const finishingReinstall=r?.operation==='reinstall'&&r.generation===reinstallGeneration;
  if(r?.operation&&!stopped&&!finishingReinstall)throw Error('Lab is stopping; wait before provisioning');
  if(r&&r.schema!==2&&r.taskArn&&!stopped)throw Error('Legacy workspace requires migration');
  if(!r||r.schema!==2||r.expires<=now()||stopped||finishingReinstall){
    const code=await get('code#'+id),expiry=Number(code?.expires_at?.N);
    if(!code?.credentials||!Number.isFinite(expiry)||expiry<=now())throw Error('Code unavailable');
    const seed=randomBytes(32),privateKey=createPrivateKey({key:Buffer.concat([Buffer.from('302e020100300506032b657004220420','hex'),seed]),format:'der',type:'pkcs8'});
    const pub=createPublicKey(privateKey).export({format:'der',type:'spki'}).subarray(-32);
    const prior=r,created=now();
    r={schema:2,id,generation:randomUUID(),code:request.code,publicKey:identity.publicKey,registration:{id:'yolomancer-'+randomUUID(),seed:seed.toString('base64')},browserToken:finishingReinstall?prior.browserToken:randomBytes(32).toString('hex'),bootstrapToken:randomBytes(32).toString('hex'),created,expires:workspaceDeadline(created,expiry),taskDefinition:env.WORKSPACE_TASK,...(finishingReinstall?{reinstalledFrom:prior.generation}:{})};
    try{
      await db.send(new TransactWriteItemsCommand({TransactItems:[
        {Update:{TableName:table,Key:{pk:s('code#'+id)},UpdateExpression:'SET claimed_fingerprint=:fp, claimed_public_key=:pub, claimed_at=:now, dispensed=:yes',ConditionExpression:'attribute_exists(credentials) AND expires_at > :now',ExpressionAttributeValues:{':fp':s(hash(pub)),':pub':s(pub.toString('base64')),':now':n(now()),':yes':{BOOL:true}}}},
        {Put:{TableName:table,Item:{pk:s('lab#'+id),data:s(JSON.stringify(r)),ttl:n(r.expires+86400)},ConditionExpression:previous?'#data=:previous':'attribute_not_exists(pk)',...(previous?{ExpressionAttributeNames:{'#data':'data'},ExpressionAttributeValues:{':previous':previous.data}}:{})}}
      ]}));
    }catch(error){if(error.name!=='TransactionCanceledException')throw error;const winner=await session(id);if(!winner||winner.schema!==2||winner.publicKey!==identity.publicKey)throw Error('Workspace unavailable');r=live(winner);}
  }
  live(r);if(r.publicKey!==identity.publicKey)throw Error('Identity mismatch');
  if(r.operation)throw Error('Lab is stopping');
  if(!r.taskArn){
    // Find an already-started task even after ECS's 24-hour token window.
    const existing=(await generationTasks(r)).find(task=>task.lastStatus!=='STOPPED');
    const result=existing?{tasks:[existing]}:await ecs.send(new RunTaskCommand({cluster:env.CLUSTER,taskDefinition:r.taskDefinition,launchType:'FARGATE',platformVersion:'1.4.0',clientToken:r.generation,startedBy:r.generation,count:1,networkConfiguration:{awsvpcConfiguration:{subnets:env.SUBNETS.split(','),securityGroups:[env.WORKSPACE_SG],assignPublicIp:'ENABLED'}},overrides:{containerOverrides:[{name:'workspace',environment:[{name:'LAB_ID',value:r.id},{name:'LAB_TOKEN',value:r.bootstrapToken},{name:'LAB_EXPIRES',value:String(r.expires)},{name:'LAB_URL',value:'https://lab.yolomancer.com'}]}]}}));
    if(result.failures?.length||!result.tasks?.[0])throw Error('Workspace capacity unavailable');
    r.taskArn=result.tasks[0].taskArn;
    try{
      const current=await get('lab#'+id);if(!current||JSON.parse(current.data.S).generation!==r.generation)throw Error('Lab changed');
      await db.send(new UpdateItemCommand({TableName:table,Key:{pk:s('lab#'+id)},UpdateExpression:'SET task_arn=:task',ConditionExpression:'#data=:data AND attribute_not_exists(#operation)',ExpressionAttributeNames:{'#data':'data','#operation':'operation'},ExpressionAttributeValues:{':task':s(r.taskArn),':data':current.data}}));
    }catch(error){await ecs.send(new StopTaskCommand({cluster:env.CLUSTER,task:r.taskArn,reason:'Lab was deleted or replaced during provisioning'}));throw error;}
  }
  return {record:r,identity:{id,publicKey:identity.publicKey,privateKey:identity.privateKey}};
}
