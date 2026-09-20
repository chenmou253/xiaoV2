import { useRef, useState } from "react";
import type { Segment, Word } from "./api";

type Content = { segments: Segment[]; [key: string]: unknown };
type Accent = "en-US" | "en-GB";
type Props = {
  content: Content;
  image: string;
  draftId: string;
  page: number;
  editable: boolean;
  availableAccents: Accent[];
  onChange: (value: Content) => void;
  onCommit?: (value: Content) => Promise<void> | void;
  onRegenerateAudio?: (itemId: string, accent: Accent, kind: "sentence" | "word", text: string) => Promise<void> | void;
};
type Anchor = [number, number] | [number, number, number, number];
type AudioMode = NonNullable<Segment["audio_mode"]>;
type DragState = { kind: "box" | "size" | "anchor" | "anchor-size"; s: number; w: number; x: number; y: number; values: number[]; audioID?: string };
type DrawState = { x: number; y: number; endX: number; endY: number };

const clamp = (value: number, max = 1) => Math.max(0, Math.min(max, value));
const clampRange = (value: number, min: number, max: number) => Math.max(min, Math.min(max, value));
// A normal mouse/touch click can move a few pixels between pointerdown and
// pointerup. Keep that jitter as a click so the speaker hotspot still starts
// playback; only a deliberate, visible drag should suppress the click action.
const CLICK_MOVE_THRESHOLD = 0.01;
const uid = () => crypto.randomUUID().replaceAll("-", "").slice(0, 16);
const audioMode = (segment: Segment): AudioMode => segment.audio_mode || "sentence_and_words";
const hasSpellingErrorHint = (word: Word) => /拼写(?:错误|有误)/.test(word.meaning || "");
const anchorWithSize = (anchor?: Anchor): [number, number, number, number] => anchor?.length === 4 ? [...anchor] : [anchor?.[0] ?? 0.45, anchor?.[1] ?? 0.45, 0.03, 0.03];
const boundedAnchor = (anchor?: Anchor): [number, number, number, number] => {
  const [x, y, width, height] = anchorWithSize(anchor);
  const safeWidth = clamp(width), safeHeight = clamp(height);
  return [clamp(x, 1 - safeWidth), clamp(y, 1 - safeHeight), safeWidth, safeHeight];
};
const movedAnchor = (anchor: Anchor | undefined, x: number, y: number): Anchor => {
  if (anchor?.length !== 4) return [clamp(x), clamp(y)];
  const width = clamp(anchor[2]), height = clamp(anchor[3]);
  return [clamp(x, 1 - width), clamp(y, 1 - height), width, height];
};
function wordBoxFor(anchor: Anchor | undefined, index: number, total: number): [number, number, number, number] {
  const [x, y, width, height] = boundedAnchor(anchor);
  const boxWidth = Math.min(Math.max(width / Math.max(total, 1), 0.06), 0.24);
  const boxHeight = Math.min(Math.max(height, 0.035), 0.1);
  const gap = Math.max((width - boxWidth * total) / Math.max(total - 1, 1), 0);
  return [clamp(x + index * (boxWidth + gap), 1 - boxWidth), clamp(y, 1 - boxHeight), boxWidth, boxHeight];
}

