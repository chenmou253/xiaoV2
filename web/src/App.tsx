import {lazy,Suspense,useCallback,useEffect,useRef,useState} from 'react';
import {ArrowLeft,ArrowRight,BookOpen,Check,ChevronLeft,ChevronRight,Headphones,Languages,Sparkles,Square,Volume2,ZoomIn,ZoomOut} from 'lucide-react';
import {booksAPI,type Accent,type Book,type BookPage,type PageContent,type PageGroup,type Segment,type Word} from './api';
import Admin from './Admin';
import AdminLogin from './AdminLogin';
import {StudentHeader,StudentBottomNav} from './StudentChrome';
import {StudentArea,TeacherArea,TeacherLogin} from './Learning';
import DeviceCheck from './classroom/DeviceCheck';
const Classroom=lazy(()=>import('./classroom/Classroom'));

type ShelfState={kind:'loading'}|{kind:'error';message:string}|{kind:'ready';books:Book[]};

function routeBook(){return new URLSearchParams(location.hash.split('?')[1]||'').get('book')||''}
function hasSpeakableText(value:string){return /[\p{L}\p{N}]/u.test(value)}
function segmentAudioMode(segment:Segment){return segment.audio_mode||'sentence_and_words'}
function hasSentenceAudio(segment:Segment){return segmentAudioMode(segment)==='sentence_and_words'}
function hasWordAudio(segment:Segment){return segmentAudioMode(segment)!=='none'}
const pageGroupNames:Record<PageGroup,string>={cover:'封面',front:'首页',title:'扉页',contents:'目录',body:'正文',appendix:'附录',other:'其他'};
function pageDisplayName(item:BookPage,pages:BookPage[]){
 if(item.page_label)return item.page_label;
 if(item.printed_page!=null)return item.page_group==='appendix'?`附录第 ${item.printed_page} 页`:`第 ${item.printed_page} 页`;
 if(item.page_group==='contents'){
  const contents=pages.filter(page=>page.page_group==='contents');
  return contents.length>1?`目录 ${contents.findIndex(page=>page.page===item.page)+1}/${contents.length}`:'目录';
 }
 return pageGroupNames[item.page_group as PageGroup]||'';
}
function pageOptionLabel(item:BookPage,pages:BookPage[]){const title=item.title===`第 ${item.page} 页`?'':item.title;return [pageDisplayName(item,pages),item.unit,title].filter(Boolean).join(' · ')}
function pageGroupKey(item:BookPage){return `${item.page_group}:${item.page_group==='body'?(item.unit||''):''}`}
function pageGroupTitle(item:BookPage){const group=pageGroupNames[item.page_group as PageGroup];return item.page_group==='body'&&item.unit?`${group} · ${item.unit}`:group}

export default function App(){
 const path=location.pathname;
 const studentClass=path.match(/^\/classroom\/(\d+)(\/check)?$/);
 if(studentClass)return studentClass[2]?<DeviceCheck role="student" id={Number(studentClass[1])}/>:<Suspense fallback={<main className="admin-loading">正在加载课堂…</main>}><Classroom role="student" id={Number(studentClass[1])}/></Suspense>;
 const teacherClass=path.match(/^\/teacher\/classroom\/(\d+)(\/check)?$/);
 if(teacherClass)return teacherClass[2]?<DeviceCheck role="teacher" id={Number(teacherClass[1])}/>:<Suspense fallback={<main className="admin-loading">正在加载课堂…</main>}><Classroom role="teacher" id={Number(teacherClass[1])}/></Suspense>;
 if(path==='/teacher/login')return <TeacherLogin/>;
 if(path==='/teacher'||path.startsWith('/teacher/'))return <TeacherArea/>;
 if(path==='/account'||path.startsWith('/account/'))return <StudentArea/>;
 if(location.pathname==='/admin/login')return <AdminLogin/>;
 if(location.pathname==='/admin')return <Admin/>;
 const [state,setState]=useState<ShelfState>({kind:'loading'}),[selected,setSelected]=useState(routeBook());
 const load=useCallback(()=>{const ctrl=new AbortController();setState({kind:'loading'});booksAPI.list(ctrl.signal).then(books=>setState({kind:'ready',books})).catch(error=>{if(error.name!=='AbortError')setState({kind:'error',message:error.message})});return()=>ctrl.abort()},[]);
 useEffect(load,[load]);
 useEffect(()=>{const sync=()=>setSelected(routeBook());window.addEventListener('hashchange',sync);window.addEventListener('popstate',sync);return()=>{window.removeEventListener('hashchange',sync);window.removeEventListener('popstate',sync)}},[]);
 const books=state.kind==='ready'?state.books:[],book=books.find(item=>item.book_id===selected);
 return <main className={book?'':'student-shelf-page'}><StudentHeader onShelf={()=>{location.hash=''}}/>{book?<Reader book={book}/>:<><Shelf state={state} onRetry={load}/><StudentBottomNav active="shelf"/></>}</main>
}


