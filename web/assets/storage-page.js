(function(){
 'use strict';
 const A=App,t=A.t,$=s=>document.querySelector(s);
 let current=null,busy=false;
 const allowed=()=>!!A.user()&&A.canManage();
 function controls(){const off=busy||!current||!allowed();$('#storage-fields').disabled=off;$('#storage-save').disabled=off;$('#storage-reload').disabled=busy||!allowed();}
 function status(text,error=false){$('#storage-status').textContent=text;$('#storage-status').classList.toggle('text-danger',error);}

 async function load(){if(busy||!allowed())return;busy=true;controls();try{current=await API.request('/api/admin/settings/storage');$('#storage-base').value=current.values.base_directory;status(current.cleanup_pending?t('旧数据还没清理完，再保存一次重试','Old data cleanup is pending. Save again to retry'):'',current.cleanup_pending);}catch(e){status(e.message,true);}finally{busy=false;controls();}}
  $('#storage-reload').onclick=load;
 $('#storage-form').onsubmit=async function(e){
  e.preventDefault();if(busy||!current||!allowed())return;
  const base=$('#storage-base').value.trim();
  if(!base.startsWith('/')||base==='/'){status(t('请填写服务器上的绝对路径','Enter an absolute path on the server'),true);return;}
  if(base!==current.values.base_directory&&!confirm(t('切换后会删除旧的 Git 副本和索引，仓库需要重新同步。继续吗？','Old Git copies and indexes will be deleted and repositories will sync again. Continue?')))return;
  busy=true;controls();status(t('正在保存…','Saving…'));
  const body={revision:current.revision,base_directory:base,create:false};
  try{
   let result;
   try{result=await API.request('/api/admin/settings/storage',{method:'PATCH',body});}
   catch(err){
    if(err.code!=='DIRECTORY_CREATE_REQUIRED')throw err;
    status(err.message,true);
    if(!confirm(err.message+'\n'+t('由服务创建这个目录并保存吗？','Create this directory on the server and save?')))return;
    result=await API.request('/api/admin/settings/storage',{method:'PATCH',body:{...body,create:true}});
   }
   current=result;$('#storage-base').value=result.values.base_directory;status(result.cleanup_pending?t('目录已切换，但旧数据还没清理完，准备任务已暂停。再保存一次重试','Directory changed, but cleanup is pending and preparation is paused. Save again to retry'):t('已保存，仓库会从新目录重新同步','Saved. Repositories will sync again from the new directory'),result.cleanup_pending);
  }catch(err){status(err.message+(err.code==='SETTINGS_REVISION_CONFLICT'?t('，请重新加载',' Reload and try again.') : ''),true);}
  finally{busy=false;controls();}
 };
 document.addEventListener('app:auth',controls);API.ready.then(load);
})();
