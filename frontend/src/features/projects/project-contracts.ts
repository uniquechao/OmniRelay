import { z } from '../../api/validation.ts'
import type { RequestOptions } from '../../api/session-client'
export type ProjectRequest = (path: string, options?: RequestOptions) => Promise<unknown>
const text = z.string()
const maybe = text.nullish()
export const projectTypeSchema = z.object({ name: text, tone: text })
export const projectSchema = z.object({
  id: text,
  name: text,
  status: text,
  project_type: text,
  business_timezone: text,
  background: text.optional(),
  version: z.number().int(),
  board_version: z.number().int(),
  short_code: text.optional(),
  planned_start: text.optional(),
  planned_end: text.optional(),
  updated_at: text.optional(),
  followed: z.boolean().optional(),
})
export const projectPageSchema = z.object({
  items: z.array(projectSchema),
  total: z.number().int(),
  can_create: z.boolean(),
  project_types: z.array(projectTypeSchema),
})
export type Project = z.infer<typeof projectSchema>
export type ProjectType = z.infer<typeof projectTypeSchema>
export const candidateSchema = z.object({
  account_id: text,
  display_name: text,
  username: text.optional(),
  version: z.number().int(),
})
const memberSchema = candidateSchema.extend({
  id: text,
  role: text,
  project_role: text,
  active: z.boolean().optional(),
  responsibility: maybe,
  last_spoke_at: maybe,
  organization_paths: z.array(z.array(text)).nullish(),
  avatar_url: maybe,
})
export const directorySchema = z.object({
  members: z.array(memberSchema),
  project_roles: z.array(z.object({ id: text, name: text, scope: text })),
  can_manage: z.boolean(),
  can_delegate: z.boolean(),
})
export type Candidate = z.infer<typeof candidateSchema>
export type Member = z.infer<typeof memberSchema>
export type Directory = z.infer<typeof directorySchema>
const deadline = {
  due_at: maybe,
  due_label: maybe,
  due_alert: z.boolean().optional(),
  due_alert_label: maybe,
}
export const briefingSchema = z.object({
  id: text,
  business_date: text,
  title: maybe,
  body: maybe,
  excerpt: maybe,
  generated_at: maybe,
  delivery_state: maybe,
  visible_submissions: z.number().nullish(),
  source_state: maybe,
  target_chat_id: maybe,
  delivery_confirmed_at: maybe,
})
export type Briefing = z.infer<typeof briefingSchema>
const milestoneSchema = z.object({
  id: text,
  title: text,
  kind: text,
  status: maybe,
  occurs_on: maybe,
  owner_name: maybe,
  owner_member_id: maybe,
  task_id: maybe,
  task: z.object(deadline).nullish(),
})
export const overviewSchema = z.object({
  workspace: z.object({
    project: projectSchema,
    can_manage_profile: z.boolean(),
    project_types: z.array(projectTypeSchema),
    owner: z.object({ display_name: text }).nullish(),
  }),
  milestones: z.object({
    items: z.array(milestoneSchema),
    progress: z.object({ state: maybe, record: z.object({ title: text }).nullish() }).nullish(),
  }),
  latest_briefing: briefingSchema.nullish(),
})
export const boardAccessSchema = z.object({
  can_create: z.boolean(),
  can_comment: z.boolean(),
  can_configure: z.boolean(),
  can_assign: z.boolean(),
  business_timezone: text,
  member_id: text,
})
export const sourceTaskSchema = z.object({
  id: text,
  title: text,
  status: text,
  priority_label: maybe,
  milestone_id: maybe,
  owner_member_id: maybe,
  ...deadline,
})
export const reportSchema = z.object({
  id: text.optional(),
  person_name: maybe,
  work_date: maybe,
  note: maybe,
  submission_state: text,
})
export const meetingSchema = z.object({
  id: text.optional(),
  kind: text,
  resource_token: maybe,
  file_id: z.number().int().optional(),
  version_id: z.number().int().nullish(),
  chat_id: maybe,
  message_id: maybe,
  created_at: maybe,
  title: maybe,
})
export const connectionSchema = z.object({
  state: text,
  target_name: maybe,
  can_check: z.boolean().optional(),
  plan: z
    .object({ target_chat_id: maybe, plan_enabled: z.boolean(), local_time: maybe, timezone: maybe })
    .optional(),
  briefings: z.object({ items: z.array(briefingSchema) }).optional(),
})
export const coverageSchema = z.object({
  coverage: z.object({ complete: z.boolean().optional(), state: maybe }).optional(),
  source_statuses: z.array(z.object({ access_denied: z.boolean().optional(), status: maybe })).optional(),
  items: z
    .array(
      z.object({
        truncated: z.boolean().optional(),
        coverage: z.object({ complete: z.boolean().optional() }).optional(),
        status: maybe,
      }),
    )
    .optional(),
})

export const assignmentDirectorySchema = z.object({
  data: z.object({
    members: z.array(
      z.object({
        id: text,
        display_name: text,
        active: z.boolean().optional(),
        organization_path: text.optional(),
      }),
    ),
  }),
})
