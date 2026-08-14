import{$ as e,Ar as t,Bt as n,Cn as r,Dr as i,Er as a,Gr as o,Hr as s,Jr as c,Ki as l,Kn as u,Mr as d,Ni as f,Nr as p,On as m,Or as h,Pr as g,Qi as _,Qt as v,Sn as y,Sr as b,Tr as x,Ur as S,Vr as C,Vt as w,Wi as T,Wn as E,Wr as D,X as O,Xr as k,Yi as A,Zr as j,br as Pg,dr as Je,d as N,dn as P,fr as Qe,hn as F,it as I,jr as L,kr as R,lr as dt,na as z,oa as B,or as yt,qi as V,qr as H,ri as U,rt as W,sr as Eq,ur as Nt,xr as G,Qn as Hx}from"./store-BnRstdzp.js";
var ee=()=>{
  let o=e();
  O(`缩略图管理`);
  let{to:f}=W(),
  [st,setSt]=B(null),
  [tree,setTree]=B([]),
  [queued,setQueued]=B(0),
  [ruEnabled,setRuEnabled]=B(!0),
  [ruStart,setRuStart]=B(3),
  [ruEnd,setRuEnd]=B(6),
  [ruBatch,setRuBatch]=B(5),
  [ruInterval,setRuInterval]=B(30),
  [ruWorker,setRuWorker]=B(3),
  [genPower,setGenPower]=B(`medium`),
  [pendingUp,setPendingUp]=B(0),
  [genActive,setGenActive]=B(0),
  [genBlocked,setGenBlocked]=B(!1),
  [totalQueued,setTotalQueued]=B(0),
  [busy,setBusy]=B(``),
  [sel,setSel]=B(``),
  [selFiles,setSelFiles]=B([]),
  [selCount,setSelCount]=B(0),
  [selExcluded,setSelExcluded]=B([]),
  [checked,setChecked]=B({}),
  [expanded,setExp]=B(new Set()),
  [treeLoading,setTreeLoading]=B(!1),
  [scanStatus,setScanStatus]=B(`complete`),
  [open,setOpen]=B(!1),
  [startPath,setStart]=B(`/`),
  [stale,setStale]=B([]),
  [mounts,setMounts]=B([]),
  [oldP,setOldP]=B(``),
  [newP,setNewP]=B(``),
  flat=()=>{let out=[];let walk=(ns,d)=>{for(let k of ns||[]){out.push({path:k.path,name:k.name,cached:k.cached,depth:d});if(k.children&&k.children.length)walk(k.children,d+1)}};walk(tree(),0);return out},
  allDirs=()=>[{path:`/`,name:`/`,cached:st()?st().cached_files:0,depth:0}].concat(flat()),
  load=async()=>{let r=await v.get(`/admin/thumb/status`);n(r,r=>{setSt(r),setQueued(r.prewarm_queued||0),setStale(r.stale_by_dir||[]),setMounts(r.mounts||[]),setOldP((r.stale_by_dir||[]).length?(r.stale_by_dir[0].dir.split(`/`).slice(0,2).join(`/`)):``),setNewP((r.mounts||[])[0]||``),setRuEnabled(r.remote_upload_enabled!==!1),setRuStart(r.remote_upload_start||3),setRuEnd(r.remote_upload_end||6),setRuBatch(r.remote_upload_batch||5),setRuInterval(r.remote_upload_interval||30),setRuWorker(r.worker_concurrency||3),setGenPower(r.gen_power||`medium`),setPendingUp(r.pending_upload||0),setGenActive(r.active_workers||0),setGenBlocked(!!r.blocked)})},
  loadTree=async()=>{setTreeLoading(!0);try{let r=await v.get(`/admin/thumb/tree`);n(r,r=>{setTree(r.children||[]),setScanStatus(r.scan_status||`complete`)})}finally{setTreeLoading(!1)}},
  loadDir=async(pp)=>{if(!pp)return;let r=await v.get(`/admin/thumb/dir`,{params:{path:pp}});n(r,r=>{setSelFiles(r.files||[]),setSelCount(r.count||0),setSelExcluded(r.excluded||[]),(()=>{let z={};for(let ff of r.files||[]){z[ff]=!((r.excluded||[]).includes(ff))}setChecked(z)})()})},
  queueGen=async(pp,force)=>{setBusy(pp);try{let r=await v.post(`/admin/thumb/generate`,{path:pp,recursive:!0,force:!!force});n(r,r=>{P.success(`已加入队列：${r.queued} 个${r.blocked?`（115 风控中，缩略图本地生成，上传将在上传窗口自动进行）`:``}${r.truncated?`（已达单次上限，可再次点击分批生成）`:``}，按当前生成强度分批生成`),setTotalQueued(t=>t+(r.queued||0)),load()})}finally{setBusy(``)}},
  retryAll=async()=>{try{let r=await v.post(`/admin/thumb/retry_fails`,{});n(r,r=>{P.success(`已重试：${r.retried} 个`),load()})}catch(q){}},saveGen=async()=>{let items=[{key:`thumb_worker_concurrency`,value:String(ruWorker())},{key:`thumb_generation_power`,value:genPower()}];try{let r=await v.post(`/admin/setting/save`,items);n(r,r=>{P.success(`已保存生成配置`),load()})}catch(q){P.error(`保存失败`)}},saveRu=async()=>{let items=[{key:`thumb_remote_upload_enabled`,value:ruEnabled()?`true`:`false`},{key:`thumb_remote_upload_start`,value:String(ruStart())},{key:`thumb_remote_upload_end`,value:String(ruEnd())},{key:`thumb_remote_upload_batch`,value:String(ruBatch())},{key:`thumb_remote_upload_interval`,value:String(ruInterval())}];try{let r=await v.post(`/admin/setting/save`,items);n(r,r=>{P.success(`已保存配置`),load()})}catch(q){P.error(`保存配置失败`)}},
  runAll=async()=>{setBusy(`一键`);try{let r=await v.post(`/admin/thumb/generate`,{path:startPath(),recursive:!0});n(r,r=>{P.success(`已加入队列：${r.queued} 个${r.blocked?`（115 风控中，缩略图本地生成，上传将在上传窗口自动进行）`:``}`),setTotalQueued(t=>t+(r.queued||0)),setOpen(!1),load()})}finally{setBusy(``)}},
  genSel=async(force)=>{if(!sel()){P.warning(`请先选择目录`);return}queueGen(sel(),force)},
  retrySel=async()=>{if(!sel()){P.warning(`请先选择目录`);return}try{let r=await v.post(`/admin/thumb/retry_fails`,{path:sel()});n(r,r=>{P.success(`已重试：${r.retried} 个`),load()})}catch(q){}},
  clearSel=async()=>{if(!sel()){P.warning(`请先选择目录`);return}if(!window.confirm(`确认清空该目录下所有缩略图？（${sel()}）`))return;setBusy(sel()+`-c`);try{let r=await v.post(`/admin/thumb/clear`,{path:sel()});n(r,r=>{P.success(`已清空 ${r.removed} 个缩略图${r.remote_skipped?`（115 风控中，远程缩略图待恢复后清理）`:``}`),loadTree(),loadDir(sel()),load()})}catch(q){P.error(`清空失败：`+(q&&q.message||q))}finally{setBusy(``)}},clearAll=async()=>{if(!window.confirm(`确认清空全部缩略图缓存与索引？（已生成 ${st()?st().cached_files:0} 个缩略图将被删除，可重新生成）`))return;setBusy(`全部清空`);try{let r=await v.post(`/admin/thumb/clear_all`,{});n(r,r=>{P.success(`已清空 ${r.removed} 个缩略图缓存`),loadTree(),setSel(``),setSelFiles([]),setSelCount(0),load()})}catch(q){P.error(`清空失败：`+(q&&q.message||q))}finally{setBusy(``)}},
  toggleFile=p=>setChecked(cc=>{let z={};for(let k in cc){z[k]=cc[k]}z[p]=!z[p];return z}),
  excludeUnchecked=async()=>{if(!sel()){P.warning(`请先选择目录`);return}let paths=selFiles().filter(p=>!checked()[p]);if(!paths.length){P.warning(`没有需要排除的视频`);return}try{let r=await v.post(`/admin/thumb/exclude`,{paths,exclude:!0});n(r,r=>{P.success(`已排除 ${paths.length} 个视频`),loadDir(sel())})}catch(q){}},
  restoreExcluded=async()=>{if(!sel()){P.warning(`请先选择目录`);return}let ex=selExcluded();if(!ex.length){P.warning(`没有已排除的视频`);return}try{let r=await v.post(`/admin/thumb/exclude`,{paths:ex,exclude:!1});n(r,r=>{P.success(`已恢复 ${ex.length} 个视频`),loadDir(sel())})}catch(q){}},
  migrate=async()=>{if(!oldP()||!newP()){P.warning(`请填写旧/新路径`);return}let r=await v.post(`/admin/thumb/migrate`,{old_prefix:oldP(),new_prefix:newP()});n(r,r=>{P.success(`已迁移 ${r.migrated} 个缓存文件`),load(),loadTree()})},
  toggle=pp=>setExp(ss=>{let z=new Set(ss);z.has(pp)?z.delete(pp):z.add(pp);return z}),
  selectDir=pp=>{setSel(pp),setExp(ss=>{let z=new Set(ss);z.add(pp);return z}),loadDir(pp)},
  TN=(nn,depth)=>_(j,{w:`$full`,get children(){return[
    _(u,{direction:`row`,spacing:`$1`,alignItems:`center`,p:`$2`,rounded:`$md`,get _hover(){return{bgColor:U(`$neutral2`,`$neutral3`)()}},get background(){return sel()===nn.path?U(`$info2`,`$info3`)():U(`$neutral1`,`$neutral2`)()},style:{paddingLeft:(10+depth*10)+`px`,cursor:`pointer`},onClick:()=>selectDir(nn.path),get children(){return[
      _(m,{size:`xs`,onClick:q=>{q.stopPropagation(),toggle(nn.path)},get children(){return expanded().has(nn.path)?`▾`:`▸`}}),
      _(j,{mr:`$1`,get children(){return`📁`}}),
      _(j,{css:{flex:`1 1 auto`,wordBreak:`break-all`,fontSize:`$sm`},get children(){return nn.name}}),
      _(y,{get colorScheme(){return sel()===nn.path?`info`:`neutral`},get children(){return`${nn.cached}`}}),
      _(V,{get when(){return nn.videos>nn.cached},get children(){return _(y,{colorScheme:`warning`,get children(){return`缺 ${nn.videos-nn.cached}`}})}}),
      _(m,{size:`xs`,get disabled(){return busy()===nn.path},onClick:r=>{r.stopPropagation(),queueGen(nn.path,!1)},get children(){return `生成`}})
    ]}}),
    _(V,{get when(){return expanded().has(nn.path)&&(nn.children||[]).length>0},get children(){return _(T,{get each(){return nn.children||[]},children:q=>TN(q,depth+1)})}})
  ]}});
  load(),loadTree();
  setInterval(()=>{load()},10000);
  return _(j,{spacing:`$3`,alignItems:`start`,w:`$full`,get children(){return[
    _(u,{spacing:`$2`,gap:`$2`,w:`$full`,wrap:{"@initial":`wrap`,"@md":`unset`},get children(){return[
      _(j,{p:`$3`,rounded:`$lg`,border:`1px solid $neutral7`,get background(){return U(`$neutral1`,`$neutral2`)()},get children(){return`缓存 ${st()?st().cached_files:0} 个`}}),
      _(j,{p:`$3`,rounded:`$lg`,border:`1px solid $neutral7`,get background(){return U(`$neutral1`,`$neutral2`)()},get children(){return`队列 ${queued()} 个`}}),
      _(j,{p:`$3`,rounded:`$lg`,border:`1px solid $neutral7`,get background(){return U(`$neutral1`,`$neutral2`)()},get children(){return`失败 ${st()?st().fail_markers:0} 个`}}),
      _(j,{p:`$3`,rounded:`$lg`,border:`1px solid $neutral7`,get background(){return U(`$neutral1`,`$neutral2`)()},get children(){return`占用 ${st()?(st().cache_size/1048576).toFixed(1):`0`} MB`}})
    ]}}),
    _(j,{mt:`$2`,rounded:`$lg`,border:`1px solid $neutral6`,p:`$2`,get children(){return[
      _(u,{spacing:`$2`,alignItems:`center`,wrap:`wrap`,get children(){return[
        _(y,{get colorScheme(){return genBlocked()?`danger`:genActive()>0?`success`:queued()>0?`warning`:`neutral`},get children(){return genBlocked()?`115 风控中，生成已暂停`:genActive()>0?`正在生成中`:queued()>0?`已入队，等待生成`:`空闲`}}),
        _(j,{fontSize:`$sm`,color:`$neutral9`,get children(){return`队列剩余 ${queued()} 个 · 本次已生成 ${Math.min(totalQueued()-queued(),totalQueued())} 个`}})
      ]}}),
      _(Pg,{mt:`$2`,get value(){return totalQueued()>0?Math.min(100,Math.round((totalQueued()-queued())/totalQueued()*100)):0},get max(){return 100},get indeterminate(){return genActive()>0&&totalQueued()===0},get size(){return`sm`}}),
      _(u,{spacing:`$2`,alignItems:`center`,mt:`$2`,wrap:{"@initial":`wrap`,"@md":`unset`},get children(){return[
        _(j,{fontWeight:`$medium`,get children(){return`生成控制`}}),
        _(j,{fontSize:`$sm`,get children(){return`生成强度`}}),
        _(m,{size:`xs`,get colorScheme(){return genPower()===`low`?`accent`:`neutral`},onClick:()=>setGenPower(`low`),get children(){return`低`}}),
        _(m,{size:`xs`,get colorScheme(){return genPower()===`medium`?`accent`:`neutral`},onClick:()=>setGenPower(`medium`),get children(){return`中`}}),
        _(m,{size:`xs`,get colorScheme(){return genPower()===`high`?`accent`:`neutral`},onClick:()=>setGenPower(`high`),get children(){return`高`}}),
        _(j,{fontSize:`$sm`,ml:`$3`,get children(){return`生成并发`}}),
        _(Hx,{get value(){return String(ruWorker())},onInput:e=>setRuWorker(Math.min(8,Math.max(1,parseInt(e.currentTarget.value)||3))),w:`$full`,maxW:`70px`}),
        _(m,{size:`xs`,colorScheme:`accent`,onClick:saveGen,get children(){return`保存生成配置`}})
      ]}})
    ]}}),
    _(j,{mt:`$2`,rounded:`$lg`,border:`1px solid $neutral6`,p:`$2`,get children(){return[
      _(u,{spacing:`$2`,alignItems:`center`,get children(){return[
        _(j,{fontWeight:`$medium`,get children(){return`远程缩略图补传（规避 115 风控）`}}),
        _(y,{get colorScheme(){return pendingUp()?`danger`:`neutral`},get children(){return`待上传 ${pendingUp()} 个`}}),
        _(m,{size:`xs`,get colorScheme(){return ruEnabled()?`success`:`neutral`},onClick:()=>setRuEnabled(!ruEnabled()),get children(){return ruEnabled()?`已启用`:`已停用`}})
      ]}}),
      _(u,{direction:{"@initial":`column`,"@md":`row`},spacing:`$2`,alignItems:`center`,mt:`$2`,get children(){return[
        _(j,{fontSize:`$sm`,get children(){return`上传时段`}}),
        _(Hx,{get value(){return String(ruStart())},onInput:e=>setRuStart(parseInt(e.currentTarget.value)||0),w:`$full`,maxW:`80px`}),
        _(j,{fontSize:`$sm`,get children(){return`-`}}),
        _(Hx,{get value(){return String(ruEnd())},onInput:e=>setRuEnd(parseInt(e.currentTarget.value)||0),w:`$full`,maxW:`80px`}),
        _(j,{fontSize:`$sm`,color:`$neutral9`,get children(){return`（中国时区小时，可跨天）`}}),
        _(j,{fontSize:`$sm`,ml:`$3`,get children(){return`每批`}}),
        _(Hx,{get value(){return String(ruBatch())},onInput:e=>setRuBatch(parseInt(e.currentTarget.value)||1),w:`$full`,maxW:`70px`}),
        _(j,{fontSize:`$sm`,get children(){return`间隔`}}),
        _(Hx,{get value(){return String(ruInterval())},onInput:e=>setRuInterval(parseInt(e.currentTarget.value)||1),w:`$full`,maxW:`70px`}),
        _(j,{fontSize:`$sm`,get children(){return`秒`}}),
        _(m,{size:`xs`,colorScheme:`accent`,onClick:saveRu,get children(){return`保存配置`}})
      ]}})
    ]}}),
    _(u,{spacing:`$2`,alignItems:`center`,wrap:{"@initial":`wrap`,"@md":`unset`},get children(){return[
      _(m,{colorScheme:`accent`,get loading(){return busy()===`一键`},onClick:()=>setOpen(!0),get children(){return`一键缩略图`}}),
      _(m,{colorScheme:`warning`,onClick:retryAll,get children(){return`重试全部失败`}}),_(m,{colorScheme:`danger`,get disabled(){return busy()===`全部清空`},onClick:clearAll,get children(){return`清空全部缩略图`}}),
      _(j,{fontSize:`$sm`,color:`$neutral10`,get children(){return`点击目录查看已有缩略图，可勾选排除不需要缩略图的视频；生成按 5 个一批提交（防风控）`}})
    ]}}),
    _(u,{direction:{"@initial":`column`,"@md":`row`},spacing:`$3`,w:`$full`,alignItems:`flex-start`,get children(){return[
      _(j,{w:{"@initial":`$full`,"@md":`50%`},rounded:`$lg`,border:`1px solid $neutral6`,p:`$1`,get children(){return[
        _(j,{fontWeight:`$medium`,p:`$2`,get children(){return`目录`}}),
        _(V,{get when(){return scanStatus()!==`complete`},get children(){return _(j,{p:`$2`,fontSize:`$sm`,color:`$warning9`,get children(){return`115 网盘受限中，当前仅展示已有缩略图的目录；恢复后自动显示全部`}})}}),
        _(V,{get when(){return treeLoading()},get children(){return _(j,{p:`$2`,fontSize:`$sm`,color:`$neutral9`,get children(){return`加载中...`}})}}),
        _(u,{direction:`column`,w:`$full`,get children(){return _(T,{get each(){return tree()},children:q=>TN(q,0)})}})
      ]}}),
      _(j,{w:{"@initial":`$full`,"@md":`50%`},rounded:`$lg`,border:`1px solid $neutral6`,p:`$2`,get children(){return[
        _(j,{fontWeight:`$medium`,css:{wordBreak:`break-all`},get children(){return sel()?sel():`未选择目录`}}),
        _(u,{spacing:`$1`,mt:`$2`,wrap:`wrap`,get children(){return[
          _(y,{colorScheme:`info`,get children(){return`已有缩略图 ${selCount()} 个`}}),
          _(y,{get colorScheme(){return selExcluded().length?`warning`:`neutral`},get children(){return`已排除 ${selExcluded().length} 个`}}),
          _(m,{size:`xs`,get disabled(){return!sel()},onClick:()=>genSel(!1),get children(){return`生成缺失`}}),
          _(m,{size:`xs`,colorScheme:`accent`,get disabled(){return!sel()},onClick:()=>genSel(!0),get children(){return`重建优化`}}),
          _(m,{size:`xs`,colorScheme:`warning`,get disabled(){return!sel()},onClick:retrySel,get children(){return`重试失败`}}),
          _(m,{size:`xs`,colorScheme:`danger`,get disabled(){return busy()===sel()+`-c`},onClick:clearSel,get children(){return`清空`}})
        ]}}),
        _(u,{spacing:`$2`,alignItems:`center`,mt:`$2`,wrap:`wrap`,get children(){return[
          _(m,{size:`xs`,colorScheme:`warning`,get disabled(){return!sel()},onClick:excludeUnchecked,get children(){return`排除未勾选`}}),
          _(m,{size:`xs`,get disabled(){return!sel()||!selExcluded().length},onClick:restoreExcluded,get children(){return`恢复已排除`}}),
          _(j,{fontSize:`$xs`,color:`$neutral9`,get children(){return`取消勾选 = 不需要缩略图`}})
        ]}}),
        _(j,{mt:`$2`,maxH:`420px`,overflowY:`auto`,rounded:`$md`,border:`1px solid $neutral6`,p:`$1`,get children(){return _(T,{get each(){return selFiles()},children:q=>_(u,{direction:`row`,spacing:`$2`,alignItems:`center`,p:`$2`,rounded:`$sm`,get _hover(){return{bgColor:U(`$neutral2`,`$neutral3`)()}},get children(){return[
          _(m,{size:`xs`,variant:`subtle`,get colorScheme(){return checked()[q]?`success`:`neutral`},onClick:()=>toggleFile(q),get children(){return checked()[q]?`✓`:`○`}}),
          _(j,{css:{flex:`1 1 auto`,wordBreak:`break-all`,fontSize:`$sm`,opacity:checked()[q]?`1`:`0.5`},get children(){return q.replace(sel(),``).replace(/^\//,``)}}),
          _(V,{get when(){return !checked()[q]},get children(){return _(y,{colorScheme:`warning`,get children(){return`已排除`}})}})
        ]}})})}})
      ]}})
    ]}}),
    _(V,{get when(){return stale().length>0},get children(){return _(u,{spacing:`$2`,mt:`$3`,w:`$full`,direction:`column`,get children(){return[
      _(j,{fontWeight:`$medium`,get children(){return`检测到旧挂载路径（存储挂载路径已变更）：`}}),
      _(j,{fontSize:`$sm`,get children(){return stale().map(e=>e.dir+`（`+e.count+`）`).join(`、`)}}),
      _(u,{direction:{"@initial":`column`,"@md":`row`},spacing:`$2`,alignItems:`center`,get children(){return[
        _(j,{get children(){return`旧前缀`}}),
        _(Hx,{get value(){return oldP()},onInput:e=>setOldP(e.currentTarget.value),placeholder:`如 /影视相关`,w:`$full`,maxW:`260px`}),
        _(j,{get children(){return`→ 新前缀`}}),
        _(Hx,{get value(){return newP()},onInput:e=>setNewP(e.currentTarget.value),placeholder:`如 /01_影视相关`,w:`$full`,maxW:`260px`}),
        _(m,{colorScheme:`info`,onClick:migrate,get children(){return`迁移`}})
      ]}})
    ]}})}}),
    _(yt,{get opened(){return open()},onClose:()=>setOpen(!1),size:{"@initial":`xs`,"@md":`md`},get children(){return[
      _(Qe,{}),
      _(dt,{get children(){return[
        _(Je,{get children(){return`一键缩略图`}}),
        _(j,{fontSize:`$sm`,color:`$neutral10`,get children(){return`选择起始目录，递归扫描并生成该目录下缺失的视频缩略图（跳过已有缓存与已排除视频，按 5 个一批提交）`}})
      ]}}),
      _(j,{mt:`$2`,maxH:`300px`,overflowY:`auto`,rounded:`$md`,border:`1px solid $neutral6`,p:`$1`,get children(){return _(T,{get each(){return allDirs()},children:q=>_(j,{p:`$2`,rounded:`$sm`,get _hover(){return{bgColor:U(`$neutral2`,`$neutral3`)()}},get background(){return startPath()===q.path?U(`$info2`,`$info3`)():U(`$neutral1`,`$neutral2`)()},cursor:`pointer`,onClick:()=>setStart(q.path),get children(){return[
          _(j,{css:{flex:`1 1 auto`,wordBreak:`break-all`,fontSize:`$sm`},get children(){return q.path===`/`?`根目录 /（所有挂载）`:`📁 `+q.path}}),
          _(y,{colorScheme:`neutral`,ml:`$2`,get children(){return`${q.cached}`}})
        ]}})})}}),
      _(Nt,{display:`flex`,gap:`$2`,mt:`$3`,justifyContent:`flex-end`,get children(){return[
        _(m,{colorScheme:`neutral`,onClick:()=>setOpen(!1),get children(){return`取消`}}),
        _(m,{colorScheme:`accent`,get loading(){return busy()===`一键`},onClick:runAll,get children(){return`开始生成`}})
      ]}})
    ]}})
  ]}})
};
export{ee as default};
