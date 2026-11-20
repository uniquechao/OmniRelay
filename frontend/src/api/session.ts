export async function readSession(signal: AbortSignal): Promise<'active' | 'anonymous'> {
  const response = await fetch('/api/v1/me', { signal, cache: 'no-store' });
  if (response.status === 401) return 'anonymous';
  if (!response.ok) throw new Error('无法读取会话');
  return 'active';
}
