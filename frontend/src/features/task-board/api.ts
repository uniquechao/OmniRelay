import {client, ApiError} from '../../../../web/session-client.js'
export {ApiError}
const json = <T,>(path: string, options: {method?: string; body?: unknown; signal?: AbortSignal} = {}): Promise<T> => client.bind()(path,options) as Promise<T>
const body = (body: unknown) => ({method:'POST',body})
const put = (body: unknown) => ({method:'PUT',body})
export interface ProjectMember {id:string; display_name:string; organization_path?:string; active?:boolean}
export interface ProjectDetail {owner_member_id?:string;id:string; name:string; business_timezone:string; current_member_id:string; members:ProjectMember[]; milestones?:{id:string;title:string}[]; actions:string[]}
export type TaskStatus = 'todo' | 'in_progress' | 'blocked' | 'done' | 'cancelled'
export interface TaskFields { start_at?: string | null; tag_ids?: string[]; title: string; description: string; owner_member_id: string | null; participant_ids: string[]; priority: 'P0' | 'P1' | 'P2' | 'P3' | null; due_at: string | null; acceptance: string }
export interface ProjectTask extends TaskFields { interaction_participants?: ProjectMember[]; created_by_name?:string; updated_by_name?:string; milestone_id?:string | null; display_number?: number; column_id?: string; rank?: number; id: string; project_id: string; status: TaskStatus; blocked_from: TaskStatus | null; blocked_reason: string; completion_note: string; version: number; created_by: string; updated_by: string; created_at: string; updated_at: string; overdue: boolean }
export function getProjectTasks(id: string, params: URLSearchParams, signal?: AbortSignal) { return json<{ items: ProjectTask[]; next_cursor: string | null }>(`/api/v1/projects/${encodeURIComponent(id)}/tasks/query?${params}`, { signal }) }
export function createProjectTask(id: string, fields: TaskFields & { column_id?: string; blocked_reason?: string }, requestKey: string) { return json<ProjectTask>(`/api/v1/projects/${encodeURIComponent(id)}/tasks`, body({ ...fields, request_key: requestKey })) }
export function updateProjectTask(id: string, task: ProjectTask, fields: TaskFields & { status: TaskStatus; blocked_reason: string; completion_note: string; change_reason: string; request_key?: string }) { return json<ProjectTask>(`/api/v1/projects/${encodeURIComponent(id)}/tasks/${encodeURIComponent(task.id)}`, put({ ...fields, milestone_id: task.milestone_id, expected_task_version: task.version })) }
export function deleteProjectTask(projectId: string, taskId: string, version: number, reason: string, requestKey: string) {
  return json(`/api/v1/projects/${encodeURIComponent(projectId)}/tasks/${encodeURIComponent(taskId)}`, {
    method: 'DELETE', body: {expected_version: version, change_reason: reason, request_key: requestKey},
  })
}
export function getProjectTask(id: string, taskId: string, signal?: AbortSignal) { return json<ProjectTask>(`/api/v1/projects/${encodeURIComponent(id)}/tasks/${encodeURIComponent(taskId)}`, { signal }) }
export function getProjectTaskHistory(id: string, taskId: string, signal?: AbortSignal) { return json<{ items: { id: string; actor_id: string; action: string; before: Record<string, unknown>; after: Record<string, unknown>; created_at: string }[] }>(`/api/v1/projects/${encodeURIComponent(id)}/tasks/${encodeURIComponent(taskId)}/activity?kind=change`, { signal }) }
export interface CandidateEvidence { id: string; base_key: string; kind: 'message' | 'meeting_minutes' | 'document'; created_at: string; text: string; revision: string; chat_id?: string; title?: string | null; resource_token?: string; part: number }
export interface TaskCandidate { id: string; project_id: string; fields: Omit<TaskFields, 'owner_member_id'> & { owner_member_id: string | null; confidence: number }; evidence: CandidateEvidence[]; status: 'pending_review' | 'confirmed' | 'ignored' | 'merged' | 'superseded'; version: number; task_id: string | null; model_name: string; prompt_version: string; created_at: string }
export function getProjectCandidates(id: string, status = 'pending_review', cursor = '', signal?: AbortSignal, filters?: URLSearchParams, limit = 25) { const params = new URLSearchParams(filters); params.set('status', status); params.set('limit', String(limit)); if (cursor) params.set('cursor', cursor); return json<{ items: TaskCandidate[]; next_cursor: string | null; total?: number }>(`/api/v1/projects/${encodeURIComponent(id)}/task-candidates?${params}`, { signal }) }
export function reviewProjectCandidate(id: string, candidate: TaskCandidate, values: { action: 'confirm' | 'ignore' | 'merge'; fields?: TaskFields; reason?: string; target_task_id?: string; target_version?: number; share_ack?: boolean }) { return json<{ status: TaskCandidate['status']; task_id: string | null }>(`/api/v1/projects/${encodeURIComponent(id)}/task-candidates/${encodeURIComponent(candidate.id)}/review`, body({ ...values, no_deadline_ack:values.action==="confirm" && !values.fields?.due_at, expected_version: candidate.version })) }
export function getProjectTaskEvidence(id: string, taskId: string, signal?: AbortSignal) { return json<{ items: { accessible: boolean; reason?: string; evidence?: CandidateEvidence[]; model_name?: string; created_at?: string }[] }>(`/api/v1/projects/${encodeURIComponent(id)}/tasks/${encodeURIComponent(taskId)}/evidence`, { signal }) }
export interface TaskFeedback { id: string; task_id: string; user_id: string; member_id: string | null; task_version: number; proposed_status: 'in_progress' | 'blocked' | 'done'; note: string; source_kind: 'member' | 'ai'; status: 'pending' | 'accepted' | 'rejected'; version: number; created_at: string; review_reason: string | null; evidence?: CandidateEvidence[] }
export function getTaskFeedback(projectId: string, taskId: string, signal?: AbortSignal) { return json<{ can_submit: boolean; items: TaskFeedback[] }>(`/api/v1/projects/${encodeURIComponent(projectId)}/tasks/${encodeURIComponent(taskId)}/update-suggestions`, { signal }) }
export function submitTaskFeedback(projectId: string, task: ProjectTask, fields: { proposed_status: TaskFeedback['proposed_status']; note: string }, requestKey: string) { return json<TaskFeedback>(`/api/v1/projects/${encodeURIComponent(projectId)}/tasks/${encodeURIComponent(task.id)}/update-suggestions`, body({ ...fields, task_version: task.version, request_key: requestKey })) }
export function reviewTaskFeedback(projectId: string, feedback: TaskFeedback, action: 'accept' | 'reject', reason: string) { return json<TaskFeedback>(`/api/v1/projects/${encodeURIComponent(projectId)}/task-update-suggestions/${encodeURIComponent(feedback.id)}/review`, body({ action, reason, expected_version: feedback.version })) }
export type BoardStage = Exclude<TaskStatus, 'cancelled'>
export type TaskSort = 'manual' | 'priority' | 'due' | 'title'
export interface BoardColumn { id: string; name: string; color: string; stage: BoardStage }
export interface TaskTag { id: string; project_id: string; type: string; name: string; color: string; usage_count?: number }
export interface ProjectBoard { project_id: string; version: number; default_column_id: string; columns: (BoardColumn & { project_id: string; position: number })[]; tags: TaskTag[] }
export interface ProjectBoardPage extends ProjectBoard { total: number; sort: TaskSort; columns: (ProjectBoard['columns'][number] & { total: number; items: ProjectTask[]; next_cursor: string | null })[] }
export interface MoveTaskCommand { expected_task_version: number; expected_board_version: number; target_column_id: string; before_id?: string | null; after_id?: string | null; sort?: TaskSort; blocked_reason?: string; completion_note?: string; change_reason?: string; request_key: string }
export interface SaveColumnsCommand { tags?: Omit<TaskTag, 'project_id' | 'usage_count'>[]; expected_version: number; columns: BoardColumn[]; default_column_id: string; migrations: Record<string, string>; blocked_reason?: string; change_reason?: string; request_key: string }
export function getProjectBoard(id: string, params = new URLSearchParams(), signal?: AbortSignal) { return json<ProjectBoardPage>(`/api/v1/projects/${encodeURIComponent(id)}/board?${params}`, { signal }) }
export function saveProjectBoard(id: string, command: SaveColumnsCommand) { return json<ProjectBoard>(`/api/v1/projects/${encodeURIComponent(id)}/board`, put(command)) }
export function moveProjectTask(id: string, taskId: string, command: MoveTaskCommand) { return json<ProjectTask>(`/api/v1/projects/${encodeURIComponent(id)}/tasks/${encodeURIComponent(taskId)}/move`, body(command)) }
export interface TaskActivityEvent { id: string; kind: 'change' | 'comment'; action: string; actor_id: string; actor_name?: string; created_at: string; before: Record<string, unknown>; after: Record<string, unknown>; body: string; source_ref: string | null }
export interface TaskActivityPage { items: TaskActivityEvent[]; next_cursor: string | null }
export function getTaskActivity(pid: string, tid: string, kind: string, cursor = '', signal?: AbortSignal) { const query = new URLSearchParams({ kind }); if (cursor) query.set('cursor', cursor); return json<TaskActivityPage>(`/api/v1/projects/${encodeURIComponent(pid)}/tasks/${encodeURIComponent(tid)}/activity?${query}`, { signal }) }
export function commentOnTask(pid: string, tid: string, expected_version: number, request_key: string, text: string) { return json<TaskActivityEvent>(`/api/v1/projects/${encodeURIComponent(pid)}/tasks/${encodeURIComponent(tid)}/comments`, body({ expected_version, request_key, body: text })) }

export function updateTaskPriorities(projectId: string, items: {id: string; expected_version: number; priority: ProjectTask["priority"]}[], key: string) {
  return json(`/api/v1/projects/${encodeURIComponent(projectId)}/tasks/bulk-update`, body({items, request_key: key}))
}

export function createBoardTag(projectId: string, tag: {id:string;name:string;type:string;color:string;expected_version:number;request_key:string}) {
  return json<{tags:TaskTag[]}>(`/api/v1/projects/${encodeURIComponent(projectId)}/board/tags`, body(tag))
}
