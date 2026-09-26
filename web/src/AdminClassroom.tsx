import {useEffect,useState} from 'react';
import {api} from './api';

type Teacher={id:number;email:string;display_name:string;country:string;lesson_duration_minutes:number;break_minutes:number;avatar?:string;active:boolean;verified:boolean;bio:string;activation_email_sent?:boolean};
type Availability={weekday:number;start_minute:number;end_minute:number;active:boolean};
type Lesson={id:number;teacher_id:number;student_id:number;student_name:string;teacher_name:string;scheduled_start_at:string;scheduled_end_at:string;status:string;duration_minutes:number;teaching_seconds:number;cancel_reason:string};
type ScheduleConflict={previous_lesson_id:number;next_lesson_id:number;previous_end_at:string;next_start_at:string;gap_minutes:number;required_minutes:number};
const days=['周日','周一','周二','周三','周四','周五','周六'];
const time=(minute:number)=>`${Math.floor(minute/60).toString().padStart(2,'0')}:${(minute%60).toString().padStart(2,'0')}`;
const minute=(value:string)=>{const [h,m]=value.split(':').map(Number);return h*60+m};
const chinaTime=(value:string)=>new Intl.DateTimeFormat('zh-CN',{timeZone:'Asia/Shanghai',month:'numeric',day:'numeric',hour:'2-digit',minute:'2-digit',hourCycle:'h23'}).format(new Date(value));
const chinaDateTimeISO=(value:string)=>{const [date,clock]=value.split('T'),[year,month,day]=date.split('-').map(Number),[hour,minute]=clock.split(':').map(Number);return new Date(Date.UTC(year,month-1,day,hour-8,minute)).toISOString()};
const chinaMonth=()=>new Intl.DateTimeFormat('en-CA',{timeZone:'Asia/Shanghai',year:'numeric',month:'2-digit'}).format(new Date());
const chinaDate=(value:string)=>new Intl.DateTimeFormat('zh-CN',{timeZone:'Asia/Shanghai',dateStyle:'medium',timeStyle:'short'}).format(new Date(value));

function previewSlots(rows:Availability[],weekday:number,duration:number,rest:number){
 if(!Number.isInteger(duration)||!Number.isInteger(rest)||duration<=0||rest<=0)return [];
 const candidates:{start:number;end:number}[]=[];
 for(const dayOffset of [-1,0]){
  const rowDay=(weekday+dayOffset+7)%7;
  for(const row of rows.filter(a=>a.active&&a.weekday===rowDay))for(let start=row.start_minute;start+duration<=row.end_minute;start+=duration+rest)candidates.push({start:start+dayOffset*1440,end:start+duration+dayOffset*1440});
 }
 candidates.sort((a,b)=>a.start-b.start);
 const result:{start:number;end:number}[]=[];
 for(const slot of candidates)if(!result.length||slot.start>=result[result.length-1].end+rest)result.push(slot);
 return result.filter(slot=>slot.start>=0);
}

function TeacherSchedulePreview({teacher,availability}:{teacher:Teacher;availability:Availability[]}){
 const weekdays=days.map((_,i)=>i).filter(i=>availability.some(row=>row.active&&row.weekday===i));
 return <div className="admin-panel teacher-preview"><h3>一周排班预览</h3><p>按中国时间显示常规排班，不含临时请假。每节课 {teacher.lesson_duration_minutes} 分钟，课间至少休息 {teacher.break_minutes} 分钟。</p>
  {weekdays.length===0?<p>还没有设置开放时段。</p>:weekdays.map(weekday=>{
   const slots=previewSlots(availability,weekday,teacher.lesson_duration_minutes,teacher.break_minutes);
   return <section key={weekday} className="teacher-preview-day"><h4>{days[weekday]}</h4><p>{availability.filter(row=>row.active&&row.weekday===weekday).sort((a,b)=>a.start_minute-b.start_minute).map(row=>`${time(row.start_minute)}–${time(row.end_minute)}`).join('、')}</p>
    {slots.length===0?<small>开放时间不足一节课。</small>:<div className="teacher-preview-slots">{slots.map((slot,i)=><div key={slot.start} className="teacher-preview-pair"><span className="teacher-preview-lesson">课程 {time(slot.start)}–{time(slot.end)}</span>{i<slots.length-1&&<span className="teacher-preview-break">间隔 {time(slot.end)}–{time(slots[i+1].start)}</span>}</div>)}</div>}
   </section>;
  })}
 </div>;
}

