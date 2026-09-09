import test from 'node:test';
import assert from 'node:assert/strict';
import {workspaceDeadline,confirmAction,metadataFence,replaceAfterStopAcknowledged} from './lifecycle.mjs';
test('new labs last 48 hours, never beyond code expiry',()=>{
  assert.equal(workspaceDeadline(1000,1000+72*3600),1000+48*3600);
  assert.equal(workspaceDeadline(1000,1100),1100);
});
test('replacement starts after stop acknowledgement, without waiting for STOPPED',async()=>{
  const events=[];let acknowledge;
  const result=replaceAfterStopAcknowledged(async()=>{events.push('stop');await new Promise(r=>acknowledge=r);},async()=>{events.push('provision');return 'new task';});
  assert.deepEqual(events,['stop']);acknowledge();
  assert.equal(await result,'new task');assert.deepEqual(events,['stop','provision']);
});
test('stop request failure does not launch a replacement',async()=>{
  let provisioned=false;
  await assert.rejects(replaceAfterStopAcknowledged(async()=>{throw Error('ECS unavailable');},async()=>{provisioned=true;}));
  assert.equal(provisioned,false);
});
test('metadata writes fence in-flight requests against shutdown and replacement',()=>{
  const record={generation:'old',bootstrapToken:'old-token',taskArn:'task',operation:'reinstall'};
  const fence=metadataFence(record);
  assert.equal(fence.ConditionExpression,'#data=:data AND attribute_not_exists(#operation)');
  assert.deepEqual(JSON.parse(fence.ExpressionAttributeValues[':data'].S),{generation:'old',bootstrapToken:'old-token'});
  assert.notEqual(fence.ExpressionAttributeValues[':data'].S,metadataFence({...record,generation:'new',bootstrapToken:'new-token'}).ExpressionAttributeValues[':data'].S);
});
test('destructive actions require explicit confirmation of the current generation',()=>{
  const record={generation:'current'};
  for(const request of [undefined,{}, {generation:'current'}, {generation:'current',confirm:false},{generation:'current',confirm:'true'},{generation:'old',confirm:true}])assert.throws(()=>confirmAction(record,request));
  assert.throws(()=>confirmAction({}, {confirm:true}));
  assert.doesNotThrow(()=>confirmAction(record,{generation:'current',confirm:true}));
});
