import { api, getSession, logout, mountLogin, setSession } from './login.js';
import { mountAdmin } from './admin.js';
import { feishuBotAPI, systemAPI } from './system-api.js';
import { mountFeishuBot } from './feishu-bot.js';
import { mountConnectionSettings } from './connection-settings.js';
import { clearChatDrafts } from './chat-composer.js';
import { chatAPI, chatRoute, mountChatReader } from './chat-reader.js';
import { projectRoute, mountProjects, mountProjectSummary } from './projects.js';
import { mountRail } from './rail.js';
import { mountAccount, updateAccount } from './account.js';

import { mountDrive } from './drive.js';

const driveHash = '#/feishu/drive';
const titles = {
  users: '用户管理', roles: '角色权限', 'feishu-bot': '飞书 Bot 配置', settings: '连接设置', audit: '审计日志'
};
const legacyRoutes = new Map([
  ['#/settings', '#/system/settings'],
  ['#/system/bot', '#/system/feishu-bot']
]);
const ui = window.OmniRelayUI;
const shell = document.querySelector('.shell');
const page = document.getElementById('page');
const breadcrumb = document.getElementById('breadcrumb');
const mobileNav = document.getElementById('mobile-nav');
const systemNav = document.getElementById('system-nav');
const feishuNav = document.getElementById('feishu-nav');
const chatNav = feishuNav.querySelector('a');
const chatOption = mobileNav.querySelector('option[value="feishu/bot-chats"]');
const loginPage = document.getElementById('login-page');
const sessionStatus = document.getElementById('session-status');
const retrySession = sessionStatus.querySelector('button');
const sessionLoading = sessionStatus.querySelector('[data-session-loading]');
const sessionError = sessionStatus.querySelector('[data-session-error]');
let session;
let routeRequest;
let view;
let renderedHash;
let projectMemory = {};
let checkingNavigation = false;
const oauthReturnKey = 'omnirelay.feishu.return-route';
const workbenchRoute = '#/workbench';
const loginRoute = '#/login';
let returnRoute = '';
let accessRefresh;
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
  returnRoute = '';
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

mountRail(shell, [feishuNav, systemNav]);
mountAccount({
  currentUser: () => session?.user,
  onAuthError(error) {
    if (error.status === 401) showLogin();
    else if (error.status === 403) void refreshAccess();
  },
  async onLogout() {
    if (view?.confirmNavigation && !await view.confirmNavigation()) return;
    await logout();
    showLogin({ preserveTarget: false });
  },
  async onSessionChanged(nextSession) {
    await establishSession(nextSession);
    await render();
  }
});

async function protectedAPI(path, options) {
  const requestSession = session;
  try {
    return await api(path, options);
  } catch (error) {
    if (error.status === 401 && requestSession && session === requestSession && !options?.signal?.aborted) {
      showLogin();
    } else if (error.status === 403 && requestSession === session && !options?.signal?.aborted) {
      await refreshAccess();
    }
    throw error;
  }
}
const adminAPI = systemAPI(protectedAPI);
const botAPI = feishuBotAPI(protectedAPI);
const chats = chatAPI(protectedAPI);

function closeSessionOverlays() {
  for (const popover of document.querySelectorAll(':popover-open')) popover.hidePopover();
  for (const dialog of document.querySelectorAll('dialog[open]')) dialog.close('cancel');
}

function showLogin({ preserveTarget = true } = {}) {
  if (view?.hasDraft?.()) ui.toast('warning', '会话已结束，未保存的修改已清除。');
  projectMemory = {};
  clearChatDrafts();
  closeSessionOverlays();
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
  hash = legacyRoutes.get(hash) ?? hash;
  const project = projectRoute(hash);
  if (project) return project.hash;
  const chat = chatRoute(hash);
  if (chat) return chat.hash;
  const section = hash?.split('/')[2];
  if (hash === workbenchRoute || hash === driveHash) return hash;
  return Object.hasOwn(titles, section) && hash === '#/system/' + section ? hash : '';
}

function permission(user, section) {
  return user.navigation?.system === true && user.adminCapabilities?.[section === 'feishu-bot' ? 'feishu_bot' : section] === true;
}

function allowedRoute(hash) {
  if (!hash) return false;
  if (projectRoute(hash)) return session.user.navigation?.workbench === true;
  if (hash === driveHash || chatRoute(hash)) return session.user.navigation?.bot_chats === true;
  return hash === workbenchRoute ? session.user.navigation?.workbench === true : permission(session.user, hash.split('/')[2]);
}

