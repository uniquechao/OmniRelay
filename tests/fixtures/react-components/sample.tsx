import {StrictMode,useState} from 'react';
import {createRoot} from 'react-dom/client';
import * as C from '../../../frontend/src/ui/components';

const rows=Array.from({length:12},(_,i)=>({id:String(i+1),name:'项目 '+(i+1)}));
const name=(r:typeof rows[number])=>r.name;
const columns=[{key:'name',label:'名称',text:name,sort:name},{key:'status',label:'状态',render:()=><C.Status tone="success">已完成</C.Status>}];
const choices=[{id:'lin',label:'林晓',detail:'研发中心',search:'linxiao'}];
const nodes=[{id:'dept',name:'研发中心'},{id:'team',name:'产品组',parentId:'dept'}];
function Sample(){
  const [tab,setTab]=useState('list');
  const [date,setDate]=useState('2026-12-31');
  const [org,setOrg]=useState<C.Selection|null>(null);
  const [overlay,setOverlay]=useState('');
  const [action,setAction]=useState('');
  const [shown,setShown]=useState(true);
  const [submitted,submit]=useState('');
  return <><C.WorkbenchLayout navigation={<a href="#列表">列表</a>} mobileNavigation={<select aria-label="主导航"><option>列表</option></select>} breadcrumb={<C.Breadcrumbs items={[{label:"工作空间",href:"#空间"},{label:"组件"}]}/>} tools={<C.ThemeToggle/>}>
    <C.PageHeading title="组件验收"/><C.ColorPalette/>
    <C.Tabs label="页面视图" items={[{id:"list",label:"列表",content:"列表视图"},{id:"detail",label:"详情",content:"详情视图"}]} value={tab} onChange={setTab}/>
    <ButtonGroup open={setOverlay}/>
    <C.Panel title="标准列表"><C.DataList label="示例项目" itemLabel="项目" rows={rows} columns={columns} rowLabel={name} search={name}
      actions={[{label:'查看',run:r=>setAction(r.name)}]}/><output>{action}</output></C.Panel>
    <C.FormGrid onSubmit={e=>{e.preventDefault();submit(JSON.stringify(Object.fromEntries(new FormData(e.currentTarget))));}}>
      <C.TextField name="title" label="标题" required/><C.TextArea name="note" label="备注"/><C.DateField name="date" label="日期"/>
      <C.SearchSelect name="category" label="分类" choices={choices}/><C.PersonSelect name="people" label="人员" multiple choices={choices}/>
      <C.OrganizationPicker name="org" nodes={nodes} value={org} onChange={setOrg}/>
      {shown && <C.RichText name="body" label="正文"/>}<C.Button type="submit">提交</C.Button>
    </C.FormGrid><output id="submitted">{submitted}</output>
    <C.Button onClick={()=>setShown(!shown)}>切换编辑器</C.Button>
    <C.Panel title="概要"><C.Facts items={[{label:'状态',value:<C.Status tone="success">已完成</C.Status>}]}/><C.Multiline>{'第一行\n第二行'}</C.Multiline>
      <C.PersonCard person={{name:'林晓',org:'研发中心',personKey:'lin'}}/></C.Panel>
    <C.Markdown source={'## 说明\n\n段落'}/><C.DateStrip mode="day" value={date} onChange={setDate}/><C.DateStrip mode="week" value={date} onChange={setDate}/>
    <C.Loading size="page" label="正在加载页面"/><C.Loading progress={42} label="正在读取"/><C.Button busy>正在保存</C.Button>
    <div className="login-page"><C.BrandMotion tagline="让工作脉络自然浮现"/></div>
    {overlay==='确认' && <C.Dialog title="确认操作" variant="confirm" tone="danger" confirm="确定" onClose={()=>setOverlay('')}>确认本次操作？</C.Dialog>}
    {overlay.startsWith('抽屉') && <C.Drawer width={overlay==='抽屉加宽'?'wide':'normal'} title="编辑" variant="form" confirm="保存" isDirty={form=>!!new FormData(form).get('draft')} onClose={()=>setOverlay('')}><C.TextField label="草稿" name="draft"/></C.Drawer>}
  </C.WorkbenchLayout></>;
}
function ButtonGroup({open}:{open:(name:string)=>void}){
  return <>{['确认','抽屉','抽屉加宽'].map(s=><C.Button key={s} onClick={()=>open(s)}>{s}</C.Button>)}<C.Button onClick={()=>C.notify('success','已保存')}>提示</C.Button></>;
}
createRoot(document.getElementById('root')!).render(<StrictMode><Sample/></StrictMode>);