function TeacherConflictNotice({teacher,conflicts}:{teacher:Teacher;conflicts:ScheduleConflict[]}){
 if(conflicts.length===0)return null;
 return <div className="admin-panel teacher-conflict-alert"><h3>已有课程间隔不足</h3><p>这些课程保持原预约时间，请管理员逐一处理。保存新设置后，后续预约会按新间隔校验。</p>
  <ul>{conflicts.map(row=><li key={`${row.previous_lesson_id}-${row.next_lesson_id}`}>课程 #{row.previous_lesson_id} 结束：{chinaTime(row.previous_end_at)}；课程 #{row.next_lesson_id} 开始：{chinaTime(row.next_start_at)}。实际间隔 {row.gap_minutes<0?'课程重叠':`${row.gap_minutes} 分钟`}，需要至少 {row.required_minutes} 分钟。</li>)}</ul>
 </div>;
}

export function AdminTeachers({canWrite,showStats}:{canWrite:boolean;showStats:boolean}){
 const [rows,setRows]=useState<Teacher[]>([]),[selected,setSelected]=useState<Teacher|null>(null),[selectedMode,setSelectedMode]=useState<'edit'|'schedule'>('schedule'),[availability,setAvailability]=useState<Availability[]>([]),[conflicts,setConflicts]=useState<ScheduleConflict[]>([]),[off,setOff]=useState<{start_at:string;end_at:string;reason:string}[]>([]),[error,setError]=useState(''),[notice,setNotice]=useState(''),[uploadingAvatar,setUploadingAvatar]=useState(false);
 const [statsMonth,setStatsMonth]=useState(chinaMonth()),[stats,setStats]=useState<{completed_lessons:number;student_no_show:number;cancelled:number;teaching_seconds:number}|null>(null);
 const [form,setForm]=useState({email:'',display_name:'',country:'',lesson_duration_minutes:30,break_minutes:15,bio:''}),[day,setDay]=useState(1),[start,setStart]=useState('09:00'),[end,setEnd]=useState('12:00'),[offStart,setOffStart]=useState(''),[offEnd,setOffEnd]=useState(''),[offReason,setOffReason]=useState('');
 const load=()=>api<Teacher[]|null>('/admin/teachers').then(v=>setRows(v??[])).catch(e=>setError(e.message));
 useEffect(()=>{void load()},[]);
 useEffect(()=>{if(!selected)return;const controller=new AbortController();void api<Availability[]|null>(`/admin/teachers/${selected.id}/availability`,controller.signal).then(v=>setAvailability(v??[])).catch(e=>{if((e as Error).name!=='AbortError')setError((e as Error).message)});void api<typeof off|null>(`/admin/teachers/${selected.id}/time-off`,controller.signal).then(v=>setOff(v??[])).catch(e=>{if((e as Error).name!=='AbortError')setError((e as Error).message)});return ()=>controller.abort()},[selected?.id]);
 useEffect(()=>{if(!selected||!Number.isInteger(selected.break_minutes)||selected.break_minutes<10||selected.break_minutes>60){setConflicts([]);return}const controller=new AbortController();void api<ScheduleConflict[]|null>(`/admin/teachers/${selected.id}/schedule-conflicts?break_minutes=${selected.break_minutes}`,controller.signal).then(v=>setConflicts(v??[])).catch(e=>{if((e as Error).name!=='AbortError')setError((e as Error).message)});return ()=>controller.abort()},[selected?.id,selected?.break_minutes]);
 useEffect(()=>{if(!selected||selectedMode!=='edit'||!showStats)return;void api<typeof stats>(`/admin/teachers/${selected.id}/statistics?month=${statsMonth}`).then(setStats).catch(e=>setError(e.message))},[selected?.id,selectedMode,statsMonth,showStats]);
 async function create(e:React.FormEvent){e.preventDefault();if(!canWrite){setError('当前账号没有外教编辑权限');return}setError('');try{const row=await api<Teacher>('/admin/teachers',{method:'POST',body:JSON.stringify(form)});setNotice(row.activation_email_sent?'外教已创建，激活邮件已发送。':'外教已创建，但激活邮件未送达，请重发。');setForm({email:'',display_name:'',country:'',lesson_duration_minutes:30,break_minutes:15,bio:''});await load()}catch(err){setError((err as Error).message)}}
 async function resend(t:Teacher){try{await api('/teacher/auth/forgot',{method:'POST',body:JSON.stringify({email:t.email})});setNotice('设置密码邮件已重发。')}catch(e){setError((e as Error).message)}}
 async function saveTeacher(e:React.FormEvent){e.preventDefault();if(!selected)return;setError('');try{const updated=await api<Teacher>(`/admin/teachers/${selected.id}`,{method:'PATCH',body:JSON.stringify({display_name:selected.display_name,country:selected.country,lesson_duration_minutes:selected.lesson_duration_minutes,break_minutes:selected.break_minutes,bio:selected.bio})});setSelected(updated);await load();setNotice(selectedMode==='schedule'?'排班设置已保存':'外教资料已保存')}catch(err){setError((err as Error).message)}}
 async function uploadAvatar(file?:File){if(!file||!selected)return;if(file.size>5*1024*1024){setError('头像文件不能超过 5MB');return}setUploadingAvatar(true);setError('');try{const form=new FormData();form.append('file',file);const updated=await api<Teacher>(`/admin/teachers/${selected.id}/avatar`,{method:'POST',body:form});setSelected({...selected,avatar:updated.avatar});await load();setNotice('头像已上传')}catch(e){setError((e as Error).message)}finally{setUploadingAvatar(false)}}
 async function toggle(t:Teacher){try{await api(`/admin/teachers/${t.id}`,{method:'PATCH',body:JSON.stringify({active:!t.active})});await load()}catch(e){setError((e as Error).message)}}
 function openTeacher(t:Teacher,mode:'edit'|'schedule'){setAvailability([]);setOff([]);setConflicts([]);setError('');setNotice('');setSelected(t);setSelectedMode(mode)}
 async function saveAvailability(rows:Availability[]){if(!selected)return;try{await api(`/admin/teachers/${selected.id}/availability`,{method:'POST',body:JSON.stringify({rows})});setAvailability(rows);setNotice('开放时间已保存')}catch(e){setError((e as Error).message)}}
 async function addOff(e:React.FormEvent){e.preventDefault();if(!selected)return;try{await api(`/admin/teachers/${selected.id}/time-off`,{method:'POST',body:JSON.stringify({start_at:chinaDateTimeISO(offStart),end_at:chinaDateTimeISO(offEnd),reason:offReason})});setOff((await api<typeof off|null>(`/admin/teachers/${selected.id}/time-off`))??[]);setNotice('请假已登记')}catch(err){setError((err as Error).message)}}
 return <><div className="admin-panel"><h2>新建外教</h2><p>创建后会发送设置密码邮件。</p>{error&&<p className="admin-error">{error}</p>}{notice&&<p className="admin-success">{notice}</p>}<form className="form-grid" onSubmit={create}><label>邮箱<input type="email" required value={form.email} onChange={e=>setForm({...form,email:e.target.value})}/></label><label>姓名<input required value={form.display_name} onChange={e=>setForm({...form,display_name:e.target.value})}/></label><label>国家<input value={form.country} onChange={e=>setForm({...form,country:e.target.value})}/></label><label>每节课时长（分钟）<input type="number" min="15" max="180" step="1" required value={form.lesson_duration_minutes} onChange={e=>setForm({...form,lesson_duration_minutes:Number(e.target.value)})}/></label><label>课间休息（分钟）<input type="number" min="10" max="60" step="1" required value={form.break_minutes} onChange={e=>setForm({...form,break_minutes:Number(e.target.value)})}/></label><button className="admin-primary">创建外教</button></form></div>
 <div className="admin-panel"><h2>外教列表</h2><div className="table-scroll"><table><thead><tr><th>姓名</th><th>邮箱</th><th>国家</th><th>状态</th><th>操作</th></tr></thead><tbody>{rows.map(t=><tr key={t.id}><td>{t.avatar&&<img className="teacher-avatar-thumb" src={t.avatar} alt=""/>}{t.display_name}</td><td>{t.email}</td><td>{t.country}</td><td>{t.active?(t.verified?'启用':'待激活'):'停用'}</td><td><button disabled={!canWrite} onClick={()=>openTeacher(t,'edit')}>编辑资料</button> <button onClick={()=>openTeacher(t,'schedule')}>排班/请假</button> <button onClick={()=>void toggle(t)}>{t.active?'停用':'启用'}</button> {!t.verified&&<button onClick={()=>void resend(t)}>重发邮件</button>}</td></tr>)}</tbody></table></div></div>
 {selected&&selectedMode==='edit'&&<div className="admin-panel"><div className="teacher-edit-heading"><h2>{selected.display_name} · 编辑资料</h2><button type="button" onClick={()=>setSelected(null)}>关闭</button></div>{error&&<p className="admin-error">{error}</p>}{notice&&<p className="admin-success">{notice}</p>}<div className="teacher-avatar-editor">{selected.avatar?<img src={selected.avatar} alt={`${selected.display_name}头像`}/>:<div className="teacher-avatar-placeholder">{selected.display_name.slice(0,1)||'外'}</div>}<label className="teacher-avatar-upload">{uploadingAvatar?'正在上传…':'上传头像'}<input type="file" accept="image/jpeg,image/png,image/gif" disabled={uploadingAvatar} onChange={e=>{void uploadAvatar(e.target.files?.[0]);e.currentTarget.value=''}}/></label><small>支持 JPG、PNG、GIF，文件不超过 5MB。</small></div><form className="form-grid" onSubmit={saveTeacher}><label>姓名<input required value={selected.display_name} onChange={e=>setSelected({...selected,display_name:e.target.value})}/></label><label>国家<input value={selected.country} onChange={e=>setSelected({...selected,country:e.target.value})}/></label><label>每节课时长（分钟）<input type="number" min="15" max="180" step="1" required value={selected.lesson_duration_minutes} onChange={e=>setSelected({...selected,lesson_duration_minutes:Number(e.target.value)})}/></label><label>课间休息（分钟）<input type="number" min="10" max="60" step="1" required value={selected.break_minutes} onChange={e=>setSelected({...selected,break_minutes:Number(e.target.value)})}/></label><label>简介<input value={selected.bio||''} onChange={e=>setSelected({...selected,bio:e.target.value})}/></label><button className="admin-primary">保存资料</button></form><label>统计月份 <input type="month" value={statsMonth} onChange={e=>setStatsMonth(e.target.value)}/></label>{stats&&<p>已完成 {stats.completed_lessons} 节 · 实际授课 {Math.round(stats.teaching_seconds/60)} 分钟 · 学生缺席 {stats.student_no_show} 节 · 取消 {stats.cancelled} 节</p>}</div>}
 {selected&&selectedMode==='edit'&&<TeacherConflictNotice teacher={selected} conflicts={conflicts}/>}
 {selected&&selectedMode==='schedule'&&<>
  <div className="admin-panel">
   <div className="teacher-edit-heading"><h2>{selected.display_name} · 排班/请假</h2><button type="button" onClick={()=>setSelected(null)}>关闭</button></div>
   {error&&<p className="admin-error">{error}</p>}{notice&&<p className="admin-success">{notice}</p>}
   <p>按中国时间设置开放时段，每段最多到当天 24:00。较长休息可分成上午、下午两个开放时段。</p>
   <form className="form-grid" onSubmit={saveTeacher}>
    <label>每节课时长（分钟）<input type="number" min="15" max="180" step="1" required value={selected.lesson_duration_minutes} onChange={e=>setSelected({...selected,lesson_duration_minutes:Number(e.target.value)})}/></label>
    <label>课间休息（分钟）<input type="number" min="10" max="60" step="1" required value={selected.break_minutes} onChange={e=>setSelected({...selected,break_minutes:Number(e.target.value)})}/></label>
    <button className="admin-primary" disabled={!canWrite}>保存排班设置</button>
   </form>
   <small className="admin-note">下方排班预览和已有课程提醒随输入更新，保存后新预约生效。</small>
   <h3>每周开放时段</h3>
   <div className="form-grid"><label>星期<select value={day} onChange={e=>setDay(Number(e.target.value))}>{days.map((d,i)=><option key={d} value={i}>{d}</option>)}</select></label><label>开始<input type="time" step="900" value={start} onChange={e=>setStart(e.target.value)}/></label><label>结束<input type="time" step="900" value={end} onChange={e=>setEnd(e.target.value)}/></label><button className="admin-primary" disabled={!canWrite} onClick={()=>void saveAvailability([...availability,{weekday:day,start_minute:minute(start),end_minute:minute(end),active:true}])}>添加时段</button></div>
   <div className="lesson-list">{availability.map((a,i)=><article key={`${a.weekday}-${a.start_minute}-${i}`}><strong>{days[a.weekday]} {time(a.start_minute)}–{time(a.end_minute)}</strong><button disabled={!canWrite} onClick={()=>void saveAvailability(availability.filter((_,j)=>i!==j))}>移除</button></article>)}</div>
   <h3>登记临时休息或请假</h3>
   <p className="admin-note">请假和临时休息时间按中国时间填写。</p>
   <form className="form-grid" onSubmit={addOff}><label>开始<input type="datetime-local" required value={offStart} onChange={e=>setOffStart(e.target.value)}/></label><label>结束<input type="datetime-local" required value={offEnd} onChange={e=>setOffEnd(e.target.value)}/></label><label>原因<input value={offReason} onChange={e=>setOffReason(e.target.value)}/></label><button className="admin-primary" disabled={!canWrite}>登记</button></form>
   {off.map(v=><p key={v.start_at}>{chinaTime(v.start_at)} 至 {chinaTime(v.end_at)} · {v.reason}</p>)}
  </div>
  <TeacherSchedulePreview teacher={selected} availability={availability}/>
  <TeacherConflictNotice teacher={selected} conflicts={conflicts}/>
 </>}</>;
}

export function AdminLessons({canWrite,canCancel,canReadStudents}:{canWrite:boolean;canCancel:boolean;canReadStudents:boolean}){
 const [teachers,setTeachers]=useState<Teacher[]>([]),[students,setStudents]=useState<{id:number;email:string}[]>([]),[rows,setRows]=useState<Lesson[]>([]),[teacherAvailability,setTeacherAvailability]=useState<Availability[]>([]),[teacherID,setTeacherID]=useState(0),[studentID,setStudentID]=useState(0),[start,setStart]=useState(''),[note,setNote]=useState(''),[error,setError]=useState(''),[notice,setNotice]=useState('');
 const load=()=>api<Lesson[]|null>('/admin/lessons').then(v=>setRows(v??[])).catch(e=>setError(e.message));
 useEffect(()=>{void api<Teacher[]|null>('/admin/teachers').then(value=>{const v=value??[];setTeachers(v);if(v.length)setTeacherID(v[0].id)}).catch(e=>setError(e.message));if(canReadStudents)void api<typeof students|null>('/admin/students').then(value=>{const v=value??[];setStudents(v);if(v.length)setStudentID(v[0].id)}).catch(e=>setError(e.message));void load()},[canReadStudents]);
 useEffect(()=>{if(!teacherID){setTeacherAvailability([]);return}const controller=new AbortController();void api<Availability[]|null>(`/admin/teachers/${teacherID}/availability`,controller.signal).then(v=>setTeacherAvailability(v??[])).catch(e=>{if((e as Error).name!=='AbortError')setError((e as Error).message)});return ()=>controller.abort()},[teacherID]);
 const t=teachers.find(v=>v.id===teacherID);
 async function create(e:React.FormEvent){e.preventDefault();if(!canWrite||!canReadStudents){setError('当前账号没有排课所需权限');return}setError('');try{await api('/admin/lessons',{method:'POST',body:JSON.stringify({student_id:studentID,teacher_id:teacherID,start_at:chinaDateTimeISO(start),duration_minutes:t?.lesson_duration_minutes||30,note,idempotency_key:crypto.randomUUID()})});setNotice('排课成功');await load()}catch(err){setError((err as Error).message)}}
 async function cancel(id:number){if(!canCancel){setError('当前账号没有取消课程权限');return}const reason=window.prompt('请输入取消原因');if(reason===null)return;try{await api(`/admin/lessons/${id}/cancel`,{method:'POST',body:JSON.stringify({reason})});await load()}catch(e){setError((e as Error).message)}}
 const openWindows=teacherAvailability.filter(row=>row.active).sort((a,b)=>a.weekday-b.weekday||a.start_minute-b.start_minute).map(row=>`${days[row.weekday]} ${time(row.start_minute)}–${time(row.end_minute)}`).join('、');
 return <>
  <div className="admin-panel"><h2>手工排课</h2>{error&&<p className="admin-error">{error}</p>}{notice&&<p className="admin-success">{notice}</p>}
   {canWrite&&canReadStudents?<>
    <form className="form-grid" onSubmit={create}>
     <label>学生<select value={studentID} onChange={e=>setStudentID(Number(e.target.value))}>{students.map(x=><option key={x.id} value={x.id}>{x.email}</option>)}</select></label>
     <label>外教<select value={teacherID} onChange={e=>{setTeacherAvailability([]);setTeacherID(Number(e.target.value))}}>{teachers.map(x=><option key={x.id} value={x.id}>{x.display_name}</option>)}</select></label>
     <label>开始时间（中国时间，精确到分钟）<input type="datetime-local" step="60" required value={start} onChange={e=>setStart(e.target.value)}/></label>
     <span>每节课 {t?.lesson_duration_minutes||30} 分钟 · 课间至少 {t?.break_minutes||15} 分钟</span>
     <label>备注<input value={note} onChange={e=>setNote(e.target.value)}/></label>
     <button className="admin-primary" disabled={!studentID||!teacherID||!openWindows}>创建课程</button>
    </form>
    <p className="admin-note">外教开放时段（中国时间）：{openWindows||'尚未设置，暂不能排课。'}</p>

   </>:<p>当前账号没有排课所需的课程编辑与学生查看权限。</p>}
  </div>
  <div className="admin-panel"><h2>课程列表</h2><div className="table-scroll"><table><thead><tr><th>时间</th><th>学生</th><th>外教</th><th>状态</th><th>操作</th></tr></thead><tbody>{rows.map(l=><tr key={l.id}><td>{chinaDate(l.scheduled_start_at)}</td><td>{l.student_name||l.student_id}</td><td>{l.teacher_name||l.teacher_id}</td><td>{l.status}{l.status==='completed'&&` · ${Math.round(l.teaching_seconds/60)} 分钟`}{l.cancel_reason&&<small> · {l.cancel_reason}</small>}</td><td>{canCancel&&['scheduled','in_progress'].includes(l.status)&&<button onClick={()=>void cancel(l.id)}>取消课程</button>}</td></tr>)}</tbody></table></div></div>
 </>;
}