function Shelf({state,onRetry}:{state:ShelfState;onRetry:()=>void}){
 return <div className="shelf"><div className="shelf-intro"><span className="eyebrow">MY LITTLE BOOKSHELF</span><h1>嗨，今天读哪一本？<span className="hello">☀</span></h1><p>选好课本，点一点，让英语开口说话。</p></div>
 <div className="grade-label"><span>我的英语课本</span><span>{state.kind==='ready'?`${state.books.length} 本`:'从后台加载'}</span></div>
 {state.kind==='loading'&&<Status icon={<BookOpen/>} title="正在加载书架..." detail="正在从后台获取书籍。"/>}
 {state.kind==='error'&&<Status icon={<BookOpen/>} title="书籍加载失败，请稍后重试" detail={state.message}><button onClick={onRetry}>重新加载</button></Status>}
 {state.kind==='ready'&&state.books.length===0&&<Status icon={<BookOpen/>} title="暂无书籍" detail="书架还是空的，新教材入库后会自动显示在这里。"/>}
 {state.kind==='ready'&&state.books.length>0&&<div className="books dynamic-books">{state.books.map(book=><button key={book.book_id} className="book-card available" onClick={()=>{location.hash=`read?book=${encodeURIComponent(book.book_id)}`}}><div className="book-art"><span className="book-tab">{book.semester||book.grade||'英语'}</span><div className="cover-text"><small>{[book.publisher,book.grade].filter(Boolean).join(' · ')||'ENGLISH'}</small><strong>{book.title}</strong><span>{book.subtitle}</span></div><div className="textbook-window">{book.cover?<img src={book.cover} alt={book.title}/>:<BookOpen size={64} aria-label="暂无封面"/>}</div><span className="sample-tag"><Sparkles size={14}/>点读教材</span></div><div className="book-info"><div><h2>{book.title}</h2><p>{book.publisher||book.description}</p></div><span className="round-arrow"><ArrowRight/></span></div></button>)}</div>}
 <div className="shelf-tip"><span className="tip-icon"><Volume2 size={25}/></span><div><strong>小手点单词，大声读出来</strong><p>点单词看意思，听整句学表达。按自己的节奏来就好。</p></div></div><footer>一点好奇心，一点新发现。</footer></div>
}

function Status({icon,title,detail,children}:{icon:React.ReactNode;title:string;detail:string;children?:React.ReactNode}){return <section className="empty-state" aria-live="polite"><span>{icon}</span><h2>{title}</h2><p>{detail}</p>{children}</section>}

