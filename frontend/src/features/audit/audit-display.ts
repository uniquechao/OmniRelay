export const eventKinds:Record<string,string> = { login:'身份认证', account:'用户管理', role:'角色授权', config:'系统配置', directory:'组织同步' };
export const eventResults:Record<string,string> = { success:'成功', denied:'已拒绝', cancelled:'已取消', failed:'失败', requested:'已请求', coalesced:'已合并', protected:'最后一位管理员保护' };
const dateFormat = new Intl.DateTimeFormat('sv-SE', {
  timeZone:'Asia/Singapore', year:'numeric', month:'2-digit', day:'2-digit',
  hour:'2-digit', minute:'2-digit', second:'2-digit'
});
export const showTime = (value?:string|null) => value && Number.isFinite(Date.parse(value)) ? dateFormat.format(new Date(value)) : '—';
