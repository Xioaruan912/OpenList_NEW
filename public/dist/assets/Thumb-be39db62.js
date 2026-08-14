import{$ as e,Ar as t,Bt as n,Cn as r,Dr as i,Er as a,Gr as o,Hr as s,Jr as c,Ki as l,Kn as u,Mr as d,Ni as f,Nr as p,On as m,Or as h,Pr as g,Qi as _,Qt as v,Sn as y,Sr as b,Tr as x,Ur as S,Vr as C,Vt as w,Wi as T,Wn as E,Wr as D,X as O,Xr as k,Yi as A,Zr as j,br as M,dr as Je,d as N,dn as P,fr as Qe,hn as F,it as I,jr as L,kr as R,lr as dt,na as z,oa as B,or as yt,qi as V,qr as H,ri as U,rt as W,sr as Eq,ur as Nt,xr as G,Qn as Hx}from"./store-BnRstdzp.js";
var ee=()=>{
  let o=e();
  O(`缩略图管理`);
  let{to:f}=W(),
  [st,setSt]=B(null),
  [tree,setTree]=B([]),
  [queued,setQueued]=B(0),
  [expanded,setExp]=B(new Set()),
  [busy,setBusy]=B(``),
  [stale,setStale]=B([]),
  [mounts,setMounts]=B([]),
  [oldP,setOldP]=B(``),
  [newP,setNewP]=B(``),
  load=async()=>{let r=await v.get(`/admin/thumb/status`);n(r,r=>{setSt(r),setQueued(r.prewarm_queued||0),setStale(r.stale_by_dir||[]),setMounts(r.mounts||[]),setOldP((r.stale_by_dir||[]).length?(r.stale_by_dir[0].dir.split(`/`).slice(0,2).join(`/`)):``),setNewP((r.mounts||[])[0]||``)})},
  loadTree=async()=>{let r=await v.get(`/admin/thumb/tree`);n(r,r=>setTree(r.children||[]))},
  queueGen=async(pp)=>{setBusy(pp);try{let r=await v.post(`/admin/thumb/generate`,{path:pp,recursive:!0});n(r,r=>{P.success(`已加入队列：${r.queued} 个`),load()})}finally{setBusy(``)}},
  retryAll=async()=>{try{let r=await v.post(`/admin/thumb/retry_fails`,{});n(r,r=>{P.success(`已重试：${r.retried} 个`),load()})}catch(q){}},
  migrate=async()=>{if(!oldP()||!newP()){P.warning(`请填写旧/新路径`);return}let r=await v.post(`/admin/thumb/migrate`,{old_prefix:oldP(),new_prefix:newP()});n(r,r=>{P.success(`已迁移 ${r.migrated} 个缓存文件`),load(),loadTree()})},
  toggle=pp=>setExp(ss=>{let z=new Set(ss);z.has(pp)?z.delete(pp):z.add(pp);return z}),
  TN=(nn,depth)=>_(j,{w:`$full`,get children(){return[_(u,{direction:`row`,spacing:`$1`,alignItems:`center`,p:`$1`,get _hover(){return{bgColor:U(`$neutral1`,`$neutral2`)()}},style:{paddingLeft:depth*18+`px`},get children(){return[_(m,{size:`xs`,onClick:q=>{q.stopPropagation(),toggle(nn.path)},get children(){return expanded().has(nn.path)?`▾`:`▸`}}),_(j,{css:{flex:`1 1 auto`,wordBreak:`break-all`},get children(){return nn.name}}),_(y,{colorScheme:`info`,get children(){return`${nn.cached}`}}),_(m,{size:`xs`,get disabled(){return busy()===nn.path},onClick:q=>{q.stopPropagation(),queueGen(nn.path)},get children(){return `生成`}})]}}),_(V,{get when(){return expanded().has(nn.path)&&(nn.children||[]).length>0},get children(){return _(T,{get each(){return nn.children||[]},children:q=>TN(q,depth+1)})}})]}});
  load(),loadTree();
  setInterval(()=>{load()},10000);
  return _(j,{spacing:`$3`,alignItems:`start`,w:`$full`,get children(){return[
    _(u,{spacing:`$2`,gap:`$2`,w:`$full`,wrap:{"@initial":`wrap`,"@md":`unset`},get children(){return[
      _(j,{p:`$3`,rounded:`$lg`,border:`1px solid $neutral7`,get children(){return`缓存 ${st()?st().cached_files:0} 个`}}),
      _(j,{p:`$3`,rounded:`$lg`,border:`1px solid $neutral7`,get children(){return`队列 ${queued()} 个`}}),
      _(j,{p:`$3`,rounded:`$lg`,border:`1px solid $neutral7`,get children(){return`失败 ${st()?st().fail_markers:0} 个`}}),
      _(j,{p:`$3`,rounded:`$lg`,border:`1px solid $neutral7`,get children(){return`占用 ${st()?(st().cache_size/1048576).toFixed(1):`0`} MB`}})
    ]}}),
    _(u,{spacing:`$2`,alignItems:`center`,get children(){return[
      _(j,{get children(){return`点击目录“生成”加入队列，按 5 个一批自动提交（防风控）`}}),
      _(m,{colorScheme:`warning`,onClick:retryAll,get children(){return `重试全部失败`}})
    ]}}),
    _(j,{mt:`$2`,rounded:`$lg`,border:`1px solid $neutral6`,p:`$1`,get children(){return[
      _(j,{fontWeight:`$medium`,p:`$1`,get children(){return `已有缩略图的目录`}}),
      _(u,{direction:`column`,w:`$full`,get children(){return _(T,{get each(){return tree()},children:q=>TN(q,0)})}})
    ]}}),
    _(V,{get when(){return stale().length>0},get children(){return _(u,{spacing:`$2`,mt:`$3`,w:`$full`,direction:`column`,get children(){return[
      _(j,{fontWeight:`$medium`,get children(){return`检测到旧挂载路径（存储挂载路径已变更）：`}}),
      _(j,{fontSize:`$sm`,get children(){return stale().map(e=>e.dir+`（`+e.count+`）`).join(`、`)}}),
      _(u,{spacing:`$2`,alignItems:`center`,get children(){return[
        _(j,{get children(){return`旧前缀`}}),
        _(Hx,{get value(){return oldP()},onInput:e=>setOldP(e.currentTarget.value),placeholder:`如 /影视相关`,w:`$full`,maxW:`260px`}),
        _(j,{get children(){return`→ 新前缀`}}),
        _(Hx,{get value(){return newP()},onInput:e=>setNewP(e.currentTarget.value),placeholder:`如 /01_影视相关`,w:`$full`,maxW:`260px`}),
        _(m,{colorScheme:`info`,onClick:migrate,get children(){return`迁移`}})
      ]}})
    ]}})}})
  ]}})
};
export{ee as default};
