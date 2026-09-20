import { useEffect, useState } from "react";
import { api, type Identity } from "./api";

type Row = Record<string, any>;

type ReviewQueueItem = {
  draft_id: string;
  book_id: string;
  title: string;
  page: number;
  issue_count: number;
  oldest_issue_at: string;
};

const audioReasonLabels: Record<string, string> = {
  asr_text_mismatch: "语音识别结果与原文不一致",
  phoneme_alignment_low: "音素对齐分数偏低",
  trailing_silence_too_long: "结尾静音过长",
  leading_silence_too_long: "开头静音过长",
  clipping_detected: "音频存在削波失真",
  audio_too_short: "音频过短",
  audio_too_long: "音频过长",
};

export default function AudioReviewPage({
  me,
  run,
  notice,
  refreshToken,
}: {
  me: Identity;
  run: (work: () => Promise<void>) => Promise<void>;
  notice: (message: string) => void;
  refreshToken: number;
}) {
  const query = new URLSearchParams(location.search);
  const draftId = query.get("draft") || "";
  const page = Number(query.get("page"));
  const [queue, setQueue] = useState<ReviewQueueItem[]>([]);
  const [detail, setDetail] = useState<any>(null);
  const [issues, setIssues] = useState<Row[]>([]);
  const [ttsModels, setTTSModels] = useState<Row[]>([]);
  const [voiceOptions, setVoiceOptions] = useState<Record<string, Row[]>>({});
  const [retrySettings, setRetrySettings] = useState<
    Record<string, { model_id: string; voice_id: string }>
  >({});
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");
  const canWrite = me.permissions.includes("content.write");

  async function reload() {
    setLoading(true);
    setError("");
    try {
      if (!draftId || !Number.isSafeInteger(page) || page < 1) {
        setQueue((await api<ReviewQueueItem[]>("/admin/audio-review")) || []);
        setDetail(null);
        setIssues([]);
        return;
      }
      const [status, nextIssues, models] = await Promise.all([
        api<Row>(`/admin/drafts/${encodeURIComponent(draftId)}/status`),
        api<Row[]>(`/admin/drafts/${encodeURIComponent(draftId)}/pages/${page}/audio-issues`),
        api<Row[]>(`/admin/drafts/${encodeURIComponent(draftId)}/models`),
      ]);
      setDetail({
        draft: {
          id: status.draft_id,
          book_id: status.book_id,
          title: status.title,
          status: status.draft_status,
          version: status.revision,
        },
        jobs: status.jobs || [],
      });
      setIssues(nextIssues || []);
      setTTSModels((models || []).filter((model) => model.type === "tts"));
    } catch (cause) {
      setError((cause as Error).message);
    } finally {
      setLoading(false);
    }
  }

  useEffect(() => {
    void reload();
  }, [draftId, page, refreshToken]);

  async function loadVoices(modelID: string) {
    if (!modelID || voiceOptions[modelID]) return;
    try {
      const values = await api<Row[]>(
        `/admin/drafts/${encodeURIComponent(draftId)}/models/${encodeURIComponent(modelID)}/voices`,
      );
      setVoiceOptions((current) => ({ ...current, [modelID]: values || [] }));
    } catch (cause) {
      setError((cause as Error).message);
    }
  }

  useEffect(() => {
    for (const modelID of new Set(issues.map((issue) => issue.model_id).filter(Boolean))) {
      void loadVoices(modelID);
    }
  }, [issues]);

  const active = !!detail?.jobs?.some(
    (job: Row) => job.status === "queued" || job.status === "running",
  );
  useEffect(() => {
    if (!draftId || !active) return;
    let stopped = false;
    let timer = 0;
    const poll = async () => {
      try {
        const status = await api<Row>(
          `/admin/drafts/${encodeURIComponent(draftId)}/status`,
        );
        if (stopped) return;
        setDetail((current: any) =>
          current
            ? {
                ...current,
                draft: {
                  ...current.draft,
                  status: status.draft_status,
                  version: status.revision,
                },
                jobs: status.jobs || [],
              }
            : current,
        );
        const stillActive = (status.jobs || []).some(
          (job: Row) => job.status === "queued" || job.status === "running",
        );
        if (!stillActive) {
          await reload();
          return;
        }
      } catch (cause) {
        if (!stopped) setError((cause as Error).message);
      }
      if (!stopped) timer = window.setTimeout(poll, 2000);
    };
    timer = window.setTimeout(poll, 1000);
    return () => {
      stopped = true;
      window.clearTimeout(timer);
    };
  }, [draftId, page, active]);

  function openReview(targetDraft: string, targetPage: number) {
    const params = new URLSearchParams({
      tab: "audio-review",
      draft: targetDraft,
      page: String(targetPage),
    });
    window.location.assign(`/admin?${params.toString()}`);
  }

  function backToEditor() {
    const params = new URLSearchParams({ tab: "drafts" });
    if (draftId) params.set("draft", draftId);
    if (Number.isSafeInteger(page) && page > 0) {
      params.set("page", String(page));
    }
    window.location.assign(`/admin?${params.toString()}`);
  }

  function retryKey(issue: Row) {
    return `${issue.item_id}-${issue.accent}`;
  }

  function settingFor(issue: Row) {
    return retrySettings[retryKey(issue)] || {
      model_id: issue.model_id || "",
      voice_id: issue.voice_id || "",
    };
  }

  function updateRetrySetting(
    issue: Row,
    next: { model_id: string; voice_id: string },
  ) {
    setRetrySettings((current) => ({ ...current, [retryKey(issue)]: next }));
    if (next.model_id) void loadVoices(next.model_id);
  }

  async function retry(issue: Row) {
    if (!detail?.draft) return;
    const setting = settingFor(issue);
    await run(async () => {
      await api(
        `/admin/drafts/${encodeURIComponent(draftId)}/pages/${page}/audio-issues/${encodeURIComponent(issue.item_id)}/retry`,
        {
          method: "POST",
          body: JSON.stringify({
            accent: issue.accent,
            model_id: setting.model_id,
            voice_id: setting.voice_id,
            version: detail.draft.version,
          }),
        },
      );
      await reload();
      notice(
        `${issue.text || issue.item_id}（${issue.accent === "en-US" ? "美式" : "英式"}）已进入优先处理队列`,
      );
    });
  }

  async function approve(issue: Row) {
    if (
      !detail?.draft ||
      !confirm(
        `确认已经试听并人工通过“${issue.text || issue.item_id}”的${issue.accent === "en-US" ? "美式" : "英式"}失败候选音频吗？`,
      )
    ) {
      return;
    }
    await run(async () => {
      await api(
        `/admin/drafts/${encodeURIComponent(draftId)}/pages/${page}/audio-issues/${encodeURIComponent(issue.item_id)}/approve`,
        {
          method: "POST",
          body: JSON.stringify({
            accent: issue.accent,
            version: detail.draft.version,
          }),
        },
      );
      await reload();
      notice("该音频已人工确认，并已更新正式音频状态");
    });
  }

  if (!draftId || !Number.isSafeInteger(page) || page < 1) {
    return (
      <section className="admin-panel audio-review-home">
        <h2>待人工审核</h2>
        <p className="admin-note">
          这里只读取数据库中的失败状态，不加载整本页面内容或扫描音频清单。
        </p>
        {error && <p className="admin-error">{error}</p>}
        {loading ? (
          <p>正在加载审核队列…</p>
        ) : queue.length === 0 ? (
          <p className="audio-issue-success">当前没有需要人工处理的音频。</p>
        ) : (
          <div className="review-queue-list">
            {queue.map((item) => (
              <button
                key={`${item.draft_id}-${item.page}`}
                onClick={() => openReview(item.draft_id, item.page)}
              >
                <strong>{item.title}</strong>
                <span>
                  {item.book_id} · 第 {item.page} 页
                </span>
                <b>{item.issue_count} 项待处理</b>
              </button>
            ))}
          </div>
        )}
      </section>
    );
  }

  const writable =
    canWrite && ["draft", "failed", "audio"].includes(detail?.draft?.status || "");
  return (
    <>
      {error && <p className="admin-error">{error}</p>}
      {detail?.draft && (
        <section className="admin-panel audio-review-context">
          <p className="eyebrow">{detail.draft.title || detail.draft.id}</p>
          <h2>第 {page} 页 · 音频异常处理</h2>
          <p>只加载当前页失败项；单项重生成使用最高任务优先级。</p>
        </section>
      )}
      {loading && !detail ? (
        <p>正在加载审核项…</p>
      ) : (
        <AudioIssueReview
          draftId={draftId}
          page={page}
          issues={issues}
          writable={writable}
          ttsModels={ttsModels}
          voiceOptions={voiceOptions}
          onBack={backToEditor}
          onRetry={retry}
          onApprove={approve}
          settingFor={settingFor}
          onChangeRetrySetting={updateRetrySetting}
        />
      )}
    </>
  );
}

