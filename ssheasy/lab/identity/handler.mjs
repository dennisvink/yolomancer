import {randomBytes} from 'node:crypto';
import {DynamoDBClient,GetItemCommand,PutItemCommand} from '@aws-sdk/client-dynamodb';
import {KMSClient,EncryptCommand,DecryptCommand} from '@aws-sdk/client-kms';
import {ECSClient,ListTasksCommand,DescribeTasksCommand,StopTaskCommand} from '@aws-sdk/client-ecs';
import {fromTemporaryCredentials} from '@aws-sdk/credential-providers';
import {hash,now} from './auth.mjs';
import {identityFromSeed} from './identity.mjs';

const db=new DynamoDBClient({region:'eu-west-1',credentials:fromTemporaryCredentials({params:{RoleArn:process.env.DISPENSER_ROLE,RoleSessionName:'lab-identity'},clientConfig:{region:'eu-west-1'}})});
const kms=new KMSClient({region:'eu-west-1'}),ecs=new ECSClient({region:'eu-west-1'}),table=process.env.DISPENSER_TABLE;
const S=x=>({S:x});
const get=async pk=>(await db.send(new GetItemCommand({TableName:table,Key:{pk:S(pk)},ConsistentRead:true}))).Item;
async function enroll(code) {
  if(typeof code!=='string'||!/^[ABCDEF012345678]{10}$/.test(code))throw Error('Invalid code');
  const id=hash(code),account=await get('code#'+id),expiry=Number(account?.expires_at?.N);
  // Possession of a still-valid code authorizes recovery, including used codes.
  if(!account?.credentials||!Number.isFinite(expiry)||expiry<=now())throw Error('Code unavailable');
  let item=await get('ssh#'+id);
  if(!item){
    const seed=randomBytes(32),cipher=await kms.send(new EncryptCommand({KeyId:process.env.KEY_ARN,Plaintext:seed,EncryptionContext:{lab:id}}));
    const candidate={pk:S('ssh#'+id),ciphertext:S(Buffer.from(cipher.CiphertextBlob).toString('base64')),ttl:{N:String(expiry+86400)}};
    try{await db.send(new PutItemCommand({TableName:table,Item:candidate,ConditionExpression:'attribute_not_exists(pk)'}));item=candidate;}
    catch(error){if(error.name!=='ConditionalCheckFailedException')throw error;item=await get('ssh#'+id);}
  }
  const plain=await kms.send(new DecryptCommand({KeyId:process.env.KEY_ARN,CiphertextBlob:Buffer.from(item.ciphertext.S,'base64'),EncryptionContext:{lab:id}}));
  return identityFromSeed(Buffer.from(plain.Plaintext));
}
async function reap() {
  // Enforce the deadline outside the container: sudo must not bypass expiry.
  for(const desiredStatus of ['RUNNING','PENDING']){
    let nextToken;
    do{
      const list=await ecs.send(new ListTasksCommand({cluster:process.env.CLUSTER,family:'yolomancer-lab-workspace',desiredStatus,nextToken}));nextToken=list.nextToken;
      if(!list.taskArns?.length)continue;
      const tasks=(await ecs.send(new DescribeTasksCommand({cluster:process.env.CLUSTER,tasks:list.taskArns}))).tasks||[];
      for(const task of tasks){
        const vars=task.overrides?.containerOverrides?.find(c=>c.name==='workspace')?.environment||[];
        const expiry=Number(vars.find(e=>e.name==='LAB_EXPIRES')?.value),id=vars.find(e=>e.name==='LAB_ID')?.value;
        const item=/^[a-f0-9]{64}$/.test(id||'')?await get('lab#'+id):null;
        const record=item?.data?JSON.parse(item.data.S):null;
        const replaced=record?.generation&&record.generation!==task.startedBy;
        if(item?.operation||replaced||expiry>0&&expiry<=now())await ecs.send(new StopTaskCommand({cluster:process.env.CLUSTER,task:task.taskArn,reason:item?.operation||replaced?'Lab deleted or replaced':'Lab workspace lifetime expired'}));
      }
    }while(nextToken);
  }
}
export async function handler(event) {
  if(event.source==='aws.events'){await reap();return;}
  try{
    if(event.requestContext?.http?.method!=='POST')throw Error('Invalid method');
    const raw=event.isBase64Encoded?Buffer.from(event.body||'','base64').toString():event.body||'';
    if(raw.length>1024)throw Error('Request too large');
    const identity=await enroll(JSON.parse(raw).code);
    return {statusCode:200,headers:{'content-type':'application/json','cache-control':'no-store'},body:JSON.stringify(identity)};
  }catch{return {statusCode:403,headers:{'content-type':'application/json','cache-control':'no-store'},body:JSON.stringify({error:'Code invalid or expired'})};}
}
