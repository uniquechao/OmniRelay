export const connectionStates = new Map([
  ['disabled', ['pending', '已停用']],
  ['connecting', ['progress', '连接中']],
  ['connected', ['success', '在线']],
  ['reconnecting', ['progress', '重连中']],
  ['unavailable', ['warning', '连接不可用']]
]);
export const checkStates = new Map([
  ['passed', ['success', '通过']],
  ['failed', ['danger', '失败']],
  ['skipped', ['pending', '未执行']]
]);
export const checkErrors = new Map([
  ['credentials_missing', '部署尚未注入飞书应用凭据。'],
  ['credentials_rejected', '飞书拒绝了应用凭据，请核对部署配置。'],
  ['bot_unavailable', '无法读取 Bot 信息，请检查飞书应用的机器人能力。'],
  ['provider_temporarily_unavailable', '飞书平台暂不可用，请稍后重试。']
]);
const dateTime = new Intl.DateTimeFormat('zh-CN', {
  dateStyle: 'medium', timeStyle: 'medium', timeZone: 'UTC'
});

export function time(value?: string | null) {
  const timestamp = value ? Date.parse(value) : NaN;
  return Number.isFinite(timestamp) ? dateTime.format(timestamp) + ' UTC' : '';
}
