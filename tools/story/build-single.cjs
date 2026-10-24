const fs=require('node:fs');
const path=require('node:path');
const root=__dirname,dist=path.join(root,'dist');
let html=fs.readFileSync(path.join(dist,'index.html'),'utf8');
html=html.replace('<link rel="stylesheet" href="story.css">',()=>'<style>\n'+fs.readFileSync(path.join(dist,'story.css'),'utf8')+'\n</style>');
html=html.replace('<link rel="stylesheet" href="upgrade.css">',()=>'<style>\n'+fs.readFileSync(path.join(dist,'upgrade.css'),'utf8')+'\n</style>');
for(const file of ['replay-data.js','diagrams.js','story.js']){
 const source=fs.readFileSync(path.join(dist,file),'utf8').replace(/<\/script/gi,'<\\/script');
 html=html.replace(`<script src="${file}" defer></script>`,()=>`<script defer>\n${source}\n</script>`);
}
// Inline scripts execute immediately; place them after the DOM instead of in head.
const scripts=[];html=html.replace(/<script defer>[\s\S]*?<\/script>/g,s=>{scripts.push(s.replace('<script defer>','<script>'));return '';});
html=html.replace('</body>',()=>scripts.join('\n')+'\n</body>');
const target=path.join(root,'ONpoint-交互路演.html');fs.writeFileSync(target,html,'utf8');
console.log(target);
