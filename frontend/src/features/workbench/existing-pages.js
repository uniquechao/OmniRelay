import {client} from '../../api/session';
import {projectRoute,mountProjects} from '../../../../web/projects.js';
import {chatRoute,chatAPI,mountChatReader} from '../../../../web/chat-reader.js';
import {mountDrive} from '../../../../web/drive.js';
import '../../../../design/prototype/omni-relay-split-pane.js';
import '../../../../design/prototype/omni-relay-bot-chat.css';
import '../../../../web/chat-reader.css';
import '../../../../design/prototype/omni-relay-drive.css';
import '../../../../design/prototype/omni-relay-project.css';
import '../../../../design/prototype/omni-relay-task-editor.css';

export async function mountExistingPage(host,target,user,guard,signal){
  const api=client.bind();
  const scope=client.scope;
  const hash=target.path.slice(1);
  switch(target.route.id){
    case 'projects':
      await mountProjects(host,projectRoute(hash),api,{signal,memory:scope.projects,scope,navigation:guard.current});
      break;
    case 'drive':
      mountDrive(host,api,signal,{memory:scope.drive});
      break;
    case 'bot-chats': {
      const {chatId,messageId}=chatRoute(hash);
      mountChatReader(host,chatAPI(api),{signal,draftScope:scope,chatId,messageId,onLocation(chat,message){
        if(signal.aborted)return;
        const next=chatRoute('#/feishu/bot-chats'+(chat?'/'+chat:'')+(message?'/'+message:''));
        if(next)history.replaceState(history.state,'',next.hash);
      }});
      break;
    }
    default: throw new Error('页面未注册。');
  }
}
