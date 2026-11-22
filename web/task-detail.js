import { memberSearch } from './task-pinyin.js';
import { mountTaskActivity } from './task-activity.js';

const { escape: esc, openDialog } = window.OmniRelayUI;
const statuses = {
  todo: '待开始',
  in_progress: '进行中',
  blocked: '受阻',
  done: '已完成',
  cancelled: '已取消',
};
const priorities = { P0: '紧急', P1: '重要', P2: '普通', P3: '低优先级' };
const labels = {
  title: '标题',
  description: '正文',
  acceptance: '验收条件',
  status: '状态',
  priority: '优先级',
  owner_member_id: '主负责人',
  participant_ids: '参与人',
  tag_ids: '标签',
  milestone_id: '里程碑',
  start_at: '开始日期',
  due_at: '截止日期',
  blocked_reason: '受阻原因',
  completion_note: '完成结果',
  change_reason: '变更原因',
};
const equal = (a, b) => JSON.stringify(a) === JSON.stringify(b);
function fields(t) {
  return Object.fromEntries(
    Object.keys(labels).map((k) => [
      k,
      k === 'change_reason'
        ? ''
        : (t[k] ??
          (['tag_ids', 'participant_ids'].includes(k)
            ? []
            : ['priority', 'owner_member_id', 'milestone_id', 'start_at', 'due_at'].includes(k)
              ? null
              : '')),
    ]),
  );
}
const options = (entries, value) =>
  entries
    .map(
      ([id, name]) =>
        '<option value="' +
        esc(id) +
        '"' +
        (id === (value ?? '') ? ' selected' : '') +
        '>' +
        esc(name) +
        '</option>',
    )
    .join('');

