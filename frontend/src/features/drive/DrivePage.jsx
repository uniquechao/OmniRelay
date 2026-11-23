import {useLayoutEffect, useRef} from 'react';
import {client} from '../../api/session';
import {categories, mountDrive} from './drive.js';
import '../../../../design/prototype/omni-relay-drive.css';

export function DrivePage({projectId = null, initialQuery = '', memory = client.scope.drive}) {
  const root = useRef(null);
  useLayoutEffect(() => {
    const controller = new AbortController();
    mountDrive(root.current, client.bind(), controller.signal, {projectId, initialQuery, memory});
    root.current.querySelector('#page-heading')?.focus({preventScroll:true});
    return () => controller.abort();
  }, [projectId, initialQuery, memory]);
  const Host = projectId ? 'div' : 'main';
  return <Host ref={root} className={projectId ? 'project-drive-page' : 'page'} id={projectId ? undefined : 'page'} data-layout={projectId ? undefined : 'list'}>
    {!projectId && <><header className="head"><h1 id="page-heading" tabIndex={-1}>文件网盘</h1></header>
      <section className="drive-sync" aria-label="文件同步进度"><div data-drive-sync-loader=""/><p data-drive-sync="" role="status"/></section></>}
    <div className="drive-categories" role="group" aria-label="文件分类">{Object.entries(categories).map(([key,label]) =>
      <button key={key} type="button" className="button" aria-controls="drive-file-list" data-drive-category={key}>{label}</button>)}</div>
    <p data-partial="" role="status" hidden>部分数据暂不可用。</p><section id="drive-file-list"/>
  </Host>;
}
