const test=require('node:test');
const assert=require('node:assert/strict');
const {parseTag,plan,execute}=require('./cleanup.cjs');
const tags=names=>names.map(name=>({name}));
test('retains highest sequence separately for every date',()=>{
  const p=plan(tags(['BatchArchiveExtractor_20261114_0001','BatchArchiveExtractor_20261114_0002','BatchArchiveExtractor_20261113_0001']),[]);
  assert.deepEqual(p.targets.map(t=>t.name),['BatchArchiveExtractor_20261114_0001']); assert.equal(p.retained.length,2);
});
test('inclusive range, invalid dates and inverted ranges',()=>{
  const list=tags(['BatchArchiveExtractor_20261113_0001','BatchArchiveExtractor_20261114_0001','BatchArchiveExtractor_20261115_0001']);
  assert.equal(plan(list,[],'range','2026-11-13','2026-11-14').targets.length,2);
  assert.throws(()=>plan(list,[],'range','2026-11-15','2026-11-14'));
  assert.throws(()=>plan(list,[],'range','2026-02-30','2026-11-14'));
});
test('known legacy formats; ambiguous and other projects left untouched',()=>{
  for (const value of ['20261114','2026-11-14','20261114153000','20261114_153000','2026-11-14T15:30:00Z']) assert.ok(parseTag(value),value);
  for (const value of ['v1.0','1700000000','OtherProject_20261114_0001','20261114253000','20260230']) assert.equal(parseTag(value),null,value);
  const p=plan(tags(['20261114_130000','20261114_140000','v1.0']),[]); assert.equal(p.targets[0].name,'20261114_130000'); assert.deepEqual(p.skipped,['v1.0']);
});
test('release association and tags without releases',()=>{
  const p=plan(tags(['BatchArchiveExtractor_20261114_0001','BatchArchiveExtractor_20261114_0002']),[{id:10,tag_name:'BatchArchiveExtractor_20261114_0001'}]);
  assert.equal(p.targets[0].release.id,10); assert.equal(p.targets[0].tagExists,true);
});
test('failed release deletion never deletes its tag; plan exposed before deleting',async()=>{
  const events=[]; const a='BatchArchiveExtractor_20261114_0001',b='BatchArchiveExtractor_20261114_0002';
  const listTags=()=>{},listReleases=()=>{};
  const github={paginate:async fn=>fn===listTags?tags([a,b]):[{id:10,tag_name:a}],rest:{repos:{listTags,listReleases,deleteRelease:async()=>{events.push('release');throw Error('denied')}},git:{deleteRef:async()=>events.push('tag')}}};
  const summary={addHeading(){return this},addCodeBlock(){return this},async write(){events.push('plan')}};
  const core={info(){},error(){},warning(){},summary};
  await assert.rejects(execute({github,context:{repo:{owner:'o',repo:'r'}},core}),/denied/);
  assert.deepEqual(events,['plan','release']);
});
