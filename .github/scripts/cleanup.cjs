function validDate(value) {
  if (!/^\d{4}-\d{2}-\d{2}$/.test(value || '')) throw new Error('日期必须为 YYYY-MM-DD');
  const date = new Date(value+'T00:00:00Z');
  if (!Number.isFinite(date.getTime()) || date.toISOString().slice(0,10)!==value) throw new Error('无效日期');
  return value;
}
function parseTag(name) {
  let m = /^(BatchArchiveExtractor)_(\d{4})(\d{2})(\d{2})_(\d{4})$/.exec(name);
  if (m) {
    try { return {name,group:m[1],date:validDate(`${m[2]}-${m[3]}-${m[4]}`),rank:m[5]}; } catch { return null; }
  }
  // Only date/timestamp-only legacy names with an unambiguous calendar layout are accepted.
  m = /^(\d{4})-?(\d{2})-?(\d{2})(?:[_T-]?(\d{2})[:-]?(\d{2})[:-]?(\d{2})Z?)?$/.exec(name);
  if (!m) return null;
  if (m[4] && (+m[4]>23||+m[5]>59||+m[6]>59)) return null;
  try { return {name,group:'legacy-date-only',date:validDate(`${m[1]}-${m[2]}-${m[3]}`),rank:(m[4]||'00')+(m[5]||'00')+(m[6]||'00')}; } catch { return null; }
}
function plan(tags,releases,mode='retain',start='',end='') {
  if (!['retain','range'].includes(mode)) throw new Error('未知清理模式');
  if (mode==='range') { validDate(start); validDate(end); if (start>end) throw new Error('开始日期不能晚于结束日期'); }
  const names = [...new Set([...tags.map(t=>t.name),...releases.map(r=>r.tag_name)])].sort();
  const items=[],skipped=[],targets=[],retained=[];
  for (const name of names) {
    const item=parseTag(name);
    const matches=releases.filter(r=>r.tag_name===name);
    if (!item || matches.length>1) { skipped.push(name); continue; }
    items.push({...item,release:matches[0]||null,tagExists:tags.some(t=>t.name===name)});
  }
  for (const item of items) {
    const remove = mode==='range' ? item.date>=start&&item.date<=end :
      items.some(other=>other.group===item.group&&other.date===item.date&&other.rank>item.rank);
    (remove?targets:retained).push(item);
  }
  return {mode,start,end,targets,retained,skipped};
}
async function execute({github,context,core}) {
  const repo=context.repo;
  const tags=await github.paginate(github.rest.repos.listTags,{...repo,per_page:100});
  const releases=await github.paginate(github.rest.repos.listReleases,{...repo,per_page:100});
  const p=plan(tags,releases,process.env.CLEANUP_MODE||'retain',process.env.START_DATE||'',process.env.END_DATE||'');
  const report=JSON.stringify(p,null,2);
  core.info('Cleanup plan (Asia/Shanghai), before any deletion:\n'+report);
  await core.summary.addHeading('Manual cleanup plan').addCodeBlock(report,'json').write();
  const failures=[];
  for (const item of p.targets) {
    try {
      // Never delete a corresponding tag if its release deletion failed.
      if (item.release) await github.rest.repos.deleteRelease({...repo,release_id:item.release.id});
      if (item.tagExists) await github.rest.git.deleteRef({...repo,ref:'tags/'+item.name});
      core.info('Deleted: '+item.name);
    } catch (error) { failures.push(item.name+': '+error.message); core.error('Failed: '+item.name+' '+error.message); }
  }
  if (p.skipped.length) core.warning('Unrecognized or ambiguous tags left untouched: '+p.skipped.join(', '));
  if (failures.length) throw new Error(failures.join('\n'));
}
module.exports={validDate,parseTag,plan,execute};
