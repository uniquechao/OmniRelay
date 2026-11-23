import {useLayoutEffect, useRef, useState} from 'react';
import {createPortal} from 'react-dom';
import {client} from '../../api/session';
import {categories, mountDrive} from './drive.js';
import '../../../../design/prototype/omni-relay-drive.css';

function MeetingMark({projectId, row, signal, refresh}) {
  const [busy,setBusy]=useState(false);
  if (row.marked_minutes) return <small>已标记为会议纪要</small>;
  async function mark() {
    setBusy(true);
    try {
      await client.bind()('/api/v1/projects/'+encodeURIComponent(projectId)+'/meetings/mark',{method:'PUT',body:{file_ref:row.file_ref},signal});
      if (!signal.aborted) await refresh();
    } catch (err) {
      if (!signal.aborted) window.OmniRelayUI.toast('error',[403,404].includes(err.status)?'文件或项目权限已变化。':'标记失败，请重试。');
    } finally {
      if (!signal.aborted) setBusy(false);
    }
  }
  return <button type="button" className="row-action" disabled={busy} onClick={mark}>{busy?'正在标记…':'标记为会议纪要'}</button>;
}

export function DrivePage({projectId = null, initialQuery = '', memory = client.scope.drive}) {
  const root = useRef(null);
  const [meetingRows,setMeetingRows]=useState([]);
  useLayoutEffect(() => {
    const controller = new AbortController();
    mountDrive(root.current, client.bind(), controller.signal, {projectId, initialQuery, memory, onMeetingRows:projectId?setMeetingRows:undefined});
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
    {meetingRows.map(slot=>createPortal(<MeetingMark projectId={projectId} {...slot}/>,slot.host,slot.row.file_ref))}
  </Host>;
}
