export const connectionStates = new Map([
  ['disabled', ['pending', '已停用']],
  ['connecting', ['progress', '连接中']],
  ['connected', ['success', '在线']],
  ['reconnecting', ['progress', '重连中']],
  ['unavailable', ['warning', '连接不可用']]
]);
export const checkStates = new Map([
  ['passed', ['success', '通过']],
  ['failed', ['danger', '失败']],
  ['skipped', ['pending', '未执行']]
]);
export const checkErrors = new Map([
  ['credentials_missing', '部署尚未注入飞书应用凭据。'],
  ['credentials_rejected', '飞书拒绝了应用凭据，请核对部署配置。'],
  ['bot_unavailable', '无法读取 Bot 信息，请检查飞书应用的机器人能力。'],
  ['provider_temporarily_unavailable', '飞书平台暂不可用，请稍后重试。']
]);
const dateTime = new Intl.DateTimeFormat('zh-CN', {
  dateStyle: 'medium', timeStyle: 'medium', timeZone: 'UTC'
});
const inProgress = task => task?.state === 'queued' || task?.state === 'running';

export function time(value) {
  const timestamp = value ? Date.parse(value) : NaN;
  return Number.isFinite(timestamp) ? dateTime.format(timestamp) + ' UTC' : '';
}

function chip(node, [tone, label]) {
  node.dataset.tone = tone;
  node.textContent = label;
}