export async function mountTaskDetail(host, { task, project, route, back, request, signal, navigation, creating = false, capabilities = {} }) {
  const root = '/api/v1/projects/' + route.id,
    url = root + '/tasks/' + task.id;
  const get = (path) => request(path, { signal });
  const [permissions, directory, tagList, nodes] = await Promise.allSettled([
    creating ? Promise.resolve({edit_fields:capabilities.can_create,update_progress:capabilities.can_create,assign:capabilities.can_assign}) : get(url + '/actions'),
    get(root + '/members'),
    get(root + '/task-tags'),
    get(root + '/milestones'),
  ]);
  if (signal.aborted) return;
  let actions = permissions.value || {};
  const people = new Map(
    (directory.value?.data?.members || [])
      .filter((m) => m.member_id || m.id)
      .map((m) => [m.member_id || m.id, m.display_name || m.id]),
  );
  const tags = new Map((tagList.value?.items || []).map((t) => [t.id, t.name]));
  const milestones = new Map((nodes.value?.items || []).map((n) => [n.id, n.title]));
  const search = new Map(
    [...people].map(([id, name]) => [id, memberSearch(name + ' ' + id).concat(memberSearch(name))]),
  );
  let baseline = fields(task),
    draft = structuredClone(baseline),
    busy = false,
    prompt,
    pendingKey,
    pendingBody;
  let ownerPending = false,
    latest;
  host.innerHTML =
    '<article class="or-task-inspector task-detail"><header class="dialog-head"><div class="task-inspector-path"><span class="task-path-project"></span> / <span class="task-path-code"></span> / <strong class="task-path-title"></strong></div><button class="button primary" data-save>保存任务</button><a class="button" data-close aria-label="收起任务详情">收起</a></header><p role="status" data-notice></p><section data-conflict hidden></section><div class="task-edit-grid"><section class="task-edit-copy"></section><aside class="task-edit-sidebar"></aside><section class="task-edit-comment" aria-label="任务评论"></section><section class="task-edit-activity" aria-label="任务动态"></section></div><section class="task-support" data-support></section></article>';
  const el = host.firstElementChild,
    q = (s) => el.querySelector(s);
  q('.task-path-project').textContent = project;
  q('.task-path-code').textContent = creating ? '新建任务' : task.id;
  if (creating) q('[data-save]').textContent = '创建任务';
  q('[data-close]').href = back;
  q('[data-close]').innerHTML = window.OmniRelayUI.icon('action-close');
  q('[data-close]').classList.add('dialog-close');
  const notice = (msg) => {
    q('[data-notice]').textContent = msg;
  };
  const dirty = () => ownerPending || !equal(baseline, draft);
  const canSave = () => actions.update_progress && dirty() && !busy && !latest;
  const control = (k) => el.querySelector('[name="' + k + '"]');
  const enabled = (k) =>
    ['status', 'blocked_reason', 'completion_note'].includes(k)
      ? actions.update_progress
      : (['title', 'description'].includes(k) ? actions.edit_fields : (actions.assign ?? actions.edit_fields)) &&
        ({ owner_member_id: directory, participant_ids: directory, tag_ids: tagList, milestone_id: nodes }[k]
          ?.status ?? 'fulfilled') === 'fulfilled';
  function field(k, type = 'area', choices = []) {
    const attrs = ' name="' + k + '" aria-label="' + labels[k] + '"' + (enabled(k) ? '' : ' disabled');
    let input;
    if (type === 'select') input = '<select' + attrs + '>' + options(choices, draft[k]) + '</select>';
    else if (type === 'area')
      input =
        '<textarea' +
        attrs +
        ' maxlength="' +
        (k === 'description' ? 10000 : 4000) +
        '">' +
        esc(draft[k] || '') +
        '</textarea>';
    else
      input =
        '<input type="' +
        type +
        '"' +
        attrs +
        ' maxlength="200" value="' +
        esc(type === 'date' ? (draft[k] || '').slice(0, 10) : draft[k] || '') +
        '">';
    const cls =
      { title: 'task-edit-title', description: 'task-edit-description' }[k] ||
      (type === 'area' ? 'task-edit-label' : 'task-property');
    return (
      '<label class="' +
      cls +
      '" data-field="' +
      k +
      '"><span' +
      (['title', 'description'].includes(k) ? ' class="sr-only"' : '') +
      '>' +
      labels[k] +
      '</span>' +
      input +
      '</label>'
    );
  }
  function picker(k, choices, multiple) {
    const ids = multiple ? draft[k] : draft[k] ? [draft[k]] : [];
    const selected = ids
      .map((id) => {
        const name = choices.get(id) || id;
        const text = (k === 'owner_member_id' ? window.OmniRelayUI.personAvatar(name, id) : '') + esc(name);
        return enabled(k)
          ? '<button type="button" class="button" data-remove="' +
              esc(id) +
              '" aria-label="移除' +
              esc(name) +
              '">' +
              text +
              ' ×</button>'
          : '<span>' + text + '</span>';
      })
      .join('');
    const input =
      '<input type="search" name="' +
      k +
      '" aria-label="' +
      labels[k] +
      '" placeholder="' +
      (k === 'tag_ids' ? '搜索标签' : '搜索姓名或拼音') +
      '" autocomplete="off"' +
      (enabled(k) ? '' : ' disabled') +
      '>';
    const searchBox = input + '<div data-results hidden></div>';
    const contents =
      '<div data-selected>' +
      (selected || (enabled(k) ? '' : '未设置')) +
      '</div>' +
      (enabled(k)
        ? k === 'tag_ids'
          ? '<details><summary>添加标签</summary>' + searchBox + '</details>'
          : searchBox
        : '<div hidden>' + searchBox + '</div>');
    return (
      '<div class="' +
      (k === 'participant_ids' ? 'task-edit-label' : 'task-property') +
      '" data-picker="' +
      k +
      '"><span>' +
      labels[k] +
      '</span><div>' +
      contents +
      '</div></div>'
    );
  }
  function render() {
    q('.task-path-title').textContent = task.title;
    q('.task-edit-copy').innerHTML =
      field('title', 'text') +
      field('description') +
      field('blocked_reason') +
      field('completion_note') +
      field('change_reason') +
      '<details class="task-edit-extra"><summary>验收与参与人</summary>' +
      field('acceptance') +
      picker('participant_ids', people, true) +
      '</details>';
    const states = Object.entries(statuses).filter(
      ([s]) => (actions.assign ?? actions.edit_fields) || s !== 'cancelled' || s === task.status,
    );
    const ms = new Map(milestones);
    if (draft.milestone_id && !ms.has(draft.milestone_id)) ms.set(draft.milestone_id, draft.milestone_id);
    q('.task-edit-sidebar').innerHTML =
      '<section><h3>属性</h3>' +
      field('status', 'select', states) +
      field('priority', 'select', [['', '未设置'], ...Object.entries(priorities)]) +
      picker('owner_member_id', people, false) +
      field('start_at', 'date') +
      field('due_at', 'date') +
      field('milestone_id', 'select', [['', '未关联'], ...ms]) +
      picker('tag_ids', tags, true) +
      '</section><section class="task-edit-metadata"><h3>详情</h3><dl>' +
      [
        ['创建人', task.created_by_name || task.created_by],
        ['创建时间', task.created_at],
        ['更新时间', task.updated_at],
      ]
        .map(([k, v]) => '<dt>' + k + '</dt><dd>' + esc(v ?? '未知') + '</dd>')
        .join('') +
      '</dl><details><summary>版本与更新人</summary><p>版本 ' +
      esc(task.version ?? '未知') +
      '</p><p>更新人　' +
      esc(task.updated_by_name || task.updated_by || '未知') +
      '</p></details></section>';
    sync();
  }
  function sync() {
    const changed = draft.status !== baseline.status;
    for (const [k, show] of [
      ['blocked_reason', draft.status === 'blocked'],
      ['completion_note', draft.status === 'done' || baseline.completion_note],
      [
        'change_reason',
        changed && (draft.status === 'cancelled' || ['cancelled', 'done'].includes(baseline.status)),
      ],
    ])
      q('[data-field="' + k + '"]').hidden = !show;
    q('[data-save]').disabled = !canSave();
    if (q('[data-delete]')) {
      q('[data-delete]').hidden = !actions.delete;
      q('[data-delete]').disabled = busy || dirty() || !!latest;
    }
    control('status').dataset.stage =
      { in_progress: 'progress', blocked: 'danger', done: 'success' }[draft.status] || 'pending';
  }
  function results(box) {
    const k = box.dataset.picker,
      input = box.querySelector('input'),
      list = box.querySelector('[data-results]');
    const term = input.value.trim().toLowerCase().replace(/\s/g, '');
    const matches = [...(k === 'tag_ids' ? tags : people)].filter(
      ([id, name]) =>
        !(Array.isArray(draft[k]) && draft[k].includes(id)) &&
        (!term ||
          (k === 'tag_ids' ? [name, id] : search.get(id)).some((s) =>
            s.toLowerCase().replace(/\s/g, '').includes(term),
          )),
    );
    list.innerHTML =
      matches
        .map(
          ([id, name]) =>
            '<button class="button" type="button" data-pick="' +
            esc(id) +
            '">' +
            esc(name) +
            (k === 'tag_ids' ? '' : ' · ' + esc(id)) +
            '</button>',
        )
        .join('') || '无匹配结果';
    list.hidden = false;
  }
  el.addEventListener('focusin', (e) => {
    if (e.target.matches('[data-picker] input')) results(e.target.closest('[data-picker]'));
  });
  el.addEventListener('focusout', (e) => {
    const box = e.target.closest('[data-picker]');
    if (box && !box.contains(e.relatedTarget)) box.querySelector('[data-results]').hidden = true;
  });
  el.addEventListener('click', (e) => {
    const button = e.target.closest('[data-pick],[data-remove]');
    if (!button) return;
    const box = button.closest('[data-picker]'),
      k = box.dataset.picker,
      id = button.dataset.pick;
    if (k === 'owner_member_id') {
      draft[k] = id || null;
      ownerPending = false;
    } else draft[k] = id ? [...draft[k], id] : draft[k].filter((v) => v !== button.dataset.remove);
    if (
      Array.isArray(draft[k]) &&
      draft[k].length === baseline[k].length &&
      baseline[k].every((value) => draft[k].includes(value))
    )
      draft[k] = [...baseline[k]];
    box.outerHTML = picker(k, k === 'tag_ids' ? tags : people, k !== 'owner_member_id');
    const nextInput = control(k);
    if (nextInput.closest('details')) nextInput.closest('details').open = true;
    nextInput.focus();
    sync();
  });
  el.addEventListener('input', (e) => {
    const input = e.target,
      k = input.name;
    if (!Object.hasOwn(labels, k)) return;
    input.setCustomValidity('');
    if (input.closest('[data-picker]')) {
      if (k === 'owner_member_id') ownerPending = !!input.value.trim();
      results(input.closest('[data-picker]'));
    } else {
      draft[k] = input.value || (['start_at', 'due_at', 'milestone_id', 'priority'].includes(k) ? null : '');
      if (k === 'status') {
        if (draft.status !== 'blocked') draft.blocked_reason = '';
        if (draft.status !== 'done' && !baseline.completion_note) draft.completion_note = '';
        if (
          draft.status === baseline.status ||
          (draft.status !== 'cancelled' && !['done', 'cancelled'].includes(baseline.status))
        )
          draft.change_reason = '';
        for (const key of ['blocked_reason', 'completion_note', 'change_reason'])
          control(key).value = draft[key];
      }
    }
    sync();
  });
  function invalid(k, message) {
    const input = control(k);
    if (input.closest('details')) input.closest('details').open = true;
    input.setCustomValidity(message);
    input.focus();
    input.reportValidity();
    return false;
  }
  function display(k, v) {
    const map = k === 'tag_ids' ? tags : k === 'milestone_id' ? milestones : people;
    if (Array.isArray(v)) return v.map((id) => map.get(id) || id).join('、') || '无';
    if (k === 'owner_member_id' || k === 'milestone_id') return map.get(v) || v || '无';
    return (k === 'status' ? statuses[v] : k === 'priority' ? priorities[v] : v) || '无';
  }
  async function conflict() {
    const [remote, rights] = await Promise.all([get(url), get(url + '/actions')]);
    latest = remote;
    actions = rights;
    const remoteFields = fields(remote),
      panel = q('[data-conflict]');
    panel.hidden = false;
    panel.innerHTML =
      '<h3>任务已更新至版本 ' +
      esc(remote.version) +
      '</h3><p>核对差异后继续编辑。</p>' +
      Object.keys(labels)
        .filter((k) => !equal(remoteFields[k], draft[k]))
        .map(
          (k) =>
            '<section class="task-difference"><h4>' +
            labels[k] +
            '</h4><dl><dt>我的输入</dt><dd>' +
            esc(display(k, draft[k])) +
            '</dd><dt>最新版本</dt><dd>' +
            esc(display(k, remoteFields[k])) +
            '</dd></dl>' +
            '<label>采用<select data-compare="' +
            k +
            '"' +
            (!enabled(k) ? ' disabled' : '') +
            '>' +
            options(
              [
                ['local', '我的输入'],
                ['remote', '最新版本'],
              ],
              equal(draft[k], baseline[k]) || !enabled(k) ? 'remote' : 'local',
            ) +
            '</select></label></section>',
        )
        .join('') +
      '<button class="button" data-rebase>应用比对结果</button>';
    panel.querySelector('button').onclick = () => {
      for (const select of panel.querySelectorAll('select'))
        if (select.value === 'remote' || !enabled(select.dataset.compare))
          draft[select.dataset.compare] = remoteFields[select.dataset.compare];
      task = latest;
      latest = null;
      baseline = remoteFields;
      panel.hidden = true;
      const query = control('owner_member_id').value;
      render();
      control('owner_member_id').value = query;
      sync();
      notice(actions.update_progress ? '已载入最新版本。' : '当前任务只读。');
      void activity.refresh();
    };
    panel.querySelector('select,button').focus();
    sync();
  }
  async function save() {
    if (!canSave()) return false;
    if (!draft.title.trim()) return invalid('title', '请输入任务标题。');
    if (ownerPending) return invalid('owner_member_id', '请选择有效的项目成员。');
    if (draft.start_at && draft.due_at && draft.due_at.slice(0, 10) < draft.start_at.slice(0, 10))
      return invalid('due_at', '截止日期不能早于开始日期。');
    if (draft.status === 'blocked' && !draft.blocked_reason.trim())
      return invalid('blocked_reason', '请填写受阻原因。');
    if (!q('[data-field="change_reason"]').hidden && !draft.change_reason.trim())
      return invalid('change_reason', '请填写变更原因。');
    for (const k of ['participant_ids', 'tag_ids'])
      if (draft[k].length > 50) return invalid(k, '最多可选择 50 项。');
    const body = { ...draft, expected_task_version: task.version };
    const serialized = JSON.stringify(body);
    if (serialized !== pendingBody) {
      pendingBody = serialized;
      pendingKey = crypto.randomUUID();
    }
    busy = true;
    sync();
    q('.task-edit-grid').inert = true;
    try {
      task = await request(creating ? root + '/tasks' : url, { method: creating ? 'POST' : 'PUT', body: { ...body, request_key: pendingKey }, signal });
      baseline = fields(task);
      draft = structuredClone(baseline);
      pendingBody = null;
      if (creating) { location.hash = route.base + '/tasks/' + task.id; return true; }
      actions = await get(url + '/actions').catch(() => ({}));
      render();
      notice(actions.update_progress ? '任务已保存。' : '任务已保存，当前任务只读。');
      void activity.refresh();
      return true;
    } catch (error) {
      if (error.status === 409) {
        try {
          await conflict();
        } catch {
          notice('无法读取最新版本，请重试保存。');
        }
      } else if (error.status === 403) {
        const query = control('owner_member_id').value;
        actions = {};
        render();
        control('owner_member_id').value = query;
        notice('编辑权限已变更，已保留输入。');
      } else notice('任务保存失败，已保留输入。请重试。');
      return false;
    } finally {
      busy = false;
      q('.task-edit-grid').inert = false;
      sync();
    }
  }
  if (actions.delete && !creating) {
    q('[data-save]').insertAdjacentHTML('beforebegin','<button type="button" class="button" data-delete aria-haspopup="dialog" title="请先保存修改，再删除任务">删除任务</button>');
    q('[data-delete]').onclick = () => {
      const key=crypto.randomUUID();
      const dialog=openDialog({title:'删除任务？',description:task.title,variant:'form',confirm:'删除任务',tone:'warning',
        body:'<p>删除后任务将从看板和搜索中移除，操作历史仍会保留。</p><label class="field"><span>删除原因</span><textarea name="reason" required maxlength="4000"></textarea></label><p role="status" data-delete-error></p>',
        async onSubmit(form){
          if(!form.elements.reason.value.trim())return {field:form.elements.reason,message:'请填写删除原因。'};
          try{
            await request(url,{method:'DELETE',body:{expected_version:task.version,request_key:key,change_reason:form.elements.reason.value.trim()},signal});
            baseline=structuredClone(draft);location.hash=back;
          }catch(error){form.querySelector('[data-delete-error]').textContent=error.status===409?'任务已更新，请关闭后重新加载。':[403,404].includes(error.status)?'任务已移除，或当前账号无权删除。':'删除失败，请重试。';return {keepOpen:true};}
        },
        onClose(){signal.removeEventListener('abort',abort);},
      });
      const abort=()=>dialog.close();signal.addEventListener('abort',abort,{once:true});
    };
  }
  render();
  if (creating) {
    q('.task-edit-comment').hidden = true;
    q('.task-edit-activity').hidden = true;
    q('.task-edit-metadata').hidden = true;
    if (!actions.assign) control('status').disabled = true;
  }
  if (!actions.update_progress) notice('当前任务只读。');
  if ([permissions, directory, tagList, nodes].some((r) => r.status === 'rejected'))
    notice('部分编辑选项暂不可用。');
  const activity = creating ? {refresh(){},hasDraft:()=>false,isSending:()=>false,saveDrafts:async()=>true} : mountTaskActivity(el, {
    url,
    root,
    route,
    request,
    signal,
    statuses,
    getTask: () => task,
    getActions: () => actions,
    onConflict: conflict,
    onAccepted: async () => {
      if (dirty()) return conflict();
      task = await get(url);
      actions = await get(url + '/actions');
      baseline = fields(task);
      draft = structuredClone(baseline);
      render();
    },
  });
  async function saveDrafts() {
    if (dirty() && !(await save())) return false;
    return activity.saveDrafts();
  }
  q('[data-save]').onclick = saveDrafts;
  navigation.hasDraft = () => dirty() || activity.hasDraft();
  window.addEventListener(
    'beforeunload',
    (event) => {
      if (navigation.hasDraft()) {
        event.preventDefault();
        event.returnValue = '';
      }
    },
    { signal },
  );
  navigation.confirmNavigation = () => {
    if (busy || activity.isSending()) return false;
    if (!navigation.hasDraft()) return true;
    if (!prompt)
      prompt = new Promise((resolve) =>
        openDialog({
          title: '保存修改？',
          variant: 'confirm',
          tone: 'warning',
          body: '<p>有未保存的修改，直接关闭会丢失本次更改。</p>',
          cancelLabel: '继续编辑',
          discard: '不保存',
          confirm: '保存并关闭',
          onClose: async (ok, result) => {
            const leave = result === 'discard' || (ok && (await saveDrafts()));
            prompt = null;
            resolve(leave);
          },
        }),
      );
    return prompt;
  };
  document.addEventListener(
    'keydown',
    (e) => {
      if (e.key === 'Escape' && !document.querySelector('dialog[open]')) {
        e.preventDefault();
        location.hash = back;
      }
    },
    { signal },
  );
  document.addEventListener(
    'click',
    (e) => {
      if (!el.contains(e.target) && !e.target.closest('dialog,a,button,select,input,[role=button]'))
        location.hash = back;
    },
    { signal },
  );
}
