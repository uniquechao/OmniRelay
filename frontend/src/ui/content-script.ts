const scripts=new Map<string,Promise<void>>();
export function loadContentScript(url:string) {
  const existing=scripts.get(url);
  if(existing) return existing;
  const loading=new Promise<void>((resolve,reject)=>{
    const tag=document.createElement('script');
    tag.src=url;
    tag.onload=()=>resolve(undefined);
    tag.onerror=()=>{scripts.delete(url);tag.remove();reject(new Error('内容组件加载失败。'));};
    document.head.append(tag);
  });
  scripts.set(url,loading);
  return loading;
}
