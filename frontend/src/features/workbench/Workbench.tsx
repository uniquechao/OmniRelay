import { useQuery } from '@tanstack/react-query';
import { readSession } from '../../api/session';
import { Entry } from '../../ui/Entry';

export function Workbench() {
  const session = useQuery({
    queryKey: ['session'], queryFn: ({ signal }) => readSession(signal), retry: false
  });
  return <Entry title="工作台">
    <p role="status">{session.isPending ? '正在读取会话…' : session.isError ? '无法读取会话。'
      : session.data === 'active' ? '已登录' : '尚未登录'}</p>
    {session.isError && <button onClick={() => void session.refetch()}>重试</button>}
  </Entry>;
}