function AudioIssueReview({
  draftId,
  page,
  issues,
  writable,
  ttsModels,
  voiceOptions,
  onBack,
  onRetry,
  onApprove,
  settingFor,
  onChangeRetrySetting,
}: {
  draftId: string;
  page: number;
  issues: Row[];
  writable: boolean;
  ttsModels: Row[];
  voiceOptions: Record<string, Row[]>;
  onBack: () => void;
  onRetry: (issue: Row) => Promise<void>;
  onApprove: (issue: Row) => Promise<void>;
  settingFor: (issue: Row) => { model_id: string; voice_id: string };
  onChangeRetrySetting: (
    issue: Row,
    value: { model_id: string; voice_id: string },
  ) => void;
}) {
  return (
    <section className="audio-issue-review">
      <header>
        <div>
          <h3>音频异常处理 · 未解决 {issues.length} 项</h3>
          <p>这里只处理当前页面失败的句子或单词，不会重新排队整页音频。</p>
        </div>
        <button onClick={onBack}>返回教材编辑</button>
      </header>
      {issues.length === 0 ? (
        <div className="audio-issue-success">
          所有异常音频均已处理完成，可以返回页面继续试听和审核。
        </div>
      ) : (
        <div className="audio-issue-list">
          {issues.map((issue) => {
            const active =
              issue.status === "queued" || issue.status === "running";
            const accentLabel = issue.accent === "en-US" ? "美式" : "英式";
            const setting = settingFor(issue);
            const model = ttsModels.find((item) => item.id === setting.model_id);
            const voices = voiceOptions[setting.model_id] || [];
            const failedSource = `/api/v1/admin/drafts/${encodeURIComponent(draftId)}/pages/${page}/audio-issues/${encodeURIComponent(issue.item_id)}/failed?accent=${encodeURIComponent(issue.accent)}`;
            return (
              <article
                className="audio-issue-row"
                key={`${issue.item_id}-${issue.accent}`}
              >
                <div className="audio-issue-copy">
                  <strong>{issue.text || issue.item_id}</strong>
                  <span>
                    {issue.kind === "word" ? "单词" : "整句"} · {accentLabel} ·{" "}
                    {issue.item_id}
                  </span>
                  <ul>
                    {(issue.reasons || []).map((reason: string) => (
                      <li key={reason}>{audioReasonLabels[reason] || reason}</li>
                    ))}
                  </ul>
                  <small>
                    QA 分数：{Number(issue.final_score || 0).toFixed(3)} · 已自动尝试{" "}
                    {issue.attempt || 0} 次
                  </small>
                </div>
                <div className="audio-issue-controls">
                  {issue.has_candidate ? (
                    <label>
                      失败候选试听
                      <audio controls preload="none" src={failedSource} />
                    </label>
                  ) : (
                    <span className="admin-error-inline">
                      没有可试听的失败文件，只能重新生成
                    </span>
                  )}
                  <div className="audio-retry-settings">
                    <label>
                      重试模型
                      <select
                        value={setting.model_id}
                        disabled={!writable || active}
                        onChange={(event) => {
                          const nextModel = ttsModels.find(
                            (item) => item.id === event.target.value,
                          );
                          onChangeRetrySetting(issue, {
                            model_id: event.target.value,
                            voice_id:
                              nextModel?.id === "local-qwen3-tts" && issue.accent === "en-GB"
                                ? "ryan"
                                : nextModel?.default_voice || "",
                          });
                        }}
                      >
                        {!model && setting.model_id && (
                          <option value={setting.model_id}>当前模型（不可用）</option>
                        )}
                        {ttsModels.filter((item) => item.enabled).map((item) => (
                          <option key={item.id} value={item.id} disabled={!item.available}>
                            {item.name}
                            {item.available ? "" : `（${item.unavailable_reason || "不可用"}）`}
                          </option>
                        ))}
                      </select>
                    </label>
                    <label>
                      重试音色
                      <select
                        value={setting.voice_id}
                        disabled={!writable || active || !setting.model_id || voices.length === 0}
                        onChange={(event) =>
                          onChangeRetrySetting(issue, {
                            ...setting,
                            voice_id: event.target.value,
                          })
                        }
                      >
                        {!setting.voice_id && <option value="">请选择音色</option>}
                        {voices.map((voice) => (
                          <option key={voice.id} value={voice.id}>
                            {voice.display_name || voice.name || voice.id}
                          </option>
                        ))}
                      </select>
                    </label>
                    <small>仅本条重试生效，不会修改草稿或页面的默认模型。</small>
                  </div>
                  <div className="action-row">
                    <button
                      disabled={!writable || active}
                      onClick={() => void onRetry(issue)}
                    >
                      {active
                        ? issue.status === "queued"
                          ? "等待重新生成…"
                          : "正在重新生成…"
                        : "只重新生成这一项"}
                    </button>
                    <button
                      disabled={!writable || active || !issue.has_candidate}
                      onClick={() => void onApprove(issue)}
                    >
                      人工试听通过
                    </button>
                  </div>
                </div>
              </article>
            );
          })}
        </div>
      )}
    </section>
  );
}
