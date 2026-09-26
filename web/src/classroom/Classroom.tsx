import {useEffect,useRef,useState} from 'react';
import AgoraRTC,{type IAgoraRTCClient,type ILocalAudioTrack,type ILocalVideoTrack} from 'agora-rtc-sdk-ng';
import {Fastboard,useFastboard} from '@netless/fastboard-react-lite';
import {api,APIError} from '../api';

export type Credentials={
 lesson_id:number;server_time:string;scheduled_start_at:string;scheduled_end_at:string;grace_seconds:number;
 rtc:{app_id:string;channel:string;uid:number;token:string;screen_uid?:number;screen_token?:string};
 whiteboard:{app_identifier:string;region:string;uuid:string;room_token:string};
};

function roomPath(role:'teacher'|'student',id:number){return `${role==='teacher'?'/teacher':''}/classrooms/${id}`}
function Whiteboard({value,role}:{value:Credentials;role:'teacher'|'student'}){
 const app=useFastboard(()=>({sdkConfig:{appIdentifier:value.whiteboard.app_identifier,region:value.whiteboard.region as 'sg'},joinRoom:{uid:`${role}_${value.rtc.uid}`,uuid:value.whiteboard.uuid,roomToken:value.whiteboard.room_token}}));
 return <div className="lesson-whiteboard">{app?<Fastboard app={app} language="zh-CN" config={{toolbar:{items:role==='teacher'?['clicker','selector','pencil','text','shapes','eraser','clear']:['pencil','text','eraser']}}}/>:<p>正在连接互动白板…</p>}</div>;
}

