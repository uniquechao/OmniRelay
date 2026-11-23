import {useId,type ComponentProps} from 'react';

function Tool({action,icon,label,className='',...props}:{action:string;icon:string;label:string} & ComponentProps<'button'>) {
  return <button className={('chat-icon-button '+className).trim()} type="button" data-action={action} title={label} aria-label={label} {...props}>
    <svg className="icon" aria-hidden="true"><use href={'#'+icon}/></svg></button>;
}
function Search({id,name,label,hidden}:{id:string;name:string;label:string;hidden?:boolean}) {
  return <label className={'chat-search'+(hidden?' chat-message-search':'')} id={id} hidden={hidden}>
    <svg className="icon" aria-hidden="true"><use href="#chat-search-icon"/></svg>
    <input className="search" name={name} type="search" autoComplete="off" maxLength={200} aria-label={label} placeholder={label}/></label>;
}
export function ChatReaderView({project}:{project:boolean}) {
  const id=useId(),headingId=project?id+'-heading':'page-heading',Heading=project?'h2':'h1';
  return <div className="chat-reader-host"><div className="bot-chat" data-project-chat={project?'':undefined}>
    <section className="chat-directory" id={id+'-directory'} aria-labelledby={headingId}><div className="chat-directory-tools">
      <div className="chat-directory-title">
        {!project && <Tool action="filters" icon="chat-filter-icon" label="筛选会话" aria-expanded="false" aria-controls={id+'-filters'}/>}
        <Heading id={headingId} tabIndex={-1}>{project?'项目关联群':'机器人记录'}</Heading><span data-count="" role="status"/>
      </div>
      <div className="chat-sync" hidden={project}><span data-sync="" role="status"/><div>
        <Tool action="sync" icon="chat-info" label="查看同步详情" aria-haspopup="dialog"/>
        <button className="button chat-page-action" type="button" data-action="retry-sync" hidden>重试同步</button>
        <Tool action="refresh" icon="chat-refresh" label="刷新会话"/>
      </div></div>
      <div id={id+'-filters'} className="chat-filters" role="group" aria-label="会话类型" hidden>
        {Object.entries({'':'全部',group:'群聊',p2p:'1:1'}).map(([kind,label])=><button key={kind} className="chat-filter" type="button" data-kind={kind} aria-pressed={!kind}>{label}</button>)}
      </div><Search id={id+'-directory-search'} name="directory" label={project?'搜索关联群或消息':'搜索会话或消息'}/>
    </div><div className="chat-conversations reader-directory" data-directory=""/></section>
    <div className="split-handle chat-split-handle" role="separator" aria-label="调整会话列表宽度" aria-orientation="vertical"
      aria-controls={id+'-directory'} tabIndex={0} title="左右拖动调整宽度"/>
    <section className="chat-reading" aria-label="聊天记录"><header className="chat-reading-head">
      <Tool action="back" className="chat-back" icon="action-expand" label="返回会话列表"/>
      <h2 className="chat-reading-title" data-title="" tabIndex={-1}>聊天记录</h2>
      <div className="chat-reading-tools" data-tools="" hidden>
        <Tool action="members" icon="nav-team" label="会话成员" aria-haspopup="dialog"/>
        <Tool action="search" icon="chat-search-icon" label="搜索当前会话" aria-expanded="false" aria-controls={id+'-messages'}/>
        {project && <Tool action="primary" icon="chat-star-icon" label="设为项目主群" data-chat-action="primary" hidden aria-haspopup="dialog"/>}
        <Tool action="project" icon="chat-project-icon" label="修改项目归属" hidden aria-haspopup="dialog"/>
      </div><div className="chat-project-meta" hidden/>
    </header><Search id={id+'-messages'} name="messages" label="搜索当前会话" hidden/>
      <div className="chat-timeline" data-timeline="" tabIndex={0} aria-label="消息时间线"><p className="chat-empty">没有可显示的会话。</p></div>
      <div data-composer-host=""/>
    </section></div></div>;
}
