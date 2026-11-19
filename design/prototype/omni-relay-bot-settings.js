/* Local interaction demo for configuration and permission checks. */
window.OmniRelayBotSettings = (() => {
  const { escape,toast } = OmniRelayUI;
  const { botTestUsers,checkBotProjectAccess,recordBotSettings } = OmniRelaySystem;
  let config = { appId:'cli_demo_omnirelay', secretRef:'omni-relay-feishu/app-secret', enabled:true };
  let revision = 1;
  const scenarios = {
    ready:'应用配置正常', credentials:'凭据不可用', bot:'应用未启用机器人能力',
    connection:'长连接未建立', subscription:'消息事件未订阅',
    scope:'缺少消息发送权限', group:'Bot 未加入目标群'
  };
  const operations = { read:'查询共享任务', assign:'分派任务', brief:'管理简报发布' };
  const projects = { north:'北辰交付项目', pilot:'启明试点项目' };
  const availabilitySteps = ['应用凭据','Bot 能力','长连接与事件订阅','消息权限'];
  const permissionSteps = [...availabilitySteps,'Bot 与目标群','账号及项目权限'];
  const options = items => Object.entries(items).map(([id,label]) =>
    '<option value="' + escape(id) + '">' + escape(label) + '</option>').join('');
  const select = (name,label,items) =>
    '<label class="field"><span class="field-label">' + label + '</span><select class="select" name="' + name + '">' +
    options(items) + '</select></label>';
  const feedback = (kind,initial) => '<div class="bot-check-feedback" data-bot-result="' + kind +
    '"><div class="aux" data-bot-check-summary role="status">' + initial +
    '</div><ol class="bot-check-steps" data-bot-steps hidden></ol></div>';

  function render() {
    const users = botTestUsers();
    return `<div class="stack" data-bot-settings>
      <section class="panel">
        <header class="section-head"><h2>应用配置</h2><span class="chip" data-tone="important">原型演示</span></header>
        <form id="bot-config" class="stack">
          <fieldset class="bot-fieldset">
            <div class="bot-fields">
              <label class="field"><span class="field-label">App ID</span>
                <input name="appId" value="${escape(config.appId)}" maxlength="120" required spellcheck="false">
              </label>
              <label class="field"><span class="field-label">App Secret 引用</span>
                <input name="secretRef" value="${escape(config.secretRef)}" maxlength="160" required spellcheck="false" aria-describedby="bot-secret-hint">
                <span class="aux" id="bot-secret-hint">填写部署端的密钥引用名。</span>
              </label>
            </div>
            <div class="bot-settings-line">
              <label class="bot-check"><input type="checkbox" name="enabled"${config.enabled ? ' checked' : ''}>启用 Bot</label>
              <span class="aux">接入方式 · 飞书长连接</span>
            </div>
            <div class="bot-settings-line">
              <span class="aux" data-bot-config-state role="status"></span>
              <button type="submit" class="button primary" disabled>保存配置</button>
            </div>
          </fieldset>
        </form>
      </section>
      <section class="panel">
        <header class="section-head"><h2>Bot 可用性验证</h2></header>
        <form id="bot-availability">
          <fieldset class="bot-fieldset">
            <div class="bot-fields">${select('scenario','模拟运行环境',scenarios)}</div>
            <div class="bot-settings-line">
              <span class="aux">检查 Bot 自身状态，无需选择项目。</span>
              <button type="submit" class="button primary">验证 Bot 可用性</button>
            </div>
          </fieldset>
        </form>
        ${feedback('availability','尚未验证 Bot 可用性。')}
      </section>
      <section class="panel">
        <header class="section-head"><h2>权限联调验证</h2></header>
        <form id="bot-check" class="stack">
          <fieldset class="bot-fieldset">
            <div class="bot-fields">
              ${select('userId','测试账号',Object.fromEntries(users.map(user => [user.id,user.name + (user.active ? '' : ' · 已停用')])))}
              ${select('project','目标项目',projects)}
              ${select('operation','验证操作',operations)}
              ${select('visibility','群内信息范围',{ public:'群内可公开',internal:'仅项目内可见' })}
            </div>
            <div class="bot-settings-line">
              <span class="aux">沿用上方模拟环境和已保存的示例配置。</span>
              <button type="submit" class="button primary">开始验证</button>
            </div>
          </fieldset>
        </form>
        ${feedback('permission','尚未运行权限联调。')}
      </section>
    </div>`;
  }

  function availabilityChecks(saved,scenario) {
    return [
      () => !saved.enabled ? { allowed:false,reason:'请先启用 Bot，再运行联调。' } :
        scenario === 'credentials' ? { allowed:false,reason:'示例凭据校验失败，请检查 App ID 与部署端密钥引用。' } :
          { allowed:true,reason:'示例 App ID 与密钥引用匹配。' },
      () => scenario === 'bot' ?
        { allowed:false,reason:'该应用未启用机器人能力。' } :
        { allowed:true,reason:'应用已启用机器人能力。' },
      () => scenario === 'connection' ? { allowed:false,reason:'示例长连接尚未建立。' } :
        scenario === 'subscription' ? { allowed:false,reason:'长连接可用，应用未订阅接收消息事件。' } :
          { allowed:true,reason:'长连接和接收消息事件订阅可用。' },
      () => scenario === 'scope' ?
        { allowed:false,reason:'应用缺少消息发送权限，无法在项目群返回结果。' } :
        { allowed:true,reason:'应用具备接收和发送消息所需权限。' }
    ];
  }

  function permissionChecks(saved,scenario,test) {
    return [
      ...availabilityChecks(saved,scenario),
      () => scenario === 'group' ?
        { allowed:false,reason:'示例 Bot 尚未加入「' + projects[test.project] + '」的绑定群。' } :
        { allowed:true,reason:'Bot 已加入「' + projects[test.project] + '」的绑定群。' },
      () => checkBotProjectAccess(test)
    ];
  }

  function mount(page) {
    const root = page.querySelector('[data-bot-settings]');
    const settings = root.querySelector('#bot-config');
    const availability = root.querySelector('#bot-availability');
    const verification = root.querySelector('#bot-check');
    const configState = root.querySelector('[data-bot-config-state]');
    const forms = [settings,availability,verification];
    const outputs = Object.fromEntries(['availability','permission'].map(kind => {
      const view = root.querySelector('[data-bot-result="' + kind + '"]');
      const summary = view.querySelector('[data-bot-check-summary]');
      return [kind,{ summary,list:view.querySelector('[data-bot-steps]'),initial:summary.textContent }];
    }));
    verification.elements.userId.value = 'U-002';
    let running = false;

    function readConfig() {
      return {
        appId:settings.elements.appId.value.trim(),
        secretRef:settings.elements.secretRef.value.trim(),
        enabled:settings.elements.enabled.checked
      };
    }

    function syncButtons() {
      const candidate = readConfig();
      const dirty = Object.keys(config).some(key => config[key] !== candidate[key]);
      settings.querySelector('[type="submit"]').disabled = running || !dirty;
      availability.querySelector('[type="submit"]').disabled = running || dirty;
      verification.querySelector('[type="submit"]').disabled = running || dirty;
      configState.textContent = dirty ? '保存修改后可以开始验证。' : '示例配置已保存 · 版本 ' + revision;
    }

    function resetResult({summary,list,initial}) {
      list.hidden = true;
      list.replaceChildren();
      summary.dataset.tone = '';
      summary.textContent = initial;
      summary.removeAttribute('aria-busy');
    }

    function resetResults() {
      Object.values(outputs).forEach(resetResult);
    }

    settings.addEventListener('input', () => {
      const { appId,secretRef } = settings.elements;
      appId.setCustomValidity(appId.value.trim() ? '' : '请输入 App ID。');
      secretRef.setCustomValidity(secretRef.value.trim() ? '' : '请输入密钥引用名。');
      resetResults();
      syncButtons();
    });
    settings.addEventListener('submit',event => {
      event.preventDefault();
      if (running || settings.querySelector('[type="submit"]').disabled) return;
      const previous = '版本 ' + revision + ' · ' + (config.enabled ? '启用' : '停用');
      config = readConfig();
      revision += 1;
      recordBotSettings(previous,'版本 ' + revision + ' · ' + (config.enabled ? '启用' : '停用'));
      syncButtons();
      toast('success','已保存飞书 Bot 示例配置。');
    });
    async function runChecks({summary,list},steps,evaluate,{progress,success,failure}) {
      running = true;
      forms.forEach(form => { form.querySelector('fieldset').disabled = true; });
      syncButtons();
      summary.dataset.tone = '';
      const loading = OmniRelayLoading.mount(summary,{size:'compact',label:progress,progress:0});
      list.hidden = false;
      const rows = steps.map(step => {
        const row = document.createElement('li');
        row.innerHTML = '<strong>' + step + '</strong><span class="chip" data-tone="pending">待检查</span><p class="aux"></p>';
        return row;
      });
      list.replaceChildren(...rows);

      let blocked = false;
      for (const [index,row] of rows.entries()) {
        const label = row.querySelector('.chip');
        if (blocked) {
          label.textContent = '未执行';
          row.querySelector('p').textContent = '前一步未通过。';
          continue;
        }
        label.textContent = '检查中';
        label.dataset.tone = 'progress';
        await new Promise(resolve => setTimeout(resolve,190));
        if (!root.isConnected) return;
        const { allowed, reason } = evaluate[index]();
        blocked = !allowed;
        label.textContent = allowed ? '通过' : '未通过';
        label.dataset.tone = allowed ? 'success' : 'danger';
        row.querySelector('p').textContent = reason;
        loading.update({progress:(index + 1) / rows.length * 100});
      }

      summary.dataset.tone = blocked ? 'danger' : 'success';
      summary.textContent = blocked ? failure : success;
      summary.removeAttribute('aria-busy');
      forms.forEach(form => { form.querySelector('fieldset').disabled = false; });
      running = false;
      syncButtons();
    }

    availability.addEventListener('change',resetResults);
    verification.addEventListener('change',() => resetResult(outputs.permission));
    availability.addEventListener('submit',event => {
      event.preventDefault();
      if (availability.querySelector('[type="submit"]').disabled) return;
      runChecks(outputs.availability,availabilitySteps,availabilityChecks(config,availability.elements.scenario.value),{
        progress:'正在验证 Bot 可用性…',
        success:'模拟可用 · 应用能力和连接检查全部通过。',
        failure:'模拟不可用。处理未通过项后可重新验证。'
      });
    });
    verification.addEventListener('submit',event => {
      event.preventDefault();
      if (verification.querySelector('[type="submit"]').disabled) return;
      const test = Object.fromEntries(new FormData(verification));
      runChecks(outputs.permission,permissionSteps,permissionChecks(config,availability.elements.scenario.value,test),{
        progress:'正在验证「' + operations[test.operation] + '」…',
        success:'模拟通过 · 测试账号可对「' + projects[test.project] + '」执行「' + operations[test.operation] + '」。',
        failure:'模拟未通过。先处理未通过项，再重新验证。'
      });
    });
    syncButtons();
  }

  return { render,mount };
})();
