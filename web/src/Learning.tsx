import {useEffect,useState} from 'react';
import {api,type Identity} from './api';
import Account from './Account';

type Teacher={id:number;email:string;display_name:string;country:string;timezone:string;bio:string;active:boolean};
type Lesson={id:number;student_id:number;teacher_id:number;student_name:string;teacher_name:string;scheduled_start_at:string;scheduled_end_at:string;duration_minutes:number;status:string;teaching_seconds:number;cancel_reason:string;actual_start_at:string|null;actual_end_at:string|null};
type Stats={completed_lessons:number;teaching_seconds:number;student_no_show:number;teacher_no_show:number;cancelled:number};
type Profile={display_name:string;avatar:string;grade:number;timezone:string;parent_name:string;parent_email:string};
const statusName:Record<string,string>={scheduled:'待上课',in_progress:'上课中',completed:'已完成',teacher_no_show:'老师未参加',student_no_show:'未参加',cancelled:'已取消',expired:'已过期'};
const safeZone=(zone?:string)=>{try{new Intl.DateTimeFormat('en-US',{timeZone:zone||'Asia/Shanghai'});return zone||'Asia/Shanghai'}catch{return 'Asia/Shanghai'}};
const fmt=(value:string,zone?:string)=>new Intl.DateTimeFormat('zh-CN',{timeZone:safeZone(zone),year:'numeric',month:'2-digit',day:'2-digit',hour:'2-digit',minute:'2-digit',hour12:false}).format(new Date(value));
const localDate=(zone:string)=>new Intl.DateTimeFormat('en-CA',{timeZone:safeZone(zone),year:'numeric',month:'2-digit',day:'2-digit'}).format(new Date());
function navLink(label:string,url:string){return <a href={url} className={location.pathname===url?'active':''}>{label}</a>}
function Frame({teacher,children,email}:{teacher:boolean;children:React.ReactNode;email:string}){return <main className="learning-shell"><aside><a className="learning-logo" href="/">小小点读家 <small>{teacher?'外教工作台':'学习中心'}</small></a><nav>{teacher?<>{navLink('首页','/teacher')}{navLink('我的课程','/teacher/lessons')}{navLink('我的课时','/teacher/statistics')}{navLink('我的资料','/teacher/profile')}</>:<>{navLink('我的首页','/account')}{navLink('我的课程','/account/lessons')}{navLink('预约课程','/account/booking')}{navLink('我的书架','/')}{navLink('我的资料','/account/profile')}</>}</nav><small>{email}</small><button onClick={async()=>{await api(`${teacher?'/teacher':''}/auth/logout`,{method:'POST'});location.assign(teacher?'/teacher/login':'/account?mode=login')}}>退出登录</button></aside><section className="learning-main">{children}</section></main>}

export function StudentArea(){
 const [me,setMe]=useState<Identity|null|undefined>(undefined);
 useEffect(()=>{api<{user:Identity|null}>('/me').then(v=>setMe(v.user)).catch(()=>setMe(null))},[]);
 if(me===undefined)return <main className="admin-loading">正在加载学习中心…</main>;
 if(!me||new URLSearchParams(location.search).has('mode'))return <Account/>;
 const path=location.pathname;
 return <Frame teacher={false} email={me.email}>{path==='/account/booking'?<Booking/>:path==='/account/lessons'?<StudentLessons/>:path==='/account/profile'?<StudentProfile/>:<StudentDashboard/>}</Frame>;
}

function StudentDashboard(){
 const [data,setData]=useState<{profile:Profile;next_lesson:Lesson|null;month_stats:Stats}|null>(null),[error,setError]=useState('');
 useEffect(()=>{api<typeof data>('/student/dashboard').then(setData).catch(e=>setError(e.message))},[]);
 if(error)return <p className="admin-error">{error}</p>;
 if(!data)return <p>正在加载…</p>;
 const next=data.next_lesson,minutes=Math.round((data.month_stats.teaching_seconds||0)/60);
 return <><h1>你好，{data.profile.display_name||'同学'}</h1><p>准备好今天的英语学习了吗？</p><div className="learning-cards"><article><small>下一节外教课</small>{next?<><h2>{fmt(next.scheduled_start_at,data.profile.timezone)}</h2><p>{next.teacher_name||`外教 #${next.teacher_id}`} · {next.duration_minutes} 分钟</p><a className="admin-primary" href={`/classroom/${next.id}/check`}>准备上课</a></>:<><h2>还没有待上的课程</h2><a className="admin-primary" href="/account/booking">预约课程</a></>}</article><article><small>本月已完成</small><h2>{data.month_stats.completed_lessons} 节</h2></article><article><small>学习时间</small><h2>{minutes} 分钟</h2></article></div></>;
}