export default function DraftPageEditor({ content, image, draftId, page, editable, availableAccents, onChange, onCommit, onRegenerateAudio }: Props) {
  const [si, setSI] = useState(0), [wi, setWI] = useState(0), [accent, setAccent] = useState<Accent>("en-US"), [zoom, setZoom] = useState(false);
  const [drag, setDrag] = useState<DragState | null>(null), [draw, setDraw] = useState<DrawState | null>(null);
  const [draggedWord, setDraggedWord] = useState<{ segmentID: string; wordID: string } | null>(null), [wordDropIndex, setWordDropIndex] = useState<number | null>(null);
  const [draggedSegmentID, setDraggedSegmentID] = useState<string | null>(null), [segmentDropIndex, setSegmentDropIndex] = useState<number | null>(null);
  const [inlineSegmentID, setInlineSegmentID] = useState<string | null>(null), [savingInline, setSavingInline] = useState(false), [playing, setPlaying] = useState(""), [audioError, setAudioError] = useState("");
  const [regeneratingAudio, setRegeneratingAudio] = useState("");
  const surface = useRef<HTMLDivElement>(null), player = useRef<HTMLAudioElement | null>(null), interactionMoved = useRef(false);
  const activeAccent = availableAccents.includes(accent) ? accent : availableAccents[0] || "en-US";
  const segments = content.segments || [], segment = segments[si], word = segment?.words[wi];
  const inlineSegment = inlineSegmentID ? segments.find((item) => item.id === inlineSegmentID) : null;
  const inlineIndex = inlineSegment ? segments.findIndex((item) => item.id === inlineSegment.id) : -1;
  const spellingErrorWords = segments.flatMap((item, segmentIndex) =>
    item.words.flatMap((current, wordIndex) =>
      hasSpellingErrorHint(current) ? [{ segmentIndex, wordIndex, word: current }] : [],
    ),
  );

  function setSegments(next: Segment[]) { onChange({ ...content, segments: next }); }
  function updateSegment(index: number, patch: Partial<Segment>) {
    const safePatch = patch.anchor ? { ...patch, anchor: boundedAnchor(patch.anchor) } : patch;
    setSegments(segments.map((item, current) => index === current ? { ...item, ...safePatch } : item));
  }
  function updateWord(segmentIndex: number, wordIndex: number, patch: Partial<Word>) {
    const words = segments[segmentIndex].words.map((item, current) => wordIndex === current ? { ...item, ...patch } : item);
    updateSegment(segmentIndex, { words, text: words.map((item) => item.text).join(" ") });
  }
  function pointerPoint(e: React.PointerEvent) {
    const rect = surface.current!.getBoundingClientRect();
    return { x: clamp((e.clientX - rect.left) / rect.width), y: clamp((e.clientY - rect.top) / rect.height) };
  }
  function begin(e: React.PointerEvent, kind: DragState["kind"], s: number, w = 0, audioID?: string) {
    if (!editable) {
      interactionMoved.current = false;
      return;
    }
    e.stopPropagation(); interactionMoved.current = false;
    const point = pointerPoint(e), item = segments[s];
    const values = kind === "anchor" || kind === "anchor-size" ? [...anchorWithSize(item.anchor)] : [...(item.words[w].box || [0.1, 0.1, 0.08, 0.03])];
    setSI(s); setWI(w); setDrag({ kind, s, w, ...point, values, audioID }); surface.current?.setPointerCapture(e.pointerId);
  }
  function beginDraw(e: React.PointerEvent) {
    if (!editable || !surface.current) return;
    if (e.target !== e.currentTarget && !(e.target instanceof HTMLImageElement)) return;
    const point = pointerPoint(e); setDraw({ ...point, endX: point.x, endY: point.y }); interactionMoved.current = false; surface.current.setPointerCapture(e.pointerId);
  }
  function move(e: React.PointerEvent) {
    if (!surface.current) return;
    if (draw) {
      const point = pointerPoint(e); if (Math.abs(point.x - draw.x) > CLICK_MOVE_THRESHOLD || Math.abs(point.y - draw.y) > CLICK_MOVE_THRESHOLD) interactionMoved.current = true; setDraw({ ...draw, endX: point.x, endY: point.y }); return;
    }
    if (!drag) return;
    const point = pointerPoint(e), dx = point.x - drag.x, dy = point.y - drag.y;
    if (Math.abs(dx) > CLICK_MOVE_THRESHOLD || Math.abs(dy) > CLICK_MOVE_THRESHOLD) interactionMoved.current = true;
    // Do not mutate OCR coordinates for normal click jitter. Previously even a
    // sub-threshold pointer move called onChange while pointerup still counted
    // as an audio click. The next page save then correctly saw changed content
    // and removed its now-stale audio, making preview appear to delete audio.
    if (!interactionMoved.current) return;
    if (drag.kind === "anchor") { const item = segments[drag.s]; updateSegment(drag.s, { anchor: movedAnchor(item.anchor, drag.values[0] + dx, drag.values[1] + dy) }); return; }
    const [x, y, width, height] = drag.values;
    if (drag.kind === "anchor-size") {
      updateSegment(drag.s, { anchor: [x, y, clamp(width + dx, 1 - x), clamp(height + dy, 1 - y)] });
      return;
    }
    const box: [number, number, number, number] = drag.kind === "size" ? [x, y, clamp(width + dx, 1 - x), clamp(height + dy, 1 - y)] : [clamp(x + dx, 1 - width), clamp(y + dy, 1 - height), width, height];
    updateWord(drag.s, drag.w, { box, polygon: undefined });
  }
  function finishPointer(e: React.PointerEvent) {
    const audioID = drag?.audioID;
    const shouldPlay = e.type === "pointerup" && Boolean(audioID) && !interactionMoved.current;
    if (draw) {
      const left = Math.min(draw.x, draw.endX), top = Math.min(draw.y, draw.endY), width = Math.abs(draw.endX - draw.x), height = Math.abs(draw.endY - draw.y);
      if (width >= 0.01 && height >= 0.01) {
        const newSegment: Segment = { id: uid(), label: "新增片段", text: "", translation: "", anchor: [left, top, width, height], words: [], audio_mode: "sentence_and_words" };
        const next = [...segments, newSegment]; setSegments(next); setSI(next.length - 1); setWI(0); setInlineSegmentID(newSegment.id);
      }
      setDraw(null);
    }
    setDrag(null);
    if (surface.current?.hasPointerCapture(e.pointerId)) surface.current.releasePointerCapture(e.pointerId);
    // Pointer capture deliberately retargets pointerup/click to the canvas so
    // dragging works outside a small OCR box. Start playback here for a real
    // click; waiting for the button's onClick makes the speaker appear dead.
    if (shouldPlay && audioID) play(audioID);
    interactionMoved.current = false;
  }
  function stop() { player.current?.pause(); player.current = null; setPlaying(""); }
  function play(id: string) {
    if (playing === id) { stop(); return; }
    stop(); setAudioError(""); if (!availableAccents.length) { setAudioError("当前页面没有启用可试听的口音"); return; }
    const audio = new Audio(`/api/v1/admin/drafts/${draftId}/pages/${page}/audio/${encodeURIComponent(id)}?accent=${activeAccent}`); player.current = audio;
    audio.onplaying = () => setPlaying(id); audio.onended = () => setPlaying(""); audio.onerror = () => { setPlaying(""); setAudioError("音频尚未生成或无法播放"); };
    void audio.play().catch(() => { setPlaying(""); setAudioError("音频尚未生成或无法播放"); });
  }
  // Playback is independent from the coordinate editor. A pointer gesture
  // may also move a hotspot, but releasing it must never make the speaker
  // appear dead because a drag flag leaked into the following click.
  function clickAudio(id: string) { interactionMoved.current = false; play(id); }
  async function regenerateAudio(itemId: string, targetAccent: Accent, kind: "sentence" | "word", text: string) {
    if (!onRegenerateAudio || regeneratingAudio) return;
    const key = `${itemId}:${targetAccent}`;
    setRegeneratingAudio(key);
    setAudioError("");
    try {
      await onRegenerateAudio(itemId, targetAccent, kind, text);
    } catch (error) {
      setAudioError((error as Error).message || "重新生成音频失败");
    } finally {
      setRegeneratingAudio("");
    }
  }
  function addSegment() {
    const next = [...segments, { id: uid(), label: "课文点读", text: "", translation: "", anchor: [0.45, 0.45, 0.03, 0.03] as [number, number, number, number], words: [], audio_mode: "sentence_and_words" as AudioMode }];
    setSegments(next); setSI(next.length - 1); setWI(0); setInlineSegmentID(next[next.length - 1].id);
  }
  function removeSegmentAt(index: number) {
    if (!editable || !segments[index] || !window.confirm(`确定删除第 ${index + 1} 个片段吗？`)) return;
    const removedID = segments[index].id, next = segments.filter((_, current) => current !== index);
    setSegments(next); setSI(Math.max(0, Math.min(si, next.length - 1))); setWI(0); if (inlineSegmentID === removedID) setInlineSegmentID(null);
  }
  function removeWordAt(segmentIndex: number, wordIndex: number) {
    if (!editable || !segments[segmentIndex]?.words[wordIndex]) return;
    const words = segments[segmentIndex].words.filter((_, current) => current !== wordIndex); updateSegment(segmentIndex, { words, text: words.map((item) => item.text).join(" ") });
    if (segmentIndex === si) setWI(Math.max(0, Math.min(wi, words.length - 1)));
  }
  function split() {
    if (!segment || wi === 0) return;
    const left = segment.words.slice(0, wi), right = segment.words.slice(wi), next = [...segments];
    next.splice(si, 1, { ...segment, words: left, text: left.map((item) => item.text).join(" ") }, { ...segment, id: uid(), translation: "", words: right, text: right.map((item) => item.text).join(" ") });
    setSegments(next); setSI(si + 1); setWI(0);
  }
  function merge() {
    const other = segments[si + 1]; if (!segment || !other) return;
    const words = [...segment.words, ...other.words], next = [...segments]; next.splice(si, 2, { ...segment, audio_mode: "sentence_and_words", words, text: words.map((item) => item.text).join(" "), translation: [segment.translation, other.translation].filter(Boolean).join(" ") }); setSegments(next);
  }
  function insertWord(segmentIndex: number, insertIndex: number) {
    const target = segments[segmentIndex]; if (!target) return;
    const safeIndex = Math.max(0, Math.min(insertIndex, target.words.length));
    const newWord: Word = { id: uid(), text: "", meaning: "", phonetic: "", box: wordBoxFor(target.anchor, safeIndex, target.words.length + 1) };
    const words = [...target.words]; words.splice(safeIndex, 0, newWord);
    updateSegment(segmentIndex, { words }); setSI(segmentIndex); setWI(safeIndex);
  }
  function addSelectedWord() {
    if (!segment) return;
    insertWord(si, segment.words.length ? Math.min(wi + 1, segment.words.length) : 0);
  }
  function addInlineWord() {
    if (inlineIndex < 0 || !inlineSegment) return;
    insertWord(inlineIndex, inlineSegment.words.length);
  }
  function wordDropPosition(e: React.DragEvent, index: number) {
    const rect = e.currentTarget.getBoundingClientRect();
    return e.clientX < rect.left + rect.width / 2 ? index : index + 1;
  }
  function reorderWord(segmentIndex: number, targetIndex: number) {
    const target = segments[segmentIndex];
    if (!target || !draggedWord || draggedWord.segmentID !== target.id) return;
    const sourceIndex = target.words.findIndex((item) => item.id === draggedWord.wordID);
    if (sourceIndex < 0) return;
    const words = [...target.words], [moved] = words.splice(sourceIndex, 1);
    const adjustedTarget = Math.max(0, Math.min(targetIndex - (sourceIndex < targetIndex ? 1 : 0), words.length));
    words.splice(adjustedTarget, 0, moved);
    updateSegment(segmentIndex, { words, text: words.map((item) => item.text).join(" ") });
    setSI(segmentIndex); setWI(adjustedTarget);
  }
  function finishWordDrag() {
    setDraggedWord(null); setWordDropIndex(null);
  }
  function applySegmentOrder(next: Segment[]) {
    const selectedID = segment?.id;
    setSegments(next);
    const selectedIndex = selectedID ? next.findIndex((item) => item.id === selectedID) : 0;
    setSI(Math.max(0, selectedIndex));
  }
  function segmentDropPosition(e: React.DragEvent, index: number) {
    const rect = e.currentTarget.getBoundingClientRect();
    return e.clientY < rect.top + rect.height / 2 ? index : index + 1;
  }
  function reorderSegment(targetIndex: number) {
    if (!draggedSegmentID) return;
    const sourceIndex = segments.findIndex((item) => item.id === draggedSegmentID);
    if (sourceIndex < 0) return;
    const next = [...segments], [moved] = next.splice(sourceIndex, 1);
    const adjustedTarget = Math.max(0, Math.min(targetIndex - (sourceIndex < targetIndex ? 1 : 0), next.length));
    next.splice(adjustedTarget, 0, moved);
    applySegmentOrder(next);
  }
  function moveSegment(index: number, offset: -1 | 1) {
    const targetIndex = index + offset;
    if (targetIndex < 0 || targetIndex >= segments.length) return;
    const next = [...segments];
    [next[index], next[targetIndex]] = [next[targetIndex], next[index]];
    applySegmentOrder(next);
  }
  function finishSegmentDrag() {
    setDraggedSegmentID(null); setSegmentDropIndex(null);
  }
  function tokenizeInlineWords() {
    if (inlineIndex < 0 || !inlineSegment || inlineSegment.words.length > 0) return;
    const tokens = inlineSegment.text.trim().split(/\s+/).filter(Boolean); if (!tokens.length) return;
    const words = tokens.map((text, index) => ({ id: uid(), text, meaning: "", phonetic: "", box: wordBoxFor(inlineSegment.anchor, index, tokens.length) }));
    updateSegment(inlineIndex, { words }); setSI(inlineIndex); setWI(0);
  }
  async function commitInline() {
    if (!inlineSegment || savingInline) return;
    setSavingInline(true);
    try {
      await onCommit?.({ ...content, segments });
      setInlineSegmentID(null);
    } catch {
      // The parent save handler reports the API error; keep the editor open for retry.
    } finally {
      setSavingInline(false);
    }
  }

  return <div className="visual-editor">
    <div className="action-row"><button type="button" onClick={() => setZoom(!zoom)}>{zoom ? "适应宽度" : "放大原图"}</button>{availableAccents.length > 1 && <label className="editor-accent-select">试听口音<select value={activeAccent} onChange={(e) => { stop(); setAudioError(""); setAccent(e.target.value as Accent); }}><option value="en-US">美式</option><option value="en-GB">英式</option></select></label>}{playing && <span className="editor-playing">正在播放，点击当前热区停止</span>}{audioError && <span className="editor-audio-error">{audioError}</span>}</div>
    {spellingErrorWords.length > 0 && <section className="translation-spelling-alert" role="alert" aria-live="polite">
      <strong>翻译模型提示 {spellingErrorWords.length} 个单词可能存在拼写错误，请逐个核对：</strong>
      <div>{spellingErrorWords.map(({ segmentIndex, wordIndex, word: current }) => <button type="button" key={`${segments[segmentIndex].id}:${current.id}`} onClick={() => { setSI(segmentIndex); setWI(wordIndex); }}>{current.text || "空单词"} <small>{current.meaning}</small></button>)}</div>
    </section>}
    <p className="editor-canvas-help">在原图空白处拖拽框选即可新增片段；拖动喇叭可移动整句，拖动橙色角块可调整整句范围。</p>
    <div className="editor-canvas-scroll"><div ref={surface} className={`editor-canvas ${zoom ? "zoom" : ""}`} onPointerDown={beginDraw} onPointerMove={move} onPointerUp={finishPointer} onPointerCancel={finishPointer}>
      <img src={image} alt="教材原图" draggable={false} />
      {draw && (() => { const left = Math.min(draw.x, draw.endX), top = Math.min(draw.y, draw.endY); return <div className="editor-draw-selection" style={{ left: `${left * 100}%`, top: `${top * 100}%`, width: `${Math.abs(draw.endX - draw.x) * 100}%`, height: `${Math.abs(draw.endY - draw.y) * 100}%` }} />; })()}
      {segments.map((item, x) => { const anchor = boundedAnchor(item.anchor), selected = x === si; return <div key={item.id}>
        {selected && <div className="editor-segment-outline" style={{ left: `${anchor[0] * 100}%`, top: `${anchor[1] * 100}%`, width: `${anchor[2] * 100}%`, height: `${anchor[3] * 100}%` }} />}
        {selected && editable && <span className="editor-resize editor-segment-resize" title="拖动调整整句范围" style={{ left: `${(anchor[0] + anchor[2]) * 100}%`, top: `${(anchor[1] + anchor[3]) * 100}%` }} onPointerDown={(e) => begin(e, "anchor-size", x)} />}
        {item.words.map((current, y) => current.box && <button type="button" key={current.id} className={`editor-box ${selected && y === wi ? "active " : ""}${current.ocr_needs_review ? "ocr-review" : ""}${hasSpellingErrorHint(current) ? " translation-spelling-review" : ""}${playing === current.id ? " playing" : ""}`} style={{ left: `${current.box[0] * 100}%`, top: `${current.box[1] * 100}%`, width: `${current.box[2] * 100}%`, height: `${current.box[3] * 100}%` }} onPointerDown={(e) => begin(e, "box", x, y, audioMode(item) === "none" ? undefined : current.id)} onClick={(e) => { if (audioMode(item) !== "none" && (!editable || e.detail === 0)) clickAudio(current.id); }} aria-label={audioMode(item) === "none" ? `选择 ${current.text || "单词"}` : `点读 ${current.text || "单词"}`}>
          {selected && y === wi && <span className="editor-overlay-delete word-delete" role="button" title="删除单词" onPointerDown={(e) => e.stopPropagation()} onClick={(e) => { e.preventDefault(); e.stopPropagation(); removeWordAt(x, y); }}>×</span>}
          {selected && y === wi && <span className="editor-resize" onPointerDown={(e) => begin(e, "size", x, y)} />}
        </button>)}
        {item.anchor && audioMode(item) === "sentence_and_words" && <button type="button" className={`editor-anchor ${item.words.length === 0 ? "missing-anchor" : ""}${playing === item.id ? " playing" : ""}`} title={item.words.length === 0 ? "新增片段：尚未定位，请拖到原图对应位置" : "点击播放整句，拖动调整位置"} style={{ left: `${anchor[0] * 100}%`, top: `${anchor[1] * 100}%` }} onPointerDown={(e) => begin(e, "anchor", x, 0, item.words.length ? item.id : undefined)} onClick={(e) => { setSI(x); setWI(0); if (item.words.length && (!editable || e.detail === 0)) clickAudio(item.id); }} aria-label={`选择 ${item.text || "整句"}`}>{item.words.length === 0 ? "未定位" : "♪"}</button>}
        {selected && editable && <button type="button" className="editor-overlay-delete segment-delete" style={{ left: `${(anchor[0] + anchor[2]) * 100}%`, top: `${anchor[1] * 100}%` }} title="删除片段" onPointerDown={(e) => e.stopPropagation()} onClick={(e) => { e.preventDefault(); e.stopPropagation(); removeSegmentAt(x); }}>×</button>}
      </div>; })}
      {inlineSegment && editable && (() => { const anchor = boundedAnchor(inlineSegment.anchor), inlineLeft = clampRange(anchor[0] + anchor[2] / 2, 0.18, 0.82), inlineTop = clampRange(anchor[1] + anchor[3] + 0.015, 0.02, 0.78); return <div className="editor-inline-editor" style={{ left: `${inlineLeft * 100}%`, top: `${inlineTop * 100}%` }} onPointerDown={(e) => e.stopPropagation()}>
        <div className="editor-inline-title"><strong>新增片段</strong><button type="button" aria-label="关闭临时编辑域" onClick={() => setInlineSegmentID(null)}>×</button></div>
        <label>片段英文<textarea rows={2} autoFocus value={inlineSegment.text} onChange={(e) => updateSegment(inlineIndex, { text: e.target.value })} /></label>
        <div className="editor-inline-words"><span>单词</span>{inlineSegment.words.map((current, index) => <div className="editor-inline-word" key={current.id}><input value={current.text} placeholder="英文单词" onChange={(e) => updateWord(inlineIndex, index, { text: e.target.value })} /><button type="button" title="删除单词" onClick={() => removeWordAt(inlineIndex, index)}>×</button></div>)}</div>
        <div className="editor-inline-actions"><button type="button" onClick={addInlineWord}>＋单词</button>{inlineSegment.words.length === 0 && <button type="button" onClick={tokenizeInlineWords} disabled={!inlineSegment.text.trim()}>按空格生成单词</button>}<button type="button" className="admin-primary" onClick={() => void commitInline()} disabled={savingInline}>{savingInline ? "保存中…" : "保存片段并关闭"}</button></div>
      </div>; })()}
    </div></div>
    <div className="action-row">
      <select value={si} onChange={(e) => { setSI(Number(e.target.value)); setWI(0); }}>
        {segments.map((item, index) => <option key={item.id} value={index}>{index + 1}. {item.text || "未命名片段"}</option>)}
      </select>
      <button type="button" disabled={!editable} onClick={addSegment}>新增片段</button>
      <button type="button" disabled={!editable || !segment} onClick={() => removeSegmentAt(si)}>删除片段</button>
      <button type="button" disabled={!editable || wi === 0} onClick={split}>拆分片段</button>
      <button type="button" disabled={!editable || si >= segments.length - 1} onClick={merge}>合并下一片段</button>
    </div>
    {segments.length > 1 && <details className="editor-segment-order">
      <summary>调整片段顺序 <span>{segments.length} 个片段</span></summary>
      <p>拖拽左侧标记调整顺序；也可以使用上移、下移按钮。片段坐标、单词和已有音频不会改变。</p>
      <div className="editor-segment-order-list" onDragOver={(e) => { if (!draggedSegmentID || e.target !== e.currentTarget) return; e.preventDefault(); setSegmentDropIndex(segments.length); }} onDrop={(e) => { if (!draggedSegmentID) return; e.preventDefault(); reorderSegment(segmentDropIndex ?? segments.length); finishSegmentDrag(); }}>
        {segments.map((item, index) => {
          const dropBefore = segmentDropIndex === index, dropAfter = segmentDropIndex === segments.length && index === segments.length - 1;
          return <div className={`editor-segment-order-item${item.id === segment?.id ? " active" : ""}${draggedSegmentID === item.id ? " dragging" : ""}${dropBefore ? " drop-before" : ""}${dropAfter ? " drop-after" : ""}`} draggable={editable} key={item.id} onDragStart={(e) => { e.dataTransfer.effectAllowed = "move"; e.dataTransfer.setData("text/plain", item.id); setDraggedSegmentID(item.id); setSegmentDropIndex(index); }} onDragOver={(e) => { if (!draggedSegmentID) return; e.preventDefault(); e.dataTransfer.dropEffect = "move"; setSegmentDropIndex(segmentDropPosition(e, index)); }} onDrop={(e) => { if (!draggedSegmentID) return; e.preventDefault(); e.stopPropagation(); reorderSegment(segmentDropPosition(e, index)); finishSegmentDrag(); }} onDragEnd={finishSegmentDrag}>
            <span className="editor-segment-drag-handle" title="拖拽调整片段顺序" aria-hidden="true">⠿</span>
            <button type="button" className="editor-segment-order-label" onClick={() => { setSI(index); setWI(0); }}><strong>{index + 1}.</strong> {item.text || "未命名片段"}</button>
            <span className="editor-segment-order-actions">
              <button type="button" disabled={!editable || index === 0} onClick={() => moveSegment(index, -1)} aria-label={`上移第 ${index + 1} 个片段`}>↑</button>
              <button type="button" disabled={!editable || index === segments.length - 1} onClick={() => moveSegment(index, 1)} aria-label={`下移第 ${index + 1} 个片段`}>↓</button>
            </span>
          </div>;
        })}
      </div>
    </details>}
    {segment && <fieldset disabled={!editable}>
      <label>片段名称<input value={segment.label} onChange={(e) => updateSegment(si, { label: e.target.value })} /></label>
      <label>音频模式<select value={audioMode(segment)} onChange={(e) => updateSegment(si, { audio_mode: e.target.value as AudioMode })}><option value="sentence_and_words">整句＋单词</option><option value="word_only">仅单词</option><option value="none">不生成音频</option></select></label>
      {audioMode(segment) === "word_only" && <p className="editor-audio-mode-note">本片段只生成单词点读，不生成整句音频。</p>}
      {audioMode(segment) === "none" && <p className="editor-audio-mode-note">本片段不生成整句或单词音频。</p>}
      <label>片段英文（可直接编辑）<textarea rows={3} value={segment.text} onChange={(e) => updateSegment(si, { text: e.target.value })} /></label>
      <label>整句翻译<textarea value={segment.translation || ""} onChange={(e) => updateSegment(si, { translation: e.target.value })} /></label>
      {onRegenerateAudio && audioMode(segment) === "sentence_and_words" && segment.text.trim() && availableAccents.length > 0 && <div className="editor-audio-regenerate">
        <span>整句音频</span>
        {availableAccents.map((itemAccent) => {
          const key = `${segment.id}:${itemAccent}`;
          return <button type="button" key={itemAccent} disabled={!editable || Boolean(regeneratingAudio)} onClick={() => void regenerateAudio(segment.id, itemAccent, "sentence", segment.text)}>
            {regeneratingAudio === key ? "正在生成…" : itemAccent === "en-US" ? "重新生成美音" : "重新生成英音"}
          </button>;
        })}
      </div>}
      {typeof segment.ocr_confidence === "number" && <p className={segment.ocr_needs_review ? "ocr-confidence review" : "ocr-confidence"}>本段 OCR 置信度：{Math.round(segment.ocr_confidence * 100)}%{segment.ocr_needs_review ? " · 建议人工核对" : ""}</p>}
      <div className="coordinate-grid">{["整句左", "整句上", "整句宽", "整句高"].map((label, index) => <label key={label}>{label}%<input type="number" min="0" max="100" step="0.1" value={Number((boundedAnchor(segment.anchor)[index] * 100).toFixed(2))} onChange={(e) => { const anchor = boundedAnchor(segment.anchor), value = Number(e.target.value) / 100; if (index === 0) anchor[0] = clamp(value, 1 - anchor[2]); else if (index === 1) anchor[1] = clamp(value, 1 - anchor[3]); else if (index === 2) anchor[2] = clamp(value, 1 - anchor[0]); else anchor[3] = clamp(value, 1 - anchor[1]); updateSegment(si, { anchor }); }} /></label>)}</div>
      <div className="editor-words" onDragOver={(e) => { if (!draggedWord || draggedWord.segmentID !== segment.id || e.target !== e.currentTarget) return; e.preventDefault(); setWordDropIndex(segment.words.length); }} onDrop={(e) => { if (!draggedWord || draggedWord.segmentID !== segment.id) return; e.preventDefault(); reorderWord(si, wordDropIndex ?? segment.words.length); finishWordDrag(); }}>
        {segment.words.map((current, index) => {
          const dropBefore = wordDropIndex === index, dropAfter = wordDropIndex === segment.words.length && index === segment.words.length - 1;
          return <span className={`editor-word-chip${draggedWord?.wordID === current.id ? " dragging" : ""}${dropBefore ? " drop-before" : ""}${dropAfter ? " drop-after" : ""}`} draggable={editable} key={current.id} onDragStart={(e) => { e.dataTransfer.effectAllowed = "move"; e.dataTransfer.setData("text/plain", current.id); setDraggedWord({ segmentID: segment.id, wordID: current.id }); setWordDropIndex(index); }} onDragOver={(e) => { if (!draggedWord || draggedWord.segmentID !== segment.id) return; e.preventDefault(); e.dataTransfer.dropEffect = "move"; setWordDropIndex(wordDropPosition(e, index)); }} onDrop={(e) => { if (!draggedWord || draggedWord.segmentID !== segment.id) return; e.preventDefault(); e.stopPropagation(); reorderWord(si, wordDropPosition(e, index)); finishWordDrag(); }} onDragEnd={finishWordDrag} title="拖拽调整单词顺序">
            <span className="editor-word-drag-handle" aria-hidden="true">⠿</span>
            <button type="button" className={`${index === wi ? "active " : ""}${current.ocr_needs_review ? "ocr-review" : ""}${hasSpellingErrorHint(current) ? "translation-spelling-review" : ""}`} onClick={() => setWI(index)}>{current.text || "新单词"}{hasSpellingErrorHint(current) ? " ⚠" : ""}</button>
          </span>;
        })}
        <button type="button" className="editor-add-word" onDragOver={(e) => { if (!draggedWord || draggedWord.segmentID !== segment.id) return; e.preventDefault(); setWordDropIndex(segment.words.length); }} onDrop={(e) => { if (!draggedWord || draggedWord.segmentID !== segment.id) return; e.preventDefault(); e.stopPropagation(); reorderWord(si, segment.words.length); finishWordDrag(); }} onClick={addSelectedWord}>＋单词</button>
      </div>
      <p className="editor-words-help">点击“＋单词”会插入到当前单词后；拖拽单词左侧标记可调整顺序。</p>
      {word && <>
        {typeof word.ocr_confidence === "number" && <p className={word.ocr_needs_review ? "ocr-confidence review" : "ocr-confidence"}>PaddleOCR 置信度：{Math.round(word.ocr_confidence * 100)}%{word.ocr_needs_review ? " · 建议人工核对" : ""}</p>}
        {hasSpellingErrorHint(word) && <p className="translation-spelling-word-warning"><strong>⚠ 翻译模型提示拼写错误</strong>：请核对英文、词义和音标。当前返回：{word.meaning}</p>}
        <div className="form-grid"><label>英文<input value={word.text} onChange={(e) => updateWord(si, wi, { text: e.target.value })} /></label><label>词义<input value={word.meaning || ""} onChange={(e) => updateWord(si, wi, { meaning: e.target.value })} /></label><label>音标<input value={word.phonetic || ""} onChange={(e) => updateWord(si, wi, { phonetic: e.target.value })} /></label></div>
        {onRegenerateAudio && audioMode(segment) !== "none" && word.text.trim() && availableAccents.length > 0 && <div className="editor-audio-regenerate">
          <span>当前单词音频</span>
          {availableAccents.map((itemAccent) => {
            const key = `${word.id}:${itemAccent}`;
            return <button type="button" key={itemAccent} disabled={!editable || Boolean(regeneratingAudio)} onClick={() => void regenerateAudio(word.id, itemAccent, "word", word.text)}>
              {regeneratingAudio === key ? "正在生成…" : itemAccent === "en-US" ? "重新生成美音" : "重新生成英音"}
            </button>;
          })}
        </div>}
        <div className="coordinate-grid">{["左", "上", "宽", "高"].map((label, index) => <label key={label}>{label}%<input type="number" min="0" max="100" step="0.1" value={Number(((word.box?.[index] || 0) * 100).toFixed(2))} onChange={(e) => { const box = [...(word.box || [0.1, 0.1, 0.08, 0.03])] as [number, number, number, number]; box[index] = Number(e.target.value) / 100; updateWord(si, wi, { box }); }} /></label>)}</div>
        <button type="button" onClick={() => removeWordAt(si, wi)}>删除单词</button>
      </>}
    </fieldset>}
  </div>;
}