function Reader({book}:{book:Book}){
 const [pages,setPages]=useState<BookPage[]|null>(null),[error,setError]=useState(''),[index,setIndex]=useState(0),[content,setContent]=useState<PageContent|null>(null),[retry,setRetry]=useState(0),[zoom,setZoom]=useState(false),[segmentIndex,setSegmentIndex]=useState(0),[word,setWord]=useState<Word|null>(null),[translated,setTranslated]=useState(false);
 const audio=useAudio(book.book_id,pages?.[index]?.page||0,book.audio?.available_accents??['en-US','en-GB']);
 useEffect(()=>{const ctrl=new AbortController();setError('');setPages(null);booksAPI.pages(book.book_id,ctrl.signal).then(items=>setPages(items.filter(item=>item.page_group in pageGroupNames))).catch(e=>{if(e.name!=='AbortError')setError(e.message)});return()=>ctrl.abort()},[book.book_id,retry]);
 const page=pages?.[index];
 useEffect(()=>{if(!page)return;const ctrl=new AbortController();setContent(null);setError('');booksAPI.page(book.book_id,page.page,ctrl.signal).then(setContent).catch(e=>{if(e.name!=='AbortError')setError(e.message)});return()=>ctrl.abort()},[book.book_id,page?.page,retry]);
 function move(next:number){if(!pages)return;audio.stop();setIndex(Math.max(0,Math.min(pages.length-1,next)));setSegmentIndex(0);setWord(null);setTranslated(false);setZoom(false)}
 function leave(){audio.stop();location.hash=''}
 if(error)return <div className="reader"><Status icon={<BookOpen/>} title="页面加载失败" detail={error}><button onClick={()=>setRetry(v=>v+1)}>重新加载</button> <button onClick={leave}>返回书架</button></Status></div>;
 if(!pages)return <div className="reader"><Status icon={<BookOpen/>} title="正在加载教材..." detail="正在获取目录。"/></div>;
 if(!pages.length)return <div className="reader"><Status icon={<BookOpen/>} title="这本书还没有可阅读页面" detail="请在后台为需要展示的页面设置分组。"><button onClick={leave}>返回书架</button></Status></div>;
 const segments=content?.segments||[],current=segments[segmentIndex];
 const groupKeys=[...new Set(pages.map(pageGroupKey))];
 return <div className="reader"><div className="reader-heading"><button className="back" onClick={leave}><ArrowLeft size={18}/> 我的书架</button><span>{book.title} <span className="slash">/</span> {page&&pageDisplayName(page,pages)}</span><select className="page-select" aria-label="选择课本页" value={index} onChange={e=>move(Number(e.target.value))}>{groupKeys.map(key=>{const first=pages.find(item=>pageGroupKey(item)===key)!;return <optgroup key={key} label={pageGroupTitle(first)}>{pages.map((item,i)=>pageGroupKey(item)===key&&<option key={item.page} value={i}>{pageOptionLabel(item,pages)}</option>)}</optgroup>})}</select><span className="sample-badge">{page?.interactive?'可点读':'课本浏览'}</span></div>
 <div className="lesson-heading"><div><span className="eyebrow">{page?.unit||pageGroupTitle(page!)}</span><h1>{page?.title&&page.title!==`第 ${page.page} 页`?page.title:pageDisplayName(page!,pages)}</h1></div><div className="page-arrows"><span className="page-position" aria-live="polite">{index+1} / {pages.length} 张</span><button disabled={index===0} aria-label="上一页" onClick={()=>move(index-1)}><ChevronLeft/></button><button disabled={index===pages.length-1} aria-label="下一页" onClick={()=>move(index+1)}><ChevronRight/></button></div></div>
 <div className="reading-grid"><section className="page-panel"><div className="panel-bar"><span><BookOpen size={17}/> 我的课本 · {index+1}/{pages.length}</span><button onClick={()=>setZoom(!zoom)}>{zoom?<ZoomOut size={19}/>:<ZoomIn size={19}/>} {zoom?'缩小':'放大'}</button></div><div className="page-scroll"><div className={`paper ${zoom?'zoomed':''}`}>{page&&<img src={page.image} alt={`${book.title} ${pageDisplayName(page,pages)}`}/>} {segments.map((segment,i)=><Hotspots key={segment.id} segment={segment} selected={segmentIndex===i} active={audio.active} onSentence={()=>{setSegmentIndex(i);setWord(null);audio.speak(segment.id)}} onWord={item=>{setSegmentIndex(i);setWord(item);audio.speak(item.id)}}/>)}</div></div><p className="paper-hint">点单词查意思 · 点小喇叭听整句 · 小字可放大</p></section>
 <aside className="study-column">{current?<section className="study-card">
  <div className="card-kicker"><span><Sparkles size={17}/> 一起读一读</span><span>{segmentIndex+1} / {segments.length} 段</span></div>
  <span className="section-label">{current.label}</span>
  <div className="big-sentence">{current.words.map(item=>hasSpeakableText(item.text)&&hasWordAudio(current)?<button key={item.id} className={word?.id===item.id?'picked':''} onClick={()=>{setWord(item);audio.speak(item.id)}}>{item.text}</button>:<span key={item.id}>{item.text}</span>)}</div>
  <div className="sentence-actions">{hasSentenceAudio(current)&&<button className="primary" onClick={()=>audio.active?audio.stop():audio.speak(current.id)}>{audio.active?<Square size={19}/>:<Volume2 size={20}/>} {audio.active?'停止朗读':'听整句'}</button>}<button className="translation-button" onClick={()=>setTranslated(!translated)}><Languages size={19}/>{translated?'收起翻译':'看翻译'}</button></div>
  {translated&&<p className="sentence-translation">{current.translation||'本段翻译尚待校对。'}</p>}
  {hasWordAudio(current)&&<div className={`word-card ${word?'has-word':''}`}>{word?<><div><small>这个词的意思</small><strong>{word.text}</strong>{word.phonetic&&<em className="word-phonetic">{word.phonetic}</em>}<span>{word.meaning||'词义尚待校对'}</span></div><button onClick={()=>audio.speak(word.id)}><Volume2/></button></>:<><span className="word-cursor">Aa</span><p>哪个单词有点陌生？<small>点一下上面的单词，意思就在这里。</small></p></>}</div>}
  <div className="sentence-nav"><button disabled={segmentIndex===0} onClick={()=>{setSegmentIndex(segmentIndex-1);setWord(null)}}><ChevronLeft/> 上一段</button><button disabled={segmentIndex===segments.length-1} onClick={()=>{setSegmentIndex(segmentIndex+1);setWord(null)}}>下一段 <ChevronRight/></button></div>
 </section>:<section className="unmarked-card" aria-live="polite"><span className="unmarked-icon"><BookOpen size={26}/></span><h2>{content?page&&pageDisplayName(page,pages):'正在打开这一页…'}</h2><p>{content?'这一页暂无点读内容，可以继续翻阅。':'正在准备这一页的点读内容和翻译。'}</p>{content&&index<pages.length-1&&<div className="unmarked-actions"><button onClick={()=>move(index+1)}>下一页继续 <ArrowRight size={19}/></button></div>}</section>}{segments.length>0&&<section className="sentence-list"><div className="list-heading"><strong>这一页，读这些</strong><span>{segments.length} 段</span></div>{segments.map((segment,i)=><button key={segment.id} className={segmentIndex===i?'current':''} onClick={()=>{setSegmentIndex(i);setWord(null);audio.speak(segment.id)}}><span className="line-number">{String(i+1).padStart(2,'0')}</span><span>{segment.text}</span>{segmentIndex===i?<Volume2 size={17}/>:<ChevronRight size={16}/>}</button>)}</section>}</aside></div>
 <section className="audio-dock"><div className="dock-title"><Headphones/><span>我的朗读设置<small>按你的节奏听</small></span></div>{audio.available.length>0&&<div className="setting"><span>发音</span><div className="choice-group">{audio.available.map(value=><button key={value} className={audio.accent===value?'active':''} onClick={()=>audio.setAccent(value)}>{audio.accent===value&&<Check size={13}/>} {value==='en-GB'?'英式':'美式'}</button>)}</div></div>}<div className="setting"><span>语速</span><div className="choice-group">{([0.25,0.5,0.75,1] as const).map(value=><button key={value} className={audio.rate===value?'active':''} onClick={()=>audio.setRate(value)}>{value}×</button>)}</div></div><button className="stop-button" aria-label="停止朗读" onClick={audio.stop}><Square/></button></section>{audio.error&&<p className="voice-status">{audio.error}</p>}</div>
}

