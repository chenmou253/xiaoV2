export type APIResponse<T> = {code:number; message:string; data:T};

export class APIError extends Error {
  constructor(public status:number, message:string){super(message)}
}

export async function api<T>(path:string, options?:RequestInit|AbortSignal):Promise<T>{
  const init:RequestInit=options instanceof AbortSignal?{signal:options}:{...(options||{})};
  const headers=new Headers(init.headers);
  if(init.method&&!['GET','HEAD'].includes(init.method.toUpperCase()))headers.set('X-Requested-With','xiaov2-web');
  if(init.body&&!(init.body instanceof FormData)&&!headers.has('Content-Type'))headers.set('Content-Type','application/json');
  const response=await fetch(`/api/v1${path}`,{...init,headers,cache:'no-store',credentials:'same-origin'});
  const payload=await response.json().catch(()=>null) as APIResponse<T>|null;
  if(!response.ok||!payload||payload.code!==0)throw new APIError(response.status,payload?.message||'服务器返回了无效响应');
  return payload.data;
}

export type Accent='en-US'|'en-GB';
export type BookAudio={available_accents:Accent[];default_accent:Accent|''};
export type Book={book_id:string;title:string;subtitle:string;description:string;publisher:string;grade:string;semester:string;cover:string;page_count:number;audio:BookAudio};
export type BookPage={book_id:string;page:number;printed_page:number|null;title:string;unit:string;image:string;interactive:boolean};
export type Word={id:string;text:string;meaning?:string;phonetic?:string;box?:[number,number,number,number];polygon?:[number,number][];ocr_confidence?:number;ocr_needs_review?:boolean;translation_status?:string;translation_failure_reason?:string};
export type Segment={id:string;label:string;text:string;translation?:string;anchor?:[number,number]|[number,number,number,number];words:Word[];audio_mode?:'sentence_and_words'|'word_only'|'none';ocr_confidence?:number;ocr_needs_review?:boolean;translation_status?:string;translation_failure_reason?:string};
export type PageContent=BookPage&{segments:Segment[]};
export type Identity={id:number;email:string;kind:'student'|'admin';permissions:string[]};
export type SiteConfig={email_enabled:boolean;email_mode:'local'|'smtp'|'disabled'};
export type SiteSetting={id:number;dict_code:string;item_label:string;item_value:string;sort:number;status:0|1;remark:string|null;created_at:string;updated_at:string};
export type AIModel={id:string;name:string;type:'ocr'|'translation'|'tts';provider:string;enabled:boolean;cloud:boolean;available:boolean;unavailable_reason?:string;capabilities:string[];default_voice?:string;retry_policy:string};
export type AIVoice={id:string;name:string;display_name:string};
export type AIModelSettings={ocr_model:string;translation_model:string;tts_model:string;tts_voice:string};

export const booksAPI={
  list:(signal?:AbortSignal)=>api<Book[]>('/books',signal),
  pages:(bookId:string,signal?:AbortSignal)=>api<BookPage[]>(`/books/${encodeURIComponent(bookId)}/pages`,signal),
  page:(bookId:string,page:number,signal?:AbortSignal)=>api<PageContent>(`/books/${encodeURIComponent(bookId)}/pages/${page}`,signal),
};
