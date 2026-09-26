import {useEffect,useState} from 'react';
import {ArrowLeft,ArrowRight,CalendarDays,ChevronLeft,ChevronRight,Clock3,UserRound} from 'lucide-react';
import {api} from './api';
import {teacherDate,teacherError,teacherStatus,teacherText,useTeacherLanguage} from './teacher-i18n';

type ScheduleSlot={start_at:string;end_at:string;state:'open'|'booked'|'time_off'|'past';lesson_id?:number;lesson_status?:string};
type Lesson={id:number;student_id:number;student_name:string;scheduled_start_at:string;scheduled_end_at:string;duration_minutes:number;status:string;note?:string;teaching_seconds:number;actual_start_at:string|null;actual_end_at:string|null;cancel_reason:string};
const zone='Asia/Shanghai';
const currentDay=()=>new Intl.DateTimeFormat('en-CA',{timeZone:zone,year:'numeric',month:'2-digit',day:'2-digit'}).format(new Date());
const timeText=(value:string)=>new Intl.DateTimeFormat('zh-CN',{timeZone:zone,hour:'2-digit',minute:'2-digit',hourCycle:'h23'}).format(new Date(value));
const dayText=(language:'en'|'zh',day:string)=>teacherDate(language,`${day}T00:00:00+08:00`);
function validDay(value:string|null):value is string{
 if(!value||!/^\d{4}-\d{2}-\d{2}$/.test(value))return false;
 const parsed=new Date(`${value}T00:00:00Z`);
 return !Number.isNaN(parsed.getTime())&&parsed.toISOString().slice(0,10)===value;
}
function moveDay(day:string,step:number){const date=new Date(`${day}T00:00:00Z`);date.setUTCDate(date.getUTCDate()+step);return date.toISOString().slice(0,10)}
function selectedDay(){const value=new URLSearchParams(location.search).get('date');return validDay(value)?value:currentDay()}

export function TeacherSchedulePage(){
 const language=useTeacherLanguage(),t=(value:string)=>teacherText(language,value);
 const [day,setDay]=useState(selectedDay),[slots,setSlots]=useState<ScheduleSlot[]>([]),[loading,setLoading]=useState(true),[error,setError]=useState('');
 useEffect(()=>{
   const controller=new AbortController();
   setLoading(true);setError('');setSlots([]);
   api<ScheduleSlot[]|null>(`/teacher/schedule?date=${encodeURIComponent(day)}`,controller.signal).then(rows=>setSlots(rows??[])).catch(e=>{if((e as Error).name!=='AbortError')setError((e as Error).message)}).finally(()=>{if(!controller.signal.aborted)setLoading(false)});
   return()=>controller.abort();
 },[day]);
 function chooseDay(next:string){if(!validDay(next))return;setDay(next);const url=new URL(location.href);if(next===currentDay())url.searchParams.delete('date');else url.searchParams.set('date',next);history.replaceState(null,'',`${url.pathname}${url.search}${url.hash}`)}
 const booked=slots.filter(slot=>slot.state==='booked').length;
 return <div className="teacher-schedule-page">
   <div className="teacher-schedule-heading"><div><span className="teacher-schedule-eyebrow">{t('我的教学安排')}</span><h1>{t('我的课程')}</h1><p>{t('按中国时间查看每天的排班与预约。')}</p></div><div className="teacher-schedule-date"><label htmlFor="teacher-schedule-day">{t('选择日期')}</label><div><button type="button" aria-label={t('前一天')} onClick={()=>chooseDay(moveDay(day,-1))}><ChevronLeft size={19}/></button><input id="teacher-schedule-day" type="date" value={day} onChange={event=>chooseDay(event.target.value)}/><button type="button" aria-label={t('后一天')} onClick={()=>chooseDay(moveDay(day,1))}><ChevronRight size={19}/></button></div></div></div>
   <div className="teacher-schedule-summary"><div><CalendarDays size={20}/><strong>{dayText(language,day)}</strong>{day===currentDay()&&<span>{t('今天')}</span>}</div><small>{loading?t('正在读取排班…'):`${slots.length}${t('个时段')} · ${booked}${t('个有预约')}`}</small></div>
   {error&&<p className="teacher-schedule-message is-error" role="alert">{teacherError(language,error)}</p>}
   {loading&&<p className="teacher-schedule-message" role="status">{t('正在加载当天排班…')}</p>}
   {!loading&&!error&&slots.length===0&&<div className="teacher-schedule-empty"><CalendarDays size={28}/><h2>{t('当天没有排班时段')}</h2><p>{t('可以选择其他日期查看；如需调整排班，请联系管理员。')}</p></div>}
   {!loading&&!error&&slots.length>0&&<div className="teacher-schedule-grid">{slots.map(slot=>{
     const reserved=slot.state==='booked'&&!!slot.lesson_id;
     const content=<><div className="teacher-slot-time"><Clock3 size={18}/><strong>{timeText(slot.start_at)}–{timeText(slot.end_at)}</strong></div><span className={`teacher-slot-badge is-${slot.state}`}>{t(reserved?'有预约':slot.state==='time_off'?'请假休息':slot.state==='past'?'已过时段':'暂无预约')}</span><div className="teacher-slot-bottom"><small>{reserved?(slot.lesson_status?teacherStatus(language,slot.lesson_status):t('查看预约详情')):t(slot.state==='open'?'等待学生预约':slot.state==='time_off'?'该时段不开放预约':'该时段没有预约')}</small>{reserved&&<ArrowRight size={17}/>}</div></>;
     return reserved?<a key={`${slot.start_at}-${slot.lesson_id}`} className="teacher-slot-card is-booked" href={`/teacher/lessons/${slot.lesson_id}?date=${day}`} aria-label={`${timeText(slot.start_at)} ${t('有预约')}，${t('查看预约详情')}`}>{content}</a>:<article key={slot.start_at} className="teacher-slot-card">{content}</article>;
   })}</div>}
 </div>;
}