function StudentLessons(){
 const [rows,setRows]=useState<Lesson[]>([]),[error,setError]=useState('');
 useEffect(()=>{api<Lesson[]|null>('/lessons').then(v=>setRows(v??[])).catch(e=>setError(e.message))},[]);
 return <><h1>我的课程</h1>{error&&<p className="admin-error">{error}</p>}{rows.length===0?<p>还没有课程，先预约一节外教课吧。</p>:<div className="lesson-list">{rows.map(l=><article key={l.id}><strong>{fmt(l.scheduled_start_at)}</strong><span>{l.teacher_name||`外教 #${l.teacher_id}`} · {l.duration_minutes} 分钟</span><span>{statusName[l.status]||l.status}</span>{l.status==='completed'&&<small>实际共同在线 {Math.round(l.teaching_seconds/60)} 分钟</small>}{l.status==='cancelled'&&l.cancel_reason&&<small>取消原因：{l.cancel_reason}</small>}{['scheduled','in_progress'].includes(l.status)&&<a href={`/classroom/${l.id}/check`}>设备检测 / 进入课堂 →</a>}</article>)}</div>}</>;
}

function Booking(){
 const [teachers,setTeachers]=useState<Teacher[]>([]),[teacher,setTeacher]=useState(0),[zone,setZone]=useState('Asia/Shanghai'),[day,setDay]=useState(()=>localDate('Asia/Shanghai')),[slots,setSlots]=useState<{start_at:string;end_at:string;available:boolean}[]>([]),[busy,setBusy]=useState(false),[error,setError]=useState(''),[notice,setNotice]=useState('');
 useEffect(()=>{api<Profile>('/student/profile').then(p=>{const z=p.timezone||'Asia/Shanghai';setZone(z);setDay(localDate(z))}).catch(e=>setError(e.message))},[]);
 useEffect(()=>{api<Teacher[]|null>('/teachers').then(value=>{const rows=value??[];setTeachers(rows);if(rows.length)setTeacher(rows[0].id)}).catch(e=>setError(e.message))},[]);
 useEffect(()=>{if(!teacher)return;setError('');api<{slots:typeof slots}>(`/teachers/${teacher}/availability?date=${day}`).then(v=>setSlots(v.slots||[])).catch(e=>setError(e.message))},[teacher,day]);
 async function book(slot:typeof slots[number]){
   const t=teachers.find(x=>x.id===teacher);
   if(!window.confirm(`确认预约 ${t?.display_name||'外教'} 的 ${fmt(slot.start_at,zone)} 课程？`))return;
   setBusy(true);setError('');try{await api('/bookings',{method:'POST',body:JSON.stringify({teacher_id:teacher,start_at:slot.start_at,duration_minutes:30,idempotency_key:crypto.randomUUID()})});setNotice('预约成功，课程已加入“我的课程”。');const v=await api<{slots:typeof slots}>(`/teachers/${teacher}/availability?date=${day}`);setSlots(v.slots||[])}catch(e){setError((e as Error).message)}finally{setBusy(false)}
 }
 return <><h1>预约外教课</h1><p>以下时间按 {zone} 显示。</p>{error&&<p className="admin-error">{error}</p>}{notice&&<p className="admin-success">{notice}</p>}<div className="booking-controls"><label>外教<select value={teacher} onChange={e=>setTeacher(Number(e.target.value))}>{teachers.map(t=><option key={t.id} value={t.id}>{t.display_name} · {t.country}</option>)}</select></label><label>日期<input type="date" value={day} onChange={e=>setDay(e.target.value)}/></label></div><div className="slot-grid">{slots.filter(s=>s.available).map(s=><button key={s.start_at} disabled={busy} onClick={()=>void book(s)}>{new Intl.DateTimeFormat('zh-CN',{timeZone:zone,hour:'2-digit',minute:'2-digit',timeZoneName:'shortOffset'}).format(new Date(s.start_at))}</button>)}{slots.every(s=>!s.available)&&<p>这一天没有可约时段。</p>}</div></>;
}

