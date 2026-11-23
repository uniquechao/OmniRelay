/* OmniGrid ProjectTaskInspector layout and interactions adapted to the local prototype. */
window.OmniRelayTaskEditor = (() => {
  const {escape,icon,openDialog,toast} = OmniRelayUI;
  const storageKey = 'omni-relay.task-edits.v1';
  const stored = new Map();
  let routeGuard;
  const stamp = () => new Intl.DateTimeFormat('sv-SE',{
    timeZone:'Asia/Singapore',year:'numeric',month:'2-digit',day:'2-digit',hour:'2-digit',minute:'2-digit',second:'2-digit'
  }).format(new Date());
  try {
    for (const [key,value] of Object.entries(JSON.parse(localStorage.getItem(storageKey) || '{}'))) {
      if (value && typeof value.title === 'string' && typeof value.owner === 'string' &&
          ['pending','progress','danger','success','cancelled'].includes(value.stage) &&
          ['urgent','important','normal','low'].includes(value.priority) && Array.isArray(value.activities)) stored.set(key,value);
    }
  } catch {}

  function dateValue(value) {
    if (!value) return '';
    if (/^\d{4}-\d{2}-\d{2}$/.test(value)) return value;
    const parsed = value.match(/^(\d+)\s*月\s*(\d+)\s*日$/);
    return parsed ? '2026-' + parsed[1].padStart(2,'0') + '-' + parsed[2].padStart(2,'0') : '';
  }
  const displayDate = value => value ? Number(value.slice(5,7)) + ' 月 ' + Number(value.slice(8,10)) + ' 日' : '';
  const labels = {
    title:'标题',description:'正文',owner:'主负责人',stage:'状态',priority:'优先级',start:'开始日期',due:'截止日期',
    milestone:'里程碑',tags:'标签',participants:'参与人',acceptance:'验收条件',blocker:'受阻原因',completion:'完成结果',changeReason:'变更原因'
  };
  const seedDescriptions = [
    '目标：完成验收主路径的接口联调。\n范围：权限验证、核心接口、异常回滚。\n行动：测试环境恢复后组织端到端回归，登记缺失项并跟进关闭。',
    '目标：恢复可用的测试环境。\n依赖：数据库实例恢复。\n行动：检查依赖服务和访问凭据，完成冒烟验证后通知项目群。'
  ];

  function read(project,id,number,defaults) {
    const previous = stored.get(id + '/' + number);
    const [title,owner,due,stage] = project.tasks[number-1];
    return previous ? {...previous,changeReason:''} : {
      ...defaults,title,owner,due:dateValue(due),stage,start:defaults.start || '',
      description:id === 'beichen' ? seedDescriptions[number-1] || '' : '',
      acceptance:id === 'beichen' && number <= 2 ? (number === 1 ?
        '验收主路径联调通过，权限验证完成，记录验收结果。' : '测试环境恢复并通过冒烟验证。') : '',
      participants:[],tags:defaults.tag ? [defaults.tag] : [],blocker:defaults.blocker || '',completion:'',changeReason:'',
      created:'2026-11-17 10:00:00',updated:'2026-11-18 17:50:00',creator:project.lead,version:1,activities:[]
    };
  }

  function persist(id,number,value) {
    stored.set(id + '/' + number,value);
    try { localStorage.setItem(storageKey,JSON.stringify(Object.fromEntries(stored))); return true; }
    catch { return false; }
  }

  const taskNumber = (project,number) => project.short_code + '-T' + String(number).padStart(3,'0');

  function open({project,id,number,snapshot,dropStage,owners,milestones,stages,priorities,onSave,restoreFocus}) {
    let baseline = structuredClone(snapshot);
    const unique = 'task-editor-' + id + '-' + number;
    const taskCode = taskNumber(project,number);
    const initialStage = dropStage || baseline.stage;
    const options = (values,selected) => Object.entries(values).map(([key,label]) =>
      '<option value="' + escape(key) + '"' + (selected === key ? ' selected' : '') + '>' + escape(label) + '</option>').join('');
    const input = (name,label,type = 'text') => '<input type="' + type + '" name="' + name + '" id="' + unique + '-' + name +
      '" aria-label="' + label + '" value="' + escape(baseline[name] || '') + '">';
    const area = (name,label,rows=2) => '<label class="task-edit-label"><span>' + label + '</span><textarea name="' + name +
      '" aria-label="' + label + '" rows="' + rows + '" maxlength="10000">' + escape(baseline[name] || '') + '</textarea></label>';
    const prop = (name,label,control) => '<div class="task-property"><label for="' + unique + '-' + name + '">' + label + '</label>' + control + '</div>';
    const select = (name,label,values,selected) => prop(name,label,'<select name="' + name + '" id="' + unique + '-' + name +
      '" aria-label="' + label + '">' + options(values,selected) + '</select>');
    const ownerChoices = owners.map(name => ({id:name,label:name,search:({'王宁':'wang ning wn','陈悦':'chen yue cy','林晓':'lin xiao lx','赵可':'zhao ke zk'})[name] || ''}));
    const {combo,mountCombo} = OmniRelayDeveloper;
    const tagNames = [...new Set([...baseline.tags,'短期','联调','交付','质量','环境'])];
    const body = `<div class="task-edit-grid">
      <section class="task-edit-copy">
        <label class="task-edit-title"><span class="sr-only">任务标题</span>${input('title','任务标题')}</label>
        <label class="task-edit-description"><span class="sr-only">任务正文</span><textarea name="description" aria-label="任务正文" rows="6"
          placeholder="添加任务说明…" maxlength="10000">${escape(baseline.description)}</textarea></label>
        <div data-edit-blocker${initialStage === 'danger' ? '' : ' hidden'}>${area('blocker','受阻原因')}</div>
        <div data-edit-completion${initialStage === 'success' || baseline.completion ? '' : ' hidden'}>${area('completion','完成结果')}</div>
        <div data-edit-reason${baseline.stage === 'success' ? '' : ' hidden'}>${area('changeReason','变更原因')}</div>
        <details class="task-edit-extra"><summary>验收与参与人</summary>
          ${area('acceptance','验收条件')}
          ${combo(unique + '-participants','参与人','people',true)}
        </details>
      </section>
      <aside class="task-edit-sidebar" aria-label="任务属性">
        <section><h3>属性</h3>
          ${select('stage','状态',stages,initialStage)}
          ${select('priority','优先级',priorities,baseline.priority)}
          <div class="task-property task-edit-owner"><span>主负责人</span>${combo(unique + '-owner','负责人','people')}</div>
          ${prop('start','开始日期',input('start','开始日期','date'))}
          ${prop('due','截止日期',input('due','截止日期','date'))}
          ${select('milestone','里程碑',{'':'未关联',...milestones},baseline.milestone)}
          <div class="task-property task-edit-tags"><span>标签</span>
            <div><div class="task-edit-selected-tags"></div>
              <details><summary>添加标签</summary><div class="task-edit-tag-options">
                ${tagNames.map(name => '<label><input type="checkbox" name="tags" value="' + escape(name) + '"' +
                  (baseline.tags.includes(name) ? ' checked' : '') + '>' + escape(name) + '</label>').join('')}
              </div></details>
            </div>
          </div>
        </section>
        <section class="task-edit-metadata"><h3>详情</h3>
          <dl><dt>创建者</dt><dd>${escape(baseline.creator)}</dd><dt>创建于</dt><dd>${escape(baseline.created)}</dd>
            <dt>更新于</dt><dd data-edit-updated>${escape(baseline.updated)}</dd></dl>
          <details><summary>版本与更新人</summary><p data-edit-version>版本 v${baseline.version}</p><p>更新人　我</p><p>业务时区　Asia/Singapore</p></details>
        </section>
      </aside>
      <section class="task-edit-comment" aria-label="任务评论">
        <label><span class="sr-only">发表评论</span><textarea name="commentDraft" aria-label="发表评论" rows="2"
          maxlength="4000" placeholder="留下评论…"></textarea></label>
        <button type="button" class="button" data-post-comment aria-label="提交评论" disabled>${icon('compose-send')}</button>
      </section>
      <section class="task-edit-activity" aria-label="任务动态">
        <header><h3>动态</h3><div role="group" aria-label="动态类型">
          ${[['all','全部'],['edit','修改记录'],['comment','评论']].map(([kind,label]) => '<button type="button" class="button" data-activity-kind="' +
            kind + '" aria-pressed="' + (kind === 'all') + '">' + label + '</button>').join('')}
        </div></header><ol data-task-activities></ol>
      </section>
    </div>`;

    let ownerQuery = false;
    let activityFilter = 'all';
    const openingRoute = location.hash;
    let pendingRoute;
    let dialog;
    const readValues = form => ({
      title:form.elements.title.value.trim(),description:form.elements.description.value,
      stage:form.elements.stage.value,priority:form.elements.priority.value,owner:form.querySelector('[data-editor-owner] [data-combo-result]').value,
      start:form.elements.start.value,due:form.elements.due.value,milestone:form.elements.milestone.value,
      acceptance:form.elements.acceptance.value,participants:form.querySelector('[data-editor-participants] [data-combo-result]').value.split(',').filter(Boolean),
      tags:[...form.querySelectorAll('[name=tags]:checked')].map(el => el.value),blocker:form.elements.blocker.value.trim(),
      completion:form.elements.completion.value,changeReason:form.elements.changeReason.value
    });
    const comparable = value => Object.fromEntries(Object.keys(labels).map(name => [
      name,Array.isArray(value[name]) ? [...value[name]].sort().join('、') : value[name] || ''
    ]));
    const isDirty = form => ownerQuery || JSON.stringify(comparable(readValues(form))) !== JSON.stringify(comparable(baseline));
    const describe = (key,value) => {
      if (key === 'stage') return stages[value];
      if (key === 'priority') return priorities[value];
      if (key === 'milestone') return milestones[value] || '未关联';
      return (Array.isArray(value) ? value.join('、') : value) || '未设置';
    };
    function paintActivity() {
      const entries = baseline.activities.filter(entry => activityFilter === 'all' || entry.kind === activityFilter);
      dialog.querySelector('[data-task-activities]').innerHTML = entries.length ? entries.map(entry =>
        '<li data-kind="' + entry.kind + '"><header><strong>我</strong><span>' + (entry.kind === 'comment' ? '评论' : '修改任务') +
        '</span><time>' + escape(entry.at) + '</time></header>' + (entry.kind === 'comment' ? '<p>' + escape(entry.text) + '</p>' :
          '<dl>' + entry.changes.map(change => '<div><dt>' + escape(labels[change.name]) + '</dt><dd>' +
            escape(describe(change.name,change.before)) + '<span aria-hidden="true"> → </span>' + escape(describe(change.name,change.after)) +
            '</dd></div>').join('') + '</dl>') + '</li>').join('') : '<li class="aux">暂无动态。</li>';
    }
    function updateControls() {
      const form = dialog.querySelector('form');
      dialog.querySelector('[data-task-save]').disabled = !isDirty(form);
      dialog.querySelector('[data-post-comment]').disabled = !form.elements.commentDraft.value.trim();
      const stageControl = form.elements.stage;
      stageControl.dataset.stage = stageControl.value;
      dialog.querySelector('[data-edit-reason]').hidden = stageControl.value !== 'cancelled' && baseline.stage !== 'success';
      dialog.querySelector('[data-edit-blocker]').hidden = stageControl.value !== 'danger';
      dialog.querySelector('[data-edit-completion]').hidden = form.elements.stage.value !== 'success' && !baseline.completion;
      const tags = [...form.querySelectorAll('[name=tags]:checked')].map(el => el.value);
      dialog.querySelector('.task-edit-selected-tags').innerHTML = tags.map(name => '<span>' + escape(name) + '</span>').join('');
    }
    dialog = openDialog({
      title:'任务详情',description:project.name + ' · ' + taskCode,body,drawer:'wide',variant:'form',confirm:'保存任务',
      isDirty:form => isDirty(form) || Boolean(form.elements.commentDraft.value.trim()),
      onSubmit(form) {
        const next = readValues(form);
        if (ownerQuery) return {field:form.querySelector('[data-editor-owner] [role=combobox]'),message:'请从搜索结果中选择负责人。'};
        if (!next.title) return {field:form.elements.title,message:'请输入任务名称。'};
        if (next.stage === 'danger' && !next.blocker) return {field:form.elements.blocker,message:'请填写受阻原因。'};
        if (baseline.stage === 'success' && next.stage !== 'success' && !next.changeReason.trim()) {
          return {field:form.elements.changeReason,message:'请填写重新打开任务的变更原因。'};
        }
        if (next.stage === 'cancelled' && !next.changeReason.trim()) return {field:form.elements.changeReason,message:'请填写取消任务的变更原因。'};
        if (next.start && next.due && next.start > next.due) return {field:form.elements.due,message:'截止日期不能早于开始日期。'};
        const changes = Object.keys(labels).filter(name =>
          comparable(next)[name] !== comparable(baseline)[name]).map(name => ({name,before:baseline[name],after:next[name]}));
        if (changes.length) {
          baseline = {...baseline,...next,version:baseline.version+1,updated:stamp(),
            activities:[{kind:'edit',at:stamp(),changes},...baseline.activities]};
          baseline.changeReason = '';
          form.elements.changeReason.value = '';
          const saved = persist(id,number,baseline);
          onSave(baseline);
          toast(saved ? 'success' : 'info',saved ? '示例任务已保存' : '任务已更新，仅本次浏览有效');
          dialog.querySelector('[data-edit-updated]').textContent = baseline.updated;
          dialog.querySelector('[data-edit-version]').textContent = '版本 v' + baseline.version;
          dialog.querySelector('.task-path-title').textContent = baseline.title;
          paintActivity();
        }
        if (form.elements.commentDraft.value.trim()) postComment();
        updateControls();
        return {keepOpen:true};
      },
      onClose() {
        routeGuard = undefined;
        if (pendingRoute) location.hash = pendingRoute;
        else if (location.hash === '#/projects/' + id + '/tasks/' + number) window.history.replaceState(null,'','#/projects/' + id + '/tasks');
      },
      restoreFocus
    });
    dialog.classList.add('or-task-inspector');
    dialog.querySelector('form').noValidate = true;
    const ownerCombo = dialog.querySelector('.task-edit-owner [data-combo]');
    ownerCombo.dataset.editorOwner = '';
    const participants = dialog.querySelector('.task-edit-extra [data-combo]');
    participants.dataset.editorParticipants = '';
    mountCombo(ownerCombo,{choices:[{id:'',label:'未分配'},...ownerChoices],initial:baseline.owner});
    const ownerSearch = ownerCombo.querySelector('[role=combobox]');
    const ownerOutput = ownerCombo.querySelector('[data-combo-result]');
    const ownerPortrait = document.createElement('span');
    ownerPortrait.className = 'task-edit-owner-avatar';
    ownerCombo.append(ownerPortrait);
    function showOwner() {
      const name = ownerOutput.value;
      ownerSearch.value = name || '未分配';
      ownerPortrait.innerHTML = name ? OmniRelayUI.personAvatar(name,({'王宁':'wang-ning','陈悦':'chen-yue','林晓':'lin-xiao','赵可':'zhao-ke'})[name] || name) : '';
      ownerSearch.style.paddingInlineStart = name ? '32px' : '7px';
      ownerQuery = false;
    }
    showOwner();
    ownerCombo.addEventListener('input',event => {
      if (event.target === ownerOutput) showOwner();
      else if (event.target === ownerSearch) ownerQuery = true;
    });
    mountCombo(participants,{choices:ownerChoices,initial:baseline.participants});
    const top = dialog.querySelector('.dialog-head');
    const close = dialog.querySelector('.dialog-close');
    const save = dialog.querySelector('[type=submit]');
    save.dataset.taskSave = '';
    top.innerHTML = '<span class="task-inspector-path">' + icon('nav-components') + '<span class="task-path-project">' + escape(project.name) +
      '</span>' + icon('action-expand') + '<span class="task-path-code">' + taskCode + '</span><span class="task-path-title">' +
      escape(baseline.title) + '</span></span><div class="task-edit-top-actions"></div>';
    close.innerHTML = icon('task-close-panel');
    top.querySelector('div').append(save,close);
    close.setAttribute('aria-label','收起任务详情');
    dialog.querySelector('.dialog-footer').hidden = true;
    // The native dialog keeps the established focus and draft guard behavior.
    dialog.setAttribute('aria-label','任务详情');
    dialog.removeAttribute('aria-labelledby');
    dialog.removeAttribute('aria-describedby');
    function postComment() {
      const field = dialog.querySelector('[name=commentDraft]');
      const text = field.value.trim();
      if (!text) return;
      baseline = {...baseline,activities:[{kind:'comment',text,at:stamp()},...baseline.activities]};
      const saved = persist(id,number,baseline);
      field.value = '';
      paintActivity();
      toast(saved ? 'success' : 'info',saved ? '示例评论已发送' : '评论已添加，仅本次浏览有效');
    }
    dialog.addEventListener('input',updateControls);
    dialog.addEventListener('change',updateControls);
    dialog.querySelector('[data-post-comment]').addEventListener('click',() => { postComment(); updateControls(); });
    dialog.addEventListener('click',event => {
      const choice = event.target.closest('[data-activity-kind]');
      if (!choice) return;
      activityFilter = choice.dataset.activityKind;
      for (const button of dialog.querySelectorAll('[data-activity-kind]')) button.setAttribute('aria-pressed',String(button === choice));
      paintActivity();
    });
    function place() {
      const bounds = document.getElementById('project-panel').getBoundingClientRect();
      dialog.style.setProperty('--task-edit-left',Math.max(0,bounds.left) + 'px');
      dialog.style.setProperty('--task-edit-top',Math.max(0,bounds.top) + 'px');
      dialog.style.setProperty('--task-edit-width',Math.min(innerWidth - bounds.left,bounds.width) + 'px');
      dialog.style.setProperty('--task-edit-height',Math.max(180,Math.min(innerHeight - bounds.top,bounds.height)) + 'px');
    }
    routeGuard = () => {
      if (location.hash === openingRoute) return false;
      pendingRoute = location.hash;
      window.history.replaceState(null,'',openingRoute);
      close.click();
      return true;
    };
    dialog.addEventListener('or-dialog-guard-cancel',() => { pendingRoute = undefined; });
    const beforeUnload = event => {
      const form = dialog.querySelector('form');
      if (!isDirty(form) && !form.elements.commentDraft.value.trim()) return;
      event.preventDefault();
      event.returnValue = '';
    };
    window.addEventListener('beforeunload',beforeUnload);
    const observer = new ResizeObserver(place);
    observer.observe(document.getElementById('project-panel'));
    window.addEventListener('resize',place);
    dialog.addEventListener('close',() => { observer.disconnect();window.removeEventListener('resize',place);window.removeEventListener('beforeunload',beforeUnload); },{once:true});
    place();
    paintActivity();
    updateControls();
    dialog.querySelector('[name=title]').focus({preventScroll:true});
    return dialog;
  }

  return { read, persist, open, taskNumber, dateValue, displayDate, guardRoute:() => routeGuard?.() || false };
})();
