import pptxgen from "pptxgenjs";

const markSvg = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 40 40"><g fill="#8fc7a2"><path d="M7 34V17C7 9.82 12.82 4 20 4h2c7.18 0 13 5.82 13 13s-5.82 13-13 13h-5v-6h5a7 7 0 1 0 0-14h-2a7 7 0 0 0-7 7v17H7Z"/><path d="M17 34V17a3 3 0 0 1 3-3h2a3 3 0 1 1 0 6h-1v14h-4Z"/></g></svg>`;
const markData = `data:image/svg+xml;base64,${Buffer.from(markSvg).toString("base64")}`;
const pptx = new pptxgen();
pptx.layout = "LAYOUT_WIDE";
pptx.author = "Pgfy";
pptx.subject = "Pgfy pitch deck";
pptx.title = "Pgfy — every project’s database on one server you own";
pptx.company = "Pgfy";
pptx.lang = "en-US";
pptx.theme = {
  headFontFace: "Inter", bodyFontFace: "Inter", lang: "en-US"
};
pptx.defineSlideMaster({
  title: "PGFY",
  background: { color: "111412" },
  objects: [
    { image: { data: markData, x: .68, y: .29, w: .28, h: .28 } },
    { text: { text: "pgfy", options: { x: .96, y: .32, w: .5, h: .25, fontFace: "Inter", fontSize: 12, bold: true, color: "ECEFE9", margin: 0 } } },
    { text: { text: ".", options: { x: 1.335, y: .32, w: .12, h: .25, fontFace: "Inter", fontSize: 12, bold: true, color: "8FC7A2", margin: 0 } } }
  ],
  slideNumber: { x: 12.24, y: 7.16, w: .35, h: .15, color: "737C74", fontFace: "JetBrains Mono", fontSize: 7, align: "right", margin: 0 }
});

const C={bg:"111412",surface:"191E1B",raised:"202622",border:"2A2F2C",strong:"3A403C",text:"ECEFE9",muted:"A2AAA0",faint:"737C74",accent:"8FC7A2",accentSoft:"202E25",warn:"E6B866",warnSoft:"30291D",bad:"EE8077",code:"0F1210",codeFg:"D9E6DC"};
const S=pptx.ShapeType;
const addText=(s,t,x,y,w,h,o={})=>s.addText(t,{x,y,w,h,fontFace:o.mono?"JetBrains Mono":"Inter",fontSize:o.size??20,color:o.color??C.text,bold:o.bold??false,breakLine:o.breakLine,margin:o.margin??0,valign:o.valign??"mid",align:o.align??"left",fit:"shrink",...o});
const box=(s,x,y,w,h,fill=C.surface,line=C.border,r=.08)=>s.addShape(S.roundRect,{x,y,w,h,rectRadius:r,fill:{color:fill},line:{color:line,width:1}});
const title=(s,t)=>addText(s,t,.82,.72,11.7,.52,{size:29,bold:true,breakLine:false,margin:0});
const pill=(s,t,x,y,w,color,fill)=>{s.addShape(S.roundRect,{x,y,w,h:.29,rectRadius:.15,fill:{color:fill},line:{color:fill}});addText(s,t,x,y,w,.29,{size:10,bold:true,color,align:"center"})};
const footer=(s,t)=>addText(s,t,.82,6.84,11.1,.26,{size:11.5,color:C.muted});

// 1 — Hook (PowerPoint fallback shows the completed build)
{
 const s=pptx.addSlide("PGFY");
 addText(s,"50 databases\n$12 / month\nrestores you can verify",.82,1.75,11.5,2.85,{size:56,bold:true,breakLine:false});
 addText(s,"on AWS · Lightsail + S3",.82,4.72,6,.35,{size:17,color:C.muted});
}

