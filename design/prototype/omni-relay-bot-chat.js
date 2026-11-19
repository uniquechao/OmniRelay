/* Local conversations for the Bot chat history prototype. */
window.OmniRelayBotChat = (() => {
  const { escape,icon,personAvatar,personButton,personView,mountPersonCards,openDialog,toast } = OmniRelayUI;
  const bot = 'OmniRelay';
  const sentMessages = new Map();
  const message = (at,author,text,card) => ({ at,author,text,card });
  const records = [
    {
      id:'beichen', kind:'group', title:'北辰交付项目群', project:'北辰交付项目', projectId:'beichen',
      messages:[
        message('2026-11-17 17:42','陈悦','测试环境还在恢复，接口联调先等我确认。'),
        message('2026-11-17 17:43','王宁','收到，我先核对验收清单，等你更新环境状态。'),
        message('2026-11-18 17:36','王宁','@OmniRelay 本周有哪些验收任务还没完成？'),
        message('2026-11-18 17:36',bot,'还有 3 项与本周验收有关。接口联调目前在等测试环境恢复。',{
          type:'任务查询', title:'本周验收任务', items:[
            ['接口联调验收','王宁 · 11 月 20 日'],['测试环境恢复','陈悦 · 已阻塞'],['验收用例确认','赵可 · 进行中']
          ], tab:'tasks', link:'查看项目任务'
        }),
        message('2026-11-18 17:48','王宁','@OmniRelay 把验收用例确认分派给赵可，11 月 20 日前完成。'),
        message('2026-11-18 17:48',bot,'已分派，赵可可以在项目任务中查看。',{
          type:'任务分派', title:'验收用例确认', status:'已分派',
          items:[['负责人','赵可'],['截止日期','11 月 20 日']], tab:'tasks',link:'查看项目任务'
        }),
        message('2026-11-18 18:00',bot,'北辰交付项目的每日简报已发布。',{
          type:'项目简报', title:'11 月 18 日 · 项目每日简报',
          items:[['进展','权限方案评审、回滚预案演练已完成'],['阻塞','测试环境尚未恢复，接口联调仍在等待']],
          tab:'brief',link:'查看项目简报'
        })
      ]
    },
    {
      id:'qa', kind:'group', title:'北辰联调协作群', projectId:'beichen', projectOnly:true,
      messages:[
        {...message('2026-11-18 14:25','林晓','灰度方案还差一轮复核，明天上午请测试组一起看。'), task:4},
        {...message('2026-11-18 15:08','赵可','验收用例已更新，等环境就绪就开始验证。'), task:3}
      ]
    },
    { id:'qiming', kind:'group', title:'启明试点项目群', projectId:'qiming', projectOnly:true, messages:[] },
    {
      id:'wang-ning', kind:'direct', title:'王宁', project:'北辰交付项目', projectId:'beichen',
      messages:[
        message('2026-11-18 17:24','王宁','我还有哪些任务在这周到期？'),
        message('2026-11-18 17:24',bot,'北辰交付项目里，你这周需要完成接口联调验收，截止时间是 11 月 20 日。',{
          type:'任务查询', title:'我的近期任务', items:[['接口联调验收','11 月 20 日 · 进行中']],
          tab:'tasks',link:'查看项目任务'
        }),
        message('2026-11-18 17:52','王宁','这项任务现在卡在哪里？'),
        message('2026-11-18 17:52',bot,'联调需要测试环境，陈悦正在跟进恢复时间。')
      ]
    },
    {
      id:'coordination', kind:'group', title:'交付协调群',
      messages:[
        message('2026-11-18 17:39','陈悦','明天联调先检查环境和接口文档，赵可帮忙确认下用例。'),
        message('2026-11-18 17:41','赵可','用例我会核对，环境恢复后我再跑一遍。'),
        message('2026-11-18 17:43','陈悦','@OmniRelay 查一下测试环境恢复进度。'),
        message('2026-11-18 17:43',bot,'这个群还没有关联项目，请先说明要查哪个项目。')
      ]
    },
    {
      id:'chen-yue',kind:'direct',title:'陈悦',project:'北辰交付项目',projectId:'beichen',
      messages:[
        message('2026-11-18 17:19','陈悦','帮我把北辰的简报改到每天 17:30。'),
        message('2026-11-18 17:19',bot,'你可以查看项目简报。修改发布时间需要北辰交付项目的管理员权限，原计划保持 18:00。')
      ]
    },
    {
      id:'lin-xiao',kind:'direct',title:'林晓',project:'启明试点项目',projectId:'qiming',
      messages:[
        message('2026-11-18 16:20','林晓','启明试点首批要做哪些准备？'),
        message('2026-11-18 16:20',bot,'先确认首批参与人员，再整理体验反馈清单。',{
          type:'任务查询',title:'启明试点准备',items:[['确认试点参与人员','林晓 · 11 月 20 日'],['整理体验反馈清单','赵可 · 11 月 23 日']],
          tab:'tasks',link:'查看项目任务'
        })
      ]
    }
  ];

  const people = {
    王宁:['wang-ning','交付中心 / 交付一组'], 陈悦:['chen-yue','研发中心 / 平台组'],
    林晓:['lin-xiao','研发中心 / 产品组'], 赵可:['zhao-ke','交付中心 / 测试组']
  };
  const members = {qiming:['林晓'], qa:['林晓','赵可'], beichen:['王宁','陈悦','赵可',bot], coordination:['王宁','陈悦','赵可',bot]};
  const kindLabels = { group:'飞书群聊', direct:'员工 1:1' };
  const filters = { all:'全部', group:'群聊', direct:'员工 1:1' };
  const last = record => record.messages.at(-1);
  const shortDate = at => Number(at.slice(5,7)) + ' 月 ' + Number(at.slice(8,10)) + ' 日';
  const time = (item, withDate = false) => '<time datetime="' + item.at.replace(' ','T') + ':00+08:00">' + (withDate ? Number(item.at.slice(5,7)) + '/' + Number(item.at.slice(8,10)) + ' ' : '') + item.at.slice(11) + '</time>';
  const messageText = item => [item.author,item.text,item.card?.title,
    ...(item.card?.items.flat() ?? []), ...(item.files?.map(file => file.name) ?? [])].filter(Boolean).join(' ');
  const has = (text,query) => text.toLowerCase().includes(query.toLowerCase());

  function highlight(text,query) {
    if (!query) return escape(text);
    const lower = text.toLowerCase();
    const needle = query.toLowerCase();
    let result = '';
    let start = 0;
    let index = lower.indexOf(needle);
    while (index >= 0) {
      result += escape(text.slice(start,index)) + '<mark>' + escape(text.slice(index,index + query.length)) + '</mark>';
      start = index + query.length;
      index = lower.indexOf(needle,start);
    }
    return result + escape(text.slice(start));
  }

  function avatar(label, kind) {
    if (kind === 'person') return personAvatar(label,people[label]?.[0]);
    const content = kind === 'group' ? icon('nav-team')
      : '<img src="omni-relay-avatar.png" width="32" height="32" alt="">';
    return '<span class="chat-avatar" data-kind="' + kind + '" aria-hidden="true">' + content + '</span>';
  }

  const profileOf = name => ({ name, personKey:people[name]?.[0], org:people[name]?.[1], source:'feishu' });
  const personControl = (name, content, className = '') =>
    personButton(profileOf(name),content,'chat-person-button ' + className);

  function personCopy(name, detail, isPrimary = false) {
    return '<span class="person-copy"><span class="chat-title-name"><strong class="person-name" title="' + escape(name) + '">' + escape(name) +
      '</strong>' + (isPrimary ? '<span class="chat-primary-star" role="img" aria-label="项目主群" title="项目主群">' +
        icon('chat-star-icon') + '</span>' : '') + '</span><span class="person-org" title="' + escape(detail) + '">' + escape(detail) + '</span></span>';
  }

  const tool = (action, symbol, label, disclosure = '') =>
    '<button type="button" class="chat-icon-button" data-chat-action="' + action + '" aria-label="' +
    label + '" title="' + label + '"' + disclosure + '>' + icon(symbol) + '</button>';

  function render(projectId) {
    const scoped = Boolean(projectId);
    return `<div class="bot-chat" data-bot-chat${scoped ? ' data-project-chat' : ''}>
      <section class="chat-directory" id="chat-directory" aria-labelledby="${scoped ? 'project-chat-heading' : 'page-heading'}">
        <div class="chat-directory-tools">
          <div class="chat-directory-title">
            ${scoped ? '<h2 id="project-chat-heading">项目关联群</h2>' : tool('filters','chat-filter-icon','筛选会话',' aria-expanded="false" aria-controls="chat-filters"') + '<h1 id="page-heading" tabindex="-1">机器人记录</h1>'}
            <span data-chat-count role="status"></span>
          </div>
          <div class="chat-sync"${scoped ? ' hidden' : ''}>
            <span><i aria-hidden="true"></i>示例已同步</span>
            <div>${tool('sync-info','chat-info','查看同步详情')}${tool('refresh','chat-refresh','刷新会话')}</div>
          </div>
          <div id="chat-filters" class="chat-filters" role="group" aria-label="会话类型" hidden>
            ${Object.entries(filters).map(([key,label]) => '<button class="chat-filter" type="button" data-chat-filter="' +
              key + '" aria-pressed="' + String(key === 'all') + '">' + label + '</button>').join('')}
          </div>
          <label class="chat-search">${icon('chat-search-icon')}
            <input class="search" type="search" data-chat-search aria-label="${scoped ? '搜索关联群或消息' : '搜索会话或消息'}" placeholder="${scoped ? '搜索关联群或消息' : '搜索会话或消息'}">
          </label>
        </div>
        <ul class="chat-conversations" data-chat-conversations></ul>
        <p class="chat-empty" data-chat-empty hidden>没有匹配的会话。</p>
      </section>
      <div class="split-handle chat-split-handle" role="separator" aria-label="调整会话列表宽度"
        aria-orientation="vertical" aria-controls="chat-directory" tabindex="0" title="左右拖动调整宽度"></div>
      <section class="chat-reading" aria-label="聊天记录">
        <header class="chat-reading-head">
          <button type="button" class="chat-icon-button chat-back" data-chat-back aria-label="返回会话列表">${icon('action-expand')}</button>
          <div class="chat-reading-title" data-chat-title></div>
          <div class="chat-reading-tools" data-chat-reading-tools>
            ${tool('members','nav-team','查看会话成员')}
            ${tool('message-search','chat-search-icon','搜索当前会话',' aria-expanded="false" aria-controls="chat-message-search"')}
            ${scoped ? tool('primary','chat-star-icon','设为项目主群',' hidden aria-haspopup="dialog"') : ''}
            ${tool('project','chat-project-icon','修改项目归属',' hidden aria-haspopup="dialog"')}
          </div>
          <div class="chat-project-meta"${scoped ? ' hidden' : ''}>
            <span class="chip" data-chat-project-empty hidden>未关联项目</span>
            <a data-chat-project hidden></a>
          </div>
        </header>
        <label class="chat-search chat-message-search" id="chat-message-search" hidden>${icon('chat-search-icon')}
          <input class="search" type="search" data-chat-message-search aria-label="搜索当前会话" placeholder="搜索当前会话">
        </label>
        <div class="chat-timeline" data-chat-timeline tabindex="0" aria-label="消息时间线"></div>
        ${OmniRelayChatControls.composer()}
      </section>
    </div>`;
  }

  function cardMarkup(card,record,query) {
    if (!card) return '';
    return '<section class="chat-receipt"><div class="chat-receipt-type"><span>' + escape(card.type) + '</span>' +
      (card.status ? '<span class="chip" data-tone="success">' + escape(card.status) + '</span>' : '') +
      '</div><h3>' + highlight(card.title,query) + '</h3><dl>' +
      card.items.map(([label,value]) => '<div><dt>' + highlight(label,query) + '</dt><dd>' + highlight(value,query) + '</dd></div>').join('') +
      '</dl><a href="#/projects/' + record.projectId + '/' + card.tab + '">' + escape(card.link) + '</a></section>';
  }

  function composedBody(text,query) {
    return text.split(/(\*\*[^*\n]+\*\*|\*[^*\n]+\*)/g).map((part,index) => {
      if (index % 2 === 0) return highlight(part,query);
      const bold = part.startsWith('**');
      const length = bold ? 2 : 1;
      const tag = bold ? 'strong' : 'em';
      return '<' + tag + '>' + highlight(part.slice(length,-length),query) + '</' + tag + '>';
    }).join('');
  }

  function timeline(record,query,filterMessages) {
    let day = '';
    return record.messages.filter(item => !filterMessages || !query || has(messageText(item),query)).map(item => {
      const nextDay = item.at.slice(0,10);
      const date = nextDay === day ? '' : '<li class="chat-day"><span>' + shortDate(item.at) + '</span></li>';
      day = nextDay;
      const fromBot = item.author === bot;
      const picture = avatar(item.author,fromBot ? 'bot' : 'person');
      const name = highlight(item.author,query);
      return date + '<li class="chat-message" data-author="' + (fromBot ? 'bot' : item.sent ? 'self' : 'person') + '"' +
        (item.id ? ' data-chat-message="' + escape(item.id) + '" tabindex="-1"' : '') + '>' +
        (fromBot || item.sent ? picture : personControl(item.author,picture)) + '<div class="chat-message-body">' +
        '<div class="chat-message-meta">' + (fromBot || item.sent ? '<strong>' + name + '</strong>' : personControl(item.author,name,'person-name')) +
        (fromBot ? '<span class="chat-bot-label">Bot</span>' : '') + time(item,true) + '</div>' +
        (item.fileFormat ? '<div class="chat-bubble chat-file">' + OmniRelayDrive.fileIcon(item.fileFormat) + '<div><strong>' +
          highlight(item.text,query) + '</strong><span class="aux">' + escape(item.fileFormat) + ' · ' + escape(item.fileSize) + '</span></div></div>' :
          item.text ? '<p class="chat-bubble">' + (item.sent ? composedBody(item.text,query) : highlight(item.text,query)) + '</p>' : '') +
        (item.files || []).map(file => '<div class="chat-bubble chat-file">' + icon('nav-logs') + '<div><strong>' +
          highlight(file.name,query) + '</strong><span class="aux">演示附件</span></div></div>').join('') + cardMarkup(item.card,record,query) +
        (item.task ? '<a class="chat-task-link" href="#/projects/' + record.projectId + '/tasks/' + item.task + '">查看关联任务</a>' : '') + '</div></li>';
    }).join('');
  }

  function mount(page, projects, scope, subview, sourceMessage) {
    const root = page.querySelector('[data-bot-chat]');
    const { connectSplitPane, mountComposer } = OmniRelayChatControls;
    connectSplitPane(root,{
      storageKey:scope ? 'omni-relay.project-chat-width' : 'omni-relay.bot-chat-width',
      defaultRatio:scope ? .32 : .34, minDirectory:scope ? 178 : 235
    });
    const search = root.querySelector('[data-chat-search]');
    const list = root.querySelector('[data-chat-conversations]');
    const count = root.querySelector('[data-chat-count]');
    const title = root.querySelector('[data-chat-title]');
    const transcript = root.querySelector('[data-chat-timeline]');
    const narrow = matchMedia('(max-width: 720px)');
    const messageSearch = root.querySelector('[data-chat-message-search]');
    const messageSearchField = messageSearch.closest('label');
    const readingTools = root.querySelector('[data-chat-reading-tools]');
    const projectLink = root.querySelector('[data-chat-project]');
    const projectEmpty = root.querySelector('[data-chat-project-empty]');
    const projectMeta = projectLink.parentElement;
    const projectButton = root.querySelector('[data-chat-action="project"]');
    const primaryButton = root.querySelector('[data-chat-action="primary"]');
    const storageKey = 'omni-relay.chat-projects';
    const mainKey = 'omni-relay.chat-primary';
    const groupProjects = new Map();
    const groupPrimary = new Map();
    const conversations = records.map(record => ({
      ...record,
      messages:[...record.messages,...OmniRelayDrive.messagesForGroup(record.id),...(sentMessages.get(record.id) || [])].sort((a,b) => a.at.localeCompare(b.at))
    }));
    const groupRecords = conversations.filter(item => item.kind === 'group');
    const sourceGroup = sourceMessage && groupRecords.find(record =>
      record.id === subview && record.messages.some(message => message.id === sourceMessage));
    try {
      const saved = JSON.parse(localStorage.getItem(storageKey) || '{}');
      for (const record of groupRecords) {
        if (Object.hasOwn(saved,record.id) && (saved[record.id] === '' || Object.hasOwn(projects,saved[record.id]))) {
          groupProjects.set(record.id,saved[record.id]);
        }
      }
    } catch {}
    const projectId = record => groupProjects.get(record.id) ?? record.projectId ?? '';
    try {
      const saved = JSON.parse(localStorage.getItem(mainKey) || '{}');
      for (const id of Object.keys(projects)) {
        if (typeof saved[id] === 'string') groupPrimary.set(id,saved[id]);
      }
    } catch {}
    function primaryOf(id) {
      const group = groupPrimary.get(id) ?? (id === 'beichen' ? 'beichen' : '');
      const record = groupRecords.find(item => item.id === group);
      return record && projectId(record) === id ? group : '';
    }
    function saveMap(key, value) {
      try { localStorage.setItem(key,JSON.stringify(Object.fromEntries(value))); return true; } catch { return false; }
    }
    const scopedRecords = () => scope ? groupRecords.filter(record => projectId(record) === scope)
      : conversations.filter(record => !record.projectOnly || record === sourceGroup);
    function updateProjectRoute(record) {
      if (!scope) return;
      const suffix = record ? '/' + (record.id === 'beichen' ? 'main' : record.id) : '';
      window.history.replaceState(null,'','#/projects/' + scope + '/chats' + suffix);
    }

    function renderProjectTag(record) {
      const id = record ? projectId(record) : '';
      const isGroup = record?.kind === 'group';
      projectButton.hidden = !isGroup;
      const label = id ? '修改项目归属' : '标记项目归属';
      projectButton.title = label;
      projectButton.setAttribute('aria-label',label);
      if (primaryButton) {
        const isPrimary = isGroup && record.id === primaryOf(scope);
        primaryButton.hidden = !isGroup;
        primaryButton.setAttribute('aria-disabled',String(isPrimary));
        primaryButton.setAttribute('aria-label',isPrimary ? '已设为项目主群' : '设为项目主群');
        primaryButton.title = isPrimary ? '已设为项目主群' : '设为项目主群';
      }
      projectMeta.hidden = Boolean(scope) || !isGroup;
      projectEmpty.hidden = Boolean(id);
      projectLink.hidden = !id;
      if (id) {
        projectLink.textContent = projects[id].name;
        projectLink.href = '#/projects/' + id + '/overview';
      }
    }

    mountPersonCards(root);

    let filter = 'all';
    const initialGroup = subview === 'main' ? 'beichen' : subview;
    let selected = scopedRecords().find(record => record.id === initialGroup) || scopedRecords()[0];
    if ((scope && subview && selected) || sourceGroup) root.dataset.detailOpen = '';
    let refreshed;
    const sendTime = new Intl.DateTimeFormat('sv-SE',{
      timeZone:'Asia/Singapore',year:'numeric',month:'2-digit',day:'2-digit',hour:'2-digit',minute:'2-digit'
    });
    const compose = mountComposer(root,(record,content) => {
      const item = {at:sendTime.format(new Date()),author:'我',...content,sent:true};
      const pending = sentMessages.get(record.id) || [];
      pending.push(item);
      sentMessages.set(record.id,pending);
      record.messages.push(item);
      messageSearch.value = '';
      search.value = '';
      paintList();
      if (selected === record) transcript.scrollTop = transcript.scrollHeight;
    });

    function updateTranscript(record) {
      const localQuery = messageSearchField.hidden ? '' : messageSearch.value.trim();
      const query = localQuery || search.value.trim();
      const items = timeline(record,query,Boolean(localQuery));
      transcript.innerHTML = items ? '<ol class="chat-messages">' + items + '</ol>'
        : '<p class="chat-empty">当前会话没有匹配的消息。</p>';
      const match = transcript.querySelector('mark');
      transcript.scrollTop = match ? match.closest('.chat-message').offsetTop - transcript.clientHeight / 4 : transcript.scrollHeight;
    }

    function showConversation(record) {
      if (selected !== record) messageSearch.value = '';
      selected = record;
      for (const button of list.querySelectorAll('[data-chat-id]')) {
        if (button.dataset.chatId === record?.id) button.setAttribute('aria-current','true');
        else button.removeAttribute('aria-current');
      }
      const person = record?.kind === 'direct';
      readingTools.hidden = !record;
      compose.show(record, !record ? [] : person ? [record.title,bot] : members[record.id]);
      renderProjectTag(record);
      if (!record) {
        title.innerHTML = '<h2 tabindex="-1">聊天记录</h2>';
        transcript.innerHTML = '<p class="chat-empty">没有可显示的会话。</p>';
        return;
      }
      const groupRole = scope && primaryOf(scope) === record.id ? '项目主群' : '群聊';
      const identity = person ? personView(profileOf(record.title)) : '<span class="person-view">' + avatar(record.title,'group') +
        personCopy(record.title,groupRole + ' · ' + members[record.id].length + ' 人',groupRole === '项目主群') + '</span>';
      title.innerHTML = '<h2 tabindex="-1" aria-label="' + escape(record.title) + '">' + identity + '</h2>';
      updateTranscript(record);
    }

    function paintList() {
      const query = search.value.trim();
      const visible = scopedRecords().filter(record => (filter === 'all' || record.kind === filter) &&
        (!query || has(record.title + ' ' + (projects[projectId(record)]?.name ?? ''),query) || record.messages.some(item => has(messageText(item),query))))
        .sort((a,b) => last(b).at.localeCompare(last(a).at));
      list.innerHTML = visible.map(record => {
        const recent = last(record);
        const hit = query && record.messages.findLast(item => has(messageText(item),query));
        const groupRole = scope && primaryOf(scope) === record.id ? '项目主群' : '群聊';
        const groupLabel = groupRole + ' · ' + (members[record.id]?.length || 0) + ' 人';
        const isDirect = record.kind === 'direct';
        const name = highlight(record.title,query);
        const org = people[record.title]?.[1] || kindLabels.direct;
        const preview = hit ? highlight(messageText(hit),query) : isDirect ? '1:1 · ' + escape(org) : groupLabel;
        const previewLine = '<span class="chat-conversation-preview person-org" title="' +
          escape(isDirect ? org : groupLabel) + '">' + preview + '</span>';
        if (isDirect) {
          return '<li class="chat-direct-row">' + personControl(record.title,avatar(record.title,'person')) +
            '<div class="chat-conversation-copy"><div class="chat-conversation-top">' +
            personControl(record.title,name,'person-name') + time(recent,true) + '</div>' +
            '<button class="chat-conversation chat-direct-preview" type="button" data-chat-id="' + record.id +
            '" aria-label="打开与' + escape(record.title) + '的会话">' + previewLine + '</button></div></li>';
        }
        return '<li><button class="chat-conversation" type="button" data-chat-id="' + record.id + '">' +
          avatar(record.title,'group') + '<span class="chat-conversation-copy"><span class="chat-conversation-top">' +
          '<strong class="person-name" title="' + escape(record.title) + '">' + name + '</strong>' + time(recent,true) +
          '</span>' + previewLine + '</span></button></li>';
      }).join('');
      const empty = root.querySelector('[data-chat-empty]');
      empty.hidden = Boolean(visible.length);
      empty.textContent = scope && !query ? '暂无项目关联群。' : '没有匹配的会话。';
      count.textContent = visible.length;
      count.setAttribute('aria-label',visible.length + ' 个会话');
      showConversation(visible.includes(selected) ? selected : visible[0]);
    }

    search.addEventListener('input',paintList);
    messageSearch.addEventListener('input',() => { if (selected) updateTranscript(selected); });
    const actions = {
      filters(button) {
        const group = root.querySelector('#chat-filters');
        group.hidden = !group.hidden;
        button.setAttribute('aria-expanded',String(!group.hidden));
      },
      'sync-info'() {
        openDialog({title:'会话同步详情',drawer:'standard',
          body:'<dl class="facts"><div><dt>状态</dt><dd>示例已同步</dd></div><div><dt>会话数</dt><dd>' + scopedRecords().length +
            '</dd></div><div><dt>最近消息</dt><dd>11 月 18 日 18:00</dd></div></dl>' +
            (refreshed ? '<p class="aux chat-sync-time">本次刷新 ' + escape(refreshed) + '</p>' : '')});
      },
      refresh() {
        refreshed = new Date().toLocaleTimeString('zh-CN',{hour:'2-digit',minute:'2-digit'});
        paintList();
        toast('success','示例会话已刷新');
      },
      members() {
        if (!selected) return;
        const participants = selected.kind === 'group' ? members[selected.id] : [selected.title,bot];
        const dialog = openDialog({title:'会话成员',description:selected.title,drawer:'standard',body:
          '<ul class="chat-members">' + participants.map(name => {
            const identity = name === bot ? '<span class="person-view">' + avatar(name,'bot') +
              personCopy(name,'会话机器人') + '</span>' : personView(profileOf(name));
            return '<li>' + identity + '</li>';
          }).join('') + '</ul>'});
        mountPersonCards(dialog);
      },
      primary(trigger) {
        const record = selected;
        if (!scope || !record || primaryOf(scope) === record.id) return;
        const before = groupRecords.find(item => item.id === primaryOf(scope));
        openDialog({title:'设为项目主群',description:record.title,variant:'confirm',confirm:'确认设置',
          body:'<p class="dialog-note">' + (before ? '主群将从「' + escape(before.title) + '」更换为当前群。' : '将当前群设为项目主群。') + '</p>',
          onSubmit() {
            groupPrimary.set(scope,record.id);
            const persisted = saveMap(mainKey,groupPrimary);
            toast(persisted ? 'success' : 'info',persisted ? '项目主群已更新' : '项目主群已更新，仅本次浏览有效');
          },
          onClose(confirmed) { if (confirmed) paintList(); },
          restoreFocus:(confirmed) => confirmed ? projectButton : trigger});
      },
      project() {
        const record = selected;
        if (record?.kind !== 'group') return;
        const current = projectId(record);
        const { combo, mountCombo } = OmniRelayDeveloper;
        const choices = Object.entries(projects).map(([id,project]) => ({id,label:project.name}));
        const dialog = openDialog({title:'修改项目归属',description:record.title,variant:'form',confirm:'保存',
          body:combo('chat-project-picker','所属项目','projects'),
          isDirty:form => form.querySelector('[data-combo-result]').value !== current,
          onSubmit(form) {
            const next = form.querySelector('[data-combo-result]').value;
            if (next === current) return;
            const wasPrimary = current && primaryOf(current) === record.id;
            groupProjects.set(record.id,next);
            let persisted = saveMap(storageKey,groupProjects);
            if (wasPrimary) {
              groupPrimary.set(current,'');
              persisted = saveMap(mainKey,groupPrimary) && persisted;
            }
            toast(persisted ? 'success' : 'info',persisted ? (next ? '群聊项目归属已保存' : '群聊项目归属已清除') : '群聊项目归属已更新，仅本次浏览有效');
          },
          onClose(confirmed) {
            if (!confirmed) return;
            if (scope && projectId(record) !== scope) {
              paintList();
              updateProjectRoute(selected);
            } else renderProjectTag(record);
          },
          restoreFocus:() => projectButton.hidden ? search : projectButton});
        const picker = dialog.querySelector('[data-combo]');
        mountCombo(picker,{choices,initial:current});
        picker.querySelector('[role="combobox"]').focus();
      },
      'message-search'(button) {
        messageSearchField.hidden = !messageSearchField.hidden;
        button.setAttribute('aria-expanded',String(!messageSearchField.hidden));
        if (messageSearchField.hidden) {
          messageSearch.value = '';
          if (selected) updateTranscript(selected);
        } else messageSearch.focus();
      }
    };
    root.addEventListener('click',event => {
      const action = event.target.closest('[data-chat-action]');
      if (action) actions[action.dataset.chatAction](action);
      const filterButton = event.target.closest('[data-chat-filter]');
      if (filterButton) {
        filter = filterButton.dataset.chatFilter;
        for (const button of root.querySelectorAll('[data-chat-filter]')) {
          button.setAttribute('aria-pressed',String(button === filterButton));
        }
        paintList();
      }
      const conversation = event.target.closest('[data-chat-id]');
      if (conversation) {
        root.dataset.detailOpen = '';
        showConversation(scopedRecords().find(record => record.id === conversation.dataset.chatId));
        updateProjectRoute(selected);
        if (sourceGroup && selected && selected !== sourceGroup) {
          window.history.replaceState(null,'','#/feishu/bot-chats/' + selected.id);
        }
        if (narrow.matches) title.querySelector('h2').focus({preventScroll:true});
      }
      if (event.target.closest('[data-chat-back]')) {
        delete root.dataset.detailOpen;
        (list.querySelector('[aria-current]') ?? search).focus();
      }
    });
    paintList();
    if (sourceGroup) {
      const target = [...transcript.querySelectorAll('[data-chat-message]')].find(item => item.dataset.chatMessage === sourceMessage);
      if (target) {
        target.dataset.sourceMessage = '';
        requestAnimationFrame(() => {
          if (!target.isConnected) return;
          const offset = target.getBoundingClientRect().top - transcript.getBoundingClientRect().top;
          transcript.scrollTop += offset - Math.min(48,transcript.clientHeight / 4);
          target.focus({preventScroll:true});
        });
      }
    }
  }

  return { render,mount };
})();
