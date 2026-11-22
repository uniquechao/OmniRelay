const roles = {manager:'项目负责人',editor:'编辑者',member:'成员',reader:'只读成员'};

export function wireProjectMembers(host, {data, projectId, request, signal, navigation, refresh}) {
  const {escape:esc,openDialog,toast} = window.OmniRelayUI;
  const root='/api/v1/projects/'+projectId;
  host.querySelector('[data-member-add]')?.addEventListener('click',e=>open(null,e.currentTarget));
  host.querySelectorAll('[data-member-edit]').forEach(button=>button.onclick=()=>open(data.members.find(m=>m.account_id===button.dataset.memberEdit),button));
  function open(member,trigger) {
    let target=member, changed=false, busy=false, searchRun=0, prompt;
    const baseline={role:member?.role||'member',role_title:member?.role_title||'',responsibility:member?.responsibility||''};
    const roleOptions=Object.entries(roles).filter(([key])=>data.can_delegate||key!=='manager'||member?.role==='manager');
    const values=form=>Object.fromEntries(['role','role_title','responsibility'].map(k=>[k,form.elements[k].value]));
    const dirty=form=>JSON.stringify(values(form))!==JSON.stringify(baseline)||!member&&!!target;
    const dialog=openDialog({title:member?'编辑项目成员':'添加项目成员',description:member?.display_name,drawer:'standard',variant:'form',confirm:member?'保存修改':'添加成员',
      body:`<fieldset class="project-profile-fields"><div ${member?'hidden':''}>
        <label class="field pr-form"><span>搜索人员</span><input name="candidate_query" type="search" autocomplete="off" maxlength="200" placeholder="输入姓名或账号"></label>
        <button class="button" type="button" data-search>搜索</button><div data-candidates></div><button class="button" type="button" data-more hidden>下一页</button><p data-selected role="status"></p></div>
        <label class="field pr-form"><span>访问角色</span><select class="select" name="role" ${member?.role==='manager'&&!data.can_delegate?'disabled':''}>${roleOptions.map(([key,label])=>`<option value="${key}" ${key===baseline.role?'selected':''}>${label}</option>`).join('')}</select></label>
        <label class="field pr-form"><span>项目角色</span><input name="role_title" maxlength="100" placeholder="如：产品负责人、技术负责人" value="${esc(baseline.role_title)}"></label>
        <label class="field pr-form"><span>项目职责</span><textarea name="responsibility" rows="4" maxlength="4000" placeholder="填写此人在本项目中的职责">${esc(baseline.responsibility)}</textarea></label></fieldset>
        <p data-error role="status"></p>${member?'<button type="button" class="button" data-remove>移除成员</button>':''}`,
      isDirty:dirty,
      async onSubmit(form){
        if(busy)return {keepOpen:true};
        if(!target)return {field:form.elements.candidate_query,message:'请先搜索并选择人员。'};
        const ok=await mutate({...values(form),expected_version:target.version});
        return ok?undefined:{keepOpen:true};
      },
      onClose(){signal.removeEventListener('abort',abort);delete navigation.hasDraft;delete navigation.confirmNavigation;prompt?.close();if(changed&&!signal.aborted)refresh();},
      restoreFocus:()=>signal.aborted?undefined:trigger,
    });
    const form=dialog.querySelector('form');
    const message=dialog.querySelector('[data-error]');
    const fieldset=dialog.querySelector('fieldset');
    const submit=form.querySelector('[type=submit]');
    const abort=()=>dialog.close();
    signal.addEventListener('abort',abort,{once:true});
    navigation.hasDraft=()=>dialog.open&&dirty(form);
    navigation.confirmNavigation=()=>{
      if(busy)return false;
      if(!dirty(form)){dialog.close();return true;}
      return new Promise(resolve=>{prompt=openDialog({title:'放弃修改？',variant:'confirm',tone:'warning',body:'<p>本次修改尚未保存。</p>',cancelLabel:'继续编辑',confirm:'放弃修改',onClose(ok){prompt=null;if(ok)dialog.close();resolve(ok);}});});
    };
    const warn=e=>{if(dialog.open&&dirty(form)){e.preventDefault();e.returnValue='';}};
    window.addEventListener('beforeunload',warn,{signal});
    dialog.addEventListener('close',()=>window.removeEventListener('beforeunload',warn),{once:true});
    let page=1;
    async function search(nextPage=1){
      const query=form.elements.candidate_query.value.trim();
      if(!query){message.textContent='请输入姓名或账号。';return;}
      const run=++searchRun;
      message.textContent='正在搜索…';
      const more=dialog.querySelector('[data-more]');
      more.hidden=true;
      try{
        const result=await request(root+'/member-candidates?q='+encodeURIComponent(query)+'&page='+nextPage,{signal});
        if(signal.aborted||!dialog.open||run!==searchRun)return;
        page=nextPage;
        const box=dialog.querySelector('[data-candidates]');
        box.innerHTML=result.items.map((m,i)=>`<button class="button" type="button" data-candidate="${i}">${esc(m.display_name)} · ${esc(m.account_id)}</button>`).join('');
        box.querySelectorAll('[data-candidate]').forEach(button=>button.onclick=()=>{target=result.items[Number(button.dataset.candidate)];dialog.querySelector('[data-selected]').textContent='已选择：'+target.display_name+' · '+target.account_id;message.textContent='';});
        message.textContent=result.items.length?'':'没有可添加的启用人员。';more.hidden=result.items.length<25;
      }catch{if(dialog.open&&run===searchRun)message.textContent='搜索失败，请重试。';}
    }
    dialog.querySelector('[data-search]').onclick=()=>search();
    dialog.querySelector('[data-more]').onclick=()=>search(page+1);
    form.elements.candidate_query.oninput=()=>{searchRun++;target=member;dialog.querySelector('[data-selected]').textContent='';dialog.querySelector('[data-candidates]').replaceChildren();dialog.querySelector('[data-more]').hidden=true;};
    form.elements.candidate_query.onkeydown=e=>{if(e.key==='Enter'){e.preventDefault();void search();}};
    async function mutate(body){
      busy=true;fieldset.disabled=true;submit.disabled=true;message.textContent='正在保存…';
      try{
        await request(root+'/members/'+encodeURIComponent(target.account_id),{method:'PUT',body,signal});
        changed=true;toast('success',body.role==='revoked'?'成员已移除。':'项目成员已保存。');return true;
      }catch(error){
        if(!signal.aborted)message.textContent=error.status===409?'人员或角色已更新，或仍有未完成任务。请先交接任务，关闭后重新加载成员再操作。':[403,404].includes(error.status)?'当前账号无权修改该成员。':'保存失败，已保留输入，请重试。';return false;
      }finally{busy=false;fieldset.disabled=false;submit.disabled=false;}
    }
    dialog.querySelector('[data-remove]')?.addEventListener('click',()=>{
      if(busy)return;
      prompt=openDialog({title:'移除项目成员？',variant:'confirm',tone:'warning',confirm:'移除成员',body:`<p>将移除 ${esc(member.display_name)} 的项目访问权限。有未完成任务时，请先交接任务；最后一位负责人不能移除。</p><p role="status" data-remove-error></p>`,async onSubmit(form){if(await mutate({role:'revoked',expected_version:member.version}))return;form.querySelector('[data-remove-error]').textContent=message.textContent;return {keepOpen:true};},onClose(ok){prompt=null;if(ok)dialog.close();}});
    });
  }
}
