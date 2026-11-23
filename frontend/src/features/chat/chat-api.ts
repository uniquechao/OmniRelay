import { z } from '../../api/validation.ts'
import { assignmentAPI } from './chat-assignment'
import * as model from './chat-contracts'

type Query = Record<string, string | number | undefined | null>
export function chatAPI(request: model.ChatRequest, projectId?: string | null) {
  const base = projectId
    ? '/api/v1/projects/' + encodeURIComponent(projectId) + '/chat-reader'
    : '/api/v1/feishu/chats'
  const path = (id: string) => '/' + encodeURIComponent(id)
  function read<T>(
    schema: z.ZodType<T>,
    route: string,
    params: Query,
    signal: AbortSignal,
    activity: 'user' | 'poll' = 'user',
  ) {
    const query = new URLSearchParams()
    for (const [key, value] of Object.entries(params))
      if (value !== '' && value != null) query.set(key, String(value))
    return request(base + route + (query.size ? '?' + query : ''), { signal, activity }).then((raw) =>
      schema.parse(raw),
    )
  }
  const resourceURL = (chat: string, message: string, resource: model.Resource) =>
    base +
    path(chat) +
    '/messages' +
    path(message) +
    '/resources' +
    path(resource.kind) +
    path(resource.key)
  return {
    assignments: assignmentAPI(request),
    conversations: (params: Query, signal: AbortSignal) =>
      read(model.conversationsSchema, '', params, signal),
    conversation: (id: string, signal: AbortSignal) =>
      read(model.singleConversationSchema, path(id), {}, signal),
    messages: (id: string | null, params: Query, signal: AbortSignal) =>
      read(
        model.messagesSchema,
        (id ? path(id) : '') + (params.q ? '/search' : '/messages'),
        params,
        signal,
      ),
    context: (id: string, message: string, signal: AbortSignal) =>
      read(model.messagesSchema, path(id) + '/messages' + path(message) + '/context', {}, signal),
    resourceURL,
    async preview(
      chat: string,
      message: string,
      resource: model.Resource,
      params: URLSearchParams,
      signal: AbortSignal,
    ) {
      const image = resource.kind === 'image'
      const value = await request(resourceURL(chat, message, resource) + (image ? '' : '?' + params), {
        signal,
        acceptImage: image,
      })
      return value instanceof Blob ? value : model.previewSchema.parse(value)
    },
    members: (id: string, offset: number, signal: AbortSignal) =>
      read(model.membersSchema, path(id) + '/members', { offset }, signal),
    capabilities: (id: string, signal: AbortSignal) =>
      read(model.capabilitiesSchema, path(id) + '/send-capabilities', {}, signal),
    mentionMembers: (id: string, offset: number, signal: AbortSignal) =>
      read(model.mentionsSchema, path(id) + '/members', { purpose: 'mention', offset }, signal),
    async upload(id: string, kind: 'image' | 'file', file: File, signal: AbortSignal) {
      const body = new FormData()
      body.append('file', file)
      return model.uploadSchema.parse(
        await request(base + path(id) + '/attachments?kind=' + kind, { method: 'POST', body, signal }),
      )
    },
    send: (id: string, body: model.Payload & { operation_key: string }, signal: AbortSignal) =>
      request(base + path(id) + '/messages', { method: 'POST', body, signal }).then((raw) =>
        model.deliverySchema.parse(raw),
      ),
    delivery: (id: string, key: string, signal: AbortSignal) =>
      read(model.deliverySchema, path(id) + '/deliveries' + path(key), {}, signal),
    sync: (signal: AbortSignal) => read(model.syncSchema, '/sync', {}, signal, 'poll'),
    refresh: projectId
      ? undefined
      : (signal: AbortSignal) => request(base + '/sync', { method: 'POST', signal }),
  }
}
export type ChatAPI = ReturnType<typeof chatAPI>
