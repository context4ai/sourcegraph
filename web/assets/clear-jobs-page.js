(function(){
 const A=App,$=id=>document.getElementById(id),dialog=$('clear-jobs-dialog'),confirm=$('clear-jobs-confirm'),cancel=$('clear-jobs-cancel'),success=$('clear-jobs-success'),failed=$('clear-jobs-failed');
 let busy=false;
 function enabled(){confirm.disabled=busy||(!success.checked&&!failed.checked);cancel.disabled=busy;success.disabled=failed.disabled=busy;}
 success.onchange=failed.onchange=enabled;
 $('clear-jobs').onclick=()=>{if(busy||!A.canManage())return;success.checked=failed.checked=true;$('clear-jobs-error').textContent='';enabled();dialog.showModal();};
 cancel.onclick=()=>{if(!busy)dialog.close();};dialog.addEventListener('cancel',e=>{if(busy)e.preventDefault();});
 confirm.onclick=async()=>{
  if(busy||confirm.disabled)return;busy=true;enabled();
  try{const r=await API.request('/api/admin/settings/jobs/clear',{method:'POST',body:{confirm:'CLEAR_FINISHED_JOBS',success:success.checked,failed:failed.checked}});$('clear-jobs-status').textContent=A.t('已清除 '+r.removed+' 条任务记录','Cleared '+r.removed+' task records');dialog.close();}
  catch(e){$('clear-jobs-error').textContent=e.message||A.t('清理失败，请重试','Cleanup failed; retry');}
  finally{busy=false;enabled();}
 };
})();
