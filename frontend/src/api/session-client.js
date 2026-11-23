// @ts-check
/** @typedef {{method?:string, activity?:'user'|'poll', body?:unknown, signal?:AbortSignal, ifMatch?:string, idempotencyKey?:string, includeETag?:boolean, acceptPNG?:boolean, acceptImage?:boolean}} RequestOptions */
/** @typedef {{roles?:string[], admin?:Record<string,boolean>, navigation?:Record<string,boolean>, permissions?:Record<string,boolean>, default_route?:string, permission_version?:string}} Access */
/** @typedef {{csrf_token?:string, csrfToken?:string, user:{id:string, adminCapabilities?:Access['admin'], navigation?:Access['navigation'], permissions?:Access['permissions'], roles?:string[]}, access?:Access}} Session */
/** @typedef {'checking'|'active'|'anonymous'|'unverified'|'revoked'} Phase */
/** @typedef {'authenticated'|'unchanged'|'recovered'|'identity-changed'|'access-changed'|'unverified'|'anonymous'} Transition */
const apiBase = '/api/v1/';
const authBase = '/auth/';
export const hasHTTPS = () => location.protocol === 'https:';

export class ApiError extends Error {
  /** @param {number} status @param {unknown} [data] */
  constructor(status, data) {
    super('工作台请求失败。');
    this.name = 'ApiError';
    this.status = status;
    this.data = data;
  }
}

/** @param {string} path @param {RequestOptions} options @param {string} csrfToken */
async function request(path, { method = 'GET', activity = 'user', body, signal, ifMatch, idempotencyKey, includeETag = false, acceptPNG = false, acceptImage = false }, csrfToken) {
  const url = new URL(path, location.origin);
  if (url.origin !== location.origin ||
      !(url.pathname.startsWith(apiBase) || url.pathname.startsWith(authBase))) {
    throw new TypeError('工作台请求地址无效。');
  }

  method = method.toUpperCase();
  if (method === 'POST' && ['/auth/login', '/auth/password'].includes(url.pathname) && !hasHTTPS()) {
    throw new ApiError(403, { error: 'https_required' });
  }
  const headers = new Headers({ Accept: acceptImage ? 'application/json, image/*' : acceptPNG ? 'application/json, image/png' : 'application/json' });
  if (activity === 'poll') {
    if (method !== 'GET') throw new TypeError('后台轮询只支持读取操作。');
    headers.set('X-OmniRelay-Activity', 'poll');
  }
  if (!['GET', 'HEAD', 'OPTIONS'].includes(method) && csrfToken) {
    headers.set('X-CSRF-Token', csrfToken);
  }
  if (ifMatch !== undefined) headers.set('If-Match', ifMatch);
  if (idempotencyKey !== undefined) headers.set('Idempotency-Key', idempotencyKey);
  const multipart = body instanceof FormData;
  if (body !== undefined && !multipart) headers.set('Content-Type', 'application/json');
  const response = await fetch(url, {
    method,
    headers,
    body: body instanceof FormData ? body : JSON.stringify(body),
    credentials: 'same-origin',
    cache: 'no-store',
    redirect: 'error',
    signal
  });

  /** @param {unknown} data */
  const result = data => includeETag ? { data, etag: response.headers.get('ETag') } : data;
  if (response.status === 204) return result(null);
  const type = response.headers.get('Content-Type');
  if (response.ok && ((acceptPNG && type === 'image/png') || (acceptImage && type?.startsWith('image/')))) return response.blob();
  const isJson = type?.includes('application/json');
  let data = null;
  if (isJson) {
    try { data = await response.json(); }
    catch (error) {
      if (!response.ok) throw new ApiError(response.status);
      throw error;
    }
  }
  if (!response.ok || !isJson) throw new ApiError(response.status, data);
  return result(data);
}

