import { previewMeta } from '../drive/drive-contracts'
import { z } from '../../api/validation.ts'
import type { RequestOptions } from '../../api/session-client'

export type ChatRequest = (path: string, options?: RequestOptions) => Promise<unknown>
export function object(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}
export function errorCode(cause: unknown) {
  return object(cause) && object(cause.data) && typeof cause.data.error === 'string'
    ? cause.data.error
    : ''
}
const string = z.string()
const count = z.number().int().nonnegative()
const time = string.nullish()
export const personSchema = z.object({
  name: string,
  person_key: string.optional(),
  source: string.optional(),
  resigned: z.boolean().optional(),
  organization_paths: z.array(z.array(string)).optional(),
})
export const assignmentSchema = z.object({ project_id: string, primary: z.boolean(), version: count })
export const conversationSchema = z.object({
  id: string,
  kind: string,
  name: string,
  last_message_at: time,
  project_assignment: assignmentSchema.optional(),
  person: personSchema.optional(),
})
export const resourceSchema = z.object({
  kind: string,
  key: string,
  name: string,
  url: z.httpUrl().optional(),
  object_kind: string.optional(),
  state: string.optional(),
})
export const messageSchema = z.object({
  id: string,
  chat_id: string,
  type: string,
  body: string.optional(),
  display_text: string,
  state: string,
  sent_at: string,
  sender: personSchema,
  sender_kind: string.optional(),
  resources: z.array(resourceSchema).optional(),
  file_formats: z.array(z.object({ key: string, label: string })).optional(),
  business_receipt: z.object({ project_id: string, target_kind: string, target_id: string }).optional(),
})
export const conversationsSchema = z.object({ items: z.array(conversationSchema), total: count })
export const singleConversationSchema = conversationsSchema.pick({ items: true })
export const messagesSchema = z.object({ items: z.array(messageSchema), next_cursor: string.nullish() })
export const membersSchema = z.object({
  items: z.array(personSchema),
  total: count,
  state: string.optional(),
})
export const mentionsSchema = z.object({
  items: z.array(z.object({ name: string, mention_open_id: string })),
  total: count,
})
export const capabilitiesSchema = z.discriminatedUnion('can_send', [
  z.object({ can_send: z.literal(false), reason: string }),
  z.object({
    can_send: z.literal(true),
    sender: z.object({ name: string.optional(), open_id: string.optional() }),
    mentions: z.boolean(),
    max_attachments: count,
    max_image_bytes: count,
    max_file_bytes: count,
  }),
])
const syncItem = z.object({
  chat_id: string,
  state: string,
  coverage: string,
  attempted_at: time,
  completed_at: time,
  covered_until: time,
  earliest_message_at: time,
  pending_threads: count.optional(),
  unavailable_threads: count.optional(),
})
export const syncSchema = z.object({
  items: z.array(syncItem),
  discovery_state: string.optional(),
  total: count,
  scope: string,
})
export const uploadSchema = z.object({ id: string, expires_at: string })
export const partSchema = z.object({
  position: count,
  state: string,
  message_id: string.optional(),
  reason: string.optional(),
  attachment_id: string.optional(),
})
export const deliverySchema = z.object({
  operation_key: string,
  chat_id: string,
  state: string,
  parts: z.array(partSchema),
})
export const previewSchema = z.object({
  stage: string,
  name: string.optional(),
  kind: string.optional(),
  png: string.optional(),
  data: z.unknown().optional(),
  meta: previewMeta.optional(),
})
const segment = z.object({
  text: string.optional(),
  styles: z.array(string).optional(),
  mention_open_id: string.optional(),
})
export const payloadSchema = z.object({
  attachments: z.array(string),
  text: string.optional(),
  paragraphs: z.array(z.array(segment)).optional(),
})
export type ChatPerson = z.infer<typeof personSchema>
export type Assignment = z.infer<typeof assignmentSchema>
export type Conversation = z.infer<typeof conversationSchema>
export type ChatMessage = z.infer<typeof messageSchema>
export type Resource = z.infer<typeof resourceSchema>
export type Capabilities = z.infer<typeof capabilitiesSchema>
export type Sync = z.infer<typeof syncSchema>
export type Preview = z.infer<typeof previewSchema>
export type Payload = z.infer<typeof payloadSchema>
