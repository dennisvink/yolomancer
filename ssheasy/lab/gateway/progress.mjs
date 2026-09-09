// Fixed messages only: never expose command output, codes, or credentials.
const stages={
  identity:[2,'Configuring SSH identity'],
  clone:[3,'Updating Yolomancer source'],
  dependencies:[4,'Checking dependencies and downloading updates'],
  build:[5,'Rebuilding Yolomancer with cached dependencies'],
  register:[6,'Registering Yolomancer and configuring AWS credentials'],
  ssh:[7,'Starting SSH server'],
  ready:[8,'Connecting to your lab']
};
export function validStage(stage){return Object.hasOwn(stages,stage)||stage==='failed';}
export function progressFor(task,stage,operation){
  if(operation)return {message:operation==='reinstall'?'Stopping old lab before reinstalling':'Stopping lab',indeterminate:true};
  if(stage==='failed')return {message:'Workspace setup failed. Reinstall the lab to retry.',failed:true};
  if(task?.lastStatus==='STOPPED')return {message:'Workspace stopped. Reinstall the lab to retry.',failed:true};
  if(stages[stage])return {step:stages[stage][0],total:8,message:stages[stage][1]};
  return {step:1,total:8,message:task?.pullStartedAt&&!task.pullStoppedAt?'Downloading workspace image and preinstalled tools':'Provisioning workspace'};
}
