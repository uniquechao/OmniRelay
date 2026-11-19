import { changePassword, getSession } from './login.js';

const ui = window.OmniRelayUI;
const shell = document.querySelector('.shell');
const menu = document.getElementById('account-menu');
const roles = {
  system_admin: '系统管理员', system_operator: '系统运维者', system_user: '普通用户', audit_viewer: '审计查看者',
  project_admin: '项目管理员', project_member: '项目成员'
};

function identity(user) {
  const name = user.name || user.display_name || '用户';
  const role = (user.roles || []).map(value => roles[value]).filter(Boolean).join('、');
  return { name, role: role || '—', source: user.auth_method === 'password' ? '账号密码' : '飞书' };
}

export function updateAccount(user) {
  const { name, role, source } = identity(user);
  const initial = Array.from(name)[0];
  for (const label of document.querySelectorAll('.account-trigger-name, .account-identity strong')) {
    label.textContent = name;
  }
  for (const avatar of document.querySelectorAll('.account-avatar, .mobile-account')) {
    avatar.textContent = initial;
  }
  for (const trigger of shell.querySelectorAll('.account-trigger')) {
    trigger.setAttribute('aria-label', '用户菜单，' + name);
  }
  menu.querySelector('.account-identity div span').textContent = role + ' · ' + source;
}

export function mountAccount({ currentUser, onLogout, onSessionChanged, onAuthError }) {
  const toolbarTrigger = () => shell.querySelector('.rail').getBoundingClientRect().width
    ? shell.querySelector('.rail .account-trigger') : shell.querySelector('.mobile-account');
  let trigger;
  let logoutPending = false;

  function placeMenu() {
    const anchor = toolbarTrigger().getBoundingClientRect();
    const left = Math.max(8, Math.min(anchor.left, innerWidth - menu.offsetWidth - 8));
    const top = anchor.top - menu.offsetHeight - 6;
    menu.style.inset = (top >= 8 ? top : anchor.bottom + 6) + 'px auto auto ' + left + 'px';
  }

  const restoreFocus = () => shell.hidden ? null
    : trigger?.getClientRects().length ? trigger : toolbarTrigger();

  function profile(user) {
    const { name, role, source } = identity(user);
    const fields = [['用户名', name], ['角色', role], ['身份来源', source]];
    ui.openDialog({
      title: '个人信息',
      body: '<div class="dialog-record"><dl class="facts">' + fields.map(([label, value]) =>
        '<div><dt>' + label + '</dt><dd>' + ui.escape(value) + '</dd></div>').join('') +
        '</dl></div>',
      restoreFocus
    });
  }

  function password(user) {
    if (user.auth_method !== 'password' && !user.can_change_password) {
      ui.openDialog({
        title: '修改密码',
        body: '<p class="dialog-note">此账号由飞书管理，请在飞书的账号安全设置中修改密码。</p>',
        restoreFocus
      });
      return;
    }
    let pending = false;
    const dialog = ui.openDialog({
      title: '修改密码',
      variant: 'form',
      confirm: '保存',
      body: '<label class="field">当前密码<input name="currentPassword" type="password" autocomplete="current-password" required></label>' +
        '<label class="field">新密码<input name="newPassword" type="password" autocomplete="new-password" minlength="15" required></label>',
      restoreFocus,
      onSubmit(form, submitter) {
        if (pending) return { keepOpen: true };
        pending = true;
        submitter.disabled = true;
        window.OmniRelayLoading.button(submitter,true,'保存中…');
        const current = form.elements.namedItem('currentPassword');
        const next = form.elements.namedItem('newPassword');
        const feedback = form.querySelector('.field-error');
        feedback.hidden = true;
        changePassword(current.value, next.value)
          .then(() => getSession())
          .then(async nextSession => {
            await onSessionChanged(nextSession);
            ui.toast('success', '密码已更新。');
            if (dialog.open) dialog.close('confirm');
          })
          .catch(error => {
            onAuthError(error);
            feedback.textContent = error.data?.error === 'https_required' ? '修改密码需要 HTTPS 安全连接。'
              : error.status === 403 ? '无法验证当前账号。请重新登录后再试。'
              : error.status === 400 || error.status === 422 ? '请核对当前密码和新密码要求。'
              : '暂时无法修改密码，请稍后再试。';
            feedback.hidden = false;
          })
          .finally(() => {
            current.value = '';
            next.value = '';
            pending = false;
            submitter.disabled = false;
            window.OmniRelayLoading.button(submitter,false);
          });
        return { keepOpen: true };
      }
    });
  }

  menu.addEventListener('beforetoggle', event => {
    if (event.newState === 'open') {
      trigger = toolbarTrigger();
      requestAnimationFrame(placeMenu);
    }
  });
  addEventListener('resize', () => { if (menu.matches(':popover-open')) placeMenu(); });
  menu.addEventListener('click', event => {
    const action = event.target.closest('[data-account]')?.dataset.account;
    const user = currentUser();
    if (!action || !user) return;
    menu.hidePopover();
    if (action === 'profile') profile(user);
    if (action === 'password') password(user);
    if (action === 'logout' && !logoutPending) {
      ui.openDialog({
        title: '退出工作台', variant: 'confirm', confirm: '退出',
        body: '<p class="dialog-note">退出后将返回登录页。</p>',
        restoreFocus,
        async onClose(confirmed) {
          if (!confirmed) return;
          logoutPending = true;
          try { await onLogout(); }
          catch (error) { ui.toast('error', '暂时无法退出，请稍后再试。'); }
          finally { logoutPending = false; }
        }
      });
    }
  });
}
