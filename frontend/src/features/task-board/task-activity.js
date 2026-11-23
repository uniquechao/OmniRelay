const { escape: esc, openDialog } = window.OmniRelayUI;
function valueText(v) {
  if (Array.isArray(v)) return v.map(valueText).join('、') || '无';
  if (v && typeof v === 'object') return v.name || v.label || Object.values(v).map(valueText).join(' · ');
  return v ?? '无';
}
function eventRow(event) {
  const keys = new Set([...Object.keys(event.before || {}), ...Object.keys(event.after || {})]);
  return (
    '<li data-kind="' +
    esc(event.kind) +
    '"><header><strong>' +
    esc(event.actor_name || event.actor_id) +
    '</strong> ' +
    (event.kind === 'comment' ? '发表了评论' : '更新了任务') +
    '<time>' +
    esc(event.created_at) +
    '</time></header>' +
    (event.body ? '<p>' + esc(event.body) + '</p>' : '') +
    (keys.size
      ? '<dl>' +
        [...keys]
          .map(
            (k) =>
              '<dt>' +
              esc(k) +
              '</dt><dd>' +
              esc(valueText(event.before?.[k])) +
              ' → ' +
              esc(valueText(event.after?.[k])) +
              '</dd>',
          )
          .join('') +
        '</dl>'
      : '') +
    '</li>'
  );
}

