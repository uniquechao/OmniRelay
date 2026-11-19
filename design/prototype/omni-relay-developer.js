/* Interactive standards and isolated sample data for the developer menu. */
window.OmniRelayDeveloper = (() => {
  const { escape, icon, openDialog, mountList, mountOrganizationTree, mountPersonCards, toast } = OmniRelayUI;
  const { richEditor, mountRich, markdownView, mountMarkdown } = OmniRelayContent;
  const pages = { list:'列表页', form:'表单页', info:'信息展示页', components:'组件列表', layout:'工作台布局说明', colors:'色彩系统' };
  const statuses = { progress:'进行中', success:'已完成', pending:'待开始', disabled:'已禁用' };
  const categories = [
    { id:'delivery', label:'交付验收', search:'jiaofuyanshou jfys' },
    { id:'pilot', label:'内部试点', search:'neibushidian nbsd' },
    { id:'quality', label:'质量改进', search:'zhilianggaijin zlgj' },
    { id:'operations', label:'运营支持', search:'yunyingzhichi yyzc' }
  ];
  const people = [
    { id:'wang-ning', label:'王宁', detail:'交付中心 / 交付一组', search:'wangning wn' },
    { id:'chen-yue', label:'陈悦', detail:'研发中心 / 平台组', search:'chenyue cy' },
    { id:'lin-xiao', label:'林晓', detail:'研发中心 / 产品组', search:'linxiao lx' },
    { id:'zhao-ke', label:'赵可', detail:'交付中心 / 测试组', search:'zhaoke zk' }
  ];
  const departments = [
    { id:'engineering', label:'研发中心', teams:[{id:'platform',label:'平台组'}, {id:'product',label:'产品组'}] },
    { id:'delivery', label:'交付中心', teams:[{id:'delivery-1',label:'交付一组'}, {id:'testing',label:'测试组'}] }
  ];
  const records = Array.from({ length:14 }, (_, index) => ({
    id:'sample-' + (index+1), name:[
      '北辰交付准备','启明内部试点','远山验收演练','长风需求收集','星河质量专项','向阳文档治理','青禾灰度复核',
      '栖云运营交接','南星联调试点','海棠环境准备','新桥工单整理','山岚数据迁移','东篱回归验证','明川项目复盘'
    ][index],
    owner:people[index % people.length].label, status:['progress','pending','success','disabled'][index % 4]
  }));
  const componentRecords = records.slice(0,5).map(row => ({ ...row }));
  const note = '本周完成联调验收准备。\n接下来确认测试环境恢复时间。\n待负责人补充验收清单与回滚依据。';
  let drawerProject = { name:'北辰交付准备', owner:'王宁', status:'progress', note };
  const panel = (heading, body) => '<section class="panel"><header class="section-head"><h2>' + heading + '</h2></header>' + body + '</section>';
  const badge = tone => '<span class="chip" data-tone="' + (tone === 'disabled' ? 'pending' : tone) + '">' + statuses[tone] + '</span>';

  function field(id, label, control, extra = '') {
    return '<div class="field ' + extra + '"><label for="' + id + '">' + label + '</label>' + control + '</div>';
  }

  function combo(id, label, source, multiple = false) {
    return `<div class="field"><label for="${id}">${label}</label>
      <div class="dev-combo" data-combo data-source="${source}" data-multiple="${multiple}">
        <input id="${id}" autocomplete="off" role="combobox" aria-autocomplete="list" aria-expanded="false" aria-controls="${id}-options" placeholder="输入关键词搜索">
        <input type="hidden" name="${id}" data-combo-result>
        <div class="combo-box" id="${id}-options" hidden>
          <div role="listbox" aria-label="${label}选项"${multiple ? ' aria-multiselectable="true"' : ''}></div>
          <p class="combo-empty" role="status" hidden>没有匹配的选项。</p>
        </div>
        <div class="combo-values" aria-label="已选内容"></div>
      </div>
    </div>`;
  }

  const personView = person => OmniRelayUI.personView({ name:person.label, personKey:person.id, org:person.detail });

  function formPage() {
    return panel('项目基本信息', `<form class="dev-fields" id="developer-form" novalidate>
      ${field('sample-title','项目名称','<input id="sample-title" name="title" required maxlength="60" placeholder="输入项目名称">')}
      ${combo('sample-category','项目分类','categories')}
      ${combo('sample-owner','人员','people')}
      ${field('sample-date','计划日期','<input id="sample-date" name="date" type="date">')}
      ${organizationPicker('sample-organization','organization')}
      ${field('sample-description','备注','<textarea id="sample-description" name="note" rows="3" placeholder="补充计划说明"></textarea>','full-field')}
      ${richEditor('sample-body','项目说明（富文本）')}
      <p class="field-error full-field" role="alert" id="sample-form-error" hidden></p>
      <div class="dev-actions full-field">
        <button class="button primary" type="submit">提交示例</button>
        <button class="button neutral" type="reset">重置</button>
      </div>
    </form>`);
  }

  function infoPage() {
    return '<div class="stack">' + panel('概要', '<dl class="facts"><div><dt>项目名称</dt><dd>北辰交付准备</dd></div>' +
      '<div><dt>状态</dt><dd>' + badge('progress') + '</dd></div><div><dt>计划日期</dt><dd>2026-11-20</dd></div></dl>') +
      panel('人员', personView(people[0])) + panel('项目说明', '<div class="multiline" tabindex="0">' + escape(note) + '</div>') + '</div>';
  }

  function dateStrip(mode) {
    const week = mode === 'week';
    return `<div class="date-strip" data-date-mode="${mode}">
      <button class="button date-arrow date-prev" type="button" data-date-step="-1" aria-label="${week ? '上一周' : '前一天'}">${icon('action-expand')}</button>
      <div class="date-summary" aria-live="polite" aria-atomic="true">
        <strong data-date-primary></strong><span class="aux" data-date-secondary></span>
      </div>
      <button class="button date-arrow" type="button" data-date-step="1" aria-label="${week ? '下一周' : '后一天'}">${icon('action-expand')}</button>
    </div>`;
  }

  function mountTree(host, { groups = departments, ...options } = {}) {
    const nodes = groups.flatMap(group => [
      { id:group.id, name:group.label },
      ...group.teams.map(team => ({ id:team.id, name:team.label, parentId:group.id }))
    ]);
    return mountOrganizationTree(host, nodes, { includeAll:false, ...options });
  }

  function organizationPicker(id, name) {
    return `<div class="field" data-org-picker>
      <label id="${id}-label" for="${id}">所属组织</label>
      <div class="org-picker-controls">
        <button class="org-picker-trigger" id="${id}" type="button" data-org-open aria-haspopup="dialog"
          aria-labelledby="${id}-label ${id}-value">
          ${icon('nav-projects')}<span id="${id}-value" data-org-value>选择组织</span>${icon('action-expand')}
        </button>
        <button class="button org-picker-clear" type="button" data-org-clear aria-label="清除所选组织" title="清除所选组织" hidden>
          ${icon('action-close')}
        </button>
      </div>
      <input type="hidden" name="${name}" data-org-result>
    </div>`;
  }

  function drawerExamples() {
    return Object.entries({ normal:'标准宽度 · 480px', wide:'加宽 · 720px' }).map(([width,label]) =>
      '<div class="drawer-size-row"><strong>' + label + '</strong><div class="dev-actions">' +
      '<button class="button" type="button" data-demo-drawer="detail" data-width="' + width + '">查看详情</button>' +
      '<button class="button primary" type="button" data-demo-drawer="form" data-width="' + width + '">编辑项目</button></div></div>'
    ).join('');
  }

  function componentsPage() {
    return '<div class="stack">' +
      panel('列表组件与行操作', '<div id="developer-component-list"></div>') +
      '<div class="spec-grid">' +
      panel('对话框', '<div class="spec-actions">' +
        '<button class="button info" type="button" data-dialog-example="view">信息查看</button>' +
        '<button class="button primary" type="button" data-dialog-example="edit">表单编辑</button>' +
        '<button class="button warning" type="button" data-dialog-example="disable">操作确认</button>' +
        '<button class="button danger" type="button" data-dialog-example="delete">危险确认</button></div>') +
      panel('右侧抽屉', drawerExamples()) +
      panel('加载状态', '<div class="loading-examples"><button class="button" type="button" data-loading-example="page">页面加载</button>' +
        '<button class="button primary" type="button" data-loading-example="button">按钮加载</button>' +
        '<button class="button" type="button" data-loading-example="stop">结束演示</button></div>' +
        OmniRelayLoading.markup({size:'compact',label:'区域加载示例…'})) +
      panel('搜索下拉选择', combo('combo-single','单选','categories') + '<div class="field-stacked">' + combo('combo-multiple','多选','categories',true) + '</div>') +
      panel('人员选择框', combo('people-select','人员','people') + '<div class="field-stacked">' + combo('people-multiple','人员多选','people',true) + '</div>') +
      panel('富文本填写框', richEditor('rich-demo','编辑内容',true)) +
      panel('Markdown 显示框', markdownView()) +
      panel('文本填写框', field('short-input','单行文本','<input id="short-input" type="text" placeholder="输入标题">') +
        field('long-input','多行文本','<textarea id="long-input" rows="3" placeholder="输入补充说明"></textarea>','field-stacked')) +
      panel('多行文本展示框', '<div class="multiline" tabindex="0">' + escape(note) + '</div>') +
      panel('组织结构树', '<div data-demo-tree></div>') +
      panel('组织选择框', organizationPicker('demo-organization','demo-organization')) +
      panel('人员单个展示', '<div class="person-examples">' + people.map(personView).join('') + '</div>') +
      panel('日期选择', field('date-demo','选择日期','<input id="date-demo" type="date" value="2026-11-18">')) +
      panel('日期按天滚动', dateStrip('day')) +
      panel('日期按周滚动', dateStrip('week')) +
      panel('状态显示', '<div class="spec-actions">' + Object.keys(statuses).map(badge).join('') + '</div>') +
      panel('浮动提示', '<div class="spec-actions">' + Object.entries({info:'信息',success:'成功',warning:'警告',error:'错误'}).map(([level,label]) =>
        '<button class="button ' + (level === 'error' ? 'danger' : level) + '" type="button" data-tip="' + level + '">' + label + '</button>').join('') + '</div>') +
      '</div></div>';
  }

  function layoutPage() {
    return '<div class="stack">' + panel('标准布局', `
      <div class="layout-sketch" aria-label="工作台布局示意">
        <div class="layout-rail"><strong>侧栏</strong><p>166px / 52px</p><p>一级 / 二级菜单</p></div>
        <div class="layout-top"><strong>顶栏 · 46px</strong><span class="layout-toast">浮动提示</span><span>位置 / 全局操作</span></div>
        <div class="layout-content"><span>页面标题与工具栏 · 1–2 行</span><span class="layout-scroll">内容区 · 填满剩余高度，内部滚动</span><span>列表页分页区 · 总数 / 每页条数 / 翻页</span></div>
      </div>
      <ul class="attention layout-legend">
        <li><span class="kind">侧栏</span><div>拖动右边缘或点收放按钮，在 166 / 52px 之间切换。二级菜单随父级展开，底部是用户入口。窄屏使用分组导航下拉。</div></li>
        <li><span class="kind">列表页</span><div>标题下是一块独立工具栏。搜索常驻，次要筛选和选择模式收进弹层。表格区滚动，表头停在区内顶部，分页贴内容区底部。</div></li>
        <li><span class="kind">其他页</span><div>表单和信息展示由主内容区上下滚动。按业务分段组织内容。</div></li>
        <li><span class="kind">提示</span><div>提示从工作区顶部居中浮出，按信息、成功、警告、错误区分，默认 6 秒后关闭。</div></li>
      </ul>`) + '</div>';
  }

  function colorsPage() {
    const palette = OmniRelayTheme.href('omni-relay-color-system.html');
    return '<div class="toolbar"><a class="button" data-theme-link href="' + palette + '">单独打开交互色板</a>' +
      '<a class="button primary" href="omni-relay-colors.css" download>下载 CSS 色彩变量</a></div>' +
      '<iframe class="color-frame" title="OmniRelay 交互色板" src="' + palette + '" data-theme-frame allow="clipboard-write"></iframe>';
  }

  function render(view) {
    const content = {
      list:() => '<section id="developer-standard-list"></section>', form:formPage, info:infoPage,
      components:componentsPage, layout:layoutPage, colors:colorsPage
    };
    return '<header class="head"><h1 id="page-heading" tabindex="-1">' + pages[view] + '</h1>' +
      (view === 'layout' ? '<a class="button" href="omni-relay-ui-standards.md">查看完整组件与页面清单</a>' : '') +
      '</header>' + content[view]();
  }

  function mountCombo(root, { choices, initial } = {}) {
    const input = root.querySelector('[role="combobox"]');
    const output = root.querySelector('[data-combo-result]');
    const box = root.querySelector('.combo-box');
    const list = root.querySelector('[role="listbox"]');
    const empty = root.querySelector('.combo-empty');
    const values = root.querySelector('.combo-values');
    const multiple = root.dataset.multiple === 'true';
    const source = choices || (root.dataset.source === 'people' ? people : categories);
    const selected = new Set(initial ? [].concat(initial) : []);
    let active = -1;
    let matches = [];

    function highlight() {
      const options = [...list.children];
      options.forEach((option,index) => { option.dataset.active = String(index === active); });
      if (active < 0) input.removeAttribute('aria-activedescendant');
      else {
        input.setAttribute('aria-activedescendant',options[active].id);
        options[active].scrollIntoView({block:'nearest',inline:'nearest'});
      }
    }
    function show(query) {
      const needle = query.trim().toLocaleLowerCase();
      matches = source.filter(item => [item.label,item.detail,item.search].join(' ').toLocaleLowerCase().includes(needle));
      list.innerHTML = matches.map((item,index) => '<div class="combo-option" role="option" id="' + input.id + '-option-' + index +
        '" data-option="' + escape(item.id) + '" aria-selected="' + selected.has(item.id) + '"><span>' + escape(item.label) +
        (item.detail ? '<small>' + escape(item.detail) + '</small>' : '') + '</span>' + (selected.has(item.id) ? '<span aria-hidden="true">✓</span>' : '') + '</div>'
      ).join('');
      empty.hidden = Boolean(matches.length);
      box.hidden = false;
      input.setAttribute('aria-expanded','true');
      active = -1;
      highlight();
    }
    function close() {
      box.hidden = true;
      input.setAttribute('aria-expanded','false');
      input.removeAttribute('aria-activedescendant');
    }
    function sync() {
      output.value = [...selected].join(',');
      output.dispatchEvent(new Event('input',{bubbles:true}));
      values.innerHTML = source.filter(item => selected.has(item.id)).map(item =>
        '<button class="combo-value" type="button" data-remove="' + escape(item.id) + '" aria-label="移除 ' + escape(item.label) + '">' + escape(item.label) + ' ×</button>'
      ).join('');
    }
    function pick(id) {
      if (selected.has(id) && multiple) selected.delete(id);
      else {
        if (!multiple) selected.clear();
        selected.add(id);
      }
      input.value = '';
      sync();
      if (multiple) show(''); else close();
    }
    input.addEventListener('focus',() => show(input.value));
    input.addEventListener('click',() => { if(box.hidden) show(input.value); });
    input.addEventListener('input',() => show(input.value));
    input.addEventListener('keydown',event => {
      if (event.key === 'Escape' && !box.hidden) {
        if (choices) event.preventDefault();
        close();
        return;
      }
      if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
        event.preventDefault();
        if (box.hidden) show(input.value);
        if (!matches.length) return;
        if (active < 0) active = event.key === 'ArrowDown' ? 0 : matches.length-1;
        else active = (active + (event.key === 'ArrowDown' ? 1 : -1) + matches.length) % matches.length;
        highlight();
      }
      if (event.key === 'Enter' && !box.hidden && active >= 0) {
        event.preventDefault();
        pick(matches[active].id);
      }
    });
    list.addEventListener('pointerdown',event => { if(event.target.closest('[data-option]')) event.preventDefault(); });
    list.addEventListener('click',event => {
      const option = event.target.closest('[data-option]');
      if (option) pick(option.dataset.option);
    });
    values.addEventListener('click',event => {
      const button = event.target.closest('[data-remove]');
      if (!button) return;
      selected.delete(button.dataset.remove);
      input.focus();
      sync();
      if (!box.hidden) show(input.value);
    });
    root.addEventListener('focusout',event => {
      if (!root.contains(event.relatedTarget)) close();
    });
    root.closest('form')?.addEventListener('reset',() => {
      selected.clear();
      sync();
      close();
    });
    if (initial) sync();
  }

  function mountStrip(root) {
    const weekMode = root.dataset.dateMode === 'week';
    const primary = root.querySelector('[data-date-primary]');
    const secondary = root.querySelector('[data-date-secondary]');
    const DAY = 86_400_000;
    const monday = date => date - (new Date(date).getUTCDay() + 6) % 7 * DAY;
    const initial = Date.UTC(2026,10,18);
    let date = weekMode ? monday(initial) : initial;
    const fmt = (value, options) => new Intl.DateTimeFormat('zh-CN',{timeZone:'UTC',...options}).format(value);

    function paint() {
      if (!weekMode) {
        primary.textContent = fmt(date,{year:'numeric',month:'long',day:'numeric'});
        secondary.textContent = fmt(date,{weekday:'long'});
        return;
      }
      const sunday = date + 6*DAY;
      const year = new Date(date + 3*DAY).getUTCFullYear();
      const week = Math.round((date - monday(Date.UTC(year,0,4)))/(7*DAY)) + 1;
      const crossesYear = new Date(date).getUTCFullYear() !== new Date(sunday).getUTCFullYear();
      const options = {month:'long',day:'numeric',...(crossesYear ? {year:'numeric'} : {})};
      primary.textContent = year + ' 年第 ' + week + ' 周';
      secondary.textContent = fmt(date,options) + '（周一）– ' + fmt(sunday,options) + '（周日）';
    }
    root.addEventListener('click',event => {
      const movement = event.target.closest('[data-date-step]');
      if (movement) {
        date += Number(movement.dataset.dateStep) * (weekMode ? 7 : 1) * DAY;
        paint();
      }
    });
    paint();
  }

  function config(key, rows) {
    return {
      key, label:'示例项目', itemLabel:'项目', rows, rowLabel:row => row.name,
      search:row => row.name + ' ' + row.owner,
      filters:[{key:'status',label:'项目状态',options:statuses}],
      columns:[
        {key:'name',label:'项目名称',render:row => escape(row.name),sort:row => row.name},
        {key:'status',label:'状态',render:row => badge(row.status),sort:row => statuses[row.status]},
        {key:'owner',label:'负责人',render:row => row.owner,sort:row => row.owner,secondary:true},
        {key:'actions',label:'操作',render:row => '<div class="dev-actions">' +
          Object.entries({view:'查看',edit:'编辑',disable:row.status === 'disabled' ? '启用' : '禁用',delete:'删除'}).map(([action,label]) =>
            '<button class="row-action" type="button" data-action="' + action + '" data-item="' + row.id +
            '" aria-label="' + label + ' ' + escape(row.name) + '">' + label + '</button>'
          ).join('') + '</div>'}
      ]
    };
  }

  function mountDemoList(root, key, rows) {
    const list = mountList(root,config(key,rows));
    if (key === 'component-demo') root.classList.add('demo-table');

    function openProject(action, row, trigger) {
      const disabled = row.status === 'disabled';
      const record = content => '<div class="dialog-record"><strong>' + escape(row.name) + '</strong>' + content + '</div>';
      const note = text => '<p class="dialog-note">' + text + '</p>';
      const statusFacts = '<dl class="facts"><div><dt>负责人</dt><dd>' + row.owner +
        '</dd></div><div><dt>当前状态</dt><dd>' + badge(row.status) + '</dd></div></dl>';
      const operation = disabled ? '启用项目' : '禁用项目';
      const dialogs = {
        view: { title:'项目详情', tone:'info', body:record(statusFacts) },
        edit: {
          title:'编辑项目', variant:'form',
          body:field('demo-edit-title','项目名称','<input id="demo-edit-title" name="name" maxlength="60" required autofocus value="' + escape(row.name) + '">'),
          confirm:'保存修改',
          onSubmit(form) {
            const field = form.elements.name;
            const name = field.value.trim();
            if (!name) return { field, message:'请输入项目名称。' };
            row.name = name;
          }
        },
        disable: {
          title:operation, variant:'confirm', tone:disabled ? 'default' : 'warning',
          body:record('') + note(disabled ? '启用后，此项目将恢复到禁用前的状态。' : '项目将标为「已禁用」。之后可以在列表中重新启用。'),
          confirm:operation,
          onSubmit() {
            if (disabled) row.status = row.previousStatus ?? 'pending';
            else { row.previousStatus = row.status; row.status = 'disabled'; }
          }
        },
        delete: {
          title:'删除项目', variant:'confirm', tone:'danger',
          body:record('') + note('此项目将从示例列表移除，本次演示中无法撤销。'),
          confirm:'删除项目',
          onSubmit() { rows.splice(rows.indexOf(row),1); }
        }
      };
      openDialog({
        ...dialogs[action],
        onClose(confirmed) {
          if (!confirmed) return;
          list.refresh();
          toast('success',action === 'delete' ? '示例项目已删除。' : '示例项目已更新。');
        },
        restoreFocus() {
          if (trigger.isConnected) return trigger;
          return root.querySelector('[data-item="' + row.id + '"][data-action="' + action + '"]') ?? root.querySelector('.list-scroll');
        }
      });
    }

    root.addEventListener('click',event => {
      const button = event.target.closest('[data-action]');
      if (!button) return;
      const row = rows.find(item => item.id === button.dataset.item);
      if (row) openProject(button.dataset.action, row, button);
    });
    if (key === 'component-demo') {
      for (const button of root.closest('main').querySelectorAll('[data-dialog-example]')) {
        button.addEventListener('click',() => {
          const row = rows.find(item => item.status !== 'disabled') ?? rows[0];
          if (row) openProject(button.dataset.dialogExample,row,button);
          else toast('info','暂无示例项目，刷新可恢复。');
        });
      }
    }
  }

  function mountOrganizationPicker(root) {
    const trigger = root.querySelector('[data-org-open]');
    const clear = root.querySelector('[data-org-clear]');
    const value = root.querySelector('[data-org-value]');
    const input = root.querySelector('[data-org-result]');
    function commit(selected) {
      input.value = selected?.id ?? '';
      value.textContent = selected?.path ?? '选择组织';
      clear.hidden = !selected;
      trigger.toggleAttribute('data-has-value',Boolean(selected));
    }

    trigger.addEventListener('click',() => {
      let picker;
      const dialog = openDialog({
        title:'选择组织', variant:'form', body:'<div data-picker-tree></div>', confirm:'确认选择',
        onClose(confirmed) { if (confirmed) commit(picker.value()); },
        restoreFocus:() => trigger
      });
      const confirm = dialog.querySelector('[type="submit"]');
      picker = mountTree(dialog.querySelector('[data-picker-tree]'),{
        initialId:input.value,
        onChange() { confirm.disabled = false; }
      });
      confirm.disabled = !picker.value();
      picker.focus();
    });
    clear.addEventListener('click',() => { commit(); trigger.focus(); });
    root.closest('form')?.addEventListener('reset',() => commit());
  }

  function openProjectDrawer(trigger) {
    const { demoDrawer:variant, width } = trigger.dataset;
    const original = { ...drawerProject };
    const restoreFocus = () => trigger;
    const section = (name, body) => '<section class="drawer-section"><h3>' + name + '</h3>' + body + '</section>';
    if (variant === 'detail') {
      const record = '<dl class="facts"><div><dt>负责人</dt><dd>' + original.owner + '</dd></div>' +
        '<div><dt>当前状态</dt><dd>' + badge(original.status) + '</dd></div></dl>';
      openDialog({
        title:'项目详情', description:original.name, variant, tone:'info', drawer:width, restoreFocus,
        body:section('项目概要','<div class="dialog-record">' + record + '</div>') +
          section('项目说明','<div class="multiline" tabindex="0">' + escape(original.note) + '</div>')
      });
      return;
    }
    const options = Object.entries(statuses).map(([key,label]) =>
      '<option value="' + key + '"' + (original.status === key ? ' selected' : '') + '>' + label + '</option>'
    ).join('');
    const fields = field('drawer-name','项目名称','<input id="drawer-name" name="name" required autofocus maxlength="60" value="' + escape(original.name) + '">') +
      field('drawer-status','项目状态','<select class="select" id="drawer-status" name="status">' + options + '</select>') +
      field('drawer-note','项目说明','<textarea id="drawer-note" name="note" rows="5" maxlength="600">' + escape(original.note) + '</textarea>','full-field');
    const data = form => new FormData(form);
    openDialog({
      title:'编辑项目', description:original.name, variant, drawer:width, confirm:'保存修改', restoreFocus,
      body:section('基本信息','<div class="dev-fields">' + fields + '</div>'),
      isDirty(form) {
        const values = data(form);
        return ['name','status','note'].some(key => values.get(key) !== original[key]);
      },
      onSubmit(form) {
        const values = data(form);
        const name = values.get('name').trim();
        if (!name) return { field:form.elements.name, message:'请输入项目名称。' };
        drawerProject = { ...original, name, status:values.get('status'), note:values.get('note') };
      },
      onClose(confirmed) { if (confirmed) toast('success','示例项目已保存。'); }
    });
  }

  function mount(page, view) {
    const peopleDemo = page.querySelector('[data-person-name]')?.closest('.stack');
    if (peopleDemo) mountPersonCards(peopleDemo);
    if (view === 'list') mountDemoList(page.querySelector('#developer-standard-list'),'dev-list',records);
    if (view === 'components') mountDemoList(page.querySelector('#developer-component-list'),'component-demo',componentRecords);
    page.querySelectorAll('[data-combo]').forEach(mountCombo);
    page.querySelectorAll('[data-editor]').forEach(mountRich);
    page.querySelectorAll('[data-markdown]').forEach(root => mountMarkdown(root));
    page.querySelectorAll('[data-date-mode]').forEach(mountStrip);
    page.querySelectorAll('[data-demo-tree]').forEach(root => mountTree(root,{ selectBranches:false }));
    page.querySelectorAll('[data-org-picker]').forEach(mountOrganizationPicker);
    page.querySelectorAll('[data-demo-drawer]').forEach(trigger => {
      trigger.addEventListener('click',() => openProjectDrawer(trigger));
    });
    page.querySelectorAll('[data-loading-example]').forEach(trigger => {
      trigger.addEventListener('click',() => {
        const action = trigger.dataset.loadingExample;
        if (action === 'page') {
          const dialog = openDialog({title:'页面加载预览',cancelLabel:'结束演示',
            body:OmniRelayLoading.markup({size:'page',label:'正在打开工作台…'})});
          dialog.classList.add('or-loading-preview');
        } else {
          const button = page.querySelector('[data-loading-example="button"]');
          button.disabled = action === 'button';
          OmniRelayLoading.button(button,action === 'button',action === 'button' ? '处理中…' : '按钮加载');
        }
      });
    });
    const form = page.querySelector('#developer-form');
    if (!form) return;
    const error = form.querySelector('#sample-form-error');
    function clearError() {
      const invalid = form.querySelector('[aria-invalid="true"]');
      invalid?.removeAttribute('aria-invalid');
      invalid?.removeAttribute('aria-describedby');
      error.hidden = true;
    }
    form.addEventListener('submit',event => {
      event.preventDefault();
      clearError();
      const data = new FormData(form);
      const name = String(data.get('title')).trim();
      const owner = data.get('sample-owner');
      const invalid = !name ? form.querySelector('#sample-title') : !owner ? form.querySelector('#sample-owner') :
        !form.elements.date.validity.valid ? form.elements.date : null;
      if (!invalid) { toast('success','示例表单已提交。'); return; }
      error.textContent = !name ? '请输入项目名称。' : !owner ? '请选择负责人。' : '请输入有效的计划日期。';
      error.hidden = false;
      invalid.setAttribute('aria-invalid','true');
      invalid.setAttribute('aria-describedby',error.id);
      invalid.focus();
    });
    form.addEventListener('input',event => {
      const invalid = form.querySelector('[aria-invalid="true"]');
      if (invalid && (event.target === invalid || event.target.name === invalid.id)) clearError();
    });
    form.addEventListener('reset',clearError);
  }

  return { pages, render, mount, mountTree, combo, mountCombo };
})();
