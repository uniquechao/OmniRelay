import { z } from '../../api/validation.ts'
import type { RequestOptions } from '../../api/session-client.ts'

export type DriveRequest = (path: string, options?: RequestOptions) => Promise<unknown>
export type Refresh = () => unknown
const t = z.string()
const n = z.number().int()
const b = z.boolean()
const list = t.array()
const share = z.object({
  chat_id: t,
  message_id: t,
  chat_name: t,
  sender_name: t,
  sent_at: t,
  project_id: t.optional(),
  sync_state: t.optional(),
})
export const driveFile = z.object({
  id: z.union([n, t]).transform(String),
  name: t,
  category: t,
  format: t,
  format_key: t,
  project_version: n,
  project_ids: list,
  manual_project_ids: list.nullable(),
  inherited_project_ids: list.nullable(),
  sync_state: t,
  version_id: n.nullable(),
  updated_at: t.nullable(),
  sources: share.array(),
  size_bytes: n.nullable(),
  size_source: t,
  shared_at: t.nullish(),
  local_source_id: t.optional(),
  file_ref: t.optional(),
  marked_minutes: b.optional(),
})
export type DriveFile = z.infer<typeof driveFile>
export const filePage = z.object({
  items: driveFile.array(),
  projects: z.record(t, t),
  total: n.nonnegative(),
  partial: b.optional(),
  can_mark_minutes: b.optional(),
})
export const progress = z.object({
  groups: n,
  history_pending: n,
  unindexed_messages: n,
  pending_sources: n,
  complete_sources: n,
  attention_sources: n,
  history_attention: n,
  unresolved_wiki: n,
})
export const versionsPage = z.object({ items: z.array(z.object({ id: n })) })
export const previewMeta = z.object({
  page: n.optional(),
  pages: n.optional(),
  name: t.optional(),
  sheet: n.optional(),
  sheets: list.optional(),
  fields: list.optional(),
  total: n.optional(),
  first: n.optional(),
  last: n.optional(),
  document_format: t.optional(),
})
export type PreviewMeta = z.infer<typeof previewMeta>
export const previewTask = z.object({ task_id: t, stage: t, version_id: n, meta: previewMeta.nullable() })
export const previewFailure = z.object({ status: n, data: z.object({ error: t }).nullish() })
const run = z.object({
  text: t,
  kind: t.optional(),
  unresolved: b.optional(),
  code: b.optional(),
  bold: b.optional(),
  italic: b.optional(),
  underline: b.optional(),
  strike: b.optional(),
  color: n.optional(),
  background: n.optional(),
  link: t.optional(),
})
const image = z.object({ mime: t, data: t })
export type DocumentRun = z.infer<typeof run>
export type DocumentImage = z.infer<typeof image>
export const documentBlock = z.object({
  type: t,
  level: n.optional(),
  runs: run.array().optional(),
  checked: b.optional(),
  align: t.optional(),
  image: image.optional(),
  get children(): z.ZodOptional<z.ZodArray<typeof documentBlock>> {
    return z.array(documentBlock).optional()
  },
  get rows(): z.ZodOptional<z.ZodArray<z.ZodArray<z.ZodArray<typeof documentBlock>>>> {
    return z.array(z.array(z.array(documentBlock))).optional()
  },
})
export type DocumentBlock = z.infer<typeof documentBlock>
export const documentPage = z.object({ document: z.array(documentBlock) })
export const textPage = z.object({ text: t })
export const zipEntries = z.array(
  z.object({ entry: n.optional(), name: t, state: t, directory: b.optional() }),
)
export const tableRows = z.array(z.array(z.unknown()))
export const baseRows = z.array(z.object({ fields: z.record(t, z.unknown()) }))
export const officeBlocks = z.array(z.object({ text: t.optional(), table: tableRows.optional() }))