function StudentProfile(){
 const [p,setP]=useState<Profile|null>(null),[error,setError]=useState(''),[message,setMessage]=useState('');
 useEffect(()=>{api<Profile>('/student/profile').then(setP).catch(e=>setError(e.message))},[]);
 async function save(e:React.FormEvent){e.preventDefault();if(!p)return;try{setP(await api<Profile>('/student/profile',{method:'PATCH',body:JSON.stringify(p)}));setMessage('资料已保存')}catch(err){setError((err as Error).message)}}
 return <><h1>我的资料</h1>{error&&<p className="admin-error">{error}</p>}{message&&<p className="admin-success">{message}</p>}{p&&<form className="learning-form" onSubmit={save}><label>姓名<input value={p.display_name||''} onChange={e=>setP({...p,display_name:e.target.value})}/></label><label>年级<input type="number" min="0" max="12" value={p.grade||0} onChange={e=>setP({...p,grade:Number(e.target.value)})}/></label><label>时区<input value={p.timezone||''} onChange={e=>setP({...p,timezone:e.target.value})} placeholder="Asia/Shanghai"/></label><label>家长姓名<input value={p.parent_name||''} onChange={e=>setP({...p,parent_name:e.target.value})}/></label><label>家长邮箱<input type="email" value={p.parent_email||''} onChange={e=>setP({...p,parent_email:e.target.value})}/></label><button className="admin-primary">保存资料</button></form>}</>;
}

export function TeacherLogin(){
 const params=new URLSearchParams(location.search),[mode,setMode]=useState(params.get('mode')||'login'),[email,setEmail]=useState(''),[password,setPassword]=useState(''),[error,setError]=useState(''),[message,setMessage]=useState(''),[busy,setBusy]=useState(false);
 async function submit(e:React.FormEvent){e.preventDefault();setBusy(true);setError('');try{const out=await api<{message?:string}>(`/teacher/auth/${mode}`,{method:'POST',body:JSON.stringify({email,password,token:params.get('token')||''})});if(mode==='login'){location.assign('/teacher');return}setMessage(out.message||'操作成功');if(mode==='reset')setMode('login')}catch(e){setError((e as Error).message)}finally{setBusy(false)}}
 return <main className="account-shell"><a className="admin-brand" href="/">小小点读家 <span>外教工作台</span></a><section className="account-card"><h1>{mode==='login'?'外教登录':mode==='forgot'?'找回密码':'设置密码'}</h1>{error&&<p className="admin-error">{error}</p>}{message&&<p className="admin-success">{message}</p>}<form onSubmit={submit}>{mode!=='reset'&&<label>邮箱<input type="email" required value={email} onChange={e=>setEmail(e.target.value)}/></label>}{mode!=='forgot'&&<label>密码<input type="password" required minLength={mode==='reset'?10:undefined} value={password} onChange={e=>setPassword(e.target.value)}/></label>}<button className="admin-primary" disabled={busy}>{busy?'处理中…':'确认'}</button></form><button className="link-button" onClick={()=>{setMode(mode==='login'?'forgot':'login');setError('')}}>{mode==='login'?'找回密码':'返回登录'}</button></section></main>;
}

export function TeacherArea(){
 const [me,setMe]=useState<Identity|null|undefined>(undefined);
 useEffect(()=>{api<{user:Identity|null}>('/teacher/me').then(v=>setMe(v.user)).catch(()=>setMe(null))},[]);
 if(me===undefined)return <main className="admin-loading">正在加载外教工作台…</main>;
 if(!me)return <TeacherLogin/>;
 const path=location.pathname;
 return <Frame teacher email={me.email}>{path==='/teacher/lessons'?<TeacherLessons/>:path==='/teacher/statistics'?<TeacherStats/>:path==='/teacher/profile'?<TeacherProfile/>:<TeacherHome/>}</Frame>;
}

