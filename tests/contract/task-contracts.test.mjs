import assert from 'node:assert/strict';
import test from 'node:test';
import { activities, boardPage, evidence, feedbackList } from '../../frontend/src/features/task-board/task-contracts.ts';

test('看板分页使用服务端实际返回的列和整数数量', () => {
  const column = { id: 'todo', name: '待开始', color: '#708090', stage: 'todo',
    total: 0, next_cursor: null, items: [] };
  const response = { project_id: 'project', default_column_id: 'todo', version: 3,
    total: 0, tags: [], columns: [column] };
  assert.deepEqual(boardPage.parse(response), response);
  assert.equal(boardPage.safeParse({...response, columns: [{...column, total: '0'}]}).success, false);
});

test('动态游标和事件 ID 不能互换类型', () => {
  const change = { id: 57, kind: 'change', action: 'task.updated', actor_id: 'account',
    actor_name: '项目成员', created_at: '2026-11-23T01:00:00Z', body: '',
    before: {title: '草稿'}, after: {title: '已核对'} };
  assert.equal(activities.parse({items: [change], next_cursor: 'next'}).items[0].id, 57);
  assert.equal(activities.safeParse({items: [{...change, id: '57'}], next_cursor: null}).success, false);
});

test('受限来源不依赖正文，进展反馈必须返回操作人', () => {
  const restricted = {accessible: false, reason: '无权读取原始依据'};
  assert.deepEqual(evidence.parse({items: [restricted]}).items, [restricted]);
  const item = {id: 'feedback', task_id: 'task', actor_id: 'account', task_version: 2,
    version: 1, note: '已完成', reason: null, proposed_status: 'done', status: 'pending'};
  const valid = feedbackList.parse({can_submit: false, items: [item]});
  assert.equal(valid.items[0].actor_id, 'account');
  assert.equal(feedbackList.safeParse({can_submit: false, items: [{...item, actor_id: null}]}).success, false);
});
