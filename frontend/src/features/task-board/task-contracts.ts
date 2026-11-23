import { z } from '../../api/validation.ts'

const text = z.string()
const count = z.number().int().nonnegative()
const names = z.record(text, text)
const member = z.object({id:text, display_name:text, organization_path:text.optional(), active:z.boolean().optional()})
const stage = z.enum(['todo','in_progress','blocked','done'])
const status = z.enum([...stage.options,'cancelled'])
const priority = z.enum(['P0','P1','P2','P3']).nullable()
const fields = z.object({
  title:text, description:text, acceptance:text, owner_member_id:text.nullable(),
  participant_ids:z.array(text).nullable().transform(ids=>ids ?? []), priority,
  due_at:text.nullable(), start_at:text.nullable().optional(), milestone_id:text.nullable().optional(),
  tag_ids:z.array(text).nullable().optional().transform(ids=>ids ?? []),
})
export const task = fields.extend({
  id:text, project_id:text, task_code:text, display_number:count.optional(), version:count,
  status, column_id:text.optional(), blocked_reason:text, completion_note:text, overdue:z.boolean(),
  created_at:text.nullable(), updated_at:text.nullable(), created_by:text.nullable(), updated_by:text.nullable(),
  created_by_name:text.nullish(), updated_by_name:text.nullish(),
  relation_names:names.optional(), interaction_participants:z.array(member).optional(),
})
const column = z.object({id:text, name:text, color:text, stage})
const tag = z.object({id:text, name:text, color:text, type:text, usage_count:count.optional()})
export const tags = z.object({tags:z.array(tag)})
export const board = tags.extend({version:count, default_column_id:text, columns:z.array(column)})
export const boardPage = board.extend({
  project_id:text, total:count, columns:z.array(column.extend({total:count, next_cursor:text.nullable(), items:z.array(task)})),
})
const page = <T extends z.ZodType>(item:T)=>z.object({items:z.array(item), next_cursor:text.nullable(), total:count.optional()})
export const taskPage = page(task)
export const deleted = z.object({id:text,version:count,deleted:z.literal(true)})
export const bulk = z.object({project_id:text,request_key:text,items:z.array(task)})
const source = z.object({id:text,kind:z.enum(['message','document','minutes']),text,created_at:text,chat_id:text.optional(),title:text.optional()})
const candidateSource = source.extend({revision:text,part:count})
const candidateStatus = z.enum(['pending_review','confirmed','ignored','merged','superseded'])
export const reviewed = z.object({status:candidateStatus,task_id:text.nullable()})
const candidate = reviewed.extend({
  id:text, version:count, fields:fields.extend({confidence:z.number()}), evidence:z.array(candidateSource),
  prompt_version:text, created_at:text,
})
export const candidates = page(candidate)
export const evidence = z.object({items:z.array(z.object({accessible:z.boolean(),reason:text.optional(),evidence:z.array(source).optional()}))})
export const feedback = z.object({
  id:text, task_id:text, actor_id:text, task_version:count, version:count, note:text, reason:text.nullable(),
  proposed_status:z.enum(['in_progress','blocked','done']), status:z.enum(['pending','accepted','rejected']),
})
export const feedbackList = z.object({can_submit:z.boolean(),items:z.array(feedback)})
export const activity = z.object({
  id:count, kind:z.enum(['comment','change']), action:text, actor_id:text, actor_name:text, created_at:text, body:text,
  before:z.record(text,z.unknown()), after:z.record(text,z.unknown()),
})
export const activities = page(activity)
export type ProjectTask = z.infer<typeof task>
export type TaskStatus = z.infer<typeof status>
export type TaskFields = Omit<z.infer<typeof fields>, 'tag_ids'> & {tag_ids?:string[]}
export type ProjectMember = z.infer<typeof member>
export type TaskCandidate = z.infer<typeof candidate>
export type TaskFeedback = z.infer<typeof feedback>
export type BoardStage = z.infer<typeof stage>
export type BoardColumn = z.infer<typeof column>
export type TaskTag = z.infer<typeof tag>
export type ProjectBoard = z.infer<typeof board>
export type ProjectBoardPage = z.infer<typeof boardPage>
export type TaskActivityEvent = z.infer<typeof activity>
export type TaskActivityPage = z.infer<typeof activities>