function TeacherHome(){const [rows,setRows]=useState<Lesson[]>([]),[stats,setStats]=useState<Stats|null>(null),[zone,setZone]=useState('Asia/Shanghai'),[error,setError]=useState('');useEffect(()=>{void api<Lesson[]|null>('/teacher/lessons').then(v=>setRows(v??[])).catch(e=>setError(e.message));void api<Teacher>('/teacher/profile').then(p=>{const z=safeZone(p.timezone);setZone(z);if(z!==p.timezone)setError('外教资料中的时区无效，请联系管理员修改');return api<Stats>(`/teacher/statistics?month=${localDate(z).slice(0,7)}`)}).then(setStats).catch(e=>setError(e.message))},[]);const today=localDate(zone);const current=rows.filter(l=>new Intl.DateTimeFormat('en-CA',{timeZone:zone,year:'numeric',month:'2-digit',day:'2-digit'}).format(new Date(l.scheduled_start_at))===today);return <><h1>今天的课程</h1>{error&&<p className="admin-error">{error}</p>}<div className="learning-cards"><article><small>今天</small><h2>{current.length} 节课</h2></article><article><small>本月已完成</small><h2>{stats?.completed_lessons||0} 节</h2></article><article><small>授课时间</small><h2>{Math.round((stats?.teaching_seconds||0)/60)} 分钟</h2></article></div><LessonCards rows={current}/></>}
function LessonCards({rows}:{rows:Lesson[]}){return <div className="lesson-list">{rows.map(l=><article key={l.id}><strong>{fmt(l.scheduled_start_at)}</strong><span>{l.student_name||`学生 #${l.student_id}`} · {l.duration_minutes} 分钟</span><span>{statusName[l.status]||l.status}</span>{l.status==='completed'&&<small>实际共同在线 {Math.round(l.teaching_seconds/60)} 分钟</small>}{l.status==='cancelled'&&l.cancel_reason&&<small>取消原因：{l.cancel_reason}</small>}{['scheduled','in_progress'].includes(l.status)&&<a href={`/teacher/classroom/${l.id}/check`}>设备检测 / 进入课堂 →</a>}</article>)}{rows.length===0&&<p>暂无课程。</p>}</div>}
function TeacherLessons(){const [rows,setRows]=useState<Lesson[]>([]),[error,setError]=useState('');useEffect(()=>{api<Lesson[]|null>('/teacher/lessons').then(v=>setRows(v??[])).catch(e=>setError(e.message))},[]);return <><h1>我的课程</h1>{error&&<p className="admin-error">{error}</p>}<LessonCards rows={rows}/></>}
function TeacherStats(){const [month,setMonth]=useState(localDate('Asia/Shanghai').slice(0,7)),[stats,setStats]=useState<Stats|null>(null),[error,setError]=useState('');useEffect(()=>{api<Teacher>('/teacher/profile').then(p=>setMonth(localDate(p.timezone||'Asia/Shanghai').slice(0,7))).catch(e=>setError(e.message))},[]);useEffect(()=>{api<Stats>(`/teacher/statistics?month=${month}`).then(setStats).catch(e=>setError(e.message))},[month]);return <><h1>我的课时</h1><label>月份 <input type="month" value={month} onChange={e=>setMonth(e.target.value)}/></label>{error&&<p className="admin-error">{error}</p>}<div className="learning-cards"><article><small>已完成</small><h2>{stats?.completed_lessons||0} 节</h2></article><article><small>实际授课</small><h2>{Math.round((stats?.teaching_seconds||0)/60)} 分钟</h2></article><article><small>学生缺席</small><h2>{stats?.student_no_show||0} 节</h2></article><article><small>取消</small><h2>{stats?.cancelled||0} 节</h2></article></div></>}
function TeacherProfile(){const [p,setP]=useState<Teacher|null>(null),[error,setError]=useState(''),[message,setMessage]=useState('');useEffect(()=>{api<Teacher>('/teacher/profile').then(setP).catch(e=>setError(e.message))},[]);async function save(e:React.FormEvent){e.preventDefault();if(!p)return;try{setP(await api<Teacher>('/teacher/profile',{method:'PATCH',body:JSON.stringify({display_name:p.display_name,bio:p.bio})}));setMessage('资料已保存')}catch(err){setError((err as Error).message)}}return <><h1>我的资料</h1>{error&&<p className="admin-error">{error}</p>}{message&&<p className="admin-success">{message}</p>}{p&&<form className="learning-form" onSubmit={save}><label>姓名<input value={p.display_name} onChange={e=>setP({...p,display_name:e.target.value})}/></label><label>国家<input value={p.country} disabled/></label><label>时区<input value={p.timezone} disabled/></label><label>个人简介<textarea value={p.bio||''} onChange={e=>setP({...p,bio:e.target.value})}/></label><button className="admin-primary">保存资料</button></form>}</>}
