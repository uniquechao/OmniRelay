import type { ProjectTask } from './api'
import { taskDate } from './task-dates'

export function taskEditValues(task: ProjectTask | null, timezone: string | null | undefined) {
  return {
    title: task?.title ?? '', description: task?.description ?? '',
    participant_ids: task?.participant_ids ?? [], priority: task?.priority ?? null,
    acceptance: task?.acceptance ?? '', start: taskDate(task?.start_at, timezone || 'UTC'),
    due: taskDate(task?.due_at, timezone || 'UTC'), blocked_reason: task?.blocked_reason ?? '',
    completion_note: task?.completion_note ?? '', change_reason: '',
  }
}
export type TaskEditValues = ReturnType<typeof taskEditValues>

export function readTaskEditValues(form: HTMLFormElement, previous: TaskEditValues): TaskEditValues {
  const data = new FormData(form)
  const text = (name: keyof TaskEditValues) => data.has(name) ? String(data.get(name) ?? '') : String(previous[name] ?? '')
  const participants = form.elements.namedItem('participants') as HTMLSelectElement | null
  return {
    title: text('title'), description: text('description'), acceptance: text('acceptance'),
    participant_ids: participants?.disabled ? previous.participant_ids : data.getAll('participants').map(String),
    priority: data.has('priority') ? (String(data.get('priority')) || null) as ProjectTask['priority'] : previous.priority,
    start: text('start'), due: text('due'), blocked_reason: text('blocked_reason'),
    completion_note: text('completion_note'), change_reason: text('change_reason'),
  }
}

export function taskEditSnapshot(values: TaskEditValues, owner: string | null, tags: string[], column: string, status: ProjectTask['status'], ownerSearch = '') {
  return JSON.stringify([values.title.trim(), values.description, [...values.participant_ids].sort(), values.priority,
    values.acceptance, values.start, values.due, status === 'blocked' ? values.blocked_reason.trim() : '',
    values.completion_note, values.change_reason, owner, [...tags].sort(), column || status, status, ownerSearch])
}