export function TeacherBookingDetail({lessonID}:{lessonID:number}){
 const language=useTeacherLanguage(),t=(value:string)=>teacherText(language,value);
 const [lesson,setLesson]=useState<Lesson|null>(null),[loading,setLoading]=useState(true),[error,setError]=useState('');
 useEffect(()=>{const controller=new AbortController();api<Lesson>(`/teacher/lessons/${lessonID}`,controller.signal).then(setLesson).catch(e=>{if((e as Error).name!=='AbortError')setError((e as Error).message)}).finally(()=>{if(!controller.signal.aborted)setLoading(false)});return()=>controller.abort()},[lessonID]);
 const queryDay=new URLSearchParams(location.search).get('date'),day=validDay(queryDay)?queryDay:lesson?new Intl.DateTimeFormat('en-CA',{timeZone:zone,year:'numeric',month:'2-digit',day:'2-digit'}).format(new Date(lesson.scheduled_start_at)):null;
 const backUrl=day?`/teacher/lessons?date=${day}`:'/teacher/lessons';
 return <div className="teacher-booking-detail"><a className="teacher-detail-back" href={backUrl}><ArrowLeft size={18}/>{t('返回排班')}</a><div className="teacher-schedule-heading"><div><span className="teacher-schedule-eyebrow">{t('课程预约')}</span><h1>{t('预约详情')}</h1><p>{t('查看学生与上课安排。')}</p></div></div>
   {loading&&<p className="teacher-schedule-message" role="status">{t('正在加载预约详情…')}</p>}
   {error&&<p className="teacher-schedule-message is-error" role="alert">{teacherError(language,error)}</p>}
   {lesson&&<div className="teacher-detail-card"><div className="teacher-detail-top"><span className="teacher-slot-badge is-booked">{teacherStatus(language,lesson.status)}</span><small>{t('预约编号')} #{lesson.id}</small></div><div className="teacher-detail-student"><span><UserRound size={30}/></span><div><small>{t('预约学生')}</small><h2>{lesson.student_name||`${t('学生')} #${lesson.student_id}`}</h2></div></div><dl className="teacher-detail-facts"><div><dt>{t('上课日期')}</dt><dd>{teacherDate(language,lesson.scheduled_start_at)}</dd></div><div><dt>{t('上课时间')}</dt><dd>{timeText(lesson.scheduled_start_at)}–{timeText(lesson.scheduled_end_at)}</dd></div><div><dt>{t('课程时长')}</dt><dd>{lesson.duration_minutes} {t('分钟')}</dd></div><div><dt>{t('课程状态')}</dt><dd>{teacherStatus(language,lesson.status)}</dd></div>{lesson.status==='completed'&&<div><dt>{t('实际共同在线')}</dt><dd>{Math.round(lesson.teaching_seconds/60)} {t('分钟')}</dd></div>}</dl>{lesson.note&&<div className="teacher-detail-note"><strong>{t('预约备注')}</strong><p>{lesson.note}</p></div>}{lesson.cancel_reason&&<div className="teacher-detail-note"><strong>{t('取消原因')}</strong><p>{lesson.cancel_reason}</p></div>}{['scheduled','in_progress'].includes(lesson.status)&&<a className="teacher-detail-action" href={`/teacher/classroom/${lesson.id}/check`}>{t('课前设备检测 / 进入课堂')} <ArrowRight size={18}/></a>}</div>}
 </div>;
}
