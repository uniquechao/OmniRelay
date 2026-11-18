const apiBase = '/api/v1/';
const authBase = '/auth/';
const httpsRequired = '账号密码操作需要 HTTPS 安全连接。';
const hasHTTPS = () => location.protocol === 'https:';
let csrfToken = '';

export class ApiError extends Error {
  constructor(status, data) {
    super('工作台请求失败。');
    this.name = 'ApiError';
    this.status = status;
    this.data = data;
  }
}

export function setSession(session) {
  const token = session?.csrf_token ?? session?.csrfToken;
  csrfToken = typeof token === 'string' ? token : '';
}

export async function api(path, { method = 'GET', body, signal, ifMatch, idempotencyKey, includeETag = false } = {}) {
  const url = new URL(path, location.origin);
  if (url.origin !== location.origin ||
      !(url.pathname.startsWith(apiBase) || url.pathname.startsWith(authBase))) {
    throw new TypeError('工作台请求地址无效。');
  }

  method = method.toUpperCase();
  if (method === 'POST' && ['/auth/login', '/auth/password'].includes(url.pathname) && !hasHTTPS()) {
    throw new ApiError(403, { error: 'https_required' });
  }
  const headers = new Headers({ Accept: 'application/json' });
  if (!['GET', 'HEAD', 'OPTIONS'].includes(method) && csrfToken) {
    headers.set('X-CSRF-Token', csrfToken);
  }
  if (ifMatch !== undefined) headers.set('If-Match', ifMatch);
  if (idempotencyKey !== undefined) headers.set('Idempotency-Key', idempotencyKey);
  if (body !== undefined) headers.set('Content-Type', 'application/json');
  const response = await fetch(url, {
    method,
    headers,
    body: body === undefined ? undefined : JSON.stringify(body),
    credentials: 'same-origin',
    cache: 'no-store',
    redirect: 'error',
    signal
  });

  const result = data => includeETag ? { data, etag: response.headers.get('ETag') } : data;
  if (response.status === 204) return result(null);
  const isJson = response.headers.get('Content-Type')?.includes('application/json');
  const data = isJson ? await response.json() : null;
  if (!response.ok || !isJson) throw new ApiError(response.status, data);
  return result(data);
}

export async function getSession({ signal } = {}) {
  const session = await api(apiBase + 'me', { signal });
  const principal = session?.principal;
  const token = session?.csrf_token ?? session?.csrfToken;
  if (!principal?.account_id || typeof token !== 'string' || !token) throw new ApiError(502);
  return {
    ...session,
    user: { ...principal, id: principal.account_id }
  };
}

export async function logout() {
  try {
    await api(authBase + 'logout', { method: 'POST' });
  } catch (error) {
    if (error.status !== 401) throw error;
  }
  setSession(null);
}

export async function changePassword(currentPassword, newPassword) {
  const session = await api(authBase + 'password', {
    method: 'POST',
    body: { currentPassword, newPassword }
  });
  if (session?.csrf_token ?? session?.csrfToken) setSession(session);
  return session;
}

const feishuErrors = new Map([
  ['feishu', '飞书登录未完成。你可以重新发起登录或使用账号密码。'],
  ['cancelled', '已取消飞书授权。你可以重新发起登录或使用账号密码。'],
  ['denied', '当前飞书账号无法登录。请确认账号已由管理员开通并启用。'],
  ['unavailable', '飞书登录暂不可用。请稍后重试或使用账号密码。']
]);

export function mountLogin(page, { onSuccess, returnTo = () => '/#/workbench' }) {
  const form = page.querySelector('#password-login');
  const username = form.elements.namedItem('username');
  const password = form.elements.namedItem('password');
  const submit = form.querySelector('button[type="submit"]');
  const submitLabel = submit.textContent.trim();
  const feishu = page.querySelector('#feishu-login');
  const feedback = page.querySelector('#login-feedback');
  let busy = false;

  function clearFeedback() {
    feedback.hidden = true;
    for (const field of [username, password]) {
      field.removeAttribute('aria-invalid');
      field.removeAttribute('aria-describedby');
    }
  }

  function showFeedback(message, { tone = 'error', field } = {}) {
    feedback.dataset.tone = tone;
    feedback.textContent = message;
    feedback.hidden = false;
    if (field) {
      field.setAttribute('aria-invalid', 'true');
      field.setAttribute('aria-describedby', feedback.id);
      field.focus();
    }
  }

  function setBusy(value, label = submitLabel) {
    busy = value;
    form.toggleAttribute('data-pending', value);
    form.setAttribute('aria-busy', String(value));
    submit.disabled = value || !hasHTTPS();
    feishu.disabled = value;
    submit.textContent = label;
  }

  form.addEventListener('input', clearFeedback);
  form.addEventListener('submit', async event => {
    event.preventDefault();
    if (busy) return;
    if (!hasHTTPS()) {
      showFeedback(httpsRequired, { tone: 'warning' });
      return;
    }
    clearFeedback();
    const handle = username.value.trim();
    if (!handle) {
      showFeedback('请输入用户名。', { field: username });
      return;
    }
    setBusy(true, '登录中…');
    username.readOnly = password.readOnly = true;
    let active;
    try {
      await api(authBase + 'login', {
        method: 'POST',
        body: { username: handle, password: password.value },
        signal: AbortSignal.timeout(20000)
      });
      active = await getSession();
      form.reset();
    } catch (error) {
      if (error.status === 401) {
        password.value = '';
        showFeedback('用户名或密码不正确，请重新输入。', { field: password });
      } else if (error.status === 429) {
        showFeedback('登录尝试过多，请稍后再试。', { tone: 'warning' });
      } else if (error.data?.error === 'https_required') {
        password.value = '';
        showFeedback(httpsRequired, { tone: 'warning' });
      } else if ([404, 405, 501].includes(error.status)) {
        password.value = '';
        showFeedback('当前未开通账号密码登录，请使用飞书登录。', { tone: 'warning' });
      } else {
        showFeedback('暂时无法登录，请稍后再试。');
      }
    } finally {
      username.readOnly = password.readOnly = false;
      setBusy(false);
    }
    if (active) onSuccess(active);
  });

  feishu.addEventListener('click', () => {
    if (busy) return;
    clearFeedback();
    password.value = '';
    setBusy(true);
    const target = new URL(authBase + 'feishu/start', location.origin);
    target.searchParams.set('return_to', returnTo());
    location.assign(target);
  });

  username.disabled = password.disabled = submit.disabled = !hasHTTPS();
  window.addEventListener('pageshow', event => {
    if (event.persisted) setBusy(false);
  });

  return {
    activate(error) {
      clearFeedback();
      if (!hasHTTPS()) {
        showFeedback(httpsRequired, { tone: 'warning' });
      } else if (error) {
        showFeedback(feishuErrors.get(error) ?? feishuErrors.get('feishu'), { tone: 'warning' });
        feedback.scrollIntoView({ block: 'nearest', behavior: 'instant' });
      }
    }
  };
}