// 2 — Story
{
 const s=pptx.addSlide("PGFY"); title(s,"Why I built it");
 const colW=[3.45,4.55,3.45],gap=.19;let x=.82;
 // 01 · The bill
 box(s,x,1.48,colW[0],4.3);addText(s,"01 · THE BILL",x+.24,1.68,3,.24,{size:8.5,bold:true,color:C.muted,charSpacing:1.1});addText(s,"“Hosting is on you.”",x+.24,1.98,3.0,.42,{size:19,bold:true});
 addText(s,[{text:"A client wouldn’t pay for infrastructure. One managed Postgres was "},{text:"$20 a month — $240 a year",options:{bold:true}},{text:" — on top of AI and VPS bills, for an app with a tiny audience. Multiply by every small project an agency runs."}],x+.24,2.48,2.98,2.6,{size:13.5,valign:"top",breakLine:false});
 x+=colW[0]+gap;
 // 02 · What I tried
 box(s,x,1.48,colW[1],4.3);addText(s,"02 · WHAT I TRIED",x+.24,1.68,3,.24,{size:8.5,bold:true,color:C.muted,charSpacing:1.1});
 const tried=[["Dokploy","One-click self-hosting, but its databases weren’t managed the way I needed."],["RDS","Documents many databases per instance — then leaves every role, password and backup to you."],["Neon","Workers every 10 s keep it awake — $19.34 per project, always on.\n0.25 CU × 730 h × $0.106"]];
 tried.forEach((t,k)=>{const y=2.05+k*1.2;if(k)s.addShape(S.line,{x:x+.24,y:y-.1,w:colW[1]-.48,h:0,line:{color:C.border}});addText(s,t[0],x+.24,y,.95,.4,{size:13,bold:true,color:C.accent,valign:"top"});addText(s,t[1],x+1.2,y,colW[1]-1.45,1.0,{size:12.5,valign:"top",breakLine:false})});
 x+=colW[1]+gap;
 // 03 · What I wanted
 box(s,x,1.48,colW[2],4.3,C.accentSoft,"2F4A3A");addText(s,"03 · WHAT I WANTED",x+.24,1.68,3,.24,{size:8.5,bold:true,color:C.accent,charSpacing:1.1});addText(s,"One server I own. A database per project in one click. Backups that prove they restore.",x+.24,1.98,2.98,2.2,{size:19,bold:true,valign:"top",breakLine:false});
 footer(s,"In the AI era we all ship niche apps for small audiences: many small databases, none of them big. That is what Pgfy is for.");
}

// 3 — Architecture
{
 const s=pptx.addSlide("PGFY");title(s,"Built on AWS");
 box(s,.82,1.45,5.1,3.75);addText(s,"Lightsail · Ubuntu 24.04 · $12",1.02,1.62,4.7,.3,{size:15,bold:true});
 [["Caddy (HTTPS, Let’s Encrypt)",2.08,.55],["pgfy — Go binary + embedded React dashboard · SQLite state",2.85,.72],["PostgreSQL 18 · one role + database per project · TLS-only · pg_hba allowlists",3.79,.92]].forEach(([t,y,h],i)=>{box(s,1.05,y,4.65,h,C.raised,C.strong);addText(s,t,1.22,y+.08,4.31,h-.16,{size:12.3,align:"center"});if(i<2)addText(s,"↓",3.18,y+h, .4,.22,{size:14,color:C.faint,align:"center"})});
 addText(s,"daily pg_dump\n--format=custom",6.1,2.35,1.65,.55,{size:10.5,color:C.muted,mono:true,align:"center"});addText(s,"→",6.45,2.85,.95,.45,{size:34,color:C.accent,align:"center"});
 box(s,7.75,2.08,3.25,1.28);addText(s,"S3 bucket · SSE-S3",8.0,2.32,2.75,.32,{size:17,bold:true});addText(s,"archive + manifest + SHA-256",8.0,2.71,2.75,.3,{size:12,color:C.muted});
 box(s,6.15,4.1,2.2,.9);addText(s,"IAM user",6.35,4.24,1.8,.25,{size:14,bold:true});addText(s,"bucket-only policy",6.35,4.54,1.8,.22,{size:11,color:C.muted});addText(s,"→",8.4,4.29,.75,.35,{size:29,color:C.accent,align:"center"});
 box(s,9.35,4.36,2.3,.83,"151916",C.border);addText(s,"RDS (exit path)",9.57,4.48,1.9,.24,{size:13,bold:true,color:C.muted});addText(s,"pg_restore",9.57,4.75,1.9,.2,{size:10.5,color:C.faint,mono:true});s.addShape(S.line,{x:10.55,y:3.38,w:.03,h:.94,line:{color:C.faint,width:1.5,dash:"dash",beginArrowType:"none",endArrowType:"triangle"}});
 footer(s,"Docker Compose · pinned image digests · installs with one command in about a minute");
}

