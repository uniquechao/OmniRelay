import {useLayoutEffect,useRef,type RefObject} from 'react';
import {client} from '../../api/session';
import type {Guard,User} from '../workbench/Account';
import {bindAdmin} from './admin.js';
import {systemAPI} from '../../api/system-api.js';

export function UsersPage({user,guard,section='users'}:{user:User;guard:RefObject<Guard>;section?:'users'|'roles'}) {
  const root = useRef<HTMLElement>(null);
  const sessionUser = useRef(user);
  useLayoutEffect(()=>{
    const page = root.current!;
    const lifetime = new AbortController();
    const signal = lifetime.signal;
    void bindAdmin(page,section,systemAPI(client.bind()),{user:sessionUser.current,signal}).then(view=>{
      if (signal.aborted) return;
      guard.current = view;
      page.querySelector<HTMLElement>('h1')?.focus({preventScroll:true});
    });
    return ()=>{lifetime.abort();guard.current={};};
  },[guard,section]);
  return <main className="page" id="page" data-layout="list" ref={root}>
    <header className="head admin-heading"><h1 id="page-heading" tabIndex={-1}>{section === 'users' ? '用户管理' : '角色权限'}</h1>
      {section === 'users' && <div className="system-heading-actions"><button className="button primary" type="button" data-directory-sync="" title="同步飞书组织与用户">同步</button>
        <button className="button" type="button" data-directory-details="">同步详情</button></div>}
    </header>
    {section === 'roles' ? <section id="system-list" className="or-loading-placeholder"/> : <>
    <div className="list-toolbar-line" data-directory-notice="" hidden><span className="aux" role="status"/>
      <button className="button" type="button" data-directory-retry="" hidden>重试</button></div>
    <div className="directory-layout"><details className="directory-org"><summary>组织结构</summary>
      <div className="directory-tree" data-directory-tree=""/></details>
      <section id="system-list" className="or-loading-placeholder"/></div></>}
  </main>;
}
