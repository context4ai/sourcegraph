(function(root){
 'use strict';
 function batch(e){
  const b=e.batch;
  if(e.operation!=='read_many'||!b)return null;
  if(![b.total,b.succeeded,b.failed,b.content_items].every(Number.isSafeInteger)||b.total<1||b.total>10||b.succeeded<0||b.failed<0||b.content_items<0||b.content_items>b.total||b.succeeded+b.failed>b.total||b.content_items<b.succeeded)return null;
  return {total:b.total,succeeded:b.succeeded,failed:b.failed,unfinished:b.total-b.succeeded-b.failed,hasContent:b.content_items>0};
 }
 const api={batch};
 if(typeof module==='object'&&module.exports)module.exports=api;
 else root.QueryErrorModel=api;
})(typeof window==='object'?window:globalThis);
