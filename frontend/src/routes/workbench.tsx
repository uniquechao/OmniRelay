import {useLayoutEffect,useRef,useState} from 'react';
import {createRoot} from 'react-dom/client';
import {QueryClientProvider} from '@tanstack/react-query';
import {client,useSession} from '../api/session';
import {Workbench,isLogin} from '../features/workbench/Workbench';
import {Login} from '../features/workbench/Login';
import {Button, Loading} from '../ui/components/Elements';
import {remember} from './targets';

window.addEventListener('vite:preloadError', () => {
  if (document.getElementById('chunk-recovery')) return;
  const notice = document.createElement('aside');
  notice.id = 'chunk-recovery';
  notice.setAttribute('role', 'alert');
  notice.textContent = '页面资源加载失败。请保留未保存的内容后刷新。';
  const retry = document.createElement('button');
  retry.textContent = '刷新页面';
  retry.onclick = () => location.reload();
  notice.append(retry);
  document.body.prepend(notice);
  retry.focus();
});

function SessionLock({revoked}:{revoked:boolean}){
  const ref=useRef<HTMLDialogElement>(null);
  useLayoutEffect(()=>{const dialog=ref.current!;dialog.showModal();return ()=>dialog.close();},[]);
  return <dialog className="session-lock" ref={ref} onCancel={e=>e.preventDefault()}><section id="session-status"><p role="alert">{revoked?'工作台访问权限已撤回。':'暂时无法核验身份，连接恢复后将重新检查。'}</p>
    <Button onClick={()=>void client.verify()}>重试</Button></section></dialog>;
}
function Anonymous(){
  const [hash,setHash]=useState(location.hash);
  useLayoutEffect(()=>{
    function route(){if(!isLogin(location.hash)){
      remember('/'+location.hash);
      history.replaceState(history.state,'','#/login');
    }setHash(location.hash);}
    route();document.title='登录工作台 · OmniRelay';
    window.addEventListener('hashchange',route);
    return ()=>window.removeEventListener('hashchange',route);
  },[]);
  return <Login key={hash} hash={hash}/>;
}
function App(){
  const {queries,phase,revision}=useSession();
  if(phase==='checking')return <Loading size="page" label="正在检查会话…"/>;
  if(phase==='anonymous')return <Anonymous/>;
  const locked=phase!=='active';
  return <QueryClientProvider client={queries}>
    {client.session && <Workbench key={revision} locked={locked} onLogout={async()=>{
      await client.logout();
      if(client.phase==='anonymous'){remember('');history.replaceState(history.state,'','#/login');}
    }}/>}
    {locked && <SessionLock revoked={phase==='revoked'}/>}
  </QueryClientProvider>;
}
client.watch(window,document);
void client.verify();
createRoot(document.getElementById('root')!).render(<App/>);
