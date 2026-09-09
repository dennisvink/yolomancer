export const WORKSPACE_LIFETIME_SECONDS=48*60*60;
export function workspaceDeadline(created,codeExpiry){return Math.min(created+WORKSPACE_LIFETIME_SECONDS,codeExpiry);}
export function confirmAction(record,request){
  if(!record?.generation||request?.confirm!==true||request.generation!==record.generation)throw Error('Confirm the current lab before continuing');
}
// DynamoDB checks this at write time, not just at request authentication time.
// Bootstrap writes already in flight cannot cross a shutdown/replacement.
export function metadataFence(record){
  const {taskArn,operation,...data}=record;
  return {
    ConditionExpression:'#data=:data AND attribute_not_exists(#operation)',
    ExpressionAttributeNames:{'#data':'data','#operation':'operation'},
    ExpressionAttributeValues:{':data':{S:JSON.stringify(data)}}
  };
}
export async function replaceAfterStopAcknowledged(stop,provision){
  await stop();
  return provision();
}
