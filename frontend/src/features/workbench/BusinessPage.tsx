import {useLayoutEffect,useRef,type RefObject} from 'react';
import {mountExistingPage} from './existing-pages.js';
import type {Guard,User} from './Account';
import type {resolveTarget} from '../../routes/targets';
export type Target=NonNullable<ReturnType<typeof resolveTarget>>;
export function BusinessPage({target,user,guard,layout}:{target:Target;user:User;guard:RefObject<Guard>;layout:string}){
  const root=useRef<HTMLElement>(null);
  const userRef=useRef(user);userRef.current=user;
  useLayoutEffect(()=>{
    const host=root.current!;
    const lifetime=new AbortController();
    const {signal}=lifetime;
    void mountExistingPage(host,target,userRef.current,guard,signal).then(()=>{
      if(!signal.aborted)host.querySelector<HTMLElement>('#page-heading')?.focus({preventScroll:true});
    }).catch(()=>{
      if(!signal.aborted)host.textContent='页面暂时无法加载，请刷新后重试。';
    });
    return ()=>{lifetime.abort();guard.current={};host.replaceChildren();};
  },[target.path,guard]);
  return <main id="page" className="page" data-layout={layout} ref={root}/>;
}
