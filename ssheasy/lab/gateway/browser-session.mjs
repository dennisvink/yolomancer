export function tabId(req){
  const value=req.headers['x-lab-tab']??new URL(req.url,'https://lab.yolomancer.com').searchParams.get('tab');
  if(value==null)return '';
  if(typeof value!=='string'||!/^t-[a-f0-9-]{36}$/.test(value))throw Error('Invalid tab');
  return value;
}
export function cookieName(req){const tab=tabId(req);return tab?'lab_'+tab:'lab';}
export function duplicateDestination(req,target){
  if(typeof target!=='string'||!/^t-[a-f0-9-]{36}$/.test(target)||target===tabId(req))throw Error('Invalid duplicate destination');
  const destination={...req,headers:{...req.headers,'x-lab-tab':target}};
  if((req.headers.cookie||'').split(';').some(x=>x.trim().startsWith(cookieName(destination)+'=')))throw Error('Destination tab already exists');
  return destination;
}
export function readCookie(req){
  const name=cookieName(req),cookies=(req.headers.cookie||'').split(';').map(x=>x.trim());
  const value=cookies.find(x=>x.startsWith(name+'='))?.slice(name.length+1);
  // Migrate the former single-session cookie only for explicit migration.
  const legacy=req.headers['x-lab-migrate']==='1'?cookies.find(x=>x.startsWith('lab='))?.slice(4):'';
  return (value??legacy??'').split('.');
}
