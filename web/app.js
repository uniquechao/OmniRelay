import { api, getSession, logout, mountLogin, setSession } from './login.js';
import { mountAdmin } from './admin.js';
import { feishuBotAPI, readAdminCapabilities, systemAPI } from './system-api.js';
import { mountFeishuBot } from './feishu-bot.js';
import { mountRail } from './rail.js';
import { mountAccount, updateAccount } from './account.js';

const titles = { users: '用户管理', roles: '角色权限', audit: '审计日志', 'feishu-bot': '飞书 Bot 配置' };
const ui = window.OmniRelayUI;
const shell = document.querySelector('.shell');
const page = document.getElementById('page');
const breadcrumb = document.getElementById('breadcrumb');
const mobileNav = document.getElementById('mobile-nav');
const systemNav = document.getElementById('system-nav');
const loginPage = document.getElementById('login-page');
const sessionStatus = document.getElementById('session-status');
const retrySession = sessionStatus.querySelector('button');
let session;
let routeRequest;
let view;
let renderedHash;
let checkingNavigation = false;
const oauthReturnKey = 'omnirelay.feishu.return-route';
const workbenchRoute = '#/workbench';
const loginRoute = '#/login';
let returnRoute = workbenchRoute;
let sessionCheck;

function isLoginRoute(hash) {
  return hash === loginRoute || hash.startsWith(loginRoute + '?');
}

function readReturnRoute() {
  try { return safeRoute(sessionStorage.getItem(oauthReturnKey)); }
  catch { return returnRoute; }
}

function rememberReturnRoute(hash) {
  returnRoute = safeRoute(hash);
  try { sessionStorage.setItem(oauthReturnKey, returnRoute); } catch {}
}

function clearReturnRoute() {
  returnRoute = workbenchRoute;
  try { sessionStorage.removeItem(oauthReturnKey); } catch {}
}

const login = mountLogin(loginPage, {
  onSuccess(nextSession) {
    establishSession(nextSession).then(render).catch(() => start());
  },
  returnTo() {
    rememberReturnRoute(readReturnRoute());
    return '/' + returnRoute;
  }
});

mountRail(shell, [systemNav]);
mountAccount({
  currentUser: () => session?.user,
  async onLogout() {
    if (view?.confirmNavigation && !await view.confirmNavigation()) return;
    await logout();
    showLogin({ preserveTarget: false });
  },
  async onSessionChanged(nextSession) {
    await establishSession(nextSession);
    updateAccount(session.user);
  }
});

async function protectedAPI(path, options) {
  const requestSession = session;
  try {
    return await api(path, options);
  } catch (error) {
    if (error.status === 401 && requestSession && session === requestSession && !options?.signal?.aborted) {
      showLogin();
    }
    throw error;
  }
}
const adminAPI = systemAPI(protectedAPI);
const botAPI = feishuBotAPI(protectedAPI);

function showLogin({ preserveTarget = true } = {}) {
  sessionCheck?.abort();
  session = null;
  setSession(null);
  const hash = location.hash;
  if (!preserveTarget) clearReturnRoute();
  else if (hash && !isLoginRoute(hash)) rememberReturnRoute(hash);
  else returnRoute = readReturnRoute();

  if (!preserveTarget || !isLoginRoute(hash)) {
    history.replaceState(history.state, '', loginRoute);
  }
  render();
}

function safeRoute(hash) {
  const section = hash?.split('/')[2];
  return Object.hasOwn(titles, section) && hash === '#/system/' + section ? hash : workbenchRoute;
}

function permission(user, section) {
  return Boolean(user.adminCapabilities?.[section === 'feishu-bot' ? 'feishu_bot' : section]);
}

async function establishSession(nextSession, signal) {
  const adminCapabilities = nextSession.access?.admin ?? await readAdminCapabilities(api, signal);
  signal?.throwIfAborted();
  nextSession.user = {
    ...nextSession.user,
    adminCapabilities,
    roles: nextSession.access?.roles ?? []
  };
  setSession(nextSession);
  session = nextSession;
}

function drawBreadcrumb(title, section) {
  breadcrumb.innerHTML = '<span></span><span aria-hidden="true">/</span><span aria-current="page"></span>';
  breadcrumb.firstElementChild.textContent = section;
  breadcrumb.lastElementChild.textContent = title;
}

function goToAdministration(user) {
  const available = Object.keys(titles).filter(section => permission(user, section));
  const title = document.createElement('h1');
  title.id = 'page-heading';
  title.tabIndex = -1;
  title.textContent = '工作台总览';
  page.replaceChildren(title);
  if (!available.length) return;
  const nav = document.createElement('div');
  nav.className = 'toolbar';
  for (const section of available) {
    const link = document.createElement('a');
    link.className = 'button';
    link.href = '#/system/' + section;
    link.textContent = titles[section];
    nav.append(link);
  }
  page.append(nav);
}

