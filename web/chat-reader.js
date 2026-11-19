const base = '/api/v1/feishu/chats';
const chatHash = '#/feishu/bot-chats';
const identifier = /^[A-Za-z0-9_-]{1,160}$/;
const routePattern = /^#\/(?:feishu\/)?bot-chats(?:\/([A-Za-z0-9_-]{1,160})(?:\/([A-Za-z0-9_-]{1,160}))?)?$/;
const pageSize = 50;
const number = new Intl.NumberFormat('zh-CN');
const day = new Intl.DateTimeFormat('zh-CN', { year: 'numeric', month: 'short', day: 'numeric' });
const clock = new Intl.DateTimeFormat('zh-CN', { hour: '2-digit', minute: '2-digit' });
const types = { '': '全部', group: '群聊', p2p: '1:1' };
const contentLabels = new Map([
  ['image', '图片消息'], ['file', '文件消息'], ['interactive', '卡片消息'], ['audio', '语音消息'], ['media', '视频消息']
]);
const stateLabels = new Map(Object.entries({
  pending: '等待同步', queued: '已请求同步', complete: '已同步', partial: '部分同步',
  failed: '同步失败', access_lost: '来源授权受限', syncing: '正在同步'
}));
let instance = 0;

export function chatRoute(hash) {
  const match = routePattern.exec(hash);
  if (!match) return;
  const [, chatId, messageId] = match;
  return { hash: [chatHash, chatId, messageId].filter(Boolean).join('/'), chatId, messageId };
}

export function chatAPI(request) {
  function read(path, params, signal) {
    const query = new URLSearchParams();
    for (const [key, value] of Object.entries(params)) {
      if (value !== '' && value != null) query.set(key, value);
    }
    return request(base + path + (query.size ? '?' + query : ''), { signal });
  }
  const path = id => '/' + encodeURIComponent(id);
  return {
    conversations: (params, signal) => read('', params, signal),
    conversation: (id, signal) => read(path(id), {}, signal),
    messages: (id, { q, kind, cursor }, signal) => read(
      (id ? path(id) : '') + (q ? '/search' : '/messages'), { q, kind, cursor }, signal),
    context: (id, message, signal) => read(path(id) + '/messages' + path(message) + '/context', {}, signal),
    members: (id, offset, signal) => read(path(id) + '/members', { offset }, signal),
    sync: signal => read('/sync', {}, signal),
    refresh: signal => request(base + '/sync', { method: 'POST', signal })
  };
}

function errorText(error) {
  switch (error.data?.error) {
    case 'chat_access_lost': return '当前账号已无法访问此会话。';
    case 'message_deleted': return '这条消息已撤回。';
    case 'message_not_found': return '找不到这条消息。';
    case 'chat_unavailable': return '飞书会话暂不可用，请稍后重试。';
    case 'invalid_chat_query': return '搜索内容无效，请调整后重试。';
    default: return error.status === 403 ? '当前账号没有访问权限。' : '暂时无法读取飞书记录，请重试。';
  }
}

function accessLost(error) {
  return error.status === 403 || error.data?.error === 'chat_access_lost';
}

function receiptLink(receipt) {
  if (!receipt || ![receipt.project_id, receipt.target_id].every(id => typeof id === 'string' && identifier.test(id))) return;
  const project = '#/projects/' + receipt.project_id;
  if (receipt.target_kind === 'task') return [project + '/tasks/' + receipt.target_id, '查看关联任务'];
  if (receipt.target_kind === 'briefing') return [project + '/reports/brief/' + receipt.target_id, '查看项目简报'];
}

function messageBody(item) {
  if (item.state === 'deleted') return '这条消息已撤回。';
  if (item.body) {
    try {
      const parts = [];
      const keys = ['text', 'title', 'content', 'file_name', 'name', 'elements', 'header', 'zh_cn', 'en_us', 'ja_jp'];
      const collect = (value, depth) => {
        if (depth === 24) return;
        if (typeof value === 'string') parts.push(value);
        else if (Array.isArray(value)) value.forEach(child => collect(child, depth + 1));
        else if (value && typeof value === 'object') {
          for (const key of keys) collect(value[key], depth + 1);
        }
      };
      collect(JSON.parse(item.body), 0);
      if (parts.length) return parts.join(' ');
    } catch {
      if (item.type === 'text') return item.body;
    }
  }
  return contentLabels.get(item.type) ?? '此消息暂无可显示的正文。';
}