export function mountTaskActivity(
  el,
  { url, root, route, request, signal, statuses, getTask, getActions, onConflict, onAccepted },
) {
  const comment = el.querySelector('.task-edit-comment'),
    activity = el.querySelector('.task-edit-activity'),
    support = el.querySelector('[data-support]');
  let cursor = null,
    kind = 'all',
    sequence = 0,
    feedbackSequence = 0,
    loading = false,
    sending = false;
  const get = (path) => request(path, { signal });
  const command = (path, body) => request(path, { signal, method: 'POST', body });
  comment.innerHTML =
    '<label><span class="sr-only">发表评论</span><textarea rows="2" maxlength="4000" placeholder="留下评论…"></textarea></label><button class="button" aria-label="提交评论" disabled><svg class="icon" aria-hidden="true"><use href="#compose-send"/></svg></button><small role="status"></small>';
  const input = comment.querySelector('textarea'),
    send = comment.querySelector('button');
  function commentState() {
    input.disabled = !getActions().comment || sending;
    send.disabled = input.disabled || !input.value.trim();
  }
  input.oninput = commentState;
  const keys = new Map();
  function keyed(path, body) {
    const signature = JSON.stringify(body);
    if (keys.get(path)?.signature !== signature) keys.set(path, { signature, key: crypto.randomUUID() });
    return command(path, { ...body, request_key: keys.get(path).key });
  }
  async function postComment() {
    if (send.disabled) return;
    sending = true;
    commentState();
    try {
      await keyed(url + '/comments', { body: input.value.trim(), expected_version: getTask().version });
      keys.delete(url + '/comments');
      input.value = '';
      comment.querySelector('small').textContent = '评论已发送。';
      kind = kind === 'change' ? 'all' : kind;
      await events(false);
      return true;
    } catch (e) {
      comment.querySelector('small').textContent = '评论发送失败，已保留输入。';
      if (e.status === 409) await onConflict().catch(() => {});
    } finally {
      sending = false;
      commentState();
    }
  }
  activity.innerHTML =
    '<header><h3>动态</h3><div role="group" aria-label="动态类型">' +
    Object.entries({ all: '全部', change: '修改记录', comment: '评论' })
      .map(([k, v]) => '<button class="button" data-kind="' + k + '">' + v + '</button>')
      .join('') +
    '</div></header><ol></ol><p role="status"></p><button class="button" data-more hidden>加载更多</button>';
  send.onclick = postComment;
  const more = activity.querySelector('[data-more]'),
    list = activity.querySelector('ol'),
    status = activity.querySelector('p');
  async function events(append) {
    const turn = ++sequence;
    loading = true;
    more.disabled = true;
    if (!append) {
      cursor = null;
      list.replaceChildren();
    }
    status.textContent = '正在加载动态…';
    for (const b of activity.querySelectorAll('button[data-kind]'))
      b.setAttribute('aria-pressed', String(b.dataset.kind === kind));
    const params = new URLSearchParams({ kind, limit: '25' });
    if (append && cursor) params.set('cursor', cursor);
    try {
      const data = await get(url + '/activity?' + params);
      if (turn !== sequence || signal.aborted) return;
      list.insertAdjacentHTML('beforeend', (data.items || []).map(eventRow).join(''));
      cursor = data.next_cursor;
      more.textContent = '加载更多';
      more.hidden = !cursor;
      status.textContent = list.children.length ? '' : '暂无动态。';
    } catch {
      if (turn !== sequence) return;
      more.textContent = '重试';
      more.hidden = false;
      status.textContent = '动态加载失败。';
    } finally {
      if (turn === sequence) {
        loading = false;
        more.disabled = false;
      }
    }
  }
  activity.onclick = (e) => {
    const b = e.target.closest('button');
    if (!b) return;
    if (b.dataset.kind) {
      kind = b.dataset.kind;
      void events(false);
    } else if (b === more && !loading) void events(!!cursor);
  };
  support.innerHTML =
    '<section><h3>提炼依据</h3><div data-evidence></div></section><section><h3>进展反馈与审核</h3><div data-feedback></div></section>';
  const evidence = support.querySelector('[data-evidence]'),
    feedback = support.querySelector('[data-feedback]');
  function failed(target, retry) {
    target.innerHTML = '<p>暂时无法读取。</p><button class="button">重新加载</button>';
    target.querySelector('button').onclick = retry;
  }
  async function sources() {
    try {
      const data = await get(url + '/evidence');
      evidence.innerHTML =
        (data.items || [])
          .map((item) =>
            item.accessible
              ? (item.evidence || [])
                  .map((src) => {
                    const title =
                      src.title ||
                      { document: '原始文档', minutes: '会议纪要', message: '原始消息' }[src.kind] ||
                      '来源';
                    const dest =
                      src.kind === 'message' && src.chat_id
                        ? route.base +
                          '/chats/' +
                          encodeURIComponent(src.chat_id) +
                          '/' +
                          encodeURIComponent(src.id)
                        : null;
                    return (
                      '<details><summary>' +
                      esc(title) +
                      '</summary><p>' +
                      esc(src.text) +
                      '</p><time>' +
                      esc(src.created_at) +
                      '</time>' +
                      (dest ? ' <a href="' + esc(dest) + '">查看原始消息</a>' : '') +
                      '</details>'
                    );
                  })
                  .join('')
              : '<p>' + esc(item.reason || '当前账号无权读取原始依据。') + '</p>',
          )
          .join('') || '<p>暂无依据。</p>';
    } catch {
      failed(evidence, sources);
    }
  }
  let editor;
  signal.addEventListener('abort', () => editor?.dialog.close('cancel'), { once: true });
  function feedbackForm(item, action) {
    let saved = false,
      pending = false;
    const path = item
      ? root + '/task-update-suggestions/' + encodeURIComponent(item.id) + '/review'
      : url + '/update-suggestions';
    const version = getTask().version;
    const config = {
      title: item ? (action === 'accept' ? '采纳进展建议' : '拒绝进展建议') : '提交进展反馈',
      variant: 'form',
      confirm: '确认提交',
      body:
        (item
          ? '<p>' + esc(item.note) + '</p>'
          : '<label class="field">建议任务状态<select name="proposed_status">' +
            ['in_progress', 'blocked', 'done']
              .map((k) => '<option value="' + k + '">' + statuses[k] + '</option>')
              .join('') +
            '</select></label>') +
        '<label class="field">' +
        (item ? '审核说明' : '进展、完成结果或受阻原因') +
        '<textarea name="note" maxlength="' +
        (item ? 2000 : 4000) +
        '" required></textarea></label>',
      isDirty: (form) =>
        !saved &&
        (!!form.elements.note.value ||
          (form.elements.proposed_status?.value || 'in_progress') !== 'in_progress'),
      async onSubmit(form) {
        const note = form.elements.note.value.trim();
        if (!note) return { message: '请填写说明。', field: form.elements.note };
        if (pending) return { keepOpen: true };
        pending = true;
        form.querySelector('[type="submit"]').disabled = true;
        try {
          if (item) await command(path, { action, expected_version: item.version, reason: note });
          else
            await keyed(path, {
              task_version: version,
              proposed_status: form.elements.proposed_status.value,
              note,
            });
          keys.delete(path);
          saved = true;
        } catch (e) {
          return {
            message: e.status === 409 ? '版本已变化，请核对最新任务后重新提交。' : '提交失败，请重试。',
            field: form.elements.note,
          };
        } finally {
          pending = false;
          form.querySelector('[type="submit"]').disabled = false;
        }
      },
      async onClose() {
        editor = undefined;
        if (signal.aborted) return;
        if (!saved) return;
        try {
          if (action === 'accept') await onAccepted();
        } catch {
          if (!signal.aborted)
            window.OmniRelayUI.toast('warning', '审核已保存，请重新打开详情读取最新任务。');
        } finally {
          void refresh();
        }
      },
    };
    const dialog = openDialog(config),
      form = dialog.querySelector('form');
    editor = {
      dialog,
      dirty: () => config.isDirty(form),
      pending: () => pending,
      async save() {
        const failure = await config.onSubmit(form);
        if (failure) {
          if (failure.field) {
            failure.field.setCustomValidity(failure.message);
            failure.field.reportValidity();
            failure.field.addEventListener('input', () => failure.field.setCustomValidity(''), {
              once: true,
            });
          }
          return false;
        }
        dialog.close('confirm');
        return true;
      },
    };
  }
  async function suggestions() {
    const turn = ++feedbackSequence;
    try {
      const data = await get(url + '/update-suggestions');
      if (turn !== feedbackSequence) return;
      feedback.innerHTML =
        (data.can_submit ? '<button class="button" data-submit>提交进展反馈</button>' : '') +
        ((data.items || [])
          .map(
            (s, i) =>
              '<article><strong>' +
              esc(statuses[s.proposed_status] || s.proposed_status) +
              '</strong> · ' +
              esc({ pending: '待审核', accepted: '已采纳', rejected: '已拒绝' }[s.status] || s.status) +
              '<p>' +
              esc(s.note) +
              '</p><small>' +
              esc(s.actor_id) +
              ' · ' +
              esc(s.created_at) +
              '</small>' +
              (s.reason ? '<p>审核说明　' + esc(s.reason) + '</p>' : '') +
              (data.can_review && s.status === 'pending'
                ? '<div data-index="' +
                  i +
                  '"><button class="button" data-action="accept">采纳</button><button class="button" data-action="reject">拒绝</button></div>'
                : '') +
              '</article>',
          )
          .join('') || '<p>暂无当前账号可见的进展反馈。</p>');
      feedback.onclick = (e) => {
        const b = e.target.closest('button');
        if (!b) return;
        if (b.hasAttribute('data-submit')) feedbackForm();
        else feedbackForm(data.items[Number(b.parentElement.dataset.index)], b.dataset.action);
      };
    } catch {
      if (turn === feedbackSequence) {
        feedback.onclick = null;
        failed(feedback, suggestions);
      }
    }
  }
  function refresh() {
    commentState();
    return Promise.all([events(false), suggestions(), sources()]);
  }
  void refresh();
  return {
    refresh,
    hasDraft: () => !!input.value.trim() || !!editor?.dirty(),
    isSending: () => sending || !!editor?.pending(),
    async saveDrafts() {
      if (editor?.dirty() && !(await editor.save())) return false;
      return input.value.trim() ? !!(await postComment()) : true;
    },
  };
}