function Hotspots({segment,selected,active,onSentence,onWord}:{segment:Segment;selected:boolean;active:string;onSentence:()=>void;onWord:(word:Word)=>void}){return <>{hasWordAudio(segment)&&segment.words.filter(word=>word.box&&hasSpeakableText(word.text)).map(word=><button key={word.id} className={`hotspot ${active===word.id?'speaking':''}`} style={{left:`${word.box![0]*100}%`,top:`${word.box![1]*100}%`,width:`${word.box![2]*100}%`,height:`${word.box![3]*100}%`,clipPath:word.polygon?`polygon(${word.polygon.map(([x,y])=>`${x*100}% ${y*100}%`).join(',')})`:undefined}} aria-label={`点读 ${word.text}`} onClick={()=>onWord(word)}/>)}{hasSentenceAudio(segment)&&segment.anchor&&<button className={`sentence-hotspot ${selected?'selected':''}`} style={{left:`${segment.anchor[0]*100}%`,top:`${segment.anchor[1]*100}%`}} aria-label={`朗读 ${segment.text}`} onClick={onSentence}><Volume2/></button>}</>}

function useAudio(bookId:string,page:number,available:Accent[]){
 const choose=useCallback(()=>{const preferred=(localStorage.getItem('reader-accent')||localStorage.getItem('preferredAccent')) as Accent|null;return (preferred&&available.includes(preferred)?preferred:available[0])||'en-US'},[available.join('|')]);
 const [accent,setAccent]=useState<Accent>(choose),[rate,setRate]=useState<0.25|0.5|0.75|1>(()=>{const saved=Number(localStorage.getItem('reader-rate-v3'));return ([0.25,0.5,0.75,1] as number[]).includes(saved)?saved as 0.25|0.5|0.75|1:1}),[active,setActive]=useState(''),[error,setError]=useState('');const player=useRef<HTMLAudioElement|null>(null);
 const stop=useCallback(()=>{player.current?.pause();player.current=null;setActive('')},[]);
 useEffect(()=>stop,[bookId,page,stop]);useEffect(()=>()=>stop(),[stop]);
 useEffect(()=>{const next=choose();if(next!==accent){stop();setAccent(next)}},[choose,accent,stop]);
 function speak(itemId:string){stop();setError('');if(!available.includes(accent)){setError('本书音频仍在生成中。');return}const audio=new Audio(`/api/v1/books/${encodeURIComponent(bookId)}/pages/${page}/audio/${encodeURIComponent(itemId)}?accent=${accent}`);audio.playbackRate=rate;audio.preservesPitch=true;player.current=audio;audio.onplaying=()=>setActive(itemId);audio.onended=()=>setActive('');audio.onerror=()=>{setActive('');setError('音频尚未生成或无法播放。')};void audio.play().catch(()=>setError('音频尚未生成或无法播放。'))}
 return {accent,available,rate,setRate:(value:0.25|0.5|0.75|1)=>{stop();setRate(value);localStorage.setItem('reader-rate-v3',String(value))},setAccent:(value:Accent)=>{if(!available.includes(value))return;stop();localStorage.setItem('reader-accent',value);setAccent(value)},active,error,speak,stop};
}
