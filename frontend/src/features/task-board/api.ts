import { z } from '../../api/validation.ts'
import * as contract from './task-contracts'
import type { ProjectTask, TaskCandidate, TaskFeedback, TaskFields, TaskStatus, ProjectMember, TaskTag, BoardColumn } from './task-contracts'
export type * from './task-contracts'
import {client, ApiError} from '../../api/session-client'
import {taskRequestBody} from './task-markdown'
export {ApiError}
async function json<T extends z.ZodType>(schema:T, path:string, options:{method?:string; body?:unknown; signal?:AbortSignal} = {}) {
  const value:unknown = await client.bind()(path,options)
  const result = schema.safeParse(value)
  if (!result.success) throw new Error('任务接口返回的数据格式不正确：'+result.error.issues[0]?.path.join('.'))
  return result.data
}
const body = (body: unknown) => ({method:'POST',body})
const put = (body: unknown) => ({method:'PUT',body})
export interface ProjectDetail {owner_member_id?:string;id:string; name:string; business_timezone:string; current_member_id:string; members:ProjectMember[]; milestones?:{id:string;title:string}[]; actions:string[]}
export function getProjectTasks(id: string, params: URLSearchParams, signal?: AbortSignal) { return json(contract.taskPage, `/api/v1/projects/${encodeURIComponent(id)}/tasks/query?${params}`, { signal }) }
export function claimProjectTask(projectId:string, task:ProjectTask, key:string) {
  return json(contract.task, `/api/v1/projects/${encodeURIComponent(projectId)}/tasks/${encodeURIComponent(task.id)}/claim`, body({expected_version:task.version, request_key:key}))
}
export function createProjectTask(id: string, fields: TaskFields & { column_id?: string; blocked_reason?: string }, requestKey: string, files?:Map<string,File>) { return json(contract.task, `/api/v1/projects/${encodeURIComponent(id)}/tasks`, body(taskRequestBody({ ...fields, request_key: requestKey },files))) }
export function updateProjectTask(id: string, task: ProjectTask, fields: TaskFields & { status: TaskStatus; blocked_reason: string; completion_note: string; change_reason: string; request_key?: string }, files?:Map<string,File>) { return json(contract.task, `/api/v1/projects/${encodeURIComponent(id)}/tasks/${encodeURIComponent(task.id)}`, put(taskRequestBody({ ...fields, expected_task_version: task.version },files))) }
export function deleteProjectTask(projectId: string, taskId: string, version: number, reason: string, requestKey: string) {
  return json(contract.deleted, `/api/v1/projects/${encodeURIComponent(projectId)}/tasks/${encodeURIComponent(taskId)}`, {
    method: 'DELETE', body: {expected_version: version, change_reason: reason, request_key: requestKey},
  })
}
export function getProjectTask(id: string, taskId: string, signal?: AbortSignal) { return json(contract.task, `/api/v1/projects/${encodeURIComponent(id)}/tasks/${encodeURIComponent(taskId)}`, { signal }) }
export function getProjectCandidates(id: string, status = 'pending_review', cursor = '', signal?: AbortSignal, filters?: URLSearchParams, limit = 25) { const params = new URLSearchParams(filters); params.set('status', status); params.set('limit', String(limit)); if (cursor) params.set('cursor', cursor); return json(contract.candidates, `/api/v1/projects/${encodeURIComponent(id)}/task-candidates?${params}`, { signal }) }
export function reviewProjectCandidate(id: string, candidate: TaskCandidate, values: { action: 'confirm' | 'ignore' | 'merge'; fields?: TaskFields; reason?: string; target_task_id?: string; target_version?: number; share_ack?: boolean }) { return json(contract.reviewed, `/api/v1/projects/${encodeURIComponent(id)}/task-candidates/${encodeURIComponent(candidate.id)}/review`, body({ ...values, no_deadline_ack:values.action==="confirm" && !values.fields?.due_at, expected_version: candidate.version })) }
export function getProjectTaskEvidence(id: string, taskId: string, signal?: AbortSignal) { return json(contract.evidence, `/api/v1/projects/${encodeURIComponent(id)}/tasks/${encodeURIComponent(taskId)}/evidence`, { signal }) }
export function getTaskFeedback(projectId: string, taskId: string, signal?: AbortSignal) { return json(contract.feedbackList, `/api/v1/projects/${encodeURIComponent(projectId)}/tasks/${encodeURIComponent(taskId)}/update-suggestions`, { signal }) }
export function submitTaskFeedback(projectId: string, task: ProjectTask, fields: { proposed_status: TaskFeedback['proposed_status']; note: string }, requestKey: string) { return json(contract.feedback, `/api/v1/projects/${encodeURIComponent(projectId)}/tasks/${encodeURIComponent(task.id)}/update-suggestions`, body({ ...fields, task_version: task.version, request_key: requestKey })) }
export function reviewTaskFeedback(projectId: string, feedback: TaskFeedback, action: 'accept' | 'reject', reason: string) { return json(contract.feedback, `/api/v1/projects/${encodeURIComponent(projectId)}/task-update-suggestions/${encodeURIComponent(feedback.id)}/review`, body({ action, reason, expected_version: feedback.version })) }
export type TaskSort = 'manual' | 'priority' | 'due' | 'title'
export interface MoveTaskCommand { expected_task_version: number; expected_board_version: number; target_column_id: string; before_id?: string | null; after_id?: string | null; sort?: TaskSort; blocked_reason?: string; completion_note?: string; change_reason?: string; request_key: string }
export interface SaveColumnsCommand { tags?: Omit<TaskTag, 'project_id' | 'usage_count'>[]; expected_version: number; columns: BoardColumn[]; default_column_id: string; migrations: Record<string, string>; blocked_reason?: string; change_reason?: string; request_key: string }
export function getProjectBoard(id: string, params = new URLSearchParams(), signal?: AbortSignal) { return json(contract.boardPage, `/api/v1/projects/${encodeURIComponent(id)}/board?${params}`, { signal }) }
export function saveProjectBoard(id: string, command: SaveColumnsCommand) { return json(contract.board, `/api/v1/projects/${encodeURIComponent(id)}/board`, put(command)) }
export function moveProjectTask(id: string, taskId: string, command: MoveTaskCommand) { return json(contract.task, `/api/v1/projects/${encodeURIComponent(id)}/tasks/${encodeURIComponent(taskId)}/move`, body(command)) }
export function getTaskActivity(pid: string, tid: string, kind: string, cursor = '', signal?: AbortSignal) { const query = new URLSearchParams({ kind }); if (cursor) query.set('cursor', cursor); return json(contract.activities, `/api/v1/projects/${encodeURIComponent(pid)}/tasks/${encodeURIComponent(tid)}/activity?${query}`, { signal }) }
export function commentOnTask(pid: string, tid: string, expected_version: number, request_key: string, text: string) { return json(contract.activity, `/api/v1/projects/${encodeURIComponent(pid)}/tasks/${encodeURIComponent(tid)}/comments`, body({ expected_version, request_key, body: text })) }

export function updateTaskPriorities(projectId: string, items: {id: string; expected_version: number; priority: ProjectTask["priority"]}[], key: string) {
  return json(contract.bulk, `/api/v1/projects/${encodeURIComponent(projectId)}/tasks/bulk-update`, body({items, request_key: key}))
}

export function createBoardTag(projectId: string, tag: {id:string;name:string;type:string;color:string;expected_version:number;request_key:string}) {
  return json(contract.tags, `/api/v1/projects/${encodeURIComponent(projectId)}/board/tags`, body(tag))
}
