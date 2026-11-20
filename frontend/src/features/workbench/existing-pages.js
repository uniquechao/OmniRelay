import {client} from '../../api/session';
import {mountAdmin} from '../../../../web/admin.js';
import {mountFeishuBot} from '../../../../web/feishu-bot.js';
import {mountConnectionSettings} from '../../../../web/connection-settings.js';
import {systemAPI,feishuBotAPI} from '../../../../web/system-api.js';
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
  const admin=systemAPI(api);
  const hash=target.path.slice(1);
  switch(target.route.id){
    case 'feishu-bot':
      guard.current=mountFeishuBot(host,feishuBotAPI(api),{signal,canConfigure:user.permissions?.['system.configure']===true,
        canOperate:user.permissions?.['system.operate']===true,canSyncDirectory:user.permissions?.['directory.sync']===true && user.adminCapabilities?.users===true});
      break;
    case 'settings':
      mountConnectionSettings(host,options=>admin('/api/v1/system/connections',options),signal);
      break;
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
    default: {
      const view=await mountAdmin(host,target.route.id,admin,{user,signal});
      if(!signal.aborted)guard.current=view ?? {};
    }
  }
}
