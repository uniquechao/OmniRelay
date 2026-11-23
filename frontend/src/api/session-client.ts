import {z} from './validation.ts'
import type {DriveMemory} from '../features/drive/drive'

export type RequestOptions = {method?:string; activity?:'user'|'poll'; body?:unknown; signal?:AbortSignal;
  ifMatch?:string; idempotencyKey?:string; includeETag?:boolean; acceptPNG?:boolean; acceptImage?:boolean}
const flags=z.record(z.string(),z.boolean())
const accessSchema=z.object({roles:z.array(z.string()).optional(), admin:flags.optional(), navigation:flags.optional(),
  permissions:flags.optional(), default_route:z.string().optional(), permission_version:z.string().optional()})
const sessionSchema=z.object({csrf_token:z.string().min(1),access:accessSchema.optional(),principal:z.object({
  account_id:z.string().min(1), tenant_key:z.string().optional(),app_id:z.string().optional(),open_id:z.string().optional(),
  name:z.string().optional(),avatar_url:z.string().optional(),auth_method:z.string().optional(),can_change_password:z.boolean().optional(),
})})
type Access=z.infer<typeof accessSchema>
type Session=Awaited<ReturnType<typeof getSession>>
type Phase='checking'|'active'|'anonymous'|'unverified'|'revoked'
type Transition='authenticated'|'unchanged'|'recovered'|'identity-changed'|'access-changed'|'unverified'|'anonymous'
const apiBase = '/api/v1/';
const authBase = '/auth/';
export const hasHTTPS = () => location.protocol === 'https:';

export class ApiError extends Error {
  readonly status:number;
  readonly data:unknown;
  constructor(status:number, data?:unknown) {
    super('工作台请求失败。');
    this.name = 'ApiError';
    this.status = status;
    this.data = data;
  }
}

export function errorStatus(error:unknown):number {
  return typeof error === 'object' && error !== null && 'status' in error && typeof error.status === 'number'
    ? error.status : 0;
}

async function request(path:string, { method = 'GET', activity = 'user', body, signal, ifMatch, idempotencyKey, includeETag = false, acceptPNG = false, acceptImage = false }:RequestOptions, csrfToken:string):Promise<unknown> {
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

  const result = (data:unknown) => includeETag ? { data, etag: response.headers.get('ETag') } : data;
  if (response.status === 204) return result(null);
  const type = response.headers.get('Content-Type');
  if (response.ok && ((acceptPNG && type === 'image/png') || (acceptImage && type?.startsWith('image/')))) return response.blob();
  const isJson = type?.includes('application/json');
  let data:unknown = null;
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

export async function getSession({signal}:{signal?:AbortSignal}={}) {
  const response=sessionSchema.safeParse(await request(apiBase+'me',{signal,activity:'poll'},''))
  if(!response.success) throw new ApiError(502)
  const session=response.data
  const principal=session.principal
  return {
    ...session,
    user: { ...principal, id: principal.account_id, adminCapabilities:session.access?.admin, navigation:session.access?.navigation, permissions:session.access?.permissions, roles:session.access?.roles ?? [] }
  };
}

function accessKey(access:Access|undefined) {
  const flags = (values:Record<string,boolean>|undefined) =>
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
  projects:{list?:string;focus?:string;scroll?:number;left?:number;windowY?:number;tabFocus?:boolean;files?:DriveMemory;taskViews:Map<string,string>} = {taskViews:new Map()};
  drive:DriveMemory = {};
  drafts = new Map<string,unknown>();
  owner = '';

  activate(session:Session) {
    this.owner = JSON.stringify([session.user.id, accessKey(session.access)]);
    try {
      if (sessionStorage.getItem(ownerKey) !== this.owner) clearStoredState();
      sessionStorage.setItem(ownerKey, this.owner);
    } catch {}
  }

  read(key:string):unknown {
    try {
      if (this.owner && sessionStorage.getItem(ownerKey) === this.owner)
        return JSON.parse(sessionStorage.getItem(storagePrefix + key) ?? 'null');
    } catch {}
    return null;
  }

  store(key:string,value:string|null) {
    try {
      if (!this.owner || sessionStorage.getItem(ownerKey) !== this.owner) return;
      if (value === null) sessionStorage.removeItem(storagePrefix + key);
      else sessionStorage.setItem(storagePrefix + key, value);
    } catch {}
  }

  dispose() {
    this.requests.abort();
    this.drafts.clear();
    this.projects={taskViews:new Map()};
    this.drive={};
    this.owner = '';
  }
}

export class SessionClient extends EventTarget {
  phase:Phase = 'checking';
  session:Session | null = null;
  scope = new SessionScope();
  csrfToken = '';
  pending:{abort:AbortController, promise:Promise<void>} | undefined;

  publish(phase:Phase,reason:Transition) {
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

  establish(next:Session) {
    this.invalidateProbe();
    const previous = this.session;
    const changed = previous && previous.user.id !== next.user.id;
    const accessChanged = previous && accessKey(previous.access) !== accessKey(next.access);
    if (!previous || changed || accessChanged) {
      this.resetScope();
      this.scope.activate(next);
    }
    this.session = next;
    this.csrfToken = next.csrf_token;
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

  guard<Args extends unknown[]>(callback:(...args:Args)=>void) {
    const signal = this.scope.requests.signal;
    return (...args:Args) => {
      if (!signal.aborted && this.phase === 'active') callback(...args);
    };
  }

  // 每次挂载页面时绑定其会话，旧页面的后续请求也受同一边界约束。
  bind() {
    const scope = this.scope;
    return async (path:string,options:RequestOptions={}) => {
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

  async login(username:string,password:string) {
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

  async changePassword(currentPassword:string,newPassword:string) {
    await this.bind()('/auth/password', {method:'POST', body:{currentPassword, newPassword}});
    this.invalidateProbe();
    await this.verify();
  }

  watch(win:Window,doc:Document) {
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

export function api(path:string, options:RequestOptions = {}) { return request(path, options, client.csrfToken); }

export function setSession(session:{csrf_token:string}|null) {
  const token = session?.csrf_token;
  client.csrfToken = typeof token === 'string' ? token : '';
}
