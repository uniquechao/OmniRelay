import {matchPath} from 'react-router';
import {projectRoute} from '../features/projects/projects.ts';

export type Access = {navigation?: Record<string,boolean>; admin?: Record<string,boolean>; default_route?: string};
type Parsed = {path:string;title?:string;section?:string};
export type Route = {id:string;path:string;title:string;group:'工作空间'|'飞书'|'系统管理';icon:string;
  capability:string;parse?:(path:string)=>Parsed|undefined};
const identifier=/^[A-Za-z0-9_-][A-Za-z0-9_.:-]{0,199}$/;
export function matchPattern(pattern:string,path:string,valid=(value:string)=>identifier.test(value)) {
  // React Router 接受尾斜杠与编码分隔符，返回目标仅允许原始、完整的站内段。
  if(path.endsWith('/') || path.includes('%') || /[?#]/.test(path.slice(3)))return;
  const match=matchPath({path:pattern,caseSensitive:true,end:true},path);
  if(!match || !Object.values(match.params).every(value=>typeof value==='string' && valid(value)))return;
  return match.params;
}
function chat(path:string):Parsed|undefined {
  if(['','/:chat','/:chat/:message'].some(suffix=>matchPattern('/#/feishu/bot-chats'+suffix,path,
    value=>/^[A-Za-z0-9_-]{1,160}$/.test(value)))) return {path};
}
function project(path:string):Parsed|undefined {
  if(!/^\/#\/projects(?:[/?]|$)/.test(path) || !/^([^?]+)(\?[\w%+=.&-]*)?$/.test(path)) return;
  // URLSearchParams 会容忍残缺的百分号编码，回跳路径需要与 Go 的查询解析保持一致。
  if (/%(?![A-Fa-f0-9]{2})/.test(path)) return;
  const result=projectRoute(path.slice(1));
  if(!result) return;
  return {path:'/'+result.hash,title:result.title,section:result.id?'项目列表':undefined};
}
const system=(id:string,title:string,capability=id):Route=>({
  id,path:'/#/system/'+id,title,group:'系统管理',icon:'nav-system',capability
});
export const routes:Route[]=[
  {id:'workbench',path:'/#/workbench',title:'工作台总览',group:'工作空间',icon:'nav-workbench',capability:'workbench'},
  {id:'projects',path:'/#/projects',title:'项目列表',group:'工作空间',icon:'nav-projects',capability:'workbench',parse:project},
  {id:'agents',path:'/#/agents',title:'Agent 管理',group:'工作空间',icon:'nav-bot-chat',capability:'agents'},
  {id:'runs',path:'/#/runs',title:'运行记录',group:'工作空间',icon:'nav-workbench',capability:'runs'},
  {id:'dshbot',path:'/#/dshbot',title:'dshbot',group:'工作空间',icon:'nav-bot-chat',capability:'dshbot'},
  {id:'bot-chats',path:'/#/feishu/bot-chats',title:'机器人记录',group:'飞书',icon:'nav-bot-chat',capability:'bot_chats',parse:chat},
  {id:'drive',path:'/#/feishu/drive',title:'文件网盘',group:'飞书',icon:'file-grid',capability:'bot_chats'},
  system('users','用户管理'),system('roles','角色权限'),system('feishu-bot','飞书 Bot 配置','feishu_bot'),
  system('settings','连接设置','settings'),system('audit','审计日志')
];
export function resolveTarget(path:string,registry=routes) {
  if(path.length>4096 || !path.startsWith('/#/') || /[\\\s\x00-\x1f\x7f]/.test(path)) return;
  for(const route of registry){
    const parsed=path===route.path?{path:route.path}:route.parse?.(path);
    if(parsed) return {route,path:parsed.path,title:parsed.title ?? route.title,section:parsed.section ?? route.group};
  }
}
export function permitted(route:Route,access?:Access){
  return route.group==='系统管理' ? access?.navigation?.system===true && access.admin?.[route.capability]===true
    :access?.navigation?.[route.capability]===true;
}
export function destination(path:string,access?:Access){
  for(const candidate of [path,access?.default_route ?? '']){
    const target=resolveTarget(candidate);
    if(target && permitted(target.route,access)) return target;
  }
}
const storageKey='omnirelay.feishu.return-route';
let remembered='';
export function remember(path:string){
  remembered=resolveTarget(path)?.path.slice(1) ?? '';
  try {if(remembered) sessionStorage.setItem(storageKey,remembered); else sessionStorage.removeItem(storageKey);}catch{}
}
export function returnPath(){
  let value=remembered;
  try {value=sessionStorage.getItem(storageKey) ?? value;}catch{}
  return resolveTarget('/'+value)?.path ?? '/';
}
