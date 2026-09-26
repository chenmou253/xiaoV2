import {BookOpen,CalendarDays,UserRound,Video} from 'lucide-react';

export function StudentHeader({center=false,onShelf}:{center?:boolean;onShelf?:()=>void}){
 const brand=<><span className="brand-icon"><BookOpen size={25}/></span><span>小小点读家<small>LITTLE READERS CLUB</small></span></>;
 return <header className="topbar student-topbar">{onShelf?<button className="brand" onClick={onShelf} aria-label="回到书架">{brand}</button>:<a className="brand" href="/" aria-label="回到书架">{brand}</a>}<nav aria-label="主导航"><a href="/" aria-current={!center?'page':undefined}>点读书架</a><a href="/account" aria-current={center?'page':undefined}><UserRound size={17}/>学习中心</a></nav></header>;
}

export function StudentBottomNav({active}:{active:'shelf'|'courses'|'booking'|'account'}){
 const links=[{key:'shelf',href:'/',label:'书架',Icon:BookOpen},{key:'courses',href:'/account/lessons',label:'课程',Icon:CalendarDays},{key:'booking',href:'/account/booking',label:'预约',Icon:Video},{key:'account',href:'/account',label:'我的',Icon:UserRound}];
 return <nav className="student-bottom-nav" aria-label="主导航">{links.map(({key,href,label,Icon})=><a key={key} href={href} aria-current={active===key?'page':undefined}><Icon size={21}/><span>{label}</span></a>)}</nav>;
}