// 4 — Scale & limits
{
 const s=pptx.addSlide("PGFY");title(s,"How far $12 goes — and how it grows");
 const stats=[["DATABASES ON ONE 2 GB BOX","50"],["IDLE APP CONNECTIONS","100 · 490 MiB in use"],["10 DATABASES LOADED","943 tx/s · 8,866 reads/s"],["ALL 50 WRITING AT ONCE","962 tx/s"]];
 stats.forEach((a,i)=>{const x=.82+(i%2)*2.9,y=1.48+Math.floor(i/2)*1.5;box(s,x,y,2.68,1.28);addText(s,a[0],x+.19,y+.15,2.3,.3,{size:8.5,bold:true,color:C.muted,charSpacing:1.1});addText(s,a[1],x+.19,y+.54,2.3,.5,{size:i===1||i===2?18:24,bold:true})});
 pill(s,"daily backups, verified",.82,4.65,1.75,C.accent,C.accentSoft);pill(s,"150 pooled connections",2.7,4.65,1.75,C.accent,C.accentSoft);pill(s,"your box, your data",4.58,4.65,1.5,C.accent,C.accentSoft);
 box(s,6.82,1.48,5.68,5.0);addText(s,"Grows with you",7.08,1.66,5.15,.34,{size:18,bold:true});
 const rungs=[["1","Many small apps on one Pgfy box","$12 · 2 GB"],["2","One app takes off: its own Pgfy box, restored in under 5 min","$24 · 4 GB\n$44 · 8 GB"],["3","Want managed HA and PITR? pg_restore the archive into RDS","$116 · 8 GB\ndb.m6g.large"]];
 rungs.forEach((r,i)=>{const y=2.06+i*.5;box(s,7.02,y,5.28,.44,C.raised,C.border);s.addShape(S.ellipse,{x:7.12,y:y+.09,w:.26,h:.26,fill:{color:C.accentSoft},line:{color:C.accentSoft}});addText(s,r[0],7.12,y+.09,.26,.26,{size:9.5,bold:true,color:C.accent,align:"center"});addText(s,r[1],7.46,y+.02,3.4,.4,{size:10.2,color:C.text});addText(s,r[2],10.85,y+.02,1.38,.4,{size:8.8,bold:true,mono:true,align:"right",color:C.accent})});
 addText(s,"50 LOW-TRAFFIC DATABASES / MONTH",7.08,3.62,4.9,.24,{size:9,bold:true,color:C.muted,charSpacing:1});
 const rows=[["RDS, one instance per project","$699"],["RDS, one shared db.t4g.small","$25.66"],["Lightsail managed PostgreSQL 2 GB","$30"],["Pgfy on Lightsail + S3","$12.23"]];rows.forEach((r,i)=>{const y=3.92+i*.55;if(i===3)box(s,7.02,y-.02,5.25,.5,C.accentSoft,C.accentSoft);else s.addShape(S.line,{x:7.08,y:y-.03,w:5.07,h:0,line:{color:C.border}});addText(s,r[0],7.15,y,3.85,.42,{size:11.5,color:i===3?C.accent:C.text});addText(s,r[1],11.0,y,1.05,.42,{size:13,bold:true,mono:true,align:"right",color:i===3?C.accent:C.text})});addText(s,"Single-AZ · on-demand · us-east-1 · Sep 2026 · 90 GB of backups in S3 ≈ $2",7.08,6.14,5.03,.3,{size:8.6,color:C.faint});
}

// 5 — Learning
{
 const s=pptx.addSlide("PGFY");title(s,"What I learned");box(s,.82,1.75,11.7,2.45,C.code,C.border);addText(s,"✗ backup to S3 failed — app image had no CA bundle",1.18,2.05,11,.43,{size:19,mono:true,color:C.bad});addText(s,"→ minimal images hide what’s missing",1.18,2.63,11,.43,{size:19,mono:true,color:C.muted});addText(s,"✓ release pipeline now runs a real S3 check before publishing",1.18,3.21,11,.43,{size:19,mono:true,color:C.accent});addText(s,"“Works on my machine” is a statement about the machine.",.82,4.72,11.6,.72,{size:29,bold:false});
}

// 6 — What's next
{
 const s=pptx.addSlide("PGFY");title(s,"What’s next");const items=[["gate","Production hardening gate before real workloads"],["planned","Delete a project — final backup first, then role and database"],["planned","Backup retention cleanup in the bucket"],["planned","PgBouncer — hundreds of app connections on one box"],["planned","Alerts when a backup or certificate renewal fails"],["planned","One-click updates"],["planned","Team permissions and a SQL editor"],["planned","IPv6 and DNS-01 certificate issuance"]];items.forEach((it,i)=>{const x=.82+(i%2)*6.02,y=1.45+Math.floor(i/2)*1.12;box(s,x,y,5.72,.92);pill(s,it[0],x+.18,y+.315,.82,it[0]==="gate"?C.accent:C.warn,it[0]==="gate"?C.accentSoft:C.warnSoft);addText(s,it[1],x+1.15,y+.12,4.32,.67,{size:12.2,valign:"mid"})});footer(s,"Roadmap, straight from the README’s planned list.");
}

// 7 — Close
{
 const s=pptx.addSlide("PGFY");addText(s,[{text:"pgfy",options:{color:C.text}},{text:".",options:{color:C.accent}}],.82,1.42,5.5,.9,{size:65,bold:true});addText(s,"Every project’s database on one server you own.\nBackups that prove they restore.",.82,2.5,11.3,1.3,{size:31,bold:true});addText(s,"Open source · Go + React · PostgreSQL 18 · one-command install on Lightsail · backups to S3",.82,3.92,11.3,.4,{size:14,color:C.muted});addText(s,"github.com/c-varun14/Pgfy\nfirstcommit.webbywasp.com",.82,4.65,5.4,.72,{size:14,mono:true,color:C.muted});pill(s,"Lightsail",.82,5.72,1.05,C.text,C.surface);pill(s,"S3",2.02,5.72,.68,C.text,C.surface);pill(s,"IAM",2.84,5.72,.75,C.text,C.surface);
}

await pptx.writeFile({ fileName: "pgfy-pitch.pptx" });
console.log("Created pgfy-pitch.pptx");
