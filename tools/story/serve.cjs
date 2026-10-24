const http=require('node:http');
const fs=require('node:fs');
const path=require('node:path');
const root=path.resolve(__dirname,'dist');
const port=Number(process.env.PORT||8108);
const types={'.html':'text/html; charset=utf-8','.css':'text/css; charset=utf-8','.js':'text/javascript; charset=utf-8','.json':'application/json; charset=utf-8'};
http.createServer((req,res)=>{
  let url;try{url=decodeURIComponent(new URL(req.url,'http://localhost').pathname);}catch{res.writeHead(400);return res.end('Bad request');}
  const target=path.resolve(root,'.'+(url==='/'?'/index.html':url));
  if(!target.startsWith(root+path.sep)){res.writeHead(403);return res.end('Forbidden');}
  fs.readFile(target,(err,body)=>{if(err){res.writeHead(404);return res.end('Not found');}res.writeHead(200,{'Content-Type':types[path.extname(target)]||'application/octet-stream','Cache-Control':'no-store'});res.end(body);});
}).listen(port,'127.0.0.1',()=>console.log(`ONpoint story preview: http://127.0.0.1:${port}`));
