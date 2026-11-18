const form = document.querySelector('#setup-form');
const button = form.querySelector('button[type=submit]');
const feedback = document.querySelector('#setup-feedback');
const callback = document.querySelector('#setup-callback');
callback.querySelector('code').textContent = new URL('/auth/feishu/callback', location.origin).href;
callback.hidden = false;
const messages = {
  setup_forbidden: '设置码无效。请核对设置码，并从产品的 HTTPS 地址打开此页。',
  setup_invalid: '请检查飞书配置字段后重试。',
  setup_unavailable: '暂时无法保存配置。请稍后重试。',
};
let dirty = false;
let leaving = false;

function showFeedback(message, isError = false) {
  feedback.textContent = message;
  feedback.dataset.tone = isError ? 'error' : 'pending';
  feedback.hidden = false;
  if (isError) feedback.focus();
}

function requireConfirmation(event) {
  if (dirty && !leaving) event.preventDefault();
}

async function openLogin() {
  leaving = true;
  button.disabled = true;
  form.reset();
  showFeedback('配置已保存，正在启动登录服务…');
  for (let attempt = 0; attempt < 20; attempt += 1) {
    await new Promise(resolve => setTimeout(resolve, 500));
    const response = await fetch('/setup', {cache: 'no-store', signal: AbortSignal.timeout(2000)}).catch(() => null);
    if (response?.status === 404) {
      window.location.replace('/');
      return;
    }
  }
  showFeedback('配置已保存。请稍后刷新页面进入工作台。');
}

const result = new URLSearchParams(window.location.search);
const oauthErrors = {
  authorization: '本次飞书授权已失效。请在当前浏览器重新发起授权。',
  credentials: '飞书未接受应用凭据。请核对同一应用的 App ID 和 App Secret。',
  denied: '飞书拒绝了当前账号或应用。请检查应用是否已启用，以及账号是否在可用范围内。',
  token_exchange: '飞书授权码换取令牌失败。请重新发起授权，若仍失败请联系部署管理员。',
  user_info: '已收到飞书令牌，但读取用户信息失败。请重试，若仍失败请联系部署管理员。',
  identity: '飞书返回的账号身份不可用。请确认使用了该应用可访问的企业账号。',
  cancelled: '飞书授权已取消。准备好后可以重新提交。',
  unavailable: '暂时无法完成设置。请稍后重试。',
};

form.addEventListener('input', () => { dirty = true; });
window.addEventListener('beforeunload', requireConfirmation);
button.disabled = false;

form.addEventListener('submit', async event => {
  event.preventDefault();
  if (button.disabled) return;
  button.disabled = true;
  button.textContent = '正在准备授权…';
  feedback.hidden = true;

  try {
    const payload = Object.fromEntries(new FormData(form));
    const response = await fetch('/setup', {
      method: 'POST',
      headers: {'Content-Type': 'application/json'},
      body: JSON.stringify(payload),
      cache: 'no-store',
      signal: AbortSignal.timeout(15000),
    });
    if (response.status === 409) {
      await openLogin();
      return;
    }
    if (response.ok) {
      const {authorization_url} = await response.json();
      const destination = new URL(authorization_url);
      if (destination.origin !== 'https://accounts.feishu.cn') throw new Error('unexpected OAuth destination');
      leaving = true;
      form.reset();
      window.location.assign(destination);
      return;
    }
    const code = (await response.text()).trim();
    showFeedback(Object.hasOwn(messages, code) ? messages[code] : messages.setup_unavailable, true);
  } catch {
    showFeedback('暂时无法发起飞书授权。请稍后重试。', true);
  } finally {
    button.textContent = '使用飞书授权并保存';
    button.disabled = leaving;
  }
});

if (result.get('setup') === 'complete') {
  void openLogin();
}
const failure = result.get('error');
if (Object.hasOwn(oauthErrors, failure)) {
  showFeedback(oauthErrors[failure], true);
}
