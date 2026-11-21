/* Read-only Feishu file browser for the workbench prototype. */
window.OmniRelayDrive = (() => {
  const { escape, icon, mountList, openDialog, toast } = OmniRelayUI;
  const categories = {
    all: '全部', minutes: '会议纪要', docs: '飞书文档', tasks: '任务表格（多维表格）', images:'图片', videos:'视频', files: '文件'
  };
  const beichen = 'beichen';
  const qiming = 'qiming';
  const records = [
    { id:'m1', category:'minutes', format:'飞书妙记', name:'北辰交付 · 联调验收评审', group:'beichen', source:'北辰交付项目群', author:'王宁',
      projects:[beichen], bytes:18432, blocks:[['会议结论','确认接口验收范围和联调顺序。环境恢复后先验证权限和核心接口，再跑完整验收用例。'],
      ['待办','陈悦确认环境恢复时间，王宁组织端到端回归。']] },
    { id:'d1', category:'docs', format:'飞书文档', name:'北辰交付验收清单', group:'beichen', source:'北辰交付项目群', author:'王宁',
      projects:[beichen], bytes:32768, blocks:[['验收范围','权限验证、关键接口联调、灰度回归和回滚演练。'],
      ['签收方式','负责人逐项记录验收结果，待全部完成后汇总签收。']] },
    { id:'t1', category:'tasks', format:'多维表格', name:'北辰交付任务跟踪', group:'qa', source:'北辰联调协作群', author:'陈悦',
      projects:[beichen], bytes:49152, table:[['任务','负责人','状态'],['接口联调验收','王宁','进行中'],['测试环境恢复','陈悦','已阻塞'],
      ['验收用例确认','赵可','进行中']] },
    { id:'f1', category:'files', format:'PDF', name:'验收签字单.pdf', group:'beichen', source:'北辰交付项目群', author:'王宁',
      projects:[beichen], bytes:2411724, blocks:[['验收签字单','项目名称　北辰交付项目'],['验收结论','待联调通过后填写。'],['签署栏','交付负责人、验收负责人。']] },
    { id:'f2', category:'files', format:'Word', name:'联调操作手册.docx', group:'qa', source:'北辰联调协作群', author:'陈悦',
      projects:[beichen], bytes:442368, blocks:[['联调操作手册','先核对测试环境及依赖服务，再确认验收账号权限。'],
      ['回归顺序','登录与权限 → 核心接口 → 异常回滚。']] },
    { id:'f3', category:'files', format:'Excel', name:'验收用例.xlsx', group:'qa', source:'北辰联调协作群', author:'赵可',
      projects:[beichen,qiming], bytes:135168, table:[['用例编号','检查项','结果'],['TC-001','验收账号登录','通过'],['TC-002','核心接口回归','待执行']] },
    { id:'f4', category:'files', format:'PowerPoint', name:'启明试点介绍.pptx', group:'qiming', source:'启明试点项目群', author:'林晓',
      projects:[qiming], bytes:3565158, blocks:[['第 1 页 · 试点目标','验证首批用户在项目查询与任务跟进场景中的使用流程。'],
      ['第 2 页 · 试点安排','确认参与对象、明确反馈口径、每周复盘一次。']] },
    { id:'f5', category:'files', format:'ZIP', name:'交付材料.zip', group:'beichen', source:'北辰交付项目群', author:'王宁',
      projects:[beichen], bytes:6501171, archive:[
        ['README.txt','交付材料含签字单和验收用例。请以项目负责人确认的版本为准。'],
        ['签字/验收签字单.pdf','项目名称　北辰交付项目。验收结论待联调通过后填写。'],
        ['用例/验收用例.xlsx','TC-001 验收账号登录，通过。TC-002 核心接口回归，待执行。']] },
    { id:'d2', category:'docs', format:'飞书文档', name:'启明试点范围说明', group:'qiming', source:'启明试点项目群', author:'林晓',
      projects:[qiming], bytes:22528, blocks:[['首批范围','交付一组参与试点，先验证项目查询、任务跟进和反馈收集。']] }
  ];

  const storageKey = 'omni-relay.file-projects';
  let activeCategory = 'all';

  function messagesForGroup(group) {
    return records.map((row,index) => ({row,index})).filter(({row}) => row.group === group).map(({row,index}) => ({
      id:row.id, at:'2026-11-18 15:' + String(10 + index * 3).padStart(2,'0'), author:row.author,
      text:row.name, fileFormat:row.format, fileSize:formatSize(row.bytes)
    }));
  }

  const fileTypes = {
    PDF:['pdf','PDF'], Word:['word','W'], Excel:['excel','X'], PowerPoint:['slides','P'], ZIP:['zip',null,'file-zip'],
    飞书文档:['docs',null,'nav-logs'], 多维表格:['base',null,'file-grid'], 飞书妙记:['minutes',null,'file-minutes']
  };
  function fileIcon(format) {
    const [type,monogram,symbol] = fileTypes[format] || ['generic',null,'nav-logs'];
    return '<span class="relay-file-icon" data-file-type="' + type + '" aria-hidden="true">' +
      (monogram ? '<span>' + monogram + '</span>' : icon(symbol)) + '</span>';
  }

  function formatSize(bytes) {
    const unit = bytes >= 1048576 ? 1048576 : 1024;
    return new Intl.NumberFormat('zh-CN',{maximumFractionDigits:1}).format(bytes / unit) + (unit === 1024 ? ' KB' : ' MB');
  }

  function render() {
    return `<header class="head"><h1 id="page-heading" tabindex="-1">文件网盘</h1></header>
      <div class="drive-categories" role="group" aria-label="文件分类">
        ${Object.entries(categories).map(([key,label]) => `<button class="button" type="button" data-drive-category="${key}"
          aria-pressed="${key === activeCategory}" aria-controls="drive-file-list">${label}</button>`).join('')}
      </div><section id="drive-file-list"></section>`;
  }

  function previewContent(row) {
    if (row.table) {
      const [columns, ...values] = row.table;
      return `<div class="table-box drive-preview-table" tabindex="0" role="region" aria-label="表格预览">
        <table><thead><tr>${columns.map(value => '<th scope="col">' + escape(value) + '</th>').join('')}</tr></thead>
        <tbody>${values.map(line => '<tr>' + line.map(value => '<td>' + escape(value) + '</td>').join('') + '</tr>').join('')}</tbody></table></div>`;
    }
    if (row.archive) {
      return '<ul class="drive-archive">' + row.archive.map(([path,text]) => '<li><details><summary>' + escape(path) +
        '</summary><p>' + escape(text) + '</p></details></li>').join('') + '</ul>';
    }
    return '<div class="drive-preview-content">' + row.blocks.map(([label,text]) => '<section><h3>' + escape(label) +
      '</h3><p>' + escape(text) + '</p></section>').join('') + '</div>';
  }

  function mount(page, projects) {
    const list = page.querySelector('#drive-file-list');
    const categoryBar = page.querySelector('.drive-categories');
    const choices = Object.entries(projects).map(([id,project]) => ({id,label:project.name}));
    const overrides = new Map();
    try {
      const saved = JSON.parse(localStorage.getItem(storageKey) || '{}');
      for (const row of records) {
        if (Array.isArray(saved[row.id])) {
          overrides.set(row.id,[...new Set(saved[row.id].filter(id => typeof id === 'string' && Object.hasOwn(projects,id)))]);
        }
      }
    } catch {}

    const projectIds = row => overrides.get(row.id) ?? row.projects;
    const projectNames = row => projectIds(row).map(id => projects[id].name);
    const sourceLink = row => '<a class="drive-source" href="#/feishu/bot-chats/' + row.group + '/' + row.id + '">' +
      '<span class="drive-source-group">' + escape(row.source) + '</span>' +
      '<span class="drive-source-detail">' + escape(row.author) + '</span>' +
      '<span class="drive-source-action">定位原消息</span></a>';
    const projectChips = row => {
      const names = projectNames(row);
      return names.length ? names.map(name => '<span class="kind">' + escape(name) + '</span>').join('') : '添加所属项目';
    };
    const projectsCell = row => '<button class="row-action drive-projects" type="button" data-drive-projects="' + row.id +
      '" aria-label="修改 ' + escape(row.name) + ' 的所属项目">' + projectChips(row) + '</button>';
    let listView;
    function show(category) {
      const rows = category === 'all' ? records : records.filter(row => row.category === category);
      const table = document.createElement('div');
      list.replaceChildren(table);
      listView = mountList(table,{
        key:'feishu-drive-' + category, label:'飞书文件', itemLabel:'文件', rows, rowLabel:row => row.name, selection:false,
        search:row => [row.name,row.source,row.author,...projectNames(row),row.format,categories[row.category]].join(' '),
        filters:[{key:'projects',label:'所属项目',options:Object.fromEntries(choices.map(({id,label}) => [id,label])),
          matches:(row,id) => projectIds(row).includes(id)}],
        columns:[
          {key:'name',label:'名称',sort:row => row.name,render:row => '<button type="button" class="drive-file-name" data-drive-preview="' + escape(row.id) + '">' +
            fileIcon(row.format) + '<span><strong>' + escape(row.name) + '</strong><small>' + escape(row.format) + '</small></span></button>'},
          {key:'category',label:'分类',sort:row => categories[row.category],render:row => escape(categories[row.category])},
          {key:'source',label:'来源群',sort:row => row.source,render:sourceLink},
          {key:'project',label:'所属项目',sort:row => projectNames(row).join('、'),render:projectsCell},
          {key:'size',label:'大小',sort:row => row.bytes,render:row => '<span class="drive-file-size">' + formatSize(row.bytes) + '</span>'},
          {key:'preview',label:'操作',render:row => '<button type="button" class="row-action" data-drive-preview="' +
            escape(row.id) + '" aria-label="预览 ' + escape(row.name) + '">预览</button>'}
        ]
      });
      activeCategory = category;
      for (const button of categoryBar.querySelectorAll('[data-drive-category]')) {
        button.setAttribute('aria-pressed',String(button.dataset.driveCategory === category));
      }
    }

    function editProjects(row, trigger, updated) {
      const { combo, mountCombo } = OmniRelayDeveloper;
      const current = projectIds(row);
      const readProjects = form => form.querySelector('[data-combo-result]').value.split(',').filter(Boolean);
      const changed = next => next.length !== current.length || next.some(id => !current.includes(id));
      const dialog = openDialog({
        title:'修改所属项目', description:row.name, variant:'form', confirm:'保存',
        body:combo('drive-project-picker','所属项目','projects',true),
        isDirty:form => changed(readProjects(form)),
        onSubmit(form) {
          const next = readProjects(form);
          if (!changed(next)) return;
          overrides.set(row.id,next);
          try {
            localStorage.setItem(storageKey,JSON.stringify(Object.fromEntries(overrides)));
            toast('success','所属项目已保存');
          } catch { toast('info','所属项目已更新，仅本次浏览有效'); }
        },
        onClose(confirmed) {
          if (!confirmed) return;
          listView.refresh();
          updated?.();
        },
        restoreFocus:() => trigger.isConnected ? trigger : list.querySelector('[data-drive-projects="' + row.id + '"]') ||
          list.querySelector('[data-search]')
      });
      const picker = dialog.querySelector('[data-combo]');
      mountCombo(picker,{choices,initial:current});
      picker.querySelector('[role="combobox"]').focus();
    }

    categoryBar.addEventListener('click',event => {
      const button = event.target.closest('[data-drive-category]');
      if (button && button.getAttribute('aria-pressed') !== 'true') show(button.dataset.driveCategory);
    });
    list.addEventListener('click',event => {
      const edit = event.target.closest('[data-drive-projects]');
      if (edit) {
        editProjects(records.find(row => row.id === edit.dataset.driveProjects),edit);
        return;
      }
      const trigger = event.target.closest('[data-drive-preview]');
      if (!trigger) return;
      const row = records.find(item => item.id === trigger.dataset.drivePreview);
      const facts = [['分类',escape(categories[row.category])],['来源',sourceLink(row)],['所属项目',projectsCell(row)],
        ['大小',formatSize(row.bytes)]];
      const dialog = openDialog({title:row.name,description:row.format + ' · 示例预览',drawer:'wide',
        restoreFocus:() => trigger.isConnected ? trigger : list.querySelector('.drive-file-name[data-drive-preview="' + row.id + '"]'),
        body:'<dl class="facts drive-preview-facts">' + facts.map(([label,value]) => '<div><dt>' + label +
          '</dt><dd>' + value + '</dd></div>').join('') + '</dl>' + previewContent(row)});
      dialog.addEventListener('click',e => {
        if (e.target.closest('.drive-source') && !e.metaKey && !e.ctrlKey && !e.altKey && !e.shiftKey) dialog.close();
        const button = e.target.closest('[data-drive-projects]');
        if (button) editProjects(row,button,() => {
          const replacement = dialog.querySelector('[data-drive-projects]');
          replacement.innerHTML = projectChips(row);
        });
      });
    });
    show(activeCategory);
  }

  return { render, mount, messagesForGroup, fileIcon };
})();