/** @param {{signal?:AbortSignal}} [options] @returns {Promise<Session>} */
export async function getSession({ signal } = {}) {
  const session = /** @type {Session & {principal?:{account_id:string}}} */ (await request(apiBase + 'me', { signal, activity:'poll' }, ''));
  const principal = session?.principal;
  const token = session?.csrf_token ?? session?.csrfToken;
  if (!principal?.account_id || typeof token !== 'string' || !token) throw new ApiError(502);
  return {
    ...session,
    user: { ...principal, id: principal.account_id, adminCapabilities:session.access?.admin, navigation:session.access?.navigation, permissions:session.access?.permissions, roles:session.access?.roles ?? [] }
  };
}

/** @param {Access | undefined} access */
function accessKey(access) {
  const flags = (/** @type {Record<string,boolean> | undefined} */ values) =>
    Object.entries(values ?? {}).sort(([a], [b]) => a.localeCompare(b));
  return JSON.stringify([[...(access?.roles ?? [])].sort(), flags(access?.admin),
    flags(access?.navigation), flags(access?.permissions), access?.permission_version]);
}

// 跨刷新恢复仅用于聊天编辑器和项目列表返回位置。
const storagePrefix = 'omnirelay.session.';
const ownerKey = storagePrefix + 'owner';
function clearStoredState() {
  try {
    for (const key of Object.keys(sessionStorage)) {
      if (key.startsWith(storagePrefix) || key.startsWith('omnirelay.chat-draft.')) sessionStorage.removeItem(key);
    }
  } catch {}
}

class SessionScope {
  requests = new AbortController();
  /** @type {Record<string, unknown>} */ projects = {};
  /** @type {Record<string, unknown>} */ drive = {};
  drafts = new Map();
  owner = '';

  /** @param {Session} session */
  activate(session) {
    this.owner = JSON.stringify([session.user.id, accessKey(session.access)]);
    try {
      if (sessionStorage.getItem(ownerKey) !== this.owner) clearStoredState();
      sessionStorage.setItem(ownerKey, this.owner);
    } catch {}
  }

  /** @param {string} key */
  read(key) {
    try {
      if (this.owner && sessionStorage.getItem(ownerKey) === this.owner)
        return JSON.parse(sessionStorage.getItem(storagePrefix + key) ?? 'null');
    } catch {}
    return null;
  }

  /** @param {string} key @param {string | null} value */
  store(key, value) {
    try {
      if (!this.owner || sessionStorage.getItem(ownerKey) !== this.owner) return;
      if (value === null) sessionStorage.removeItem(storagePrefix + key);
      else sessionStorage.setItem(storagePrefix + key, value);
    } catch {}
  }

  dispose() {
    this.requests.abort();
    this.drafts.clear();
    for (const state of [this.projects, this.drive]) {
      for (const key of Object.keys(state)) delete state[key];
    }
    this.owner = '';
  }
}

export class SessionClient extends EventTarget {
  /** @type {Phase} */ phase = 'checking';
  /** @type {Session | null} */ session = null;
  scope = new SessionScope();
  csrfToken = '';
  /** @type {{abort:AbortController, promise:Promise<void>} | undefined} */ pending;

  /** @param {Phase} phase @param {Transition} reason */
  publish(phase, reason) {
    this.phase = phase;
    this.dispatchEvent(new CustomEvent('change', { detail: reason }));
  }

  invalidateProbe() {
    this.pending?.abort.abort();
    this.pending = undefined;
  }

  resetScope() {
    this.scope.dispose();
    this.scope = new SessionScope();
  }

  anonymous() {
    this.invalidateProbe();
    if (this.phase !== 'anonymous') this.resetScope();
    clearStoredState();
    this.csrfToken = '';
    this.session = null;
    this.publish('anonymous', 'anonymous');
  }

  suspend() {
    this.invalidateProbe();
    this.scope.requests.abort();
    this.scope.requests = new AbortController();
    this.csrfToken = '';
    this.publish('unverified', 'unverified');
  }

