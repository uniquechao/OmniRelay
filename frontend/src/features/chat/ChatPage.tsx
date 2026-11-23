import {useLayoutEffect, useRef} from 'react';
import {client} from '../../api/session';
import {chatRoute, mountChatReader} from './chat-reader';
import {chatAPI} from './chat-api';
import {ChatReaderView} from './ChatReaderView';
import '../../../../design/prototype/omni-relay-bot-chat.css';
import './chat-reader.css';

export function ChatPage({hash, projectId = null}:{hash:string;projectId?:string|null}) {
  const host = useRef<HTMLElement>(null);
  useLayoutEffect(() => {
    const node=host.current;
    if (!node) return;
    const controller = new AbortController();
    const base = projectId ? '#/projects/' + projectId + '/chats' : '#/feishu/bot-chats';
    const route = projectId ? hash.split('?')[0].slice(base.length + 1).split('/') : null;
    const {chatId,messageId} = route ? {chatId:route[0],messageId:route[1]} : chatRoute(hash) ?? {};
    const api = chatAPI(client.bind(), projectId);
    let current = hash;
    mountChatReader(node, api, {signal:controller.signal,project:Boolean(projectId),projectId,draftScope:client.scope,chatId,messageId,
      onLocation(chat,message) {
        if (controller.signal.aborted || location.hash !== current) return;
        current = [base,chat,message].filter(Boolean).join('/');
        history.replaceState(history.state,'',current);
      }
    });
    node.querySelector<HTMLElement>('#page-heading')?.focus({preventScroll:true});
    return () => controller.abort();
  }, [hash, projectId]);
  const reader=<ChatReaderView project={Boolean(projectId)}/>;
  return projectId ? <div ref={node=>{host.current=node}} className="project-chat-reader">{reader}</div> : <main ref={node=>{host.current=node}} id="page" className="page" data-layout="bot-chat">{reader}</main>;
}
