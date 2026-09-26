import {useEffect,useRef,useState} from 'react';
import {api} from '../api';

export default function DeviceCheck({role,id}:{role:'teacher'|'student';id:number}){
 const [lesson,setLesson]=useState<{scheduled_start_at:string;scheduled_end_at:string}|null>(null),[error,setError]=useState(''),[cameras,setCameras]=useState<MediaDeviceInfo[]>([]),[microphones,setMicrophones]=useState<MediaDeviceInfo[]>([]),[camera,setCamera]=useState(''),[microphone,setMicrophone]=useState(''),[ready,setReady]=useState(false),[now,setNow]=useState(Date.now()),[network,setNetwork]=useState('检测中');
 const video=useRef<HTMLVideoElement>(null),stream=useRef<MediaStream|null>(null);
 useEffect(()=>{const base=role==='teacher'?'/teacher':'';api<{scheduled_start_at:string;scheduled_end_at:string}>(`${base}/lessons/${id}`).then(setLesson).catch(e=>setError(e.message))},[role,id]);
 useEffect(()=>{const timer=window.setInterval(()=>setNow(Date.now()),10000);return()=>window.clearInterval(timer)},[]);
 useEffect(()=>{const started=performance.now();fetch('/healthz',{cache:'no-store'}).then(r=>setNetwork(r.ok?`可用 · ${Math.round(performance.now()-started)} 毫秒`:'服务暂不可用')).catch(()=>setNetwork('服务暂不可用'))},[]);
 useEffect(()=>{if(!lesson||now<Date.parse(lesson.scheduled_start_at)-15*60000||now>=Date.parse(lesson.scheduled_end_at))return;let alive=true;async function open(){try{
   if(!navigator.mediaDevices?.getUserMedia)throw new Error('请使用 HTTPS 或 localhost 打开课堂');
   stream.current?.getTracks().forEach(t=>t.stop());
   const next=await navigator.mediaDevices.getUserMedia({video:camera?{deviceId:{exact:camera}}:true,audio:microphone?{deviceId:{exact:microphone}}:true});
   if(!alive){next.getTracks().forEach(t=>t.stop());return}
   stream.current=next;if(video.current)video.current.srcObject=next;
   const devices=await navigator.mediaDevices.enumerateDevices();setCameras(devices.filter(d=>d.kind==='videoinput'));setMicrophones(devices.filter(d=>d.kind==='audioinput'));setReady(true);setError('');
 }catch(e){if(alive){setReady(false);setError((e as Error).message)}}}void open();return()=>{alive=false;stream.current?.getTracks().forEach(t=>t.stop())}},[camera,microphone,lesson,now>= (lesson?Date.parse(lesson.scheduled_start_at)-15*60000:0),now>= (lesson?Date.parse(lesson.scheduled_end_at):0)]);
 function testSpeaker(){const context=new AudioContext();const oscillator=context.createOscillator();oscillator.frequency.value=440;oscillator.connect(context.destination);oscillator.start();window.setTimeout(()=>{oscillator.stop();void context.close()},350)}
 const start=lesson?Date.parse(lesson.scheduled_start_at):0,canEnter=!!lesson&&now>=start-10*60000&&now<Date.parse(lesson.scheduled_end_at);
 return <main className="device-shell"><a href={role==='teacher'?'/teacher/lessons':'/account/lessons'}>← 我的课程</a><h1>课前设备检测</h1><p>确认摄像头、麦克风和扬声器可用，再进入课堂。</p>{error&&<p className="admin-error">{error}</p>}<div className="device-grid"><video ref={video} autoPlay playsInline muted/><section><label>摄像头<select value={camera} onChange={e=>setCamera(e.target.value)}><option value="">默认摄像头</option>{cameras.map(d=><option key={d.deviceId} value={d.deviceId}>{d.label||'摄像头'}</option>)}</select></label><label>麦克风<select value={microphone} onChange={e=>setMicrophone(e.target.value)}><option value="">默认麦克风</option>{microphones.map(d=><option key={d.deviceId} value={d.deviceId}>{d.label||'麦克风'}</option>)}</select></label><button onClick={testSpeaker}>测试扬声器</button><p>网络：{network}</p><button className="admin-primary" disabled={!ready||!canEnter} onClick={()=>{sessionStorage.setItem(`classroom-devices-${role}-${id}`,JSON.stringify({camera,microphone}));stream.current?.getTracks().forEach(t=>t.stop());location.assign(`${role==='teacher'?'/teacher':''}/classroom/${id}`)}}>{canEnter?'进入课堂':'上课前 10 分钟可进入'}</button></section></div></main>;
}
