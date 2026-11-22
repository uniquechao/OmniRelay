// The workbench session owns both the request and its conversation.
export async function mountProjectBot(host, project, base, request, signal) {
  const url = '/api/v1/projects/' + encodeURIComponent(project) + '/bot/messages';
  host.innerHTML = `<section class="panel project-bot"><header class="section-head"><h2>与项目 PM 对话</h2>
    <a data-connection>飞书接入</a></header><ol data-chat aria-label="我的项目对话"></ol>
    <p role="status" data-status></p><form><label for="project-bot-message">消息</label>
    <textarea id="project-bot-message" maxlength="2000" rows="3" placeholder="给项目 PM 发消息" required></textarea>
    <button type="submit" class="button primary" disabled>发送</button></form></section>`;
  host.querySelector('[data-connection]').href = base + '/bot?view=connection';
  const list = host.querySelector('ol'), status = host.querySelector('[data-status]');
  const form = host.querySelector('form'), input = form.querySelector('textarea'), send = form.querySelector('button');
  const pendingStates = new Set(['route', 'running', 'ready', 'sending']);
  let locked = true, submitting = false, timer, retryText, retryKey;
  function controls() { send.disabled = locked || submitting || !input.value.trim(); }
  function line(fragment, speaker, text) {
    const li = document.createElement('li'), label = document.createElement('strong'), body = document.createElement('p');
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
        line(fragment, '我', m.question);
        if (m.answer) {
          const split = m.answer.indexOf('\n');
          line(fragment, m.answer.slice(0, split), m.answer.slice(split + 1));
        } else if (!pendingStates.has(m.state)) line(fragment, '项目 PM', '这条消息未能完成。');
      }
      list.replaceChildren(fragment);
      status.textContent = locked ? '项目 PM 正在回复…' : items.length ? '' : '还没有对话。';
      if (locked) timer = setTimeout(refresh, 1200);
    } catch (e) {
      if (signal.aborted) return;
      locked = true;
      if ([401,403].includes(e.status)) list.replaceChildren();
      status.textContent = e.status === 403 ? '当前账号无权访问此项目。' : '暂时无法读取对话，正在重试…';
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
