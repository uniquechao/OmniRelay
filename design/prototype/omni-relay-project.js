/* Project workspace interactions and sample data shared by the two local prototypes. */
window.OmniRelayProject = (() => {
  const { escape: esc, icon, personAvatar, openDialog, toast } = OmniRelayUI;
  const stages = { pending:'待开始', progress:'进行中', danger:'受阻', success:'已完成' };
  const taskStages = {...stages,cancelled:'已取消'};
  const people = {
    王宁: ['交付中心 / 交付一组', 'wang ning wn', '验收计划、跨团队协调'],
    陈悦: ['研发中心 / 平台组', 'chen yue cy', '环境恢复、回滚演练'],
    林晓: ['研发中心 / 产品组', 'lin xiao lx', '方案复核、试点范围'],
    赵可: ['交付中心 / 测试组', 'zhao ke zk', '验收用例、反馈收集']
  };
  const priorities = { urgent:'紧急', important:'重要', normal:'普通', low:'低优先级' };
  const priorityTone = { urgent:'danger', important:'important', normal:'pending', low:'pending' };
  const milestoneCatalog = {
    beichen:{access:'权限方案评审', environment:'测试环境就绪', acceptance:'接口联调验收'},
    qiming:{scope:'确认试点范围', feedback:'汇总首批反馈'}
  };
  const initialTaskAttributes = {
    beichen:[
      {priority:'important', milestone:'acceptance', start:'2026-11-17', tag:'短期'},
      {priority:'urgent', milestone:'environment', start:'2026-11-17', tag:'短期', blocker:'等待数据库实例恢复'},
      {priority:'normal', milestone:'acceptance', start:'2026-11-18'},
      {priority:'normal', milestone:'acceptance', start:'2026-11-18'},
      {priority:'normal', milestone:'access', start:'2026-11-16'},
      {priority:'low', milestone:'environment', start:'2026-11-17'}
    ],
    qiming:[
      {priority:'important', milestone:'scope', start:'2026-11-18'},
      {priority:'normal', milestone:'feedback', start:'2026-11-20'}
    ]
  };
  const taskAttributes = new Map();
  const milestoneFilters = new Map();
  const taskDetails = (project,id,number) => taskAttributes.get(id + '/' + number) || initialTaskAttributes[id]?.[number-1] ||
    {priority:project.tasks[number-1][4] ? 'important' : 'normal',milestone:''};
  const ownerAvatar = name => personAvatar(name,people[name]?.[1].split(' ').slice(0,2).join('-'));
  const selectOptions = (options,current) => Object.entries(options).map(([key,label]) =>
    `<option value="${esc(key)}"${key === current ? ' selected' : ''}>${esc(label)}</option>`).join('');

  function deadline(due,status,compact = false) {
    const iso = OmniRelayTaskEditor.dateValue(due);
    if (!iso) return '<span class="pr-deadline">无截止日期</span>';
    const month = String(Number(iso.slice(5,7)));
    const day = String(Number(iso.slice(8,10)));
    const daysLeft = (Date.parse(iso) - Date.parse('2026-11-18')) / 86400000;
    const alert = status !== 'success' && daysLeft <= 3;
    const label = daysLeft < 0 ? '已逾期' : daysLeft === 0 ? '今日到期' : '临期';
    const display = compact ? month + '/' + day : iso.slice(0,4) + ' 年 ' + month + ' 月 ' + day + ' 日';
    return `<time class="pr-deadline" datetime="${iso}" title="${alert ? label + ' · ' : ''}${iso}"${alert ? ' data-deadline-alert' : ''}>
      ${alert ? '<span aria-hidden="true">' + icon('status-warning') + '</span><span class="sr-only">' + label + ' · </span>' : ''}
      ${esc(display)}</time>`;
  }

  const date = '2026 年 11 月 18 日';
  const meetings = [
    { title:'北辰交付 · 联调验收评审', meta:'11 月 18 日 15:30 · 飞书妙记', status:'已同步',
      text:'会议确认接口验收范围和回归顺序。环境恢复后先验证权限与核心接口，再跑完整验收用例。',
      next:'环境恢复后复核访问凭据，由陈悦确认。' },
    { title:'权限方案评审', meta:'11 月 18 日 10:00 · 飞书妙记', status:'已同步',
      text:'验收用户保留必要读写权限，回归结束后回收临时授权。', task:5 }
  ];
  const documents = [
    { title:'北辰交付验收清单', meta:'飞书文档 · 王宁 · 11 月 18 日更新', status:'交付资料',
      text:'验收项包括权限验证、关键接口联调、灰度回归和回滚演练。验收负责人在联调完成后逐项签入结果。', task:1 },
    { title:'回滚与恢复检查表', meta:'飞书表格 · 陈悦 · 11 月 17 日更新', status:'技术方案',
      text:'按恢复前备份、回滚后冒烟验证、验收环境重新开放三步核对。', task:6 },
    { title:'联调用例与缺陷登记', meta:'飞书多维表格 · 赵可 · 11 月 18 日更新', status:'测试资料',
      text:'记录用例覆盖范围、执行结果及待验证缺陷。验收前更新环境相关用例。', task:3 }
  ];
  const reports = [
    { title:'王宁 · 交付日报', meta:'17:40 · 本人提交 · 飞书', status:'已提交',
      text:'权限评审已通过，确认接口验收范围和负责人。待环境恢复后启动端到端联调。', task:1 },
    { title:'陈悦 · 平台日报', meta:'17:25 · 本人提交 · 飞书', status:'已提交',
      text:'完成回滚预案演练。测试环境恢复需等待数据库实例就绪，已登记阻塞。', task:2 }
  ];
  const preferences = new Map();

  const button = (label, action, value = '', style = '') =>
    `<button type="button" class="button ${style}" data-project-action="${action}" data-value="${esc(value)}">${esc(label)}</button>`;
  const link = (label, path, style = '') =>
    `<a class="button ${style}" href="${path}">${esc(label)}</a>`;
  const search = label => `<input class="search" type="search" aria-label="${label}" placeholder="${label}" data-project-search>`;
  const bodyText = value => `<p class="pr-copy">${esc(value)}</p>`;
  const record = (title, meta, action, value, badge = '') =>
    `<li><button type="button" class="pr-record" data-project-action="${action}" data-value="${esc(value)}">
      <span><strong>${esc(title)}</strong><small>${esc(meta)}</small></span>
      <span class="pr-record-end">${badge}<span aria-hidden="true">›</span></span></button></li>`;
  const list = rows => `<ul class="pr-records">${rows}</ul>`;
  const matching = (query, ...values) =>
    values.join(' ').toLowerCase().includes(query.trim().toLowerCase());

  function render(project, id, tab, subview, options) {
    const { chip, empty, panel, facts, overview, cardLink, botSettings, projects } = options;
    const editor = OmniRelayTaskEditor;
    const taskNumber = number => editor.taskNumber(project,number);
    function applyTask(number,state) {
      const task = project.tasks[number-1];
      task[0] = state.title;
      task[1] = state.owner || '未分配';
      task[2] = state.due;
      task[3] = state.stage;
      taskAttributes.set(id + '/' + number,{...state,tag:state.tags[0]});
    }
    project.tasks.forEach((task,index) => {
      applyTask(index+1,editor.read(project,id,index+1,taskDetails(project,id,index+1)));
    });
    const projectPath = '#/projects/' + id;
    const isBeichen = id === 'beichen';
    const saved = preferences.get(id) || {};
    const group = saved.group ?? (project.group === '待绑定' ? '' : project.group);
    const groupChoices = isBeichen ? [project.group,'北辰联调协作群'] : id === 'qiming' ? ['启明试点项目群'] : [];
    const owners = [...new Set([project.lead, ...project.tasks.map(task => task[1])])];
    const taskLink = (number, label = '查看关联任务') => link(label, projectPath + '/tasks/' + number);
    const briefPath = projectPath + '/reports/brief';
    const destination = group || '待绑定';
    const mode = subview === 'brief' ? 'brief' : 'daily';
    const records = { meetings:isBeichen ? meetings : [], docs:isBeichen ? documents : [], reports:isBeichen ? reports : [] };
    const peopleList = query => list(owners.filter(name => matching(query, name, ...(people[name] || [])))
      .map(name => record(name, people[name]?.[0] || '项目成员', 'person', name,
        chip(name === project.lead ? 'important' : 'pending', name === project.lead ? '项目负责人' : '项目成员'))).join(''));
    const recordList = (type, query) => {
      const results = records[type].map((item, index) => ({item,index})).filter(({item}) => matching(query,item.title,item.meta,item.status));
      return results.length ? list(results.map(({item,index}) =>
        record(item.title, item.meta, type, index, chip(type === 'docs' ? 'pending' : 'success',item.status))).join(''))
        : empty(query ? '没有符合条件的记录。' : '暂无记录。');
    };
    const taskState = subview === 'list';
    const milestones = milestoneCatalog[id] || {};
    const attributes = number => taskDetails(project,id,number);
    const milestoneName = number => milestones[attributes(number).milestone];
    const milestoneMatch = number => {
      const current = milestoneFilters.get(id);
      return !current || (current === 'none' ? !milestoneName(number) : attributes(number).milestone === current);
    };
    const milestoneFilter = `<select class="select" aria-label="筛选里程碑" data-project-milestone>${
      selectOptions({'':'全部里程碑',...milestones,none:'未关联里程碑'},milestoneFilters.get(id) || '')}</select>`;
    function taskResults(query, owner) {
      const items = project.tasks.map((task,index) => ({task,number:index+1}))
        .filter(({task,number}) => matching(query,task[0],task[1],taskNumber(number),milestoneName(number)) &&
          (!owner || task[1] === owner) && milestoneMatch(number));
      return `<div class="pr-board" role="region" aria-label="任务看板" tabindex="0">${Object.entries(stages).map(([key,label]) => {
        const rows = items.filter(({task}) => task[3] === key);
        return `<section class="pr-lane" data-stage="${key}"><h3>${chip(key,label)} <span>${rows.length}</span></h3>
          <ul>${rows.map(({task:[title,name,due,status],number}) => {
            const {priority,tags = [],blocker} = attributes(number);
            const milestone = milestoneName(number);
            return `<li draggable="true" data-draggable-task="${number}">
              <button type="button" class="pr-task" aria-haspopup="dialog" data-priority="${priority}" data-project-action="task" data-value="${number}">
                <span class="pr-task-top"><span class="pr-task-code">${taskNumber(number)}</span><span class="pr-task-person" title="${esc(name)}">
                  ${ownerAvatar(name)}<span class="sr-only">负责人 ${esc(name)}</span></span></span>
                <span class="pr-task-tags"><span class="pr-task-priority" data-tone="${priorityTone[priority]}">${priorities[priority]}</span>${
                  tags.slice(0,2).map(tag => '<span>' + esc(tag) + '</span>').join('')}${tags.length > 2 ? '<span>+' + (tags.length-2) + '</span>' : ''}</span>
                <strong class="pr-task-title" title="${esc(title)}">${esc(title)}</strong>
                ${status === 'danger' && blocker ? '<span class="pr-task-blocker" title="' + esc(blocker) + '">' + esc(blocker) + '</span>' : ''}
                ${milestone ? '<span class="pr-task-milestone" title="里程碑 · ' + esc(milestone) + '">' +
                  icon('status-milestone') + '<span>' + esc(milestone) + '</span></span>' : ''}
                ${deadline(due,status,true)}</button></li>`;
          }).join('')}</ul>
          ${rows.length ? '' : '<p class="pr-lane-empty">暂无任务</p>'}</section>`;
      }).join('')}</div>`;
    }
    const toolbar = (label, end = '') => `<div class="pr-toolbar">${search(label)}${end}</div>`;
    const results = content => `<div data-project-results aria-live="polite">${content}</div>`;
    const segment = (links, label) => `<nav class="pr-segment" aria-label="${label}">${links.map(([text,path,active]) =>
      `<a href="${path}"${active ? ' aria-current="page"' : ''}>${text}</a>`).join('')}</nav>`;
    const views = {
      overview: () => overview(project, id, button('项目设置','settings')),
      members: () => panel('项目成员', toolbar('搜索姓名或拼音') + results(peopleList('')), chip('pending', owners.length + ' 位成员')),
      chats: () => OmniRelayBotChat.render(id),
      meetings: () => panel('会议纪要', toolbar('搜索纪要或来源') + results(recordList('meetings',''))),
      docs: () => panel('项目文档', toolbar('搜索文档、类型或负责人') + results(recordList('docs',''))),
      reports: () => segment([['成员日报',projectPath + '/reports',mode === 'daily'],['项目简报',briefPath,mode === 'brief']],'日报视图') +
        (mode === 'brief' ? panel('项目简报', project.brief ? list(record(project.brief, project.group + ' · 18:00', 'brief', '', chip('success','示例已送达'))) : empty('暂无项目简报。'),
          project.brief ? button('预览简报','brief','','primary') : '') :
          panel('成员日报', `<p class="aux pr-period">${date}</p>` + toolbar('搜索成员或提交来源') + results(recordList('reports','')))),
      tasks: () => {
        const toggle = segment([['看板',projectPath + '/tasks',!taskState],['列表',projectPath + '/tasks/list',taskState]],'任务视图');
        return taskState ? '<div class="pr-toolbar">' + milestoneFilter + toggle + '</div><section data-project-task-list></section>' :
          toolbar('搜索任务或负责人', `<select class="select" aria-label="筛选负责人" data-project-owner><option value="">全部负责人</option>
          ${owners.map(name => `<option>${esc(name)}</option>`).join('')}</select>` + milestoneFilter + toggle) + results(taskResults('',''));
      },
      feishu: () => panel('项目接入', facts([
        ['目标群',esc(destination)], ['接入状态',group ? chip('success','示例已接入') : chip('pending','待绑定')],
        ['可用能力',group ? '项目简报、任务查询与分派' : '待项目群绑定'],
        ['每日简报',group && saved.delivery !== false ? '每日 18:00' : '已暂停']
      ]) + `<div class="pr-actions">${button('接入验证','check','', 'primary')}${botSettings ? link('系统 Bot 配置','#/system/bot') : ''}</div>`,
      button('配置接入','settings')) +
      panel('最近回执', project.brief ? list(
        record('项目简报发布','11 月 18 日 18:00 · ' + project.group,'brief','',chip('success','示例已送达')) +
        record('项目任务查询','11 月 18 日 17:36 · ' + project.group,'receipt','',chip('success','示例已完成'))
      ) : empty('暂无运行回执。'), cardLink('飞书卡片预览'))
    };

    function mount(root, refresh) {
      if (tab === 'chats') {
        OmniRelayBotChat.mount(root,projects,id,subview);
        return;
      }
      const filtered = {
        members: value => {
          const markup = peopleList(value);
          return markup === list('') ? empty('没有符合条件的成员。') : markup;
        },
        meetings: value => recordList('meetings',value),
        docs: value => recordList('docs',value),
        reports: value => recordList('reports',value),
        tasks: taskResults
      };
      const taskList = root.querySelector('[data-project-task-list]');
      const taskRows = project.tasks.map(([title,owner,due,status],index) => ({id:String(index+1),title,owner,due,status}));
      const standardList = taskList && OmniRelayUI.mountList(taskList,{
        key:'project-tasks-' + id, label:'项目任务', itemLabel:'任务', selection:false, rows:taskRows, rowLabel:row => row.title,
        search:row => [row.title,row.owner,taskNumber(row.id),milestoneName(row.id)].join(' '),
        includes:row => milestoneMatch(row.id),
        filters:[{key:'status',label:'任务状态',options:taskStages},{key:'owner',label:'负责人',options:Object.fromEntries(owners.map(name => [name,name]))}],
        columns:[
          {key:'title',label:'任务',sort:row => row.title,render:row => button(row.title,'task',row.id,'pr-task-link')},
          {key:'owner',label:'负责人',sort:row => row.owner,render:row => esc(row.owner)},
          {key:'milestone',label:'里程碑',sort:row => milestoneName(row.id) || '',render:row => esc(milestoneName(row.id) || '—')},
          {key:'due',label:'截止日期',sort:row => row.due,render:row => deadline(row.due,row.status)},
          {key:'status',label:'状态',sort:row => taskStages[row.status],render:row => chip(row.status,taskStages[row.status])}
        ]
      });
      const update = () => {
        if (standardList) {
          taskRows.forEach((row,index) => {
            [row.title,row.owner,row.due,row.status] = project.tasks[index];
          });
          standardList.refresh();
        }
        const region = root.querySelector('[data-project-results]');
        if (region && filtered[tab]) region.innerHTML = filtered[tab](
          root.querySelector('[data-project-search]')?.value || '',
          root.querySelector('[data-project-owner]')?.value || ''
        );
      };
      function detail(title, description, content, width = 'wide') {
        const dialog = openDialog({title,description,body:content,drawer:width});
        dialog.addEventListener('click',event => {
          const anchor = event.target.closest('a[href^="#/"]');
          if (anchor && !event.metaKey && !event.ctrlKey && !event.altKey && !event.shiftKey) dialog.close();
        });
      }
      function saveTask(number,{stage}) {
        const before = editor.read(project,id,number,attributes(number));
        if (stage === before.stage) return;
        const next = {...before,stage,version:before.version+1,
          activities:[{kind:'edit',at:new Date().toLocaleString('sv-SE',{timeZone:'Asia/Singapore'}),
            changes:[{name:'stage',before:before.stage,after:stage}]} ,...before.activities]};
        const stored = editor.persist(id,number,next);
        applyTask(number,next);
        update();
        toast(stored ? 'success' : 'info',stored ? '示例任务已更新' : '示例任务已更新，仅本次浏览有效');
      }
      function openTask(number, dropStage) {
        if (!project.tasks[number-1]) return;
        editor.open({
          project,id,number,dropStage,owners,milestones,stages:taskStages,priorities,
          snapshot:editor.read(project,id,number,attributes(number)),
          onSave(next) {
            applyTask(number,next);
            update();
          },
          restoreFocus:() => root.querySelector(`[data-project-action="task"][data-value="${number}"]`) || root
        });
      }
      const actions = {
        person(value) {
          detail(value,project.name,facts([['所属组织',esc(people[value]?.[0] || '项目组')],
            ['项目角色',value === project.lead ? '项目负责人' : '项目成员'],
            ['项目分工',esc(people[value]?.[2] || '项目协作')]]),'standard');
        },
        brief() {
          detail('项目简报',project.brief || date,
            `<div class="pr-detail"><h3>进展速览</h3>${bodyText(project.latest)}
            <h3>关键进展</h3>${bodyText('权限方案已评审，回滚预案已演练。验收主路径等待测试环境恢复。')}
            <h3>跟进事项</h3>${bodyText('陈悦确认环境恢复时间，王宁组织接口联调验收。')}</div>
            ${facts([['送达群',esc(project.group)],['发送时间','11 月 18 日 18:00'],['发布结果',chip('success','示例已送达')]])}`);
        },
        receipt() {
          detail('任务查询回执','11 月 18 日 17:36', facts([['运行结果',chip('success','示例已完成')],
            ['请求来源',esc(project.group)],['操作','查询未完成任务']]) +
            `<div class="pr-actions">${link('查看任务看板',projectPath + '/tasks', 'primary')}</div>`,'standard');
        },
        task: value => openTask(Number(value)),
        check() {
          detail('项目接入验证',project.name, facts([['Bot 配置',chip('success','示例可用')],
            ['目标群',group ? chip('success',group) : chip('warning','待绑定')],
            ['项目范围',group ? chip('success','示例已授权') : chip('pending','待验证')]]),'standard');
        },
        settings() {
          openDialog({
            title: tab === 'overview' ? '项目设置' : '配置项目 Bot', description:project.name, drawer:'standard',
            variant:'form', confirm:'保存设置',
            body: facts([['项目负责人',esc(project.lead)],['可见范围','项目授权成员']]) +
              `<label class="field pr-form"><span>Bot 目标群</span><select class="select" name="group">
              <option value="">未绑定</option>
              ${[...new Set([group, ...groupChoices])].filter(Boolean).map(name =>
                `<option value="${esc(name)}"${name === group ? ' selected' : ''}>${esc(name)}</option>`).join('')}
              </select></label><label class="pr-check"><input name="delivery" type="checkbox"${saved.delivery !== false ? ' checked' : ''}> 每日 18:00 生成项目简报</label>`,
            isDirty: form => form.elements.group.value !== group || form.elements.delivery.checked !== (saved.delivery !== false),
            onSubmit(form) {
              preferences.set(id,{group:form.elements.group.value, delivery:form.elements.delivery.checked});
              toast('success','示例项目设置已保存');
            },
            onClose(confirmed) { if (confirmed) refresh(); },
            restoreFocus: () => document.querySelector('#project-panel [data-project-action="settings"]')
          });
        }
      };
      root.addEventListener('input', event => { if (event.target.matches('[data-project-search]')) update(); });
      root.addEventListener('change', event => {
        if (event.target.matches('[data-project-owner]')) update();
        if (event.target.matches('[data-project-milestone]')) {
          milestoneFilters.set(id,event.target.value);
          if (standardList) standardList.resetPage();
          else update();
        }
      });
      root.addEventListener('click', event => {
        const target = event.target.closest('[data-project-action]');
        if (!target) return;
        const {projectAction:action,value} = target.dataset;
        if (Object.hasOwn(actions,action)) {
          actions[action](value);
        } else {
          const item = records[action]?.[Number(value)];
          if (!item) return;
          detail(item.title,item.meta,bodyText(item.text) +
            (item.next ? `<section class="pr-detail"><h3>待办候选</h3>${bodyText(item.next)}</section>` : '') +
            (item.task ? `<div class="pr-actions">${taskLink(item.task)}</div>` : ''));
        }
      });
      const board = root.querySelector('.pr-board');
      if (board) {
        let dragged;
        function clearDrop() {
          root.querySelector('[data-drop-active]')?.removeAttribute('data-drop-active');
        }
        root.addEventListener('dragstart',event => {
          dragged = Number(event.target.closest('[data-draggable-task]')?.dataset.draggableTask);
          if (!dragged) return;
          event.dataTransfer.setData('text/plain',String(dragged));
          event.dataTransfer.effectAllowed = 'move';
        });
        root.addEventListener('dragover',event => {
          const lane = event.target.closest('[data-stage]');
          if (!lane || !dragged) return;
          event.preventDefault();
          event.dataTransfer.dropEffect = 'move';
          clearDrop();
          lane.dataset.dropActive = '';
        });
        root.addEventListener('dragleave',event => {
          if (!event.relatedTarget?.closest?.('[data-stage]')) clearDrop();
        });
        root.addEventListener('dragend',() => { clearDrop(); dragged = undefined; });
        root.addEventListener('drop',event => {
          const stage = event.target.closest('[data-stage]')?.dataset.stage;
          if (!stage || !dragged) return;
          event.preventDefault();
          clearDrop();
          const number = dragged;
          dragged = undefined;
          if (stage === 'danger' && project.tasks[number - 1][3] !== stage) openTask(number,stage);
          else saveTask(number,{stage});
        });
      }
      if (tab === 'tasks' && /^\d+$/.test(subview || '') && project.tasks[Number(subview) - 1]) {
        openTask(Number(subview));
      }
    }
    return {html:views[tab](), mount};
  }

  return { render, deadline, taskDetails, priorities, priorityTone };
})();