async function render() {
  routeRequest?.abort();
  view = null;
  routeRequest = new AbortController();
  const { signal } = routeRequest;
  sessionStatus.hidden = true;
  shell.hidden = !session;
  loginPage.hidden = Boolean(session);
  if (!session) {
    page.replaceChildren();
    loginPage.querySelector('#login-heading').focus();
    login.activate(new URLSearchParams(location.hash.split('?')[1]).get('error'));
    return;
  }
  const currentHash = location.hash;
  const hash = isLoginRoute(currentHash) ? readReturnRoute() : safeRoute(currentHash);
  if (currentHash && currentHash !== hash) history.replaceState(history.state, '', hash);
  clearReturnRoute();
  renderedHash = hash;
  updateAccount(session.user);
  const section = hash.split('/')[2];
  const title = section ? titles[section] : '工作台总览';
  const allowed = key => permission(session.user, key);
  systemNav.hidden = !Object.keys(titles).some(allowed);
  systemNav.dataset.active = String(Boolean(section && allowed(section)));
  if (section && allowed(section)) systemNav.open = true;
  for (const link of systemNav.querySelectorAll('[data-system]')) {
    const key = link.dataset.system;
    link.hidden = !allowed(key);
    if (section === key && allowed(key)) link.setAttribute('aria-current', 'page');
    else link.removeAttribute('aria-current');
    mobileNav.querySelector('option[value="system/' + key + '"]').hidden = !allowed(key);
  }
  mobileNav.querySelector('optgroup').hidden = systemNav.hidden;
  const overview = shell.querySelector('[data-nav="workbench"]');
  if (!section) overview.setAttribute('aria-current', 'page');
  else overview.removeAttribute('aria-current');
  drawBreadcrumb(title, section ? '系统管理' : '工作空间');
  mobileNav.value = hash.slice(2);
  page.dataset.layout = section && section !== 'feishu-bot' ? 'list' : 'content';
  page.scrollTop = 0;
  if (!section) goToAdministration(session.user);
  else if (!allowed(section)) {
    const heading = document.createElement('h1');
    heading.id = 'page-heading';
    heading.tabIndex = -1;
    heading.textContent = title;
    const message = document.createElement('p');
    message.className = 'empty';
    message.textContent = '当前账号没有访问此页面的权限。';
    page.replaceChildren(heading, message);
  } else if (section === 'feishu-bot') {
    const canConfigure = session.user.roles.includes('system_admin');
    view = mountFeishuBot(page, botAPI, {
      signal, canConfigure, canSyncDirectory: canConfigure && allowed('users')
    });
  } else {
    try {
      const admin = await mountAdmin(page, section, adminAPI, { user: session.user, signal });
      if (!signal.aborted) view = admin;
    } catch (error) {
      if (!signal.aborted) {
        page.textContent = '页面暂时无法加载，请刷新后重试。';
        ui.toast('error', '页面暂时无法加载。');
      }
    }
  }
  if (!signal.aborted) page.querySelector('#page-heading')?.focus({ preventScroll: true });
}

async function start() {
  sessionCheck?.abort();
  sessionCheck = new AbortController();
  const { signal } = sessionCheck;
  routeRequest?.abort();
  view = null;
  page.replaceChildren();
  shell.hidden = true;
  loginPage.hidden = true;
  session = undefined;
  setSession(null);
  sessionStatus.hidden = false;
  retrySession.hidden = true;
  sessionStatus.querySelector('p').textContent = '正在检查会话…';
  try {
    await establishSession(await getSession({ signal }), signal);
    if (!signal.aborted) await render();
  } catch (error) {
    if (signal.aborted) return;
    if (error.status === 401) showLogin();
    else {
      sessionStatus.querySelector('p').textContent = '暂时无法连接工作台。';
      retrySession.hidden = false;
    }
  }
}

retrySession.addEventListener('click', start);
mobileNav.addEventListener('change', () => { location.hash = '#/' + mobileNav.value; });
addEventListener('hashchange', async () => {
  if (session === undefined || checkingNavigation) return;
  if (!session) {
    showLogin();
    return;
  }
  const activeView = view;
  if (activeView?.confirmNavigation) {
    checkingNavigation = true;
    let allowed = false;
    try { allowed = await activeView.confirmNavigation(); }
    finally { checkingNavigation = false; }
    if (activeView !== view) return;
    if (!allowed) {
      history.replaceState(history.state, '', renderedHash);
      mobileNav.value = renderedHash.slice(2);
      return;
    }
  }
  render();
});
addEventListener('pageshow', event => {
  if (event.persisted) start();
});
start();
