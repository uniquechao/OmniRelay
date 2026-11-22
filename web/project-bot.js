// The workbench session owns both the request and its conversation.
export async function mountProjectBot(host, project, base, request, signal) {
  const url = '/api/v1/projects/' + encodeURIComponent(project) + '/bot/messages';
  const icon = name => `<svg class="icon" aria-hidden="true"><use href="#${name}"/></svg>`;
  const avatar = new URL('../design/prototype/omni-relay-avatar.png', import.meta.url).href;
  host.innerHTML = `<section class="panel project-bot" aria-labelledby="pm-heading">
    <header class="pm-head">
      <h2 id="pm-heading">${icon('nav-bot-chat')}项目 PM 对话</h2>
      <a data-connection>飞书接入${icon('action-expand')}</a>
    </header>
    <div class="pm-column">
      <div class="pm-stage">
        <div class="pm-welcome" hidden>
          <img src="${avatar}" alt="" width="64" height="64">
          <h3>今天想了解什么项目进展？</h3>
          <div class="pm-prompts" role="group" aria-label="从一个问题开始">
            <button type="button" data-question="帮我梳理一下当前项目的进展。">${icon('chat-project-icon')}梳理项目进展</button>
            <button type="button" data-question="哪些任务还在进行中？">${icon('nav-logs')}查看进行中任务</button>
            <button type="button" data-question="列出项目成员和他们的角色。">${icon('chat-search-icon')}查询项目成员</button>
          </div>
        </div>
        <ol data-chat aria-label="我的项目对话" hidden></ol>
      </div>
      <p role="status" data-status>正在读取对话…</p>
      <form>
        <label class="sr-only" for="project-bot-message">消息</label>
        <textarea id="project-bot-message" name="message" maxlength="2000" rows="2" placeholder="给项目 PM 发消息…" required></textarea>
        <button type="submit" class="button primary" disabled>${icon('compose-send')}发送</button>
      </form>
    </div></section>`;
  host.querySelector('[data-connection]').href = base + '/bot?view=connection';
  const list = host.querySelector('ol'), status = host.querySelector('[data-status]');
  const welcome = host.querySelector('.pm-welcome');
  const prompts = [...welcome.querySelectorAll('button')];
  const form = host.querySelector('form'), input = form.querySelector('textarea'), send = form.querySelector('button');
  const pendingStates = new Set(['route', 'running', 'ready', 'sending']);
  let locked = true, submitting = false, timer, retryText, retryKey;
  function controls() {
    const draft = input.value.trim();
    send.disabled = locked || submitting || !draft;
    const customDraft = draft && !prompts.some(button => button.dataset.question === draft);
    for (const button of prompts) button.disabled = locked || submitting || Boolean(customDraft);
  }
  for (const button of prompts) {
    button.addEventListener('click', () => {
      input.value = button.dataset.question;
      controls();
      input.focus();
    });
  }
  function line(fragment, role, speaker, text) {
    const li = document.createElement('li'), label = document.createElement('strong'), body = document.createElement('p');
    li.className = role;
    label.textContent = speaker; body.textContent = text; li.append(label, body); fragment.append(li);
  }
  async function refresh() {
    clearTimeout(timer);
    try {
      const { items } = await request(url, {signal, activity: 'poll'});
      if (signal.aborted) return;
      const fragment = document.createDocumentFragment();
      locked = items.some(m => pendingStates.has(m.state));
      for (const m of items) {
        line(fragment, 'pm-user', '我', m.question);
        if (m.answer) {
          const split = m.answer.indexOf('\n');
          line(fragment, 'pm-reply', split > 0 ? m.answer.slice(0, split) : '项目 PM', split > 0 ? m.answer.slice(split + 1) : m.answer);
        } else if (!pendingStates.has(m.state)) line(fragment, 'pm-reply', '项目 PM', '这条消息未能完成。');
      }
      const atEnd = list.hidden || list.scrollHeight - list.scrollTop - list.clientHeight < 32;
      list.replaceChildren(fragment);
      list.hidden = !items.length;
      welcome.hidden = Boolean(items.length);
      if (items.length && atEnd) list.scrollTop = list.scrollHeight;
      status.textContent = locked ? '项目 PM 正在回复…' : '';
      if (locked) timer = setTimeout(refresh, 1200);
    } catch (e) {
      if (signal.aborted) return;
      locked = true;
      welcome.hidden = true;
      if ([401,403].includes(e.status)) {
        list.replaceChildren();
        list.hidden = true;
      }
      status.textContent = e.status === 401 ? '请重新登录以读取对话。' : e.status === 403 ? '当前账号无权访问此项目。' : '暂时无法读取对话，正在重试…';
      if (![401,403].includes(e.status)) timer = setTimeout(refresh, 5000);
    }
    controls();
  }
  signal.addEventListener('abort', () => clearTimeout(timer), {once:true});
  input.addEventListener('input', controls);
  form.onsubmit = async event => {
    event.preventDefault();
    const text = input.value.trim();
    if (locked || submitting || !text) return;
    if (retryText !== text) { retryKey = crypto.randomUUID(); retryText = text; }
    submitting = true; controls(); status.textContent = '正在发送…';
    try {
      await request(url, {method:'POST', body:{text}, idempotencyKey:retryKey, signal});
      if (signal.aborted) return;
      if (input.value.trim() === text) input.value = '';
      retryText = null;
      await refresh();
    } catch (e) {
      if (signal.aborted) return;
      if ([403,409].includes(e.status)) await refresh();
      else status.textContent = '发送未确认，可以重试。';
    } finally { submitting = false; controls(); }
  };
  await refresh();
}
