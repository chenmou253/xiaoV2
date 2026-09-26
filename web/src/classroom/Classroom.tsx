import {useEffect,useRef,useState} from 'react';
import AgoraRTC,{type IAgoraRTCClient,type ILocalAudioTrack,type ILocalVideoTrack,type IRemoteVideoTrack} from 'agora-rtc-sdk-ng';
import {ApplianceNames,AsyncModuleLoadMode,setAsyncModuleLoadMode,WhiteWebSdk,type Room} from 'white-web-sdk';
import {api,APIError} from '../api';
import {ArrowLeft,ArrowLeftRight,BookOpen,Camera,CameraOff,Clock3,LoaderCircle,Mic,MicOff,MonitorUp,RefreshCw,TriangleAlert,UsersRound,Wifi} from 'lucide-react';

export type Credentials={
 lesson_id:number;server_time:string;scheduled_start_at:string;scheduled_end_at:string;grace_seconds:number;
 rtc:{app_id:string;channel:string;uid:number;token:string;screen_uid?:number;screen_token?:string};
 whiteboard:{app_identifier:string;region:string;uuid:string;room_token:string};
};

function roomPath(role:'teacher'|'student',id:number){return `${role==='teacher'?'/teacher':''}/classrooms/${id}`}
const whiteboardTools=[
 {name:'画笔',appliance:ApplianceNames.pencil},
 {name:'文字',appliance:ApplianceNames.text},
 {name:'矩形',appliance:ApplianceNames.rectangle},
 {name:'圆形',appliance:ApplianceNames.ellipse},
 {name:'橡皮擦',appliance:ApplianceNames.eraser},
 {name:'选择',appliance:ApplianceNames.selector},
];
// The SDK's default IndexedDB cache loads its modules through blob: scripts.
// Keep the site's script policy narrow and load the vendor module directly.
setAsyncModuleLoadMode(AsyncModuleLoadMode.DisableCache);
function Whiteboard({value,role}:{value:Credentials;role:'teacher'|'student'}){
 const boardRef=useRef<HTMLDivElement>(null),roomRef=useRef<Room|null>(null);
 const [ready,setReady]=useState(false),[error,setError]=useState(''),[attempt,setAttempt]=useState(0),[tool,setTool]=useState(ApplianceNames.pencil);
 useEffect(()=>{
   let active=true,created:Room|null=null;
   const timeout=window.setTimeout(()=>{if(active)setError('连接超时，请重试；如果仍失败，请检查网络连接。')},20000);
   setReady(false);setError('');roomRef.current=null;
   const sdk=new WhiteWebSdk({appIdentifier:value.whiteboard.app_identifier,region:value.whiteboard.region});
   void sdk.joinRoom({uid:`${role}_${value.rtc.uid}`,uuid:value.whiteboard.uuid,roomToken:value.whiteboard.room_token})
     .then(room=>{
       window.clearTimeout(timeout);
       created=room;
       if(!active){void room.disconnect();return}
       roomRef.current=room;
       room.bindHtmlElement(boardRef.current);
       room.setMemberState({currentApplianceName:ApplianceNames.pencil});
       setTool(ApplianceNames.pencil);
       setError('');setReady(true);
     })
     .catch(reason=>{window.clearTimeout(timeout);if(active)setError(reason instanceof Error?reason.message:String(reason))});
   return()=>{active=false;window.clearTimeout(timeout);roomRef.current=null;if(created){created.bindHtmlElement(null);void created.disconnect()}};
 },[value.whiteboard.app_identifier,value.whiteboard.region,value.whiteboard.uuid,value.whiteboard.room_token,value.rtc.uid,role,attempt]);
 const selectTool=(appliance:ApplianceNames)=>{roomRef.current?.setMemberState({currentApplianceName:appliance});setTool(appliance)};
 return <div className="lesson-whiteboard">
   <div className="whiteboard-toolbar">{whiteboardTools.map(item=><button key={item.appliance} type="button" disabled={!ready} aria-pressed={tool===item.appliance} onClick={()=>selectTool(item.appliance)}>{item.name}</button>)}{role==='teacher'&&<button type="button" disabled={!ready} onClick={()=>roomRef.current?.cleanCurrentScene()}>清空本页</button>}</div>
   <div className="whiteboard-canvas" ref={boardRef}/>
   {!ready&&<div className="whiteboard-overlay"><div className="whiteboard-state">{error?<><span className="whiteboard-state-icon is-warning"><TriangleAlert size={28}/></span><h2>课堂白板暂不可用</h2><p>{error}</p><button className="classroom-primary" type="button" onClick={()=>setAttempt(n=>n+1)}><RefreshCw size={17}/>重试连接</button></>:<><span className="whiteboard-state-icon"><LoaderCircle size={28}/></span><h2>正在准备互动白板</h2><p>连接成功后，就可以一起书写和标注。</p></>}</div></div>}
 </div>;
}