  /** @param {Session} next */
  establish(next) {
    this.invalidateProbe();
    const previous = this.session;
    const changed = previous && previous.user.id !== next.user.id;
    const accessChanged = previous && accessKey(previous.access) !== accessKey(next.access);
    if (!previous || changed || accessChanged) {
      this.resetScope();
      this.scope.activate(next);
    }
    this.session = next;
    this.csrfToken = next.csrf_token ?? next.csrfToken ?? '';
    this.publish('active', changed ? 'identity-changed' : accessChanged ? 'access-changed' :
      !previous ? 'authenticated' : this.phase === 'unverified' ? 'recovered' : 'unchanged');
  }

  verify() {
    if (this.pending) return this.pending.promise;
    const abort = new AbortController();
    const promise = getSession({signal:abort.signal}).then(next => {
      if (!abort.signal.aborted) this.establish(next);
    }, error => {
      if (abort.signal.aborted) return;
      if (error instanceof ApiError && error.status === 401) this.anonymous();
      else if (error instanceof ApiError && error.status === 403) {
        this.resetScope();
        clearStoredState();
        this.session = null;
        this.csrfToken = '';
        this.invalidateProbe();
        this.publish('revoked', 'access-changed');
      } else this.suspend();
    });
    this.pending = { abort, promise };
    return promise;
  }

  /** @template {unknown[]} Args @param {(...args: Args) => void} callback */
  guard(callback) {
    const signal = this.scope.requests.signal;
    return (/** @type {Args} */ ...args) => {
      if (!signal.aborted && this.phase === 'active') callback(...args);
    };
  }

  // 每次挂载页面时绑定其会话，旧页面的后续请求也受同一边界约束。
  bind() {
    const scope = this.scope;
    return async (/** @type {string} */ path, /** @type {RequestOptions} */ options = {}) => {
      if (this.phase !== 'active' || scope !== this.scope) throw new DOMException('会话不可用', 'AbortError');
      const signal = options.signal ? AbortSignal.any([scope.requests.signal, options.signal]) : scope.requests.signal;
      signal.throwIfAborted();
      try {
        const result = await request(path, {...options, signal}, this.csrfToken);
        signal.throwIfAborted();
        return result;
      } catch (error) {
        signal.throwIfAborted();
        if (error instanceof ApiError && error.status === 401) this.anonymous();
        else if (error instanceof ApiError && error.status === 403) {
          await this.verify();
          signal.throwIfAborted();
        }
        throw error;
      }
    };
  }

  /** @param {string} username @param {string} password */
  async login(username, password) {
    const signal = AbortSignal.any([this.scope.requests.signal, AbortSignal.timeout(20000)]);
    await request('/auth/login', {method:'POST', body:{username, password}, signal}, '');
    signal.throwIfAborted();
    this.invalidateProbe();
    await this.verify();
  }

  async logout() {
    const scope = this.scope;
    try { await this.bind()('/auth/logout', {method:'POST'}); }
    catch (error) { if (!(error instanceof ApiError && error.status === 401)) throw error; }
    if (this.scope === scope) this.anonymous();
  }

  /** @param {string} currentPassword @param {string} newPassword */
  async changePassword(currentPassword, newPassword) {
    await this.bind()('/auth/password', {method:'POST', body:{currentPassword, newPassword}});
    this.invalidateProbe();
    await this.verify();
  }

  /** @param {Window} win @param {Document} doc */
  watch(win, doc) {
    const events = new AbortController();
    const options = { signal:events.signal };
    const verify = () => { void this.verify(); };
    win.addEventListener('online', verify, options);
    win.addEventListener('focus', verify, options);
    doc.addEventListener('visibilitychange', () => { if (doc.visibilityState === 'visible') verify(); }, options);
    win.addEventListener('pagehide', event => { if (event.persisted) this.suspend(); }, options);
    win.addEventListener('pageshow', event => {
      if (event.persisted) { this.suspend(); verify(); }
    }, options);
    return () => events.abort();
  }
}

export const client = new SessionClient();

/** @param {string} path @param {RequestOptions} [options] */
export function api(path, options = {}) { return request(path, options, client.csrfToken); }

/** @param {{csrf_token?:string, csrfToken?:string} | null} session */
export function setSession(session) {
  const token = session?.csrf_token ?? session?.csrfToken;
  client.csrfToken = typeof token === 'string' ? token : '';
}