export default function Classroom({role,id}:{role:'teacher'|'student';id:number}){
 const [credentials,setCredentials]=useState<Credentials|null>(null),[error,setError]=useState(''),[remaining,setRemaining]=useState(0),[muted,setMuted]=useState(false),[cameraOff,setCameraOff]=useState(false),[sharing,setSharing]=useState(false),[network,setNetwork]=useState('连接中');
 const localRef=useRef<HTMLDivElement>(null),remoteRef=useRef<HTMLDivElement>(null),screenRef=useRef<HTMLDivElement>(null);
 const clientRef=useRef<IAgoraRTCClient|null>(null),screenClientRef=useRef<IAgoraRTCClient|null>(null),micRef=useRef<ILocalAudioTrack|null>(null),cameraRef=useRef<ILocalVideoTrack|null>(null),screenTrackRef=useRef<ILocalVideoTrack|null>(null);
 const closedRef=useRef(false);
 const baseTime=useRef({server:0,mono:0});
 const exit=async()=>{
   if(closedRef.current)return;closedRef.current=true;
   try{await api(`${roomPath(role,id)}/leave`,{method:'POST'})}catch{/* navigation still proceeds */}
   screenTrackRef.current?.close();cameraRef.current?.close();micRef.current?.close();
   await Promise.allSettled([screenClientRef.current?.leave(),clientRef.current?.leave()]);
   location.assign(role==='teacher'?'/teacher/lessons':'/account/lessons');
 };
 useEffect(()=>{
   let active=true;let client:IAgoraRTCClient|null=null;
   const path=roomPath(role,id);
   async function connect(){
     try{
       const data=await api<Credentials>(`${path}/join`,{method:'POST'});
       if(!active)return;
       setCredentials(data);baseTime.current={server:Date.parse(data.server_time),mono:performance.now()};
       client=AgoraRTC.createClient({mode:'rtc',codec:'vp8'});clientRef.current=client;
       client.on('user-published',async(user,mediaType)=>{
         if(!client)return;
         await client.subscribe(user,mediaType);
         if(mediaType==='video'){
           const target=user.uid===3?screenRef.current:remoteRef.current;
           if(target){target.innerHTML='';user.videoTrack?.play(target);if(user.uid===3)setSharing(true)}
         }else user.audioTrack?.play();
       });
       client.on('user-unpublished',(user,mediaType)=>{if(mediaType==='video'&&user.uid===3){if(screenRef.current)screenRef.current.innerHTML='';setSharing(false)}});
       client.on('connection-state-change',state=>setNetwork(state==='CONNECTED'?'网络良好':state==='RECONNECTING'?'正在重连':state));
       client.on('token-privilege-will-expire',async()=>{try{const fresh=await api<Credentials>(`${path}/join`,{method:'POST'});await client?.renewToken(fresh.rtc.token)}catch{setError('课堂连接即将结束')}});
       await client.join(data.rtc.app_id,data.rtc.channel,data.rtc.token,data.rtc.uid);
       if(!active)return;
       const devices=JSON.parse(sessionStorage.getItem(`classroom-devices-${role}-${id}`)||'{}') as {camera?:string;microphone?:string};
       const [mic,camera]=await AgoraRTC.createMicrophoneAndCameraTracks(devices.microphone?{microphoneId:devices.microphone}:undefined,devices.camera?{cameraId:devices.camera}:undefined);
       if(!active){mic.close();camera.close();return}
       micRef.current=mic;cameraRef.current=camera;
       camera.play(localRef.current!);
       await client.publish([mic,camera]);
       await api(`${path}/presence?event=connected`,{method:'POST'});
     }catch(e){if(active)setError((e as Error).message)}
   }
   void connect();
   const heartbeat=window.setInterval(()=>{if(active&&client?.connectionState==='CONNECTED')void api(`${path}/presence`,{method:'POST'}).catch(e=>{if(e instanceof APIError&&e.status===403)void exit()})},20000);
   return()=>{active=false;window.clearInterval(heartbeat);screenTrackRef.current?.close();micRef.current?.close();cameraRef.current?.close();void screenClientRef.current?.leave();void client?.leave();if(!closedRef.current)void api(`${path}/leave`,{method:'POST'}).catch(()=>{})};
 },[role,id]);
 useEffect(()=>{
   if(!credentials)return;
   const tick=()=>{const serverNow=baseTime.current.server+(performance.now()-baseTime.current.mono);const seconds=Math.ceil((Date.parse(credentials.scheduled_end_at)-serverNow)/1000);setRemaining(seconds);if(seconds<=0)void exit()};
   const sync=()=>{if(document.visibilityState!=='visible')return;void api<Credentials>(`${roomPath(role,id)}/join`,{method:'POST'}).then(fresh=>{baseTime.current={server:Date.parse(fresh.server_time),mono:performance.now()};tick()}).catch(e=>{if(e instanceof APIError&&e.status===403)void exit()})};
   tick();const timer=window.setInterval(tick,1000);document.addEventListener('visibilitychange',sync);return()=>{window.clearInterval(timer);document.removeEventListener('visibilitychange',sync)};
 },[credentials]);
 async function share(){
   if(!credentials?.rtc.screen_uid||!credentials.rtc.screen_token)return;
   if(screenClientRef.current){await screenClientRef.current.leave();screenClientRef.current=null;screenTrackRef.current?.close();screenTrackRef.current=null;setSharing(false);return}
   try{
     const track=await AgoraRTC.createScreenVideoTrack({},'disable');
     const c=AgoraRTC.createClient({mode:'rtc',codec:'vp8'});
     await c.join(credentials.rtc.app_id,credentials.rtc.channel,credentials.rtc.screen_token,credentials.rtc.screen_uid);
     await c.publish(track);
     if(screenRef.current)track.play(screenRef.current);
     track.on('track-ended',()=>{void c.leave();track.close();screenClientRef.current=null;screenTrackRef.current=null;setSharing(false)});
     screenClientRef.current=c;screenTrackRef.current=track;setSharing(true);
   }catch(e){setError((e as Error).message)}
 }
 function toggleMic(){const next=!muted;void micRef.current?.setMuted(next);setMuted(next)}
 function toggleCamera(){const next=!cameraOff;void cameraRef.current?.setMuted(next);setCameraOff(next)}
 const format=(n:number)=>`${Math.floor(Math.max(n,0)/60).toString().padStart(2,'0')}:${(Math.max(n,0)%60).toString().padStart(2,'0')}`;
 return <main className="classroom-shell">
   <header><strong>1v1 外教课堂</strong><span>{!credentials?'正在连接':remaining>0?`剩余 ${format(remaining)}`:'课程已结束，正在退出'}</span><span>{network}</span><button onClick={()=>void exit()}>离开课堂</button></header>
   {error&&<p className="classroom-error">{error} <button onClick={()=>location.reload()}>重试连接</button> <a href={role==='teacher'?'/teacher/lessons':'/account/lessons'}>返回课程</a></p>}
   <div className="classroom-grid"><section className="classroom-stage">{credentials?<Whiteboard value={credentials} role={role}/>:<p>正在准备课堂…</p>}<div ref={screenRef} className="shared-screen" style={{display:sharing?'block':'none'}}/></section><aside className="classroom-videos"><div><small>对方</small><div ref={remoteRef} className="video-box"/></div><div><small>我</small><div ref={localRef} className="video-box"/></div></aside></div>
   <footer><button onClick={toggleMic}>{muted?'开启麦克风':'关闭麦克风'}</button><button onClick={toggleCamera}>{cameraOff?'开启摄像头':'关闭摄像头'}</button>{role==='teacher'&&<button onClick={()=>void share()}>{sharing?'停止共享':'共享屏幕'}</button>}</footer>
 </main>;
}