// 项目宿主提供限高容器和项目范围内的数据适配器。会话读取仍走授权接口。
// 宿主可以通过 AbortSignal 卸载阅读器，并将当前会话同步到项目路由。
export function mountChatReader(host, api, {
  signal: parentSignal, project = false, chatId, messageId, onLocation = () => {}
} = {}) {
  const lifetime = new AbortController();
  const signal = parentSignal ? AbortSignal.any([parentSignal, lifetime.signal]) : lifetime.signal;
  if (signal.aborted) return;
  const { escape: esc, icon, mountPersonCards, openDialog, personAvatar, personButton, personView, toast } = window.OmniRelayUI;
  const loading = (node, label) => window.OmniRelayLoading.mount(node, { label });
  const id = 'chat-reader-' + ++instance;
  const headingId = project ? id + '-heading' : 'page-heading';
  const heading = project ? 'h2' : 'h1';
  const tool = (name, symbol, label, attributes = '') => `<button class="chat-icon-button" type="button"
    data-action="${name}" title="${label}" aria-label="${label}" ${attributes}>${icon(symbol)}</button>`;
  function search(name, label) {
    return `<label class="chat-search${name === 'messages' ? ' chat-message-search' : ''}" id="${id}-${name}" ${name === 'messages' ? 'hidden' : ''}>
      ${icon('chat-search-icon')}<input class="search" name="${name}" type="search" autocomplete="off" maxlength="200"
      aria-label="${label}" placeholder="${label}"></label>`;
  }

  host.innerHTML = `<div class="chat-reader-host"><div class="bot-chat" ${project ? 'data-project-chat' : ''}>
    <section class="chat-directory" id="${id}-directory" aria-labelledby="${headingId}">
      <div class="chat-directory-tools">
        <div class="chat-directory-title">
          ${project ? '' : tool('filters', 'chat-filter-icon', '筛选会话', `aria-expanded="false" aria-controls="${id}-filters"`)}
          <${heading} id="${headingId}" tabindex="-1">${project ? '项目关联群' : '机器人记录'}</${heading}>
          <span data-count role="status"></span>
        </div>
        <div class="chat-sync">
          <span data-sync role="status"></span>
          <div>${tool('sync', 'chat-info', '查看同步详情', 'aria-haspopup="dialog"')}${tool('refresh', 'chat-refresh', '刷新会话')}</div>
        </div>
        <div id="${id}-filters" class="chat-filters" role="group" aria-label="会话类型" hidden>
          ${Object.entries(types).map(([kind, label]) => `<button class="chat-filter" type="button"
            data-kind="${kind}" aria-pressed="${!kind}">${label}</button>`).join('')}
        </div>
        ${search('directory', project ? '搜索关联群或消息' : '搜索会话或消息')}
      </div>
      <div class="chat-conversations reader-directory" data-directory></div>
    </section>
    <div class="split-handle chat-split-handle" role="separator" aria-label="调整会话列表宽度" aria-orientation="vertical"
      aria-controls="${id}-directory" tabindex="0" title="左右拖动调整宽度"></div>
    <section class="chat-reading" aria-label="聊天记录">
      <header class="chat-reading-head">
        <button class="chat-icon-button chat-back" type="button" data-action="back" aria-label="返回会话列表" title="返回会话列表">${icon('action-expand')}</button>
        <h2 class="chat-reading-title" data-title tabindex="-1">聊天记录</h2>
        <div class="chat-reading-tools" data-tools hidden>
          ${tool('members', 'nav-team', '会话成员', 'aria-haspopup="dialog"')}
          ${tool('search', 'chat-search-icon', '搜索当前会话', `aria-expanded="false" aria-controls="${id}-messages"`)}
        </div>
      </header>
      ${search('messages', '搜索当前会话')}
      <div class="chat-timeline" data-timeline tabindex="0" aria-label="消息时间线"><p class="chat-empty">没有可显示的会话。</p></div>
    </section>
  </div></div>`;
  const root = host.querySelector('.bot-chat');
  const get = selector => root.querySelector(selector);
  const directory = get('[data-directory]');
  const timeline = get('[data-timeline]');
  const title = get('[data-title]');
  const tools = get('[data-tools]');
  const count = get('[data-count]');
  const syncStatus = get('[data-sync]');
  const listSearch = get('[name="directory"]');
  const localSearch = get('[name="messages"]');
  const searchButton = get('[data-action="search"]');
  const dialogs = new Set();
  const requests = new Map();
  let conversations = [];
  let directoryTotal = 0;
  let matches = [];
  let matchCursor;
  let messages = [];
  let historyCursor;
  let selected;
  let desiredId = chatId;
  let anchor = messageId;
  let hasSelection = Boolean(chatId);
  let kind = project ? 'group' : '';
  let listTimer;
  let messageTimer;
  let refreshPending = false;

  mountPersonCards(root, { signal });
  window.connectOmniRelaySplitPane(root, {
    signal, storageKey: project ? 'omni-relay.project-chat-width' : 'omni-relay.bot-chat-width',
    defaultRatio: project ? .32 : .34, minDirectory: project ? 178 : 235
  });
  signal.addEventListener('abort', () => {
    clearTimeout(listTimer);
    clearTimeout(messageTimer);
    for (const dialog of dialogs) if (dialog.open) dialog.close();
  }, { once: true });

  // 适配器可能忽略取消信号，渲染前仍需检查请求是否有效。
  function requestSlot(key) {
    requests.get(key)?.abort();
    const controller = new AbortController();
    requests.set(key, controller);
    const scope = AbortSignal.any([signal, controller.signal]);
    return { scope, current: () => !scope.aborted };
  }
  const cancel = key => requests.get(key)?.abort();
  const status = text => `<p class="chat-empty" role="status">${esc(text)}</p>`;
  const button = (action, text) => `<button class="button chat-page-action" type="button" data-action="${action}">${text}</button>`;
  function finish(node, html) {
    node.removeAttribute('aria-busy');
    node.innerHTML = html;
  }
  function fail(node, error, retry) {
    finish(node, status(errorText(error)) + (retry ? button(retry, '重试') : ''));
  }
  function openReaderDialog(title, description, trigger) {
    const dialog = openDialog({
      title, description, drawer: 'standard', body: '<div data-reader-dialog></div>',
      restoreFocus: () => trigger.isConnected && !trigger.closest('[hidden]') ? trigger : undefined,
      onClose() { dialogs.delete(dialog); }
    });
    dialogs.add(dialog);
    mountPersonCards(dialog, { signal });
    return dialog;
  }
  function clearDetails() {
    cancel('selection');
    cancel('messages');
    cancel('members');
    clearTimeout(messageTimer);
    for (const dialog of dialogs) if (dialog.open) dialog.close();
    selected = undefined;
    title.textContent = '聊天记录';
    title.removeAttribute('aria-label');
    tools.hidden = true;
    localSearch.value = '';
    localSearch.closest('label').hidden = true;
    searchButton.setAttribute('aria-expanded', 'false');
    finish(timeline, status('没有可显示的会话。'));
    timeline.scrollTop = 0;
    messages = [];
    historyCursor = undefined;
  }

  const groupAvatar = () => `<span class="chat-avatar" aria-hidden="true">${icon('nav-team')}</span>`;
  const botAvatar = '<span class="chat-avatar" data-kind="bot" aria-hidden="true"><img src="omni-relay-avatar.png" width="32" height="32" alt=""></span>';
  const nameOf = record => record.person?.name || record.name || (record.kind === 'group' ? '未命名群聊' : '未知人员');
  const personOf = record => record.person ?? { name: nameOf(record) };
  const orgOf = person => person.organization_paths?.map(path => path.join(' / ')).join('；') ?? '';
  const stamp = (value, formatter = clock) => value ? formatter.format(new Date(value)) : '';
  const time = value => value ? `<time datetime="${esc(value)}" title="${esc(stamp(value, day) + ' ' + stamp(value))}">${stamp(value)}</time>` : '';
  function highlight(text, query) {
    if (!query) return esc(text);
    const find = query.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
    return text.split(new RegExp('(' + find + ')', 'giu'))
      .map((part, i) => i % 2 ? '<mark>' + esc(part) + '</mark>' : esc(part)).join('');
  }
  function paintSelection() {
    for (const row of directory.querySelectorAll('[data-chat-id]')) {
      const { chatId, messageId } = row.dataset;
      if (chatId === desiredId && (!messageId || messageId === anchor)) row.setAttribute('aria-current', 'true');
      else row.removeAttribute('aria-current');
    }
  }
  function paintDirectory() {
    const scroll = directory.scrollTop;
    const query = listSearch.value.trim();
    const rows = conversations.map(record => {
      const name = nameOf(record);
      const label = esc(name);
      const displayName = highlight(name, query);
      const target = `data-chat-id="${esc(record.id)}" aria-label="打开会话 ${label}"`;
      if (record.kind === 'group') {
        return `<li><button type="button" class="chat-conversation" ${target}>${groupAvatar()}
          <span class="chat-conversation-copy"><span class="chat-conversation-top"><strong title="${label}">${displayName}</strong>${time(record.last_message_at)}</span>
          <span class="person-org">群聊</span></span></button></li>`;
      }
      const person = personOf(record);
      const organization = orgOf(person);
      const detail = esc(organization ? '1:1 · ' + organization : '1:1');
      return `<li class="chat-direct-row">${personButton(person, personAvatar(name, person.person_key || name), 'chat-person-button')}
        <div class="chat-conversation-copy"><div class="chat-conversation-top">${personButton(person, displayName, 'person-name')}${time(record.last_message_at)}</div>
        <button type="button" class="chat-conversation chat-direct-preview" ${target}>
          <span class="chat-conversation-preview person-org" title="${detail}">${detail}</span></button></div></li>`;
    }).join('');
    const hits = matches.map(item => `<li><button type="button" class="chat-result" data-chat-id="${esc(item.chat_id)}" data-message-id="${esc(item.id)}">
      <span class="chat-conversation-top"><strong>${esc(item.sender?.name || '未知人员')}</strong>${time(item.sent_at)}</span>
      <span class="chat-conversation-preview">${highlight(messageBody(item), query)}</span></button></li>`).join('');
    count.textContent = number.format(directoryTotal);
    count.setAttribute('aria-label', count.textContent + ' 个会话');
    finish(directory, (rows ? `<ul aria-label="会话列表">${rows}</ul>` : status(project && !query ? '暂无项目关联群。' : '没有匹配的会话。')) +
      (conversations.length < directoryTotal ? button('more-chats', '加载更多会话') : '') +
      (query ? '<h3>消息结果</h3>' + (hits ? `<ul aria-label="消息结果">${hits}</ul>` : status('没有匹配的消息。')) : '') +
      (matchCursor ? button('more-matches', '加载更多消息结果') : ''));
    paintSelection();
    directory.scrollTop = scroll;
  }

  function renderTitle(record) {
    title.setAttribute('aria-label', nameOf(record));
    title.innerHTML = record.kind === 'p2p' ? personView(personOf(record)) :
      `<span class="person-view">${groupAvatar()}<span class="person-copy"><strong class="person-name" title="${esc(nameOf(record))}">
      ${esc(nameOf(record))}</strong><span class="person-org">群聊</span></span></span>`;
    tools.hidden = false;
  }
  function paintTimeline({ prepend = false, focus = false } = {}) {
    const before = timeline.scrollHeight - timeline.scrollTop;
    const query = localSearch.value.trim();
    let previousDay;
    const rows = messages.map(item => {
      const date = stamp(item.sent_at, day);
      const separator = date && date !== previousDay ? `<li class="chat-day"><span>${esc(date)}</span></li>` : '';
      previousDay = date;
      const fromBot = item.sender_kind === 'app';
      const person = item.sender ?? {};
      const name = person.name || (fromBot ? '飞书 Bot' : '未知人员');
      const receipt = item.state !== 'deleted' && receiptLink(item.business_receipt);
      const org = orgOf(person);
      return separator + `<li class="chat-message" data-message="${esc(item.id)}" tabindex="-1"
        data-author="${fromBot ? 'bot' : 'person'}" ${item.id === anchor ? 'data-source-message' : ''}>
        ${fromBot ? botAvatar : personButton(person, personAvatar(name, person.person_key || name), 'chat-person-button')}
        <div class="chat-message-body"><div class="chat-message-meta">
        ${fromBot ? '<strong>' + esc(name) + '</strong><span class="chat-bot-label">Bot</span>' : personButton(person, esc(name), 'person-name')}
        ${org ? `<span class="person-org" title="${esc(org)}">${esc(org)}</span>` : ''}${time(item.sent_at)}</div>
        <p class="chat-bubble">${highlight(messageBody(item), query)}</p>
        ${receipt ? `<section class="chat-receipt"><div class="chat-receipt-type">Bot 回执</div><a href="${receipt[0]}">${receipt[1]}</a></section>` : ''}
        ${query ? `<button type="button" class="button chat-page-action" data-action="locate" data-message-id="${esc(item.id)}">定位消息</button>` : ''}
        </div></li>`;
    }).join('');
    finish(timeline,
      (anchor ? button('latest', '返回最新消息') : '') +
      (historyCursor ? button('older', query ? '加载更多搜索结果' : '加载更早消息') : '') +
      (rows ? `<ol class="chat-messages">${rows}</ol>` : status(query ? '当前会话没有匹配的消息。' : '当前会话暂无消息。')));
    const target = get('[data-source-message]');
    if (target) {
      timeline.scrollTop += target.getBoundingClientRect().top - timeline.getBoundingClientRect().top - timeline.clientHeight / 3;
      if (focus) target.focus({ preventScroll: true });
    } else if (prepend) {
      timeline.scrollTop = timeline.scrollHeight - before;
      get('[data-action="older"]')?.focus({ preventScroll: true });
    } else {
      timeline.scrollTop = timeline.scrollHeight;
    }
  }

  function loseSelection(error) {
    clearDetails();
    finish(timeline, status(errorText(error)));
    void loadDirectory();
  }

  async function loadMessages({ older = false, focus = false } = {}) {
    const record = selected;
    if (!record) return;
    const { scope, current } = requestSlot('messages');
    const q = localSearch.value.trim();
    const cursor = older ? historyCursor : undefined;
    const target = anchor;
    const trigger = get('[data-action="older"]');
    if (older) trigger?.setAttribute('aria-disabled', 'true');
    else {
      messages = [];
      historyCursor = undefined;
      loading(timeline, '正在加载消息…');
    }
    try {
      const result = target ? await api.context(record.id, target, scope) : await api.messages(record.id, { q, cursor }, scope);
      if (!current()) return;
      const items = result.items ?? [];
      const page = target ? items : [...items].reverse();
      const ids = new Set(messages.map(item => item.id));
      messages = older ? page.filter(item => !ids.has(item.id)).concat(messages) : page;
      historyCursor = result.next_cursor;
      paintTimeline({ prepend: older, focus });
    } catch (error) {
      if (!current()) return;
      if (accessLost(error)) loseSelection(error);
      else if (older) {
        trigger?.removeAttribute('aria-disabled');
        toast('error', errorText(error));
      } else {
        fail(timeline, error, 'retry-messages');
        if (target) timeline.insertAdjacentHTML('afterbegin', button('latest', '返回最新消息'));
      }
    }
  }

  async function choose(id, target, { open = true, route = true, focus = true, record } = {}) {
    hasSelection = true;
    clearDetails();
    desiredId = id;
    anchor = target;
    root.toggleAttribute('data-detail-open', open);
    paintSelection();
    if (route) onLocation(id, target);
    if (id && focus) title.focus({ preventScroll: true });
    if (!id) {
      if (focus) (get('[data-chat-id]') ?? listSearch).focus({ preventScroll: true });
      return;
    }
    const { scope, current } = requestSlot('selection');
    if (!record) loading(timeline, '正在加载会话…');
    try {
      const item = record ?? (await api.conversation(id, scope)).items?.find(item => item.id === id);
      if (!current()) return;
      if (!item) {
        loseSelection({ data: { error: 'chat_access_lost' } });
        return;
      }
      selected = item;
      renderTitle(item);
      void loadMessages({ focus: Boolean(target) });
    } catch (error) {
      if (!current()) return;
      if (accessLost(error)) loseSelection(error);
      else fail(timeline, error, 'retry-selection');
    }
  }

  async function loadDirectory(mode = 'reset', { reconcile = false } = {}) {
    clearTimeout(listTimer);
    const { scope, current } = requestSlot('directory');
    const q = listSearch.value.trim();
    const reset = mode === 'reset';
    const moreChats = mode === 'more-chats';
    if (reset) {
      conversations = [];
      matches = [];
      matchCursor = undefined;
      directoryTotal = 0;
      count.textContent = '';
      directory.scrollTop = 0;
      loading(directory, '正在加载会话…');
    }
    const trigger = reset ? null : get('[data-action="' + mode + '"]');
    trigger?.setAttribute('aria-disabled', 'true');
    try {
      const [list, results] = await Promise.all([
        reset || moreChats ? api.conversations({ q, kind, limit: pageSize, offset: conversations.length }, scope) : null,
        q && !moreChats ? api.messages(null, { q, kind, cursor: matchCursor }, scope) : null
      ]);
      if (!current()) return;
      if (list) {
        const seen = new Set(conversations.map(item => item.id));
        conversations = conversations.concat((list.items ?? []).filter(item => !seen.has(item.id)));
        directoryTotal = list.total;
      }
      if (results) {
        const seen = new Set(matches.map(item => item.id));
        matches = matches.concat((results.items ?? []).filter(item => !seen.has(item.id)));
        matchCursor = results.next_cursor;
      }
      paintDirectory();
      if (!reset) (get('[data-action="' + mode + '"]') ?? listSearch).focus({ preventScroll: true });
      const missing = !conversations.some(item => item.id === desiredId);
      if ((!hasSelection || (reconcile && missing)) && (conversations.length || desiredId)) {
        const first = conversations[0];
        void choose(first?.id, undefined, { open: false, route: reconcile, focus: false, record: first });
      }
    } catch (error) {
      if (!current()) return;
      if (reset || accessLost(error)) fail(directory, error, 'retry-directory');
      else {
        trigger?.removeAttribute('aria-disabled');
        toast('error', errorText(error));
      }
      if (accessLost(error) && selected) {
        clearDetails();
        finish(timeline, status(errorText(error)));
      }
    }
  }

  async function showMembers(trigger) {
    if (!selected) return;
    const record = selected;
    const dialog = openReaderDialog('会话成员', nameOf(record), trigger);
    const content = dialog.querySelector('[data-reader-dialog]');
    let people = [];
    async function read() {
      const { scope, current } = requestSlot('members');
      loading(content, '正在读取会话成员…');
      try {
        const result = await api.members(record.id, people.length, scope);
        if (!current() || !dialog.open) return;
        people = people.concat(result.items ?? []);
        finish(content, (result.state === 'partial' ? status('飞书仅返回了部分成员。') : '') +
          (people.length ? `<ul class="chat-members">${people.map(person => '<li>' + personView(person) + '</li>').join('')}</ul>` :
            status('暂无会话成员。')) +
          (people.length < result.total ? button('more-members', '加载更多成员') : ''));
        if (people.length > pageSize) (content.querySelector('button[data-action]') ?? content.querySelector('li:last-child button'))?.focus();
      } catch (error) {
        if (!current() || !dialog.open) return;
        if (accessLost(error)) loseSelection(error);
        else fail(content, error, 'more-members');
      }
    }
    content.addEventListener('click', event => {
      if (event.target.closest('[data-action="more-members"]')) void read();
    });
    void read();
  }

  function syncState(result) {
    const states = new Set([result.discovery_state, ...(result.items ?? []).map(item => item.state)]);
    return ['failed', 'access_lost', 'syncing', 'queued', 'pending', 'partial', 'complete']
      .find(state => states.has(state));
  }
  const syncLabel = state => stateLabels.get(state) ?? '暂无同步记录';
  const coverage = item => item.coverage === 'all_source_visible_history' ? '飞书可见历史' : '部分历史';
  const fact = (label, value) => `<div><dt>${label}</dt><dd>${esc(value)}</dd></div>`;
  const fullStamp = value => value ? stamp(value, day) + ' ' + stamp(value) : '暂无';

  async function readSync() {
    const { scope, current } = requestSlot('sync');
    try {
      const result = await api.sync(scope);
      if (current()) {
        const state = syncState(result);
        syncStatus.dataset.state = state ?? '';
        syncStatus.textContent = syncLabel(state);
      }
    } catch (error) {
      if (current()) syncStatus.textContent = '同步状态暂不可用';
    }
  }
  async function showSync(trigger) {
    const dialog = openReaderDialog('会话同步详情', undefined, trigger);
    const content = dialog.querySelector('[data-reader-dialog]');
    const { scope, current } = requestSlot('sync-dialog');
    loading(content, '正在读取同步详情…');
    try {
      const result = await api.sync(scope);
      if (!current() || !dialog.open) return;
      const names = new Map(conversations.map(item => [item.id, nameOf(item)]));
      const ready = result.items ?? [];
      const details = ready.map(item => `<li><details><summary>${esc(names.get(item.chat_id) ?? item.chat_id)}</summary>
        <dl class="facts">${fact('状态', stateLabels.get(item.state) ?? '未知状态')}${fact('覆盖范围', coverage(item))}
        ${fact('最早归档消息', fullStamp(item.earliest_message_at))}${fact('覆盖截止', fullStamp(item.covered_until))}
        ${fact('上次尝试', fullStamp(item.attempted_at))}${fact('上次完成', fullStamp(item.completed_at))}
        ${fact('待同步话题', number.format(item.pending_threads ?? 0))}${fact('暂不可用话题', number.format(item.unavailable_threads ?? 0))}</dl></details></li>`).join('');
      finish(content, `<dl class="facts">${fact('状态', syncLabel(syncState(result)))}
        ${fact('会话数', number.format(result.total))}${fact('授权范围',
          project ? '当前项目下有权读取的关联群' :
            result.scope === 'all_authorized_bot_chats' ? '此部署已授权的 Bot 会话' : '当前群成员身份及本人的 1:1 会话')}</dl>
        ${details ? '<ul class="chat-sync-details">' + details + '</ul>' : ''}`);
    } catch (error) {
      if (current() && dialog.open) fail(content, error);
    }
  }

  let statusTimer;
  signal.addEventListener('abort', () => clearTimeout(statusTimer), { once: true });
  async function refresh(trigger) {
    if (refreshPending) return;
    refreshPending = true;
    trigger.disabled = true;
    const { scope, current } = requestSlot('refresh');
    clearTimeout(statusTimer);
    cancel('sync');
    syncStatus.dataset.state = '';
    syncStatus.textContent = '正在请求同步…';
    try {
      await api.refresh(scope);
      if (!current()) return;
      syncStatus.textContent = '已请求同步';
      toast('success', '已请求同步飞书会话。');
      await loadDirectory();
      if (!current()) return;
      if (selected) {
        selected = conversations.find(item => item.id === selected.id) ?? selected;
        renderTitle(selected);
        void loadMessages();
      }
      // 刷新由后台队列执行，稍后复查实际同步状态。
      statusTimer = setTimeout(() => void readSync(), 5000);
    } catch (error) {
      if (current()) {
        syncStatus.textContent = '请求同步失败';
        toast('error', errorText(error));
        if (accessLost(error)) loseSelection(error);
      }
    } finally {
      refreshPending = false;
      trigger.disabled = false;
    }
  }

  function resetDirectorySearch() {
    cancel('directory');
    clearTimeout(listTimer);
    count.textContent = '';
    loading(directory, '正在查询会话…');
    listTimer = setTimeout(() => void loadDirectory('reset', { reconcile: true }), 200);
  }

  function resetMessageSearch() {
    cancel('messages');
    clearTimeout(messageTimer);
    if (anchor) {
      anchor = undefined;
      onLocation(desiredId);
    }
    loading(timeline, '正在查询消息…');
    messageTimer = setTimeout(() => void loadMessages(), 200);
  }
  function showSearch() {
    const label = localSearch.closest('label');
    label.hidden = !label.hidden;
    searchButton.setAttribute('aria-expanded', String(!label.hidden));
    if (!label.hidden) localSearch.focus();
    else {
      searchButton.focus();
      if (localSearch.value) {
        localSearch.value = '';
        resetMessageSearch();
      }
    }
  }
  function locate(target) {
    clearTimeout(messageTimer);
    anchor = target;
    localSearch.value = '';
    localSearch.closest('label').hidden = true;
    searchButton.setAttribute('aria-expanded', 'false');
    onLocation(desiredId, target);
    paintSelection();
    void loadMessages({ focus: true });
  }

  let listTrigger;
  const actions = {
    filters(trigger) {
      const filters = get('.chat-filters');
      filters.hidden = !filters.hidden;
      trigger.setAttribute('aria-expanded', String(!filters.hidden));
    },
    sync: showSync, refresh, members: showMembers, search: showSearch,
    back() {
      const previous = desiredId;
      root.removeAttribute('data-detail-open');
      onLocation();
      const rows = [...directory.querySelectorAll('[data-chat-id]')];
      const row = listTrigger?.isConnected ? listTrigger : rows.find(item => item.dataset.chatId === previous);
      (row ?? listSearch).focus({ preventScroll: !row });
    },
    'more-chats': () => loadDirectory('more-chats'),
    'more-matches': () => loadDirectory('more-matches'),
    older: () => loadMessages({ older: true }),
    'retry-directory': () => loadDirectory(),
    'retry-selection': () => choose(desiredId, anchor, { focus: false }),
    'retry-messages': () => loadMessages({ focus: Boolean(anchor) }),
    locate: trigger => locate(trigger.dataset.messageId),
    latest() {
      anchor = undefined;
      onLocation(desiredId);
      title.focus({ preventScroll: true });
      void loadMessages();
    }
  };

  root.addEventListener('click', event => {
    const action = event.target.closest('[data-action]');
    if (action && action.getAttribute('aria-disabled') !== 'true') {
      void actions[action.dataset.action]?.(action);
    }
    const filter = event.target.closest('.chat-filter');
    if (filter && filter.dataset.kind !== kind) {
      kind = filter.dataset.kind;
      for (const item of root.querySelectorAll('[data-kind][aria-pressed]')) {
        item.setAttribute('aria-pressed', String(item === filter));
      }
      void loadDirectory('reset', { reconcile: true });
    }
    const row = event.target.closest('[data-chat-id]');
    if (row) {
      listTrigger = row;
      const { chatId, messageId } = row.dataset;
      void choose(chatId, messageId, { record: conversations.find(item => item.id === chatId) });
    }
  }, { signal });
  listSearch.addEventListener('input', event => {
    if (!event.isComposing) resetDirectorySearch();
  }, { signal });
  listSearch.addEventListener('compositionend', resetDirectorySearch, { signal });
  localSearch.addEventListener('input', event => {
    if (!event.isComposing) resetMessageSearch();
  }, { signal });
  localSearch.addEventListener('compositionend', resetMessageSearch, { signal });
  localSearch.addEventListener('keydown', event => {
    if (event.key !== 'Escape' || event.isComposing) return;
    event.preventDefault();
    showSearch();
  }, { signal });

  if (chatId) void choose(chatId, messageId, { route: false, focus: false });
  void loadDirectory();
  void readSync();

  return {
    dispose() { lifetime.abort(); },
    select(chatId, messageId) { return choose(chatId, messageId, { route: false }); }
  };
}