function refreshAccess() {
  if (!session) return;
  if (accessRefresh?.session === session) return accessRefresh.pending;
  const current = session;
  const pending = (async () => {
    try {
      const next = await getSession();
      if (session !== current) return;
      const visibleAccess = access => [access?.roles, access?.admin, access?.navigation, access?.default_route, access?.permissions];
      if (current.user.id !== next.user.id || JSON.stringify(visibleAccess(current.access)) !== JSON.stringify(visibleAccess(next.access))) {
        ui.toast('warning', view?.hasDraft?.() ? '当前账号或权限已变更，未保存的修改已清除。' : '账号权限已更新。');
        clearChatDrafts();
        closeSessionOverlays();
        await establishSession(next);
        void render();
      } else {
        current.access = next.access;
        setSession(next);
      }
    } catch (error) {
      if (session !== current) return;
      if (error.status === 401) showLogin();
      else {
        clearSessionView();
        showSessionError();
      }
    } finally {
      if (accessRefresh?.session === current) accessRefresh = undefined;
    }
  })();
  accessRefresh = { session:current, pending };
  return pending;
}

async function establishSession(nextSession, signal) {
  signal?.throwIfAborted();
  nextSession.user = {
    ...nextSession.user,
    adminCapabilities:nextSession.access?.admin,
    navigation:nextSession.access?.navigation,
    permissions:nextSession.access?.permissions,
    roles: nextSession.access?.roles ?? []
  };
  if (session?.user.id !== nextSession.user.id) projectMemory = {};
  setSession(nextSession);
  session = nextSession;
}

function drawBreadcrumb(title, section) {
  breadcrumb.innerHTML = '<span></span><span aria-hidden="true">/</span><span aria-current="page"></span>';
  breadcrumb.firstElementChild.textContent = section;
  breadcrumb.lastElementChild.textContent = title;
}

function goToAdministration(user, signal) {
  const available = Object.keys(titles).filter(section => permission(user, section));
  const title = document.createElement('h1');
  title.id = 'page-heading';
  title.tabIndex = -1;
  title.textContent = '工作台总览';
  page.replaceChildren(title);

  const nav = document.createElement('div');
  nav.className = 'toolbar';
  const projects = document.createElement('a');
  projects.href = '#/projects';
  projects.className = 'button primary';
  projects.textContent = '查看项目列表';
  nav.append(projects);
  for (const section of available) {
    const link = document.createElement('a');
    link.className = 'button';
    link.href = '#/system/' + section;
    link.textContent = titles[section];
    nav.append(link);
  }
  const summary = document.createElement('section');
  page.append(nav,summary);
  void mountProjectSummary(summary,protectedAPI,signal);
}

