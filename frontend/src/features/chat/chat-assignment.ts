import * as workbenchUI from '../../ui/widgets';
import {z} from '../../api/validation.ts'
import {errorStatus} from '../../api/session-client.ts';
import {assignmentSchema,type ChatRequest,type Conversation,type Assignment} from './chat-contracts'
import {requiredElement} from '../../ui/dom'
import type {Choice} from '../../ui/components/runtime'
const projectChoices=z.object({items:z.array(z.object({id:z.string(),name:z.string()})),total:z.number().int().nonnegative()})
const overview=z.object({data:z.object({workspace:z.object({project:z.object({name:z.string()})})})})
const groups=z.object({items:z.array(assignmentSchema.extend({id:z.string(),name:z.string()})),primary_version:z.number().int()})
export function assignmentAPI(request:ChatRequest) {
  const projects = '/api/v1/projects';
  const path = (id:string) => projects + '/' + encodeURIComponent(id);
  return {
    async choices(signal:AbortSignal) {
      const choices = [];
      for (let page = 1; ; page++) {
        const result = projectChoices.parse(await request(projects + '?page_size=100&page=' + page, { signal }));
        choices.push(...result.items.map(item => ({ id: item.id, label: item.name })));
        if (!result.items.length || choices.length >= result.total) return choices;
      }
    },
    async projectName(id:string, signal:AbortSignal) {
      const result = overview.parse(await request(path(id) + '/overview', { signal }));
      return result.data.workspace.project.name;
    },
    groups: (project:string, signal:AbortSignal) => request(path(project) + '/chats', { signal }).then(raw=>groups.parse(raw)),
    assign: (id:string, project_id:string, version:number, signal:AbortSignal) => request('/api/v1/feishu/chats/' + encodeURIComponent(id) + '/project',
      { method: 'PUT', body: { project_id, version }, signal }).then(raw=>assignmentSchema.parse(raw)),
    primary: (project:string, chat_id:string, assignment_version:number, primary_version:number, signal:AbortSignal) => request(path(project) + '/chats/primary',
      { method: 'PUT', body: { chat_id, assignment_version, primary_version }, signal })
  };
}

export async function editAssignment({ api, primary, projectId, record, trigger, signal, dialogs, changed, reload }:{api:ReturnType<typeof assignmentAPI>;primary:boolean;projectId?:string|null;record:Conversation;trigger:HTMLElement;signal:AbortSignal;dialogs:Set<HTMLDialogElement>;changed:(id:string,assignment:Partial<Assignment>&{project_id:string})=>void;reload:()=>void}) {
  const { combo, mountCombo, escape, openDialog, toast } = workbenchUI;
  const assignment = record.project_assignment;
  if (!assignment || (primary && !projectId)) return;
  const current = assignment.project_id;
  let snapshot:z.infer<typeof groups>|undefined;
  let choices:Choice[]=[];
  const label = primary ? '确认设置' : '保存';
  try {
    if (primary && projectId) snapshot = await api.groups(projectId, signal);
    else choices = await api.choices(signal);
  } catch {
    if (!signal.aborted) toast('error', '暂时无法读取项目信息，请重试。');
    return;
  }
  if (signal.aborted) return;
  const candidate = snapshot?.items.find(item => item.id === record.id);
  if (primary && (!candidate || candidate.primary)) {
    reload();
    return;
  }
  const before = snapshot?.items.find(item => item.primary);
  let submitting = false;
  let conflict = false;
  const dialog = openDialog({
    title: primary ? '设为项目主群' : '修改项目归属', description: record.name,
    variant: primary ? 'confirm' : 'form', confirm: label,
    body: primary ? '<p class="dialog-note">' + (before ? '主群将从「' + escape(before.name) + '」更换为当前群。' :
      '将当前群设为项目主群。') + '</p>' : combo('chat-project-picker', '所属项目', 'projects'),
    isDirty: form => !primary && !submitting && requiredElement(form,'[data-combo-result]',HTMLInputElement).value !== current,
    async onSubmit(form) {
      if (submitting) return { keepOpen: true };
      if (conflict) {
        reload();
        return;
      }
      const next = primary ? projectId ?? '' : requiredElement(form,'[data-combo-result]',HTMLInputElement).value;
      if (!primary && next === current) return;
      const submit = requiredElement(form,'[type="submit"]',HTMLButtonElement);
      submitting = true;
      for (const control of form.querySelectorAll<HTMLInputElement|HTMLSelectElement|HTMLButtonElement>('input,select,button')) control.disabled = true;
      try {
        let result:Partial<Assignment>&{project_id:string};
        if(primary) {
          if(!projectId || !candidate || !snapshot) return;
          await api.primary(projectId,record.id,candidate.version,snapshot.primary_version,signal);
          result={project_id:projectId,primary:true};
        } else result=await api.assign(record.id,next,assignment.version,signal);
        if (!signal.aborted) toast('success', primary ? '项目主群已更新' : next ? '群聊项目归属已保存' : '群聊项目归属已清除');
        changed(record.id,result);
      } catch (error) {
        if (signal.aborted) return;
        conflict = errorStatus(error) === 409 || errorStatus(error) === 404;
        submit.textContent = conflict ? '重新读取' : '重试';
        return { field: submit, message: conflict ? '归属或权限已变化，请重新读取后确认。' : '保存失败，请重试。' };
      } finally {
        submitting = false;
        for (const control of form.querySelectorAll<HTMLInputElement|HTMLSelectElement|HTMLButtonElement>('input,select,button')) control.disabled = false;
      }
    },
    onClose() { dialogs.delete(dialog); },
    restoreFocus: () => trigger.isConnected && !trigger.closest('[hidden]') ? trigger : null
  });
  dialogs.add(dialog);
  dialog.addEventListener('cancel', event => {
    if (submitting) { event.preventDefault(); event.stopImmediatePropagation(); }
  }, true);
  if (!primary) {
    mountCombo(requiredElement(dialog,'[data-combo]',HTMLElement), { choices, initial: current ? [current] : [], signal });
    const input = requiredElement(dialog,'[role="combobox"]',HTMLInputElement);
    // 保留按下时的布局，使收起选项前的点击仍落在保存按钮上。
    requiredElement(dialog,'[type="submit"]',HTMLButtonElement).addEventListener('pointerdown', event => {
      if (input.getAttribute('aria-expanded') === 'true') event.preventDefault();
    });
    input.focus();
  }
}
