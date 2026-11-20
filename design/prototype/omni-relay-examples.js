(() => {
  function mountAccount(onLogout) {
    const menu = document.getElementById('account-menu');
    const shell = document.querySelector('.shell');
    const getTrigger = () => shell.querySelector('.rail').getBoundingClientRect().width
      ? shell.querySelector('.rail .account-trigger') : shell.querySelector('.mobile-account');
    let trigger;
    const details = '<dl class="facts"><div><dt>用户名</dt><dd>苏涵</dd></div>' +
      '<div><dt>角色</dt><dd>系统管理员</dd></div><div><dt>身份来源</dt><dd>飞书 · 示例账号</dd></div></dl>';
    const dialogs = {
      profile: { title:'个人信息', body:'<div class="dialog-record">' + details + '</div>' },
      password: {
        title:'修改密码', body:'<p class="dialog-note">此账号由飞书管理，请在飞书的账号安全设置中修改密码。</p>'
      },
      logout: {
        title:'退出工作台', variant:'confirm', body:'<p class="dialog-note">退出后将返回飞书登录页。</p>', confirm:'退出',
        onClose(confirmed) {
          if (!confirmed) return;
          onLogout();
        }
      }
    };
    function placeMenu() {
      const anchor = getTrigger().getBoundingClientRect();
      const left = Math.max(8, Math.min(anchor.left, innerWidth - menu.offsetWidth - 8));
      const top = anchor.top - menu.offsetHeight - 6;
      menu.style.inset = (top >= 8 ? top : anchor.bottom + 6) + 'px auto auto ' + left + 'px';
    }
    menu.addEventListener('beforetoggle', event => {
      if (event.newState === 'open') {
        trigger = getTrigger();
        requestAnimationFrame(placeMenu);
      }
    });
    addEventListener('resize', () => { if (menu.matches(':popover-open')) placeMenu(); });
    menu.addEventListener('click', event => {
      const option = event.target.closest('[data-account]');
      if (!option) return;
      menu.hidePopover();
      window.OmniRelayUI.openDialog({
        ...dialogs[option.dataset.account],
        restoreFocus:() => shell.hidden ? null : trigger?.getClientRects().length ? trigger : getTrigger()
      });
    });
  }

  const sample = [
    '## 交付准备',
    '',
    '> 周五核对验收清单，缺项由负责人补齐。',
    '',
    '- **负责人** 王宁',
    '- **当前状态** 进行中',
    '',
    '```python',
    'def pending_items(items):',
    '    return [item for item in items',
    '            if not item["done"]]',
    '```',
    '',
    '```json',
    '{',
    '  "project": "北辰交付准备",',
    '  "ready": false',
    '}',
    '```'
  ].join('\n');
  window.OmniRelayUI.mountAccount = mountAccount;
  window.OmniRelayExamples = { markdown:sample, fillEditor(editor) {
      editor.setContents({ ops:[
        {insert:'验收安排'}, {insert:'\n',attributes:{header:2}},
        {insert:'负责人',attributes:{bold:true}}, {insert:' 王宁，周五确认验收清单。\n'},
        {insert:'核对测试环境'}, {insert:'\n',attributes:{list:'bullet'}},
        {insert:'确认回滚依据'}, {insert:'\n',attributes:{list:'bullet'}}
      ] });
  }};
})();
