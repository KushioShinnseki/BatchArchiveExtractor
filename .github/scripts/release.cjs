const fs = require('node:fs');
const path = require('node:path');
const crypto = require('node:crypto');

module.exports = async ({github, context, core}) => {
  const repo = context.repo;
  const sha = process.env.RELEASE_SHA;
  if (!/^[a-f0-9]{40}$/.test(sha || '')) throw new Error('Missing exact release revision');
  const date = new Intl.DateTimeFormat('en-CA', {timeZone:'Asia/Shanghai',year:'numeric',month:'2-digit',day:'2-digit'})
    .formatToParts(new Date()).filter(p => ['year','month','day'].includes(p.type)).map(p => p.value).join('');
  const prefix = `BatchArchiveExtractor_${date}_`;
  const tags = await github.paginate(github.rest.repos.listTags, {...repo,per_page:100});
  const releases = await github.paginate(github.rest.repos.listReleases, {...repo,per_page:100});
  const used = [...tags.map(t=>t.name),...releases.map(r=>r.tag_name)]
    .filter(t=>t.startsWith(prefix)&&/^\d{4}$/.test(t.slice(prefix.length)))
    .map(t=>Number(t.slice(prefix.length)));
  const sequence = Math.max(0,...used)+1;
  if (sequence>9999) throw new Error('Daily release sequence exhausted');
  const tag = prefix+String(sequence).padStart(4,'0');
  const files = ['BatchArchiveExtractor.exe','BatchArchiveExtractor-windows-amd64.zip','SHA256SUMS.txt'];
  const payloads = files.map(name=>({name,data:fs.readFileSync(path.join('release',name))}));
  if (payloads.some(p=>p.data.length===0)) throw new Error('Empty release artifact');
  const release = (await github.rest.repos.createRelease({...repo,tag_name:tag,target_commitish:sha,
    name:tag,draft:true,body:`Go Windows x64 批量解压助手。\n\n源码提交：${sha}\n\n已通过格式、静态检查、单元测试、原生界面初始化和打包校验。下载 ZIP 解压运行，或直接使用单个 EXE。校验值见 SHA256SUMS.txt。\n\n成功解压并校验后自动删除源包；重名与异常保留并写入报告；分卷暂不处理。`})).data;
  // Publish only after every uploaded asset has been checked. Failures leave a draft for inspection.
  for (const {name,data} of payloads) {
    const asset = (await github.rest.repos.uploadReleaseAsset({...repo,release_id:release.id,name,data,
      headers:{'content-type':'application/octet-stream','content-length':data.length}})).data;
    const hash = 'sha256:'+crypto.createHash('sha256').update(data).digest('hex');
    if (asset.size!==data.length || (asset.digest && asset.digest!==hash)) throw new Error(`Uploaded asset verification failed: ${name}`);
  }
  const published = (await github.rest.repos.updateRelease({...repo,release_id:release.id,draft:false,make_latest:'true'})).data;
  core.notice(`Published ${published.html_url} at ${sha}`);
  await core.summary.addHeading('Windows release').addLink(tag,published.html_url).addRaw(`\n\nRevision: ${sha}\n`).write();
};
