import {useLayoutEffect, useRef} from 'react';
import {client} from '../../api/session';
import {chatAPI, chatRoute, mountChatReader} from './chat-reader.js';
import {ChatReaderView} from './ChatReaderView';
import '../../../../design/prototype/omni-relay-split-pane.js';
import '../../../../design/prototype/omni-relay-bot-chat.css';
import './chat-reader.css';

export function ChatPage({hash, projectId = null}) {
  const host = useRef(null);
  useLayoutEffect(() => {
    const controller = new AbortController();
    const base = projectId ? '#/projects/' + projectId + '/chats' : '#/feishu/bot-chats';
    const route = projectId ? hash.split('?')[0].slice(base.length + 1).split('/') : null;
    const {chatId,messageId} = route ? {chatId:route[0],messageId:route[1]} : chatRoute(hash);
    const api = chatAPI(client.bind(), projectId ? '/api/v1/projects/' + projectId + '/chat-reader' : undefined);
    if (projectId) delete api.refresh;
    let current = hash;
    mountChatReader(host.current, api, {signal:controller.signal,project:Boolean(projectId),projectId,draftScope:client.scope,chatId,messageId,
      onLocation(chat,message) {
        if (controller.signal.aborted || location.hash !== current) return;
        current = [base,chat,message].filter(Boolean).join('/');
        history.replaceState(history.state,'',current);
      }
    });
    host.current.querySelector('#page-heading')?.focus({preventScroll:true});
    return () => controller.abort();
  }, [hash, projectId]);
  const reader=<ChatReaderView project={Boolean(projectId)}/>;
  return projectId ? <div ref={host} className="project-chat-reader">{reader}</div> : <main ref={host} id="page" className="page" data-layout="bot-chat">{reader}</main>;
}