export default function Classroom({role,id}:{role:'teacher'|'student';id:number}){
 const [credentials,setCredentials]=useState<Credentials|null>(null),[error,setError]=useState(''),[remaining,setRemaining]=useState(0),[muted,setMuted]=useState(false),[cameraOff,setCameraOff]=useState(false),[sharing,setSharing]=useState(false),[network,setNetwork]=useState('连接中'),[remoteReady,setRemoteReady]=useState(false),[localReady,setLocalReady]=useState(false),[localPreviewError,setLocalPreviewError]=useState(false);
 const smallVideoRef=useRef<HTMLDivElement>(null),largeVideoRef=useRef<HTMLDivElement>(null),screenRef=useRef<HTMLDivElement>(null);
 const localPipRef=useRef<HTMLDivElement>(null),dragStart=useRef<{x:number;y:number;left:number;top:number}|null>(null),suppressPipClick=useRef(false);
 const [localPipPosition,setLocalPipPosition]=useState<{left:number;top:number}|null>(null);
 const [selfLarge,setSelfLarge]=useState(false),selfLargeRef=useRef(false);
 const clientRef=useRef<IAgoraRTCClient|null>(null),screenClientRef=useRef<IAgoraRTCClient|null>(null),micRef=useRef<ILocalAudioTrack|null>(null),cameraRef=useRef<ILocalVideoTrack|null>(null),remoteTrackRef=useRef<IRemoteVideoTrack|null>(null),screenTrackRef=useRef<ILocalVideoTrack|null>(null);
 const micMutedRef=useRef(false);
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
   const devices=JSON.parse(sessionStorage.getItem(`classroom-devices-${role}-${id}`)||'{}') as {camera?:string;microphone?:string};
   async function showLocalPreview(){
     if(!active||cameraRef.current)return;
     try{
       const camera=await AgoraRTC.createCameraVideoTrack(devices.camera?{cameraId:devices.camera}:undefined);
       if(!active){camera.close();return}
       cameraRef.current=camera;
       camera.play((selfLargeRef.current?largeVideoRef:smallVideoRef).current!);
       setLocalPreviewError(false);
       setLocalReady(true);
     }catch{if(active)setLocalPreviewError(true)}
   }
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
           const target=user.uid===3?screenRef.current:(selfLargeRef.current?smallVideoRef:largeVideoRef).current;
           if(target){target.replaceChildren();user.videoTrack?.play(target);if(user.uid===3)setSharing(true);else{remoteTrackRef.current=user.videoTrack??null;setRemoteReady(true)}}
         }else user.audioTrack?.play();
       });
       client.on('user-unpublished',(user,mediaType)=>{if(mediaType!=='video')return;if(user.uid===3){screenRef.current?.replaceChildren();setSharing(false)}else{remoteTrackRef.current?.stop();remoteTrackRef.current=null;(selfLargeRef.current?smallVideoRef:largeVideoRef).current?.replaceChildren();setRemoteReady(false)}});
       client.on('user-left',user=>{if(user.uid!==3){remoteTrackRef.current?.stop();remoteTrackRef.current=null;(selfLargeRef.current?smallVideoRef:largeVideoRef).current?.replaceChildren();setRemoteReady(false)}});
       client.on('connection-state-change',state=>setNetwork(state==='CONNECTED'?'网络良好':state==='RECONNECTING'?'正在重连':state));
       client.on('token-privilege-will-expire',async()=>{try{const fresh=await api<Credentials>(`${path}/join`,{method:'POST'});await client?.renewToken(fresh.rtc.token)}catch{setError('课堂连接即将结束')}});
       await client.join(data.rtc.app_id,data.rtc.channel,data.rtc.token,data.rtc.uid);
       if(!active)return;
       const [mic,camera]=await AgoraRTC.createMicrophoneAndCameraTracks(devices.microphone?{microphoneId:devices.microphone}:undefined,devices.camera?{cameraId:devices.camera}:undefined);
       if(!active){mic.close();camera.close();return}
       micRef.current=mic;cameraRef.current=camera;
       if(micMutedRef.current)await mic.setMuted(true);
       camera.play((selfLargeRef.current?largeVideoRef:smallVideoRef).current!);
       setLocalReady(true);
       await client.publish([mic,camera]);
       await api(`${path}/presence?event=connected`,{method:'POST'});
     }catch(e){if(active){setError((e as Error).message);void showLocalPreview()}}
   }
   void connect();
   const heartbeat=window.setInterval(()=>{if(active&&client?.connectionState==='CONNECTED')void api(`${path}/presence`,{method:'POST'}).catch(e=>{if(e instanceof APIError&&e.status===403)void exit()})},20000);
   return()=>{active=false;window.clearInterval(heartbeat);screenTrackRef.current?.close();micRef.current?.close();cameraRef.current?.close();remoteTrackRef.current?.stop();screenTrackRef.current=null;micRef.current=null;cameraRef.current=null;remoteTrackRef.current=null;void screenClientRef.current?.leave();void client?.leave();if(!closedRef.current)void api(`${path}/leave`,{method:'POST'}).catch(()=>{})};
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
 function toggleMic(){const next=!micMutedRef.current;micMutedRef.current=next;void micRef.current?.setMuted(next);setMuted(next)}
 function toggleCamera(){const next=!cameraOff;void cameraRef.current?.setMuted(next);setCameraOff(next)}
 function switchVideoSize(){
   const next=!selfLargeRef.current;
   selfLargeRef.current=next;
   cameraRef.current?.stop();
   remoteTrackRef.current?.stop();
   smallVideoRef.current?.replaceChildren();
   largeVideoRef.current?.replaceChildren();
   if(cameraRef.current)cameraRef.current.play((next?largeVideoRef:smallVideoRef).current!);
   if(remoteTrackRef.current)remoteTrackRef.current.play((next?smallVideoRef:largeVideoRef).current!);
   setSelfLarge(next);
 }
 function handleVideoKeyDown(event:React.KeyboardEvent<HTMLDivElement>){if(event.key==='Enter'||event.key===' '){event.preventDefault();switchVideoSize()}}
 function startDraggingLocalVideo(event:React.PointerEvent<HTMLDivElement>){
   if(event.button!==0)return;
   const stage=event.currentTarget.parentElement;
   if(!stage)return;
   const stageRect=stage.getBoundingClientRect(),pipRect=event.currentTarget.getBoundingClientRect();
   dragStart.current={x:event.clientX,y:event.clientY,left:pipRect.left-stageRect.left,top:pipRect.top-stageRect.top};
   suppressPipClick.current=false;
   event.currentTarget.setPointerCapture(event.pointerId);
 }
 function dragLocalVideo(event:React.PointerEvent<HTMLDivElement>){
   const start=dragStart.current,stage=event.currentTarget.parentElement;
   if(!start||!stage)return;
   if(Math.abs(event.clientX-start.x)+Math.abs(event.clientY-start.y)>5)suppressPipClick.current=true;
   if(!suppressPipClick.current)return;
   const left=Math.max(0,Math.min(stage.clientWidth-event.currentTarget.offsetWidth,start.left+event.clientX-start.x));
   const top=Math.max(0,Math.min(stage.clientHeight-event.currentTarget.offsetHeight,start.top+event.clientY-start.y));
   setLocalPipPosition({left,top});
 }
 function stopDraggingLocalVideo(){dragStart.current=null}
 function handlePipClick(){if(suppressPipClick.current){suppressPipClick.current=false;return}switchVideoSize()}
 const format=(n:number)=>`${Math.floor(Math.max(n,0)/60).toString().padStart(2,'0')}:${(Math.max(n,0)%60).toString().padStart(2,'0')}`;
 return <main className="classroom-shell">
   <header className="classroom-topbar"><div className="classroom-identity"><span className="classroom-brand-mark"><BookOpen size={22}/></span><div><small>小小点读家 · {role==='teacher'?'教师中心':'学习中心'}</small><strong>1v1 外教课堂</strong></div></div><div className="classroom-topbar-status"><span className="classroom-status-chip"><Clock3 size={16}/>{!credentials?'正在连接':remaining>0?`剩余 ${format(remaining)}`:'课程已结束，正在退出'}</span><span className={`classroom-status-chip${network==='网络良好'?' is-good':''}`}><Wifi size={16}/>{network}</span></div><button className="classroom-leave" type="button" onClick={()=>void exit()}><ArrowLeft size={17}/>离开课堂</button></header>
   {error&&credentials&&<div className="classroom-notice" role="alert"><TriangleAlert size={20}/><span>{error}</span><button type="button" onClick={()=>location.reload()}><RefreshCw size={16}/>重试连接</button><a href={role==='teacher'?'/teacher/lessons':'/account/lessons'}>返回课程</a></div>}
   <div className="classroom-grid">
     <section className="classroom-stage" aria-label="互动白板">
       {credentials?<Whiteboard value={credentials} role={role}/>:<div className="whiteboard-overlay"><div className="whiteboard-state">{error?<><span className="whiteboard-state-icon is-warning"><TriangleAlert size={28}/></span><h2>课堂暂时无法进入</h2><p>{error}</p><button className="classroom-primary" type="button" onClick={()=>location.reload()}><RefreshCw size={17}/>重试连接</button></>:<><span className="whiteboard-state-icon"><LoaderCircle size={28}/></span><h2>正在准备课堂</h2><p>课堂资源加载后，即可开始上课。</p></>}</div></div>}
       <div ref={screenRef} className="shared-screen" style={{display:sharing?'block':'none'}}/>
       <div ref={localPipRef} className="local-video-pip" style={localPipPosition?{left:localPipPosition.left,top:localPipPosition.top,bottom:'auto'}:undefined} onPointerDown={startDraggingLocalVideo} onPointerMove={dragLocalVideo} onPointerUp={stopDraggingLocalVideo} onPointerCancel={stopDraggingLocalVideo} onClick={handlePipClick} onKeyDown={handleVideoKeyDown} role="button" tabIndex={0} aria-label={`${selfLarge?'对方':'我的'}视频小窗，点击切换大小`} title="点击切换大小，拖动调整位置">
         <small>{selfLarge?'对方':'我'}</small><span className="video-swap-hint" aria-hidden="true"><ArrowLeftRight size={13}/></span>
         <div ref={smallVideoRef} className="video-box"/>
         {selfLarge?!remoteReady&&<span className="local-video-placeholder">等待对方</span>:(!localReady||cameraOff)&&<span className="local-video-placeholder">{cameraOff?'摄像头已关闭':localPreviewError?'摄像头不可用':'正在开启摄像头'}</span>}
       </div>
     </section>
     <aside className="classroom-videos" aria-label="课堂视频">
       <div onClick={switchVideoSize} onKeyDown={handleVideoKeyDown} role="button" tabIndex={0} aria-label={`${selfLarge?'我的':'对方'}大视频，点击切换大小`} title="点击切换视频大小">
         <small>{selfLarge?'我':'对方视频'}</small><span className="video-swap-hint is-large" aria-hidden="true"><ArrowLeftRight size={15}/>点击切换大小</span>
         <div ref={largeVideoRef} className="video-box"/>
         {selfLarge?(!localReady||cameraOff)&&<div className="classroom-remote-empty"><span>{cameraOff?<CameraOff size={34}/>:<Camera size={34}/>}</span><strong>{cameraOff?'摄像头已关闭':localPreviewError?'摄像头不可用':'正在开启摄像头'}</strong></div>:!remoteReady&&<div className="classroom-remote-empty"><span><UsersRound size={34}/></span><strong>等待对方加入课堂</strong><p>连接成功后，视频会显示在这里。</p></div>}
       </div>
     </aside>
   </div>
   <footer className="classroom-controls"><button type="button" aria-pressed={muted} onClick={toggleMic}>{muted?<MicOff size={18}/>:<Mic size={18}/>}<span>{muted?'开启麦克风':'关闭麦克风'}</span></button><button type="button" aria-pressed={cameraOff} disabled={!cameraRef.current} onClick={toggleCamera}>{cameraOff?<CameraOff size={18}/>:<Camera size={18}/>}<span>{cameraOff?'开启摄像头':'关闭摄像头'}</span></button>{role==='teacher'&&<button type="button" aria-pressed={sharing} disabled={!credentials?.rtc.screen_uid} onClick={()=>void share()}><MonitorUp size={18}/><span>{sharing?'停止共享':'共享屏幕'}</span></button>}</footer>
 </main>;
}