async function render() {
  routeRequest?.abort();
  view = null;
  routeRequest = new AbortController();
  const { signal } = routeRequest;
  sessionStatus.hidden = true;
  sessionLoading.replaceChildren();
  sessionLoading.removeAttribute('aria-busy');
  shell.hidden = !session;
  loginPage.hidden = Boolean(session);
  if (!session) {
    page.replaceChildren();
    loginPage.querySelector('#login-heading').focus();
    login.activate(new URLSearchParams(location.hash.split('?')[1]).get('error'));
    return;
  }
  const currentHash = location.hash;
  const requested = isLoginRoute(currentHash) ? readReturnRoute() : safeRoute(currentHash);
  const fallback = safeRoute(session.access?.default_route?.replace(/^\//, ''));
  const hash = allowedRoute(requested) ? requested : allowedRoute(fallback) ? fallback : '';
  if (hash && currentHash !== hash) history.replaceState(history.state, '', hash);
  clearReturnRoute();
  renderedHash = hash;
  updateAccount(session.user);
  const chat = chatRoute(hash);
  const project = projectRoute(hash);
  const section = chat || project ? undefined : hash.split('/')[2];
  const drive = hash === driveHash;
  const title = drive ? '文件网盘' : project ? project.title || '项目列表' : chat ? '机器人记录' : section ? titles[section] : '工作台总览';
  feishuNav.hidden = chatOption.hidden = session.user.navigation?.bot_chats !== true;
  feishuNav.dataset.active = String(Boolean(chat || drive));
  mobileNav.querySelector('option[value="feishu/drive"]').hidden = feishuNav.hidden;
  const driveNav = feishuNav.querySelector('[data-nav="drive"]');
  if (drive) driveNav.setAttribute('aria-current','page');
  else driveNav.removeAttribute('aria-current');
  if (chat || drive) feishuNav.open = true;
  if (chat) chatNav.setAttribute('aria-current', 'page');
  else chatNav.removeAttribute('aria-current');
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
  overview.hidden = session.user.navigation?.workbench !== true;
  mobileNav.querySelector('option[value="workbench"]').hidden = overview.hidden;
  const projectLink = shell.querySelector('[data-nav="projects"]');
  projectLink.hidden = mobileNav.querySelector('option[value="projects"]').hidden = overview.hidden;
  if (project) projectLink.setAttribute('aria-current','page');
  else projectLink.removeAttribute('aria-current');
  const brand = shell.querySelector('.brand');
  if (allowedRoute(fallback)) brand.href = fallback;
  else brand.removeAttribute('href');
  if (hash === workbenchRoute) overview.setAttribute('aria-current', 'page');
  else overview.removeAttribute('aria-current');
  if (!hash) {
    page.textContent = '当前账号没有可访问的页面。';
    breadcrumb.replaceChildren();
    mobileNav.selectedIndex = -1;
    return;
  }
  drawBreadcrumb(title, project?.id ? '项目列表' : chat || drive ? '飞书' : section ? '系统管理' : '工作空间');
  mobileNav.value = project ? 'projects' : chat ? 'feishu/bot-chats' : hash.slice(2);
  page.dataset.layout = project?.id ? 'project' : chat ? 'bot-chat' : section && section !== 'feishu-bot' && section !== 'settings' ? 'list' : 'content';
  page.scrollTop = 0;
  if (drive) {
    mountDrive(page, protectedAPI, signal);
  } else if (project) {
    view = {};
    void mountProjects(page,project,protectedAPI,{signal,memory:projectMemory,userId:session.user.id,navigation:view});
  } else if (chat) {
    mountChatReader(page, chats, {
      signal, draftScope: session.user.id, chatId: chat.chatId, messageId: chat.messageId,
      onLocation(chatId, messageId) {
        if (signal.aborted || location.hash !== renderedHash) return;
        renderedHash = chatRoute('#/feishu/bot-chats' + (chatId ? '/' + chatId : '') +
          (messageId ? '/' + messageId : '')).hash;
        history.replaceState(history.state, '', renderedHash);
      }
    });
  } else if (!section) goToAdministration(session.user, signal);
  else if (section === 'feishu-bot') {
    const permissions = session.user.permissions ?? {};
    view = mountFeishuBot(page, botAPI, {
      signal, canConfigure: permissions['system.configure'] === true,
      canOperate: permissions['system.operate'] === true,
      canSyncDirectory: permissions['directory.sync'] === true && allowed('users')
    });
  } else if (section === 'settings') {
    mountConnectionSettings(page, options => adminAPI('/api/v1/system/connections', options), signal);
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

function clearSessionView() {
  routeRequest?.abort();
  closeSessionOverlays();
  view = null;
  page.replaceChildren();
  shell.hidden = true;
  loginPage.hidden = true;
  session = undefined;
  setSession(null);
  sessionStatus.hidden = false;
}

function showSessionError() {
  sessionLoading.replaceChildren();
  sessionLoading.removeAttribute('aria-busy');
  sessionLoading.hidden = true;
  sessionError.textContent = '暂时无法连接工作台。';
  sessionError.hidden = false;
  retrySession.hidden = false;
}

async function start() {
  sessionCheck?.abort();
  sessionCheck = new AbortController();
  const { signal } = sessionCheck;
  clearSessionView();
  retrySession.hidden = true;
  sessionError.hidden = true;
  sessionLoading.hidden = false;
  window.OmniRelayLoading.mount(sessionLoading,{size:'page',label:'正在检查会话…'});
  try {
    await establishSession(await getSession({ signal }), signal);
    if (!signal.aborted) await render();
  } catch (error) {
    if (signal.aborted) return;
    if (error.status === 401) showLogin();
    else showSessionError();
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
      mobileNav.value = chatRoute(renderedHash) ? 'feishu/bot-chats' : renderedHash.slice(2);
      return;
    }
  }
  routeRequest?.abort();
  const previous = session;
  await refreshAccess();
  if (session === previous) render();
});
document.addEventListener('visibilitychange', () => {
  if (document.visibilityState === 'visible') void refreshAccess();
});
addEventListener('focus', () => { void refreshAccess(); });
addEventListener('pageshow', event => {
  if (event.persisted) start();
});
start();