export function mountFeishuBot(page, api, { signal, canConfigure, canOperate, canSyncDirectory }) {
  const { openDialog, toast } = window.OmniRelayUI;
  page.innerHTML = `
    <header class="head">
      <h1 id="page-heading" tabindex="-1">飞书 Bot 配置</h1>
      ${canSyncDirectory ? '<a class="button" href="#/system/users">前往用户管理同步组织与用户</a>' : ''}
    </header>
    <div class="toolbar">
      <p id="bot-load-status" role="status">正在读取飞书 Bot 配置…</p>
      <button class="button" type="button" id="bot-refresh" disabled>刷新状态</button>
    </div>
    <div class="stack" id="bot-content" hidden>
      <section class="panel" aria-labelledby="bot-app-heading">
        <header class="section-head"><h2 id="bot-app-heading">应用信息</h2></header>
        <dl class="facts">
          <div><dt>App ID</dt><dd data-bot="app_id" translate="no"></dd></div>
          <div><dt>租户标识</dt><dd data-bot="tenant_key" translate="no"></dd></div>
          <div><dt>部署凭据</dt><dd><span class="chip" data-bot="credentials_configured"></span></dd></div>
          <div><dt>Bot 名称</dt><dd data-bot="bot_name"></dd></div>
          <div><dt>OAuth 回调地址</dt><dd data-bot="oauth_callback_url" translate="no"></dd></div>
        </dl>
      </section>
      <section class="panel" aria-labelledby="bot-config-heading">
        <header class="section-head"><h2 id="bot-config-heading">Bot 开关</h2><span class="chip" data-bot="enabled"></span></header>
        <form id="bot-form" class="stack" ${canConfigure ? '' : 'hidden'}>
          <label><input id="bot-enabled" type="checkbox" name="enabled" autocomplete="off"> 启用飞书 Bot</label>
          <p class="aux" id="bot-save-feedback" role="status" hidden></p>
          <div><button class="button primary" type="submit" id="bot-save" disabled>保存</button></div>
        </form>
      </section>
      <section class="panel" aria-labelledby="bot-diagnostics-heading">
        <header class="section-head">
          <h2 id="bot-diagnostics-heading">连接诊断</h2>
          <button class="button" type="button" id="bot-test">测试连接</button>
        </header>
        <ul class="attention">
          <li><span class="chip" data-bot="tenant_token"></span><div><strong>凭据校验</strong><span class="aux" data-bot="tenant_token_detail" hidden></span></div></li>
          <li><span class="chip" data-bot="bot_info"></span><div><strong>Bot 信息读取</strong><span class="aux" data-bot="bot_info_detail" hidden></span></div></li>
          <li><span class="chip" data-bot="connection"></span><div><strong>长连接</strong><span class="aux" data-bot="heartbeat" hidden></span></div></li>
          <li><span class="chip" data-bot="message"></span><div><strong>真实收信</strong><span class="aux" data-bot="message_detail"></span></div></li>
        </ul>
        <p class="context" id="bot-test-feedback" role="status" hidden></p>
      </section>
    </div>`;

  const find = id => page.querySelector('#bot-' + id);
  const fields = Object.fromEntries([...page.querySelectorAll('[data-bot]')].map(node => [node.dataset.bot, node]));
  const content = find('content');
  const refreshButton = find('refresh');
  const toggle = find('enabled');
  const saveButton = find('save');
  const testButton = find('test');
  let status;
  let etag;
  let reading = false;
  let saving = false;
  let testing = false;
  let forbidden = false;
  let confirmVersion = false;
  let refreshAfterSave = false;
  let checkKey;
  let task;
  let timer;
  let leaveDialog;
  let navigationPrompt;

  const requestSignal = () => AbortSignal.any([signal, AbortSignal.timeout(15000)]);
  const dirty = () => Boolean(status && canConfigure && (confirmVersion || toggle.checked !== status.enabled));

  function message(node, text) {
    if (node.textContent !== text) node.textContent = text;
    node.hidden = !text;
  }

  function controls() {
    content.hidden = !status || forbidden;
    refreshButton.hidden = forbidden;
    refreshButton.disabled = reading || saving;
    toggle.disabled = saving;
    saveButton.disabled = saving || reading || !etag || !dirty();
    saveButton.textContent = saving ? '保存中…' : '保存';
    testButton.hidden = !canOperate;
    testButton.disabled = !canOperate || testing;
    testButton.textContent = testing ? '检测中…' : checkKey ? '重试检测' : '测试连接';
  }

  function showStatus(response) {
    const edited = dirty();
    status = response.data;
    etag = response.etag;
    if (!edited) toggle.checked = status.enabled;
    for (const field of ['app_id', 'tenant_key', 'bot_name', 'oauth_callback_url']) {
      fields[field].textContent = status[field] || '—';
    }
    chip(fields.credentials_configured, status.credentials_configured ? ['success', '已注入'] : ['warning', '未注入']);
    chip(fields.enabled, status.enabled ? ['success', '已保存为启用'] : ['pending', '已保存为关闭']);
    const connection = status.connection;
    chip(fields.connection, connectionStates.get(connection?.state) ?? ['pending', '暂无状态']);
    const heartbeat = time(connection?.last_heartbeat_at);
    message(fields.heartbeat, heartbeat && '最近心跳 ' + heartbeat);
    const received = time(connection?.last_message_at);
    chip(fields.message, received ? ['success', '已收到消息'] : ['pending', '尚未收到']);
    message(fields.message_detail, received || '启用后，在预发布测试群 @ Bot，或向 Bot 发送私聊消息。');
    message(find('load-status'), !status.credentials_configured ? '部署尚未注入飞书应用凭据。' :
      canConfigure && !etag ? '暂时无法获取配置版本，请刷新状态后再保存。' : '');
  }

  function rejectReadAccess(error) {
    if (signal.aborted || forbidden) return true;
    if (error.status !== 403) return false;
    forbidden = true;
    status = etag = undefined;
    toggle.checked = false;
    content.hidden = true;
    for (const node of Object.values(fields)) node.textContent = '';
    message(find('save-feedback'), '');
    message(find('test-feedback'), '');
    clearTimeout(timer);
    leaveDialog?.close('cancel');
    message(find('load-status'), '当前账号没有访问飞书 Bot 配置的权限。');
    controls();
    return true;
  }

  function requestError(error, fallback) {
    if (error.status === 403) return '会话校验未通过或操作权限已变更，请重新登录后重试。';
    return fallback;
  }

  async function refresh() {
    if (signal.aborted || forbidden || reading || saving) return false;
    reading = true;
    controls();
    try {
      const response = await api.status(requestSignal());
      if (signal.aborted || forbidden) return false;
      showStatus(response);
      return Boolean(etag);
    } catch (error) {
      if (!rejectReadAccess(error)) {
        message(find('load-status'), requestError(error, '无法读取飞书 Bot 状态，请刷新重试。'));
      }
      return false;
    } finally {
      reading = false;
      if (!signal.aborted) controls();
    }
  }

  async function save() {
    if (!dirty()) return true;
    if (!etag || saving || reading || forbidden || signal.aborted) return false;
    saving = true;
    controls();
    message(find('save-feedback'), '正在保存 Bot 开关…');
    let conflict;
    try {
      const response = await api.save(toggle.checked, etag, requestSignal());
      if (signal.aborted || forbidden) return false;
      showStatus(response);
      toggle.checked = status.enabled;
      confirmVersion = false;
      message(find('save-feedback'), '');
      toast('success', 'Bot 开关已保存。');
      return true;
    } catch (error) {
      if (signal.aborted || forbidden) return false;
      if (error.status === 412 || error.status === 428) {
        conflict = error.status;
        confirmVersion = true;
        etag = undefined;
        message(find('save-feedback'), '正在读取最新配置版本，当前修改已保留…');
      } else {
        if (error.status === 403) refreshAfterSave = true;
        message(find('save-feedback'), requestError(error, '无法保存 Bot 开关，请重试。'));
      }
    } finally {
      saving = false;
      if (!signal.aborted) {
        controls();
        if (!conflict && refreshAfterSave) void refresh();
      }
      refreshAfterSave = false;
    }
    if (conflict) {
      await refresh();
      if (!signal.aborted && !forbidden) showSaveConfirmation(conflict);
    }
    return false;
  }

  function showSaveConfirmation(code) {
    const reason = code === 412 ? '已保存的配置发生了变更。' : '服务器要求提供最新配置版本。';
    message(find('save-feedback'), reason + (etag ? '请核对已保存状态，再次点击「保存」确认。' :
      '当前修改已保留，请刷新状态后再保存。'));
  }

  function showCheckResults(results = []) {
    for (const name of ['tenant_token', 'bot_info']) {
      const result = results.find(item => item.name === name);
      chip(fields[name], checkStates.get(result?.state) ?? ['pending', '待检测']);
      const detail = checkErrors.get(result?.error) || time(result?.checked_at);
      message(fields[name + '_detail'], detail);
    }
  }

  function finishCheck(result) {
    task = undefined;
    checkKey = undefined;
    testing = false;
    showCheckResults(result.results);
    const passed = result.state === 'succeeded';
    const failure = result.results?.find(item => checkErrors.has(item.error));
    message(find('test-feedback'), passed ? '连接检测完成，请查看各项结果。' :
      checkErrors.get(failure?.error) || '连接检测失败，请重试。');
    controls();
    if (saving) refreshAfterSave = true;
    else void refresh();
  }

  function checkFailure(error, fallback, { read = false } = {}) {
    if (signal.aborted || forbidden || (read && rejectReadAccess(error))) return;
    testing = false;
    if (error.status >= 400 && error.status < 500 && ![408, 429].includes(error.status)) {
      task = undefined;
      checkKey = undefined;
    }
    message(find('test-feedback'), requestError(error, fallback));
    controls();
    if (error.status === 403) {
      if (saving) refreshAfterSave = true;
      else void refresh();
    }
  }

  async function readCheck() {
    if (signal.aborted || forbidden) return;
    try {
      const result = await api.check(task.id, requestSignal());
      if (signal.aborted || forbidden) return;
      task = result;
      if (inProgress(result)) {
        message(find('test-feedback'), result.state === 'queued' ? '等待开始检测…' : '正在检测飞书应用…');
        timer = setTimeout(readCheck, 2000);
      } else {
        finishCheck(result);
      }
    } catch (error) {
      checkFailure(error, '无法读取检测结果，请重试。', { read: true });
    }
  }

  async function test() {
    if (!canOperate || testing || forbidden || signal.aborted) return;
    testing = true;
    if (!checkKey) {
      checkKey = crypto.randomUUID();
      showCheckResults();
    }
    controls();
    message(find('test-feedback'), '正在启动连接检测…');
    try {
      if (!task) task = await api.test(checkKey, requestSignal());
      // 已结束的复用检测只读取一次结果，进行中的检测才轮询。
      await readCheck();
    } catch (error) {
      checkFailure(error, '无法启动连接检测，请重试。');
    }
  }

  function confirmNavigation() {
    if (navigationPrompt) return navigationPrompt;
    if (saving) return Promise.resolve(false);
    if (!dirty()) return Promise.resolve(true);
    navigationPrompt = new Promise(resolve => {
      leaveDialog = openDialog({
        title: '保存修改？', variant: 'confirm', tone: 'warning',
        body: '<p class="dialog-note">有未保存的修改，直接离开会丢失本次更改。</p>',
        cancelLabel: '继续编辑', discard: '不保存', confirm: '保存并关闭',
        restoreFocus: () => !signal.aborted && !forbidden ? toggle : null,
        async onClose(confirmed, result) {
          leaveDialog = undefined;
          const proceed = !signal.aborted && (result === 'discard' || (confirmed && await save()));
          navigationPrompt = undefined;
          if (!proceed && !signal.aborted && !forbidden) toggle.focus();
          resolve(proceed);
        }
      });
    });
    return navigationPrompt;
  }

  signal.addEventListener('abort', () => {
    clearTimeout(timer);
    leaveDialog?.close('cancel');
  }, { once: true });
  window.addEventListener('beforeunload', event => {
    if (dirty()) {
      event.preventDefault();
      event.returnValue = true;
    }
  }, { signal });
  find('form').addEventListener('submit', event => {
    event.preventDefault();
    void save();
  });
  toggle.addEventListener('change', controls);
  testButton.addEventListener('click', test);
  refreshButton.addEventListener('click', async () => {
    if (await refresh() && confirmVersion && !signal.aborted) {
      message(find('save-feedback'), '已刷新配置版本。请核对已保存状态，再次点击「保存」确认。');
    }
  });

  showCheckResults();
  void refresh();
  return { confirmNavigation, hasDraft:dirty };
}
