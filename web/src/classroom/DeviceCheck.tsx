import {useEffect,useRef,useState} from 'react';
import {api} from '../api';
import {TeacherLanguageSwitch,teacherError,teacherText,useTeacherLanguage} from '../teacher-i18n';

type CheckStatus='checking'|'available'|'unavailable'|'untested';
const statusText:Record<CheckStatus,string>={checking:'检测中',available:'可使用',unavailable:'不可用',untested:'待检测'};

export default function DeviceCheck({role,id}:{role:'teacher'|'student';id:number}){
 const teacherLanguage=useTeacherLanguage(),language=role==='teacher'?teacherLanguage:'zh',t=(value:string)=>teacherText(language,value);
 const [lesson,setLesson]=useState<{scheduled_start_at:string;scheduled_end_at:string}|null>(null),[error,setError]=useState(''),[cameras,setCameras]=useState<MediaDeviceInfo[]>([]),[microphones,setMicrophones]=useState<MediaDeviceInfo[]>([]),[camera,setCamera]=useState(''),[microphone,setMicrophone]=useState(''),[ready,setReady]=useState(false),[now,setNow]=useState(Date.now()),[network,setNetwork]=useState('检测中'),[networkStatus,setNetworkStatus]=useState<CheckStatus>('checking'),[cameraStatus,setCameraStatus]=useState<CheckStatus>('checking'),[microphoneStatus,setMicrophoneStatus]=useState<CheckStatus>('checking'),[speakerStatus,setSpeakerStatus]=useState<CheckStatus>('untested'),[debugEarlyEntry,setDebugEarlyEntry]=useState(false);
 const video=useRef<HTMLVideoElement>(null),stream=useRef<MediaStream|null>(null);
 useEffect(()=>{const base=role==='teacher'?'/teacher':'';api<{scheduled_start_at:string;scheduled_end_at:string}>(`${base}/lessons/${id}`).then(setLesson).catch(e=>setError(e.message));api<{classroom_debug_allow_early_entry?:boolean}>('/config').then(config=>setDebugEarlyEntry(config.classroom_debug_allow_early_entry===true)).catch(()=>setDebugEarlyEntry(false))},[role,id]);
 useEffect(()=>{const timer=window.setInterval(()=>setNow(Date.now()),10000);return()=>window.clearInterval(timer)},[]);
 useEffect(()=>{let alive=true;const started=performance.now();fetch('/healthz',{cache:'no-store'}).then(r=>{if(!alive)return;const ms=Math.round(performance.now()-started);setNetwork(r.ok?`${ms} 毫秒`:'服务暂不可用');setNetworkStatus(r.ok?'available':'unavailable')}).catch(()=>{if(alive){setNetwork('服务暂不可用');setNetworkStatus('unavailable')}});return()=>{alive=false}},[]);
 useEffect(()=>{
   if(!lesson)return;
   let alive=true;
   stream.current?.getTracks().forEach(track=>track.stop());
   stream.current=null;
   if(video.current)video.current.srcObject=null;
   setReady(false);setError('');setCameraStatus('checking');setMicrophoneStatus('checking');
   async function openDevices(){
     if(!navigator.mediaDevices?.getUserMedia){setCameraStatus('unavailable');setMicrophoneStatus('unavailable');setError('请使用 HTTPS 或 localhost 打开课堂');return}
     const [cameraResult,microphoneResult]=await Promise.allSettled([
       navigator.mediaDevices.getUserMedia({video:camera?{deviceId:{exact:camera}}:true,audio:false}),
       navigator.mediaDevices.getUserMedia({video:false,audio:microphone?{deviceId:{exact:microphone}}:true}),
     ]);
     if(!alive){for(const result of [cameraResult,microphoneResult])if(result.status==='fulfilled')result.value.getTracks().forEach(track=>track.stop());return}
     const acquired:MediaStream[]=[];
     if(cameraResult.status==='fulfilled'&&cameraResult.value.getVideoTracks().some(track=>track.readyState==='live')){
       acquired.push(cameraResult.value);setCameraStatus('available');if(video.current){video.current.srcObject=cameraResult.value;void video.current.play().catch(()=>{})}
     }else{
       if(cameraResult.status==='fulfilled')cameraResult.value.getTracks().forEach(track=>track.stop());
       setCameraStatus('unavailable');setError(cameraResult.status==='rejected'?`摄像头检测失败：${cameraResult.reason instanceof Error?cameraResult.reason.message:'无法访问摄像头'}`:'未检测到可用摄像头');
     }
     if(microphoneResult.status==='fulfilled'&&microphoneResult.value.getAudioTracks().some(track=>track.readyState==='live')){
       acquired.push(microphoneResult.value);setMicrophoneStatus('available');
     }else{
       if(microphoneResult.status==='fulfilled')microphoneResult.value.getTracks().forEach(track=>track.stop());
       setMicrophoneStatus('unavailable');setError(previous=>previous?`${previous}；麦克风检测失败`:`麦克风检测失败：${microphoneResult.status==='rejected'&&microphoneResult.reason instanceof Error?microphoneResult.reason.message:'未检测到可用麦克风'}`);
     }
     if(acquired.length)stream.current=new MediaStream(acquired.flatMap(item=>item.getTracks()));
     try{const devices=await navigator.mediaDevices.enumerateDevices();if(alive){setCameras(devices.filter(device=>device.kind==='videoinput'));setMicrophones(devices.filter(device=>device.kind==='audioinput'))}}catch{/* Device access status above remains useful if enumeration is unavailable. */}
     if(alive)setReady(cameraResult.status==='fulfilled'&&cameraResult.value.getVideoTracks().some(track=>track.readyState==='live')&&microphoneResult.status==='fulfilled'&&microphoneResult.value.getAudioTracks().some(track=>track.readyState==='live'));
   }
   void openDevices();
   return()=>{alive=false;stream.current?.getTracks().forEach(track=>track.stop());stream.current=null};
 },[camera,microphone,lesson]);
 async function testSpeaker(){
   setSpeakerStatus('checking');
   try{
     const context=new AudioContext();await context.resume();
     const oscillator=context.createOscillator();oscillator.frequency.value=440;oscillator.connect(context.destination);oscillator.onended=()=>{setSpeakerStatus('available');void context.close()};oscillator.start();window.setTimeout(()=>oscillator.stop(),350);
   }catch{setSpeakerStatus('unavailable')}
 }
 const start=lesson?Date.parse(lesson.scheduled_start_at):0,canEnter=!!lesson&&(debugEarlyEntry||now>=start-10*60000)&&now<Date.parse(lesson.scheduled_end_at);
 const status=(value:CheckStatus)=><span className={`device-status device-status-${value}`}>{t(statusText[value])}</span>;
 return <main className="device-shell"><div className="teacher-device-top"><a href={role==='teacher'?'/teacher/lessons':'/account/lessons'}>← {t('我的课程')}</a>{role==='teacher'&&<TeacherLanguageSwitch/>}</div><h1>{t('课前设备检测')}</h1><p>{t('确认摄像头、麦克风和扬声器可用，再进入课堂。')}</p>{debugEarlyEntry&&<p className="admin-note">{t('本地调试模式：允许提前进入课堂，课程结束后仍不可进入。')}</p>}{error&&<p className="admin-error">{teacherError(language,error)}</p>}<div className="device-grid"><div className="device-preview"><video ref={video} autoPlay playsInline muted/>{cameraStatus!=='available'&&<span>{t(cameraStatus==='checking'?'正在检测摄像头…':'摄像头不可用')}</span>}</div><section><div className="device-control"><label>{t('摄像头')}<select value={camera} onChange={e=>setCamera(e.target.value)}><option value="">{t('默认摄像头')}</option>{cameras.map(device=><option key={device.deviceId} value={device.deviceId}>{device.label||t('摄像头')}</option>)}</select></label>{status(cameraStatus)}</div><div className="device-control"><label>{t('麦克风')}<select value={microphone} onChange={e=>setMicrophone(e.target.value)}><option value="">{t('默认麦克风')}</option>{microphones.map(device=><option key={device.deviceId} value={device.deviceId}>{device.label||t('麦克风')}</option>)}</select></label>{status(microphoneStatus)}</div><div className="device-control"><span>{t('扬声器')}</span><button type="button" onClick={()=>void testSpeaker()}>{t(speakerStatus==='checking'?'正在测试…':'测试扬声器')}</button>{status(speakerStatus)}</div><p className="device-network">{t('网络：')}{networkStatus==='available'?<>{t('可用')} · {network.replace(' 毫秒',` ${t('毫秒')}`)} {status(networkStatus)}</>:<>{t(network)} {status(networkStatus)}</>}</p><button className="admin-primary" disabled={!ready||!canEnter} onClick={()=>{sessionStorage.setItem(`classroom-devices-${role}-${id}`,JSON.stringify({camera,microphone}));stream.current?.getTracks().forEach(track=>track.stop());location.assign(`${role==='teacher'?'/teacher':''}/classroom/${id}`)}}>{t(canEnter?'进入课堂':'上课前 10 分钟可进入')}</button></section></div></main>;
}
