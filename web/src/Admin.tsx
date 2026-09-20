import { useEffect, useRef, useState } from "react";
import { api, type AIModel, type AIModelSettings, type AIVoice, type Book, type Identity, type SiteSetting } from "./api";
import AudioReviewPage from "./AudioReviewPage";
import DraftPageEditor from "./DraftPageEditor";
import "./admin-job-progress.css";

type Row = Record<string, any>;
type Tab =
  | "drafts"
  | "audio-review"
  | "books"
  | "site-settings"
  | "students"
  | "rbac"
  | "audit";
const tabs: [Tab, string, string][] = [
  ["drafts", "教材制作与审核", "content.read"],
  ["audio-review", "人工审核", "content.read"],
  ["books", "已发布教材", "content.read"],
  ["site-settings", "网站设置", "site_settings.read"],
  ["students", "学生账号", "users.read"],
  ["rbac", "角色与权限", "rbac.read"],
  ["audit", "操作日志", "audit.read"],
];

const jobKindLabels: Record<string, string> = {
  ocr: "单页 OCR（含坐标）",
  translate: "本页缺失翻译补全",
  page: "单页 OCR + 英美音频（旧任务）",
  pdf: "PDF 转换 / OCR",
  audio: "本页音频生成",
  "audio-replace": "清除并重新生成本页音频",
  "audio-item": "单项音频重新生成",
  "audio-missing": "整本生成缺失音频",
  "audio-accent": "整本重新生成指定口音",
  "audio-failed": "整本重试失败音频",
};
const jobStatusLabels: Record<string, string> = {
  queued: "等待处理",
  running: "处理中",
  completed: "已完成",
  failed: "失败",
  issues: "有待处理音频",
  manual_review_required: "需要人工复核",
  cancelled: "已终止",
};

function readableJobError(error: string) {
  if (error.includes("required command is missing: pdfinfo"))
    return "服务器缺少 PDF 分析工具 pdfinfo（Poppler），任务尚未开始转换。";
  if (error.includes("required command is missing: pdftoppm"))
    return "服务器缺少 PDF 转图片工具 pdftoppm（Poppler），任务尚未开始转换。";
  if (error.includes("PaddleOCR is not installed"))
    return "服务器的项目 Python 环境尚未安装 PaddleOCR。";
  if (
    error.includes("No module named 'onnxruntime'") ||
    error.includes("No module named 'soundfile'") ||
    error.includes("No module named 'kokoro_onnx'")
  )
    return "服务器的项目 Python 环境尚未安装完整的音频依赖。";
  if (error.includes("kokoro-v1.0.onnx") || error.includes("voices-v1.0.bin"))
    return "找不到 Kokoro 音频模型文件，请检查 AUDIO_MODEL_DIR。";
  if (
    error.includes("PADDLE") ||
    error.includes("paddlex") ||
    error.includes("PaddleOCR")
  )
    return "PaddleOCR 初始化或识别失败，请展开技术错误检查模型下载、网络或运行环境。";
  return error;
}

export default function Admin() {
  const [me, setMe] = useState<Identity | null>(null),
    [ready, setReady] = useState(false);
  const [tab, setTab] = useState<Tab>(() => {
    const requested = new URLSearchParams(location.search).get("tab");
    return tabs.find((item) => item[0] === requested)?.[0] || "drafts";
  }),
    [data, setData] = useState<any>(null);
  const [refreshToken, setRefreshToken] = useState(0);
  const [busy, setBusy] = useState(false),
    [error, setError] = useState(""),
    [notice, setNotice] = useState("");
  const loadID = useRef(0);
  const can = (permission: string) => !!me?.permissions.includes(permission);
  useEffect(() => {
    api<{ user: Identity | null }>("/admin/me")
      .then(({ user }) => {
        setMe(user);
        const requested = new URLSearchParams(location.search).get("tab");
        const first =
          tabs.find(
            (x) => x[0] === requested && user?.permissions.includes(x[2]),
          ) || tabs.find((x) => user?.permissions.includes(x[2]));
        if (first) setTab(first[0]);
      })
      .catch((e) => setError(e.message))
      .finally(() => setReady(true));
  }, []);
  useEffect(() => {
    if (!notice) return;
    const timer = window.setTimeout(() => setNotice(""), 8000);
    return () => window.clearTimeout(timer);
  }, [notice]);
  useEffect(() => {
    const url = new URL(window.location.href);
    url.searchParams.set("tab", tab);
    window.history.replaceState(
      window.history.state,
      "",
      `${url.pathname}${url.search}${url.hash}`,
    );
  }, [tab]);
  async function load(target = tab) {
    const request = ++loadID.current;
    setBusy(true);
    setError("");
    setData(null);
    try {
      let next: any = null;
      if (target === "site-settings")
        next = (await api<SiteSetting[]>("/admin/site-settings")) || [];
      else if (target === "books")
        next = (await api<Book[]>("/admin/books")) || [];
      else if (target === "students")
        next = (await api<Row[]>("/admin/students")) || [];
      else if (target === "rbac") {
        const value = await api<Row>("/admin/rbac");
        next = {
          roles: value?.roles || [],
          permissions: value?.permissions || [],
          role_permissions: value?.role_permissions || [],
          admin_roles: value?.admin_roles || [],
          admins: value?.admins || [],
        };
      } else if (target === "audit")
        next = (await api<Row[]>("/admin/audit")) || [];
      if (request === loadID.current) setData(next);
    } catch (e) {
      if (request === loadID.current) {
        setData(null);
        setError((e as Error).message);
      }
    } finally {
      if (request === loadID.current) setBusy(false);
    }
  }
  useEffect(() => {
    if (me) void load(tab);
  }, [tab, me]);
  async function run(work: () => Promise<void>) {
    setBusy(true);
    setError("");
    setNotice("");
    try {
      await work();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }
  async function runResult(work: () => Promise<void>): Promise<boolean> {
    setBusy(true);
    setError("");
    setNotice("");
    try {
      await work();
      return true;
    } catch (e) {
      setError((e as Error).message);
      return false;
    } finally {
      setBusy(false);
    }
  }
  if (!ready) return <main className="admin-loading">正在验证管理权限…</main>;
  if (!me?.permissions.includes("admin.access"))
    return (
      <main className="account-shell">
        <section className="account-card">
          <h1>管理后台</h1>
          <p>{error || "请使用管理员账号登录。"}</p>
          <a className="admin-primary" href="/admin/login">
            管理员登录
          </a>
        </section>
      </main>
    );
  return (
    <main className="admin-shell">
      <aside className="admin-sidebar">
        <a className="admin-brand" href="/">
          小小点读家<span>管理工作台</span>
        </a>
        <nav>
          {tabs
            .filter((x) => can(x[2]))
            .map((x) => (
              <button
                key={x[0]}
                className={tab === x[0] ? "active" : ""}
                onClick={() => {
                  setData(null);
                  setTab(x[0]);
                }}
              >
                {x[1]}
              </button>
            ))}
        </nav>
        <small>{me.email}</small>
        <button
          onClick={() =>
            void run(async () => {
              await api("/admin/auth/logout", { method: "POST" });
              location.assign("/admin/login");
            })
          }
        >
          退出
        </button>
      </aside>
      <section className="admin-main">
        <header className="admin-header">
          <div>
            <span className="eyebrow">XIAOV2 / ADMIN</span>
            <h1>{tabs.find((x) => x[0] === tab)?.[1]}</h1>
          </div>
          <button onClick={() => tab === "audio-review" ? setRefreshToken((value) => value + 1) : void load()}>刷新</button>
        </header>
        {busy && <p>正在处理…</p>}
        {error && <p className="admin-error">{error}</p>}
        {notice && <p className="admin-success admin-toast" role="status" aria-live="polite">{notice}</p>}
        {tab === "drafts" && <Drafts me={me} run={run} runResult={runResult} notice={setNotice} />}
        {tab === "audio-review" && <AudioReviewPage me={me} run={run} notice={setNotice} refreshToken={refreshToken} />}
        {tab === "books" && (
          <Books
            rows={data || []}
            canPublish={can("content.publish")}
            canEdit={can("content.write")}
            run={run}
          />
        )}
        {tab === "site-settings" && data && (
          <SiteSettings
            rows={data}
            writable={can("site_settings.write")}
            run={run}
            reload={load}
          />
        )}
        {tab === "students" && (
          <Students
            rows={data || []}
            writable={can("users.write")}
            run={run}
            reload={load}
          />
        )}
        {tab === "rbac" && data && (
          <RBAC
            value={data}
            writable={can("rbac.write")}
            run={run}
            reload={load}
          />
        )}
        {tab === "audit" && <Table rows={data || []} />}
      </section>
    </main>
  );
}

function SiteSettings({
  rows,
  writable,
  run,
  reload,
}: {
  rows: SiteSetting[];
  writable: boolean;
  run: any;
  reload: any;
}) {
  const [models, setModels] = useState<AIModel[]>([]);
  const [settings, setSettings] = useState<AIModelSettings | null>(null);
  const [voices, setVoices] = useState<AIVoice[]>([]);
  useEffect(() => {
    void Promise.all([
      api<AIModel[]>("/admin/models"),
      api<AIModelSettings>("/admin/settings/models"),
    ]).then(([availableModels, current]) => {
      setModels(availableModels || []);
      setSettings(current);
    });
  }, []);
  useEffect(() => {
    if (!settings?.tts_model) return;
    void api<AIVoice[]>(`/admin/models/${encodeURIComponent(settings.tts_model)}/voices`)
      .then((availableVoices) => {
        const next = availableVoices || [];
        setVoices(next);
        if (!next.some((voice) => voice.id === settings.tts_voice) && next[0]) {
          setSettings((current) => current ? { ...current, tts_voice: next[0].id } : current);
        }
      });
  }, [settings?.tts_model]);
  const ocrModels = models.filter((model) => model.type === "ocr" && model.enabled);
  const ttsModels = models.filter((model) => model.type === "tts" && model.enabled);
  return (
    <section className="admin-panel">
      <p className="admin-note">配置由数据库维护；开关状态的修改会记录在操作日志中。</p>
      {settings && (
        <form
          className="model-settings-row"
          onSubmit={(event) => {
            event.preventDefault();
            void run(async () => {
              await api("/admin/settings/models", {
                method: "PUT",
                body: JSON.stringify(settings),
              });
              await reload();
            });
          }}
        >
          <label>
            OCR 模型
            <select value={settings.ocr_model} disabled={!writable} onChange={(event) => setSettings({ ...settings, ocr_model: event.target.value })}>
              {ocrModels.map((model) => (
                <option key={model.id} value={model.id} disabled={!model.available}>
                  {model.name}{model.available ? "" : `（${model.unavailable_reason || "不可用"}）`}
                </option>
              ))}
            </select>
          </label>
          <label>
            TTS 模型
            <select
              value={settings.tts_model}
              disabled={!writable}
              onChange={(event) => {
                const model = ttsModels.find((item) => item.id === event.target.value);
                setSettings({ ...settings, tts_model: event.target.value, tts_voice: model?.default_voice || "" });
              }}
            >
              {ttsModels.map((model) => (
                <option key={model.id} value={model.id} disabled={!model.available}>
                  {model.name}{model.available ? "" : `（${model.unavailable_reason || "不可用"}）`}
                </option>
              ))}
            </select>
          </label>
          <label>
            TTS 音色
            <select value={settings.tts_voice} disabled={!writable || voices.length === 0} onChange={(event) => setSettings({ ...settings, tts_voice: event.target.value })}>
              {voices.map((voice) => <option key={voice.id} value={voice.id}>{voice.display_name}</option>)}
            </select>
          </label>
          <button className="admin-primary" disabled={!writable}>保存模型设置</button>
          <small>云模型失败或本地 QA 未通过时不会自动重试或切回本地，将进入人工复核。</small>
        </form>
      )}
      {rows.length === 0 ? (
        <p>暂无网站设置项。</p>
      ) : (
        <div className="table-scroll">
          <table className="site-settings-table">
            <thead>
              <tr>
                <th>显示名称</th>
                <th>实际值</th>
                <th>备注</th>
                <th>状态</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((item) => (
                <tr key={item.id}>
                  <td>{item.item_label}</td>
                  <td>{item.item_value}</td>
                  <td>{item.remark || "—"}</td>
                  <td>
                    <label className="site-setting-status">
                      <input
                        type="checkbox"
                        checked={item.status === 1}
                        disabled={!writable}
                        onChange={(e) =>
                          void run(async () => {
                            await api(`/admin/site-settings/${item.id}/status`, {
                              method: "PUT",
                              body: JSON.stringify({ status: e.target.checked ? 1 : 0 }),
                            });
                            await reload();
                          })
                        }
                      />
                      <span>{item.status === 1 ? "已启用" : "已禁用"}</span>
                    </label>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </section>
  );
}

function Books({
  rows,
  canPublish,
  canEdit,
  run,
}: {
  rows: (Book & { revision: number })[];
  canPublish: boolean;
  canEdit: boolean;
  run: any;
}) {
  return (
    <section className="admin-panel">
      <p>正式教材页不可直接修改；请创建草稿，审核通过后发布。</p>
      <div className="cards">
        {rows.map((book) => (
          <article key={book.book_id}>
            <h3>{book.title}</h3>
            <p>
              {book.book_id} · {book.grade}
              {book.semester} · {book.page_count} 页
            </p>
            {([['en-US','美式','american_enabled','american_voice_id'],['en-GB','英式','british_enabled','british_voice_id']] as const).map(([accent,label,enabled,voice])=>{
              const status=(book as any).audio_status?.find((item:Row)=>item.accent===accent);
              return <p key={accent}>{label}：{(book as any)[enabled]!==false?`${(book as any)[voice]||'历史角色'} · ${status?.status==='ready'?'已完成':status?.status==='partial_failed'?'部分失败':status?.status==='generating'?'生成中 / 不完整':'未生成'}`:'已关闭'}{status?.total?`（${status.ready}/${status.total}）`:''}</p>;
            })}
            <button
              disabled={!canPublish}
              onClick={() =>
                void run(async () => {
                  await api(`/admin/books/${book.book_id}`, {
                    method: "PUT",
                    body: JSON.stringify({
                      ...book,
                      status:
                        book.status === "published" ? "draft" : "published",
                    }),
                  });
                  location.reload();
                })
              }
            >
              {book.status === "published" ? "下架" : "上架"}
            </button>
            <button
              disabled={!canEdit}
              onClick={() =>
                void run(async () => {
                  const draft = await api<{ id: string }>(
                    `/admin/books/${book.book_id}/drafts`,
                    { method: "POST" },
                  );
                  location.assign(`/admin?draft=${draft.id}`);
                })
              }
            >
              创建修改草稿
            </button>
          </article>
        ))}
      </div>
      {rows.length === 0 && <p>暂无教材。</p>}
    </section>
  );
}

function Students({
  rows,
  writable,
  run,
  reload,
}: {
  rows: Row[];
  writable: boolean;
  run: any;
  reload: any;
}) {
  const [query, setQuery] = useState("");
  const [shown, setShown] = useState<Row[]>(rows);
  useEffect(() => setShown(rows), [rows]);
  async function search() {
    setShown(
      (await api<Row[]>(`/admin/students?q=${encodeURIComponent(query)}`)) ||
        [],
    );
  }
  return (
    <section className="admin-panel">
      <div className="action-row">
        <input
          placeholder="搜索邮箱"
          value={query}
          onChange={(e) => setQuery(e.target.value)}
        />
        <button onClick={() => void run(search)}>搜索</button>
      </div>
      <Table
        rows={shown}
        action={(row) => (
          <button
            disabled={!writable}
            onClick={() =>
              void run(async () => {
                await api(`/admin/students/${row.id}/status`, {
                  method: "PUT",
                  body: JSON.stringify({ active: !row.active }),
                });
                await reload();
              })
            }
          >
            {row.active ? "停用" : "启用"}
          </button>
        )}
      />
    </section>
  );
}

function RBAC({
  value,
  writable,
  run,
  reload,
}: {
  value: any;
  writable: boolean;
  run: any;
  reload: any;
}) {
  const roles: Row[] = value.roles || [],
    permissionsList: Row[] = value.permissions || [],
    admins: Row[] = value.admins || [],
    adminRoles: Row[] = value.admin_roles || [],
    rolePermissions: Row[] = value.role_permissions || [];
  const [email, setEmail] = useState(""),
    [password, setPassword] = useState(""),
    [admin, setAdmin] = useState(""),
    [roleIDs, setRoleIDs] = useState<number[]>([]),
    [roleID, setRoleID] = useState(0),
    [name, setName] = useState(""),
    [description, setDescription] = useState(""),
    [permissions, setPermissions] = useState<string[]>([]);
  const selectedRole = roles.find((role: Row) => Number(role.id) === roleID);
  const superadminRole = roles.find((role: Row) => role.name === "superadmin");
  const selectedAdminIsSuper =
    !!superadminRole && roleIDs.includes(Number(superadminRole.id));
  function resetRole() {
    setRoleID(0);
    setName("");
    setDescription("");
    setPermissions([]);
  }
  function chooseRole(id: string) {
    const nextID = Number(id) || 0;
    setRoleID(nextID);
    const role = roles.find((item: Row) => Number(item.id) === nextID);
    setName(role?.name || "");
    setDescription(role?.description || "");
    setPermissions(
      rolePermissions
        .filter((item: Row) => Number(item.role_id) === nextID)
        .map((item: Row) => item.permission_code),
    );
  }
  function chooseAdmin(id: string) {
    setAdmin(id);
    setRoleIDs(
      adminRoles
        .filter((item: Row) => String(item.admin_id) === id)
        .map((item: Row) => Number(item.role_id)),
    );
  }
  return (
    <div className="admin-columns">
      <section className="admin-panel">
        <h2>管理员账号</h2>
        <form
          onSubmit={(e) => {
            e.preventDefault();
            void run(async () => {
              await api("/admin/accounts", {
                method: "POST",
                body: JSON.stringify({ email, password }),
              });
              setPassword("");
              await reload();
            });
          }}
        >
          <label>
            邮箱
            <input
              type="email"
              required
              value={email}
              onChange={(e) => setEmail(e.target.value)}
            />
          </label>
          <label>
            初始密码
            <input
              type="password"
              minLength={10}
              required
              value={password}
              onChange={(e) => setPassword(e.target.value)}
            />
          </label>
          <button className="admin-primary" disabled={!writable}>
            创建管理员
          </button>
        </form>
        <hr />
        <label>
          选择管理员
          <select value={admin} onChange={(e) => chooseAdmin(e.target.value)}>
            <option value="">请选择</option>
            {admins.map((item: Row) => (
              <option key={item.id} value={item.id}>
                {item.email}
                {item.active ? "" : "（停用）"}
              </option>
            ))}
          </select>
        </label>
        {roles.map((role: Row) => {
          const isSuper = role.name === "superadmin";
          return (
            <label className="check" key={role.id}>
              <input
                type="checkbox"
                checked={roleIDs.includes(Number(role.id))}
                disabled={!writable || isSuper || !admin}
                onChange={(e) =>
                  setRoleIDs(
                    e.target.checked
                      ? [...roleIDs, Number(role.id)]
                      : roleIDs.filter((id) => id !== Number(role.id)),
                  )
                }
              />
              {isSuper ? "超级管理员" : role.description || role.name}
              {isSuper && <small>（系统内置，不可修改）</small>}
            </label>
          );
        })}
        {selectedAdminIsSuper && (
          <p className="admin-note">
            当前管理员是超级管理员，角色分配不可修改。
          </p>
        )}
        <button
          disabled={!writable || !admin || selectedAdminIsSuper}
          onClick={() =>
            void run(async () => {
              await api(`/admin/accounts/${admin}/roles`, {
                method: "PUT",
                body: JSON.stringify({ role_ids: roleIDs }),
              });
              await reload();
            })
          }
        >
          保存角色分配
        </button>
      </section>
      <section className="admin-panel">
        <h2>{roleID ? "编辑角色" : "新建角色"}</h2>
        <label>
          选择已有角色
          <select
            value={roleID || ""}
            onChange={(e) => chooseRole(e.target.value)}
          >
            <option value="">新建角色</option>
            {roles.map((role: Row) => (
              <option key={role.id} value={role.id}>
                {role.description || role.name}
                {role.name === "superadmin" ? "（不可编辑）" : ""}
              </option>
            ))}
          </select>
        </label>
        <form
          onSubmit={(e) => {
            e.preventDefault();
            void run(async () => {
              await api("/admin/roles", {
                method: "POST",
                body: JSON.stringify({
                  id: roleID,
                  name,
                  description,
                  permissions,
                }),
              });
              await reload();
            });
          }}
        >
          <label>
            标识
            <input
              required
              pattern="[a-z][a-z0-9_-]{1,39}"
              value={name}
              disabled={
                !!selectedRole?.built_in || selectedRole?.name === "superadmin"
              }
              onChange={(e) => setName(e.target.value)}
            />
          </label>
          <label>
            名称
            <input
              required
              value={description}
              disabled={selectedRole?.name === "superadmin"}
              onChange={(e) => setDescription(e.target.value)}
            />
          </label>
          {permissionsList.map((permission: Row) => (
            <label className="check" key={permission.code}>
              <input
                type="checkbox"
                checked={permissions.includes(permission.code)}
                disabled={selectedRole?.name === "superadmin"}
                onChange={(e) =>
                  setPermissions(
                    e.target.checked
                      ? [...permissions, permission.code]
                      : permissions.filter((code) => code !== permission.code),
                  )
                }
              />
              <span>
                {permission.description}
                <small>{permission.code}</small>
              </span>
            </label>
          ))}
          <div className="action-row">
            <button
              type="submit"
              className="admin-primary"
              disabled={!writable || selectedRole?.name === "superadmin"}
            >
              {roleID ? "保存角色修改" : "保存角色"}
            </button>
            {roleID && (
              <button type="button" onClick={resetRole}>
                新建角色
              </button>
            )}
          </div>
          {selectedRole?.name === "superadmin" && (
            <p className="admin-note">
              超级管理员权限由系统维护，不能在此修改。
            </p>
          )}
        </form>
      </section>
    </div>
  );
}

function Drafts({
  me,
  run,
  runResult,
  notice,
}: {
  me: Identity;
  run: any;
  runResult: (work: () => Promise<void>) => Promise<boolean>;
  notice: (s: string) => void;
}) {
  const initialQuery = new URLSearchParams(location.search);
  const initialDraftID = initialQuery.get("draft") || "";
  const initialPage = Number(initialQuery.get("page"));
  const [list, setList] = useState<Row[]>([]),
    [createAmerican, setCreateAmerican] = useState(true),
    [createBritish, setCreateBritish] = useState(true),
    [id, setID] = useState(initialDraftID),
    [detail, setDetail] = useState<any>(null),
    [pageNo, setPageNo] = useState(
      Number.isSafeInteger(initialPage) && initialPage > 0 ? initialPage : 1,
    ),
    [page, setPage] = useState<any>(null),
    [raw, setRaw] = useState(""),
    [waitingPage, setWaitingPage] = useState(0),
    [translationSubmitting, setTranslationSubmitting] = useState(false),
    [audioRestartSubmitting, setAudioRestartSubmitting] = useState(false),
    [audioSettingsSaving, setAudioSettingsSaving] = useState(false),
    [audioIssues, setAudioIssues] = useState<Row[]>([]),
    [draftModels, setDraftModels] = useState<AIModel[]>([]),
    [draftModelSettings, setDraftModelSettings] = useState<AIModelSettings | null>(null),
    [draftVoices, setDraftVoices] = useState<AIVoice[]>([]);
  const detailLoadID = useRef(0);
  const audioIssueLoadID = useRef(0);
  const audioIssueContext = useRef("");
  const jobState = useRef<Record<string, string>>({});
  const scrollRestorePending = useRef(Boolean(initialDraftID));
  const can = (permission: string) => me.permissions.includes(permission);
  function openAudioReview(target = id, targetPage = pageNo) {
    const params = new URLSearchParams({ tab: "audio-review", draft: target, page: String(targetPage) });
    window.location.assign(`/admin?${params.toString()}`);
  }
  async function loadList() {
    setList((await api<Row[]>("/admin/drafts")) || []);
  }
  async function loadDetail(target = id, focusPage = waitingPage) {
    if (!target) return;
    const request = ++detailLoadID.current;
    const value = await api<any>(`/admin/drafts/${target}`);
    if (request !== detailLoadID.current) return;
    const normalized = {
      ...value,
      draft: {
        ...value.draft,
        american_enabled: value.draft.american_enabled ?? true,
        british_enabled: value.draft.british_enabled ?? true,
        american_voice_id: "aiden",
        british_voice_id: "ryan",
      },
      pages: value.pages || [],
      jobs: value.jobs || [],
    };
    for (const job of normalized.jobs as Row[]) {
      const key = `${target}:${job.id}`;
      let previous = jobState.current[key];
      if (!previous) {
        try {
          previous = window.sessionStorage.getItem(`xiaov2-job:${key}`) || "";
        } catch {
          // The in-memory state is enough when browser storage is unavailable.
        }
      }
      const label = jobKindLabels[job.kind] || job.kind;
      const suffix = job.page ? `（第 ${job.page} 页）` : "";
      const itemAccent = job.accent === "en-US" ? "美式" : job.accent === "en-GB" ? "英式" : job.accent;
      const itemLabel = job.kind === "audio-item"
        ? `${job.item_id}${itemAccent ? `（${itemAccent}）` : ""}`
        : `${label}${suffix}`;
      if (previous && previous !== job.status && job.status === "completed") {
        notice(job.kind === "audio-item"
          ? `${itemLabel}重新生成并通过 QA，正式音频已更新`
          : `${itemLabel}已完成`);
      }
      if (previous && previous !== job.status && job.status === "issues") {
        notice(job.kind === "audio-item"
          ? `${itemLabel}重新生成完成，但 QA 仍未通过`
          : `${itemLabel}生成结束，但仍有失败音频，请进入异常页面逐项处理`);
        if (String(job.kind).startsWith("audio") && Number(job.page) === pageNo) {
          openAudioReview(target, Number(job.page));
        }
      }
      if (previous && previous !== job.status && job.status === "failed" && String(job.kind).startsWith("audio")) {
        notice(`${label}${suffix}失败：${readableJobError(job.error || "请查看任务错误详情")}`);
      }
      if (previous && previous !== job.status && job.status === "manual_review_required") {
        notice(`${itemLabel}未通过云端调用或本地 QA，系统未自动重试或切换模型，请人工复核后手动处理`);
        if (String(job.kind).startsWith("audio") && Number(job.page) === pageNo) {
          openAudioReview(target, Number(job.page));
        }
      }
      jobState.current[key] = job.status;
      try {
        window.sessionStorage.setItem(`xiaov2-job:${key}`, job.status);
      } catch {
        // Ignore restricted/private browser storage.
      }
    }
    setDetail(normalized);
    if (
      focusPage &&
      normalized.pages.some((x: Row) => x.position === focusPage)
    ) {
      setPageNo(focusPage);
      setWaitingPage(0);
    } else if (
      normalized.pages.length &&
      !normalized.pages.some((x: Row) => x.position === pageNo) &&
      !normalized.jobs.some(
        (job: Row) =>
          Number(job.page) === pageNo &&
          (job.status === "queued" || job.status === "running"),
      )
    )
      setPageNo(normalized.pages[0].position);
    return normalized;
  }
  async function loadPage() {
    if (!id || !pageNo) return;
    if (detail?.draft?.id !== id) return;
    if (!detail.pages.some((item: Row) => Number(item.position) === pageNo)) {
      setPage(null);
      setRaw("");
      return;
    }
    const value = await api<any>(`/admin/drafts/${id}/pages/${pageNo}`);
    setPage(value);
    setRaw(JSON.stringify(value.content, null, 2));
  }
  async function loadAudioIssues(target = id, targetPage = pageNo) {
    if (!target || !targetPage) {
      audioIssueLoadID.current += 1;
      audioIssueContext.current = "";
      setAudioIssues([]);
      return [];
    }
    const context = `${target}/${targetPage}`;
    if (audioIssueContext.current !== context) {
      audioIssueLoadID.current += 1;
      audioIssueContext.current = context;
      setAudioIssues([]);
    }
    const request = ++audioIssueLoadID.current;
    const value = (await api<Row[]>(`/admin/drafts/${target}/pages/${targetPage}/audio-issues`)) || [];
    if (request !== audioIssueLoadID.current) return value;
    setAudioIssues(value);
    return value;
  }
  useEffect(() => {
    void loadList();
  }, []);
  useEffect(() => {
    if (!id) {
      setDraftModels([]);
      return;
    }
    void api<AIModel[]>(`/admin/drafts/${encodeURIComponent(id)}/models`)
      .then((items) => setDraftModels(items || []))
      .catch(() => setDraftModels([]));
  }, [id]);
  useEffect(() => {
    if (!detail?.draft?.id) return;
    setDraftModelSettings({
      ocr_model: detail.draft.ocr_model || "local-paddleocr",
      tts_model: detail.draft.tts_model || "local-qwen3-tts",
      tts_voice: detail.draft.tts_voice || "aiden",
    });
  }, [detail?.draft?.id]);
  useEffect(() => {
    if (!draftModelSettings?.tts_model) return;
    if (!id) return;
    void api<AIVoice[]>(`/admin/drafts/${encodeURIComponent(id)}/models/${encodeURIComponent(draftModelSettings.tts_model)}/voices`)
      .then((items) => setDraftVoices(items || []))
      .catch(() => setDraftVoices([]));
  }, [draftModelSettings?.tts_model, id]);
  useEffect(() => {
    const url = new URL(window.location.href);
    if (id) {
      url.searchParams.set("draft", id);
      url.searchParams.set("page", String(pageNo));
    } else {
      url.searchParams.delete("draft");
      url.searchParams.delete("page");
    }
    window.history.replaceState(
      window.history.state,
      "",
      `${url.pathname}${url.search}${url.hash}`,
    );
  }, [id, pageNo]);
  useEffect(() => {
    if (!id) return;
    const storageKey = `xiaov2-admin-scroll:${id}`;
    const saveScroll = () => {
      if (scrollRestorePending.current) return;
      try {
        window.sessionStorage.setItem(storageKey, String(window.scrollY));
      } catch {
        // Scroll restoration remains available through the URL when storage is restricted.
      }
    };
    window.addEventListener("scroll", saveScroll, { passive: true });
    window.addEventListener("pagehide", saveScroll);
    return () => {
      window.removeEventListener("scroll", saveScroll);
      window.removeEventListener("pagehide", saveScroll);
    };
  }, [id]);
  useEffect(() => {
    if (!scrollRestorePending.current || !id || detail?.draft?.id !== id)
      return;
    const targetExists = detail.pages.some(
      (item: Row) => Number(item.position) === pageNo,
    );
    const targetIsBeingCreated = detail.jobs.some(
      (job: Row) =>
        Number(job.page) === pageNo &&
        (job.status === "queued" || job.status === "running"),
    );
    if (targetIsBeingCreated && !targetExists) return;
    if (detail.pages.length > 0 && !targetExists) return;
    if (targetExists && !page) return;
    const restore = () => {
      let scrollY = 0;
      try {
        scrollY = Number(
          window.sessionStorage.getItem(`xiaov2-admin-scroll:${id}`) || 0,
        );
      } catch {
        // Use the top of the page when browser storage is unavailable.
      }
      window.requestAnimationFrame(() => {
        window.scrollTo({ top: scrollY, behavior: "auto" });
        window.requestAnimationFrame(() => {
          window.scrollTo({ top: scrollY, behavior: "auto" });
          scrollRestorePending.current = false;
        });
      });
    };
    restore();
  }, [id, pageNo, detail, page]);
  useEffect(() => {
    void loadDetail();
  }, [id]);
  useEffect(() => {
    void loadPage();
  }, [id, pageNo, detail?.draft?.version]);
  useEffect(() => {
    void loadAudioIssues();
  }, [id, pageNo, detail?.draft?.version]);
  const tracking = !!detail?.jobs?.some(
    (job: Row) => job.status === "queued" || job.status === "running",
  );
  useEffect(() => {
    if (!id || !tracking) return;
    let stopped = false;
    let timer = 0;
    const poll = async () => {
      try {
        const status = await api<Row>(`/admin/drafts/${encodeURIComponent(id)}/status`);
        if (stopped) return;
        const nextJobs = status.jobs || [];
        const stillTracking = nextJobs.some((job: Row) => job.status === "queued" || job.status === "running");
        setDetail((current: any) => current ? {
          ...current,
          draft: { ...current.draft, status: status.draft_status, version: status.revision },
          jobs: nextJobs,
        } : current);
        if (!stillTracking) {
          await Promise.all([loadDetail(id, waitingPage), loadPage(), loadAudioIssues(id, pageNo), loadList()]);
          return;
        }
      } catch {
        // The next poll can recover from a transient refresh failure.
      }
      if (!stopped) timer = window.setTimeout(poll, 2000);
    };
    timer = window.setTimeout(poll, 500);
    return () => {
      stopped = true;
      window.clearTimeout(timer);
    };
  }, [id, tracking, pageNo, waitingPage]);
  async function action(
    name: string,
    note = "",
    version = detail.draft.version,
    page?: number,
  ) {
    await api(`/admin/drafts/${id}/actions/${name}`, {
      method: "POST",
      body: JSON.stringify({ version, note, ...(page ? { page } : {}) }),
    });
    await loadDetail();
    await loadList();
  }
  async function updateAudioSetting(
    field: "american_enabled" | "british_enabled",
    enabled: boolean,
  ) {
    if (!detail || audioSettingsSaving || processing || !editableDraft) return;
    const draft = detail.draft;
    const next = { ...draft, [field]: enabled };
    setAudioSettingsSaving(true);
    await run(async () => {
      await api(`/admin/drafts/${id}`, {
        method: "PUT",
        body: JSON.stringify({
          title: draft.title,
          grade: draft.grade,
          term: draft.term,
          edition: draft.edition,
          version: draft.version,
          american_enabled: next.american_enabled,
          british_enabled: next.british_enabled,
          american_voice_id: "aiden",
          british_voice_id: "ryan",
        }),
      });
      await Promise.all([loadDetail(id, pageNo), loadList()]);
      notice("发音设置已保存到数据库；生成任务将按已启用的口音执行");
    });
    setAudioSettingsSaving(false);
  }
  async function switchDraftModels() {
    if (!detail || !draftModelSettings || processing || !editableDraft) return;
    const draft = detail.draft;
    const ttsChanged = draft.tts_model !== draftModelSettings.tts_model || draft.tts_voice !== draftModelSettings.tts_voice;
    const ocrChanged = draft.ocr_model !== draftModelSettings.ocr_model;
    if (!ttsChanged && !ocrChanged) return;
    const impact = ttsChanged
      ? "未完成页面的旧音频与 QA 结果会清除，并改为使用新 TTS 重新生成；已完整确认的页面不会变。"
      : "仅影响后续 OCR 或你主动重新 OCR 的页面；已确认页面不会变。";
    if (!confirm(`确定切换此草稿的默认模型吗？\n${impact}`)) return;
    await run(async () => {
      await api(`/admin/drafts/${id}/models`, {
        method: "PUT",
        body: JSON.stringify({ ...draftModelSettings, version: draft.version }),
      });
      await Promise.all([loadDetail(id, pageNo), loadPage(), loadAudioIssues(id, pageNo), loadList()]);
      notice("草稿默认模型已切换；已确认页面已锁定并保留原内容和音频");
    });
  }
  async function deleteDraft() {
    if (!id || !confirm("确定删除这个草稿及其上传文件吗？删除后无法恢复。"))
      return;
    await run(async () => {
      await api(`/admin/drafts/${id}`, { method: "DELETE" });
      setID("");
      setDetail(null);
      setPage(null);
      await loadList();
      notice("草稿已删除");
    });
  }
  const pages: Row[] = detail?.pages || [],
    jobs: Row[] = detail?.jobs || [];
  // Keep job history in the database, but show only the active/latest task in
  // this workflow so old OCR/audio cards do not obscure the current action.
  const currentJob = jobs.find((job) => job.status === "running")
    || jobs.find((job) => job.status === "queued")
    || jobs[0];
  const visibleJobs = currentJob ? [currentJob] : [];
  const lastPage = pages.length
    ? Math.max(...pages.map((item) => Number(item.position)))
    : 0;
  const sourcePageCount =
    Number(detail?.draft?.source_page_count) ||
    Math.max(
      0,
      ...jobs
        .filter(
          (job) =>
            job.kind === "ocr" || job.kind === "page" || job.kind === "pdf",
        )
        .map((job) => Number(job.total) || 0),
    );
  const processing = jobs.some(
    (job) => job.status === "queued" || job.status === "running",
  );
  const currentPageActiveAudioJob = jobs.find(
    (job) =>
      String(job.kind).startsWith("audio") &&
      Number(job.page) === pageNo &&
      (job.status === "queued" || job.status === "running"),
  );
  const currentIsLast = pageNo === lastPage;
  const allSourcePagesAvailable = sourcePageCount > 0 && lastPage >= sourcePageCount;
  const editableDraft = ["draft", "failed"].includes(detail?.draft?.status || "");
  const currentHasIssues = !!(page?.issues || []).length;
  const currentHasAudioIssues = audioIssues.length > 0;
  const currentHasOCRContent =
    Array.isArray(page?.content?.segments) && page.content.segments.length > 0;
  const currentHasAudioContent =
    Array.isArray(page?.content?.segments) &&
    page.content.segments.some((segment: Row) => {
      const mode = String(segment?.audio_mode || "sentence_and_words");
      if (mode === "none") return false;
      if (
        mode !== "word_only" &&
        /[\p{L}\p{N}]/u.test(String(segment?.text || ""))
      ) {
        return true;
      }
      return (
        Array.isArray(segment?.words) &&
        segment.words.some((word: Row) =>
          /[\p{L}\p{N}]/u.test(String(word?.text || "")),
        )
      );
    });
  const audioFreePageNeedsNext =
    !currentHasAudioContent && currentIsLast && sourcePageCount > lastPage;
  const translationProcessing = jobs.some(
    (job) => job.kind === "translate" && (job.status === "queued" || job.status === "running"),
  );
  const currentOCRReviewed = !!page?.checked && !currentHasIssues;
  const currentAudioReviewed = !!page?.audio_checked && !currentHasIssues && !currentHasAudioIssues;
  const currentReviewed = currentOCRReviewed && currentAudioReviewed;
  const editingSelectedDraft = !!id && detail?.draft?.id === id;
  const selectedDraftLoading = !!id && !editingSelectedDraft;
  const americanEnabled = editingSelectedDraft
    ? !!detail.draft.american_enabled
    : createAmerican;
  const britishEnabled = editingSelectedDraft
    ? !!detail.draft.british_enabled
    : createBritish;
  const audioRequired = !!(detail?.draft?.american_enabled || detail?.draft?.british_enabled);
  const enabledAccentLabel = detail?.draft?.american_enabled && detail?.draft?.british_enabled
    ? "英美音频"
    : detail?.draft?.american_enabled
      ? "美式音频"
      : detail?.draft?.british_enabled
        ? "英式音频"
        : "音频已关闭";
  const configuredAudioReady = audioRequired && (detail?.audio || [])
    .filter((item:Row)=>item.status!=="disabled")
    .every((item:Row)=>item.status==="ready");
  // `page` is the pre-split legacy task; it already contains audio and must
  // remain reviewable without asking the worker to produce it again.
  const audioGeneratedForCurrent = typeof page?.audio_ready === "boolean"
    ? page.audio_ready
    : !audioRequired || configuredAudioReady || currentHasAudioIssues;
  function accentStatusLabel(accent: "en-US" | "en-GB") {
    if (!editingSelectedDraft) return "按网站设置";
    const enabled = accent === "en-US" ? americanEnabled : britishEnabled;
    if (!enabled) return "已关闭";
    const status = (detail.audio || []).find((item: Row) => item.accent === accent);
    const voice = detail.draft.tts_model === "qwen3-tts-flash"
      ? detail.draft.tts_voice
      : accent === "en-US" ? "aiden" : "ryan";
    return status?.status === "ready"
      ? `${voice} · 音频完整`
      : `${voice} · ${status?.ready || 0}/${status?.total || 0}`;
  }
  async function savePageReview(
    message = "本页修改已保存",
    stage: "none" | "ocr" | "audio" = "none",
    nextContent?: { segments: any[]; [key: string]: unknown },
  ) {
    const submitted =
      stage === "audio"
        ? { ...page, checked: true, audio_checked: true }
        : stage === "ocr"
          ? { ...page, checked: true, audio_checked: false }
          : page;
    await api(`/admin/drafts/${id}/pages/${pageNo}`, {
      method: "PUT",
      body: JSON.stringify({ ...submitted, content: nextContent || JSON.parse(raw) }),
    });
    const latest = await loadDetail();
    notice(message);
    return latest;
  }
  async function regenerateAudioItem(itemID: string, accent: "en-US" | "en-GB", kind: "sentence" | "word", text: string) {
    if (!detail || !page || processing) return;
    const label = kind === "sentence" ? "整句" : "单词";
    const accentLabel = accent === "en-US" ? "美音" : "英音";
    const ok = await runResult(async () => {
      // Item-only regeneration must never save/invalidate the whole page.
      // Send the visible text so the backend can reject unsaved edits instead
      // of silently regenerating stale content or clearing unrelated audio.
      await api(`/admin/drafts/${encodeURIComponent(id)}/pages/${pageNo}/audio/${encodeURIComponent(itemID)}/regenerate`, {
        method: "POST",
        body: JSON.stringify({ accent, version: detail.draft.version, text }),
      });
      await Promise.all([loadDetail(id, pageNo), loadAudioIssues(id, pageNo)]);
      notice(`${label}“${text}”的${accentLabel}已进入单项重新生成队列`);
    });
    if (!ok) {
      throw new Error("单项音频重新生成失败");
    }
  }

  async function uploadWordAudio(itemID: string, text: string, file: File) {
    if (!detail || !page || processing) return;
    const ok = await runResult(async () => {
      const form = new FormData();
      form.append("file", file);
      form.append("text", text);
      form.append("version", String(detail.draft.version));
      await api(`/admin/drafts/${encodeURIComponent(id)}/pages/${pageNo}/audio/${encodeURIComponent(itemID)}/upload`, {
        method: "POST",
        body: form,
      });
      await Promise.all([loadDetail(id, pageNo), loadPage(), loadAudioIssues(id, pageNo)]);
      notice(`单词“${text}”的人工音频已上传；整本教材同词将共用这条音频`);
    });
    if (!ok) {
      throw new Error("上传单词音频失败");
    }
  }

  async function queueAudio() {
    if (!audioRequired) {
      notice("当前草稿已关闭所有发音，本页只确认 OCR，不会生成音频");
      return;
    }
    await run(async () => {
      const latest = await savePageReview("", "ocr");
      await action("audio", "", latest.draft.version, pageNo);
      await loadDetail(id, pageNo);
      notice(`第 ${pageNo} 页 OCR 已确认，已启用的发音音频已进入生成队列`);
    });
  }
  async function queuePageAudioReplacement() {
    if (!detail || !audioRequired || !currentHasAudioContent) return;
    if (currentHasIssues) {
      notice("本页 OCR/翻译/音标仍有待处理内容，请修正后再重新生成音频");
      return;
    }
    if (
      !confirm(
        `确定清除第 ${pageNo} 页已有的全部正式音频和失败候选，并重新生成已启用的发音吗？本页 OCR、翻译和其他页面音频会保留。`,
      )
    ) {
      return;
    }
    await run(async () => {
      // Page-audio replacement must never call SavePage: a normal page save can
      // invalidate OCR/page artifacts when content changed. Compare against the
      // persisted page instead and require an explicit save only when the
      // editor has unsaved changes.
      const persisted = await api<any>(`/admin/drafts/${id}/pages/${pageNo}`);
      let visibleContent: any;
      try {
        visibleContent = JSON.parse(raw);
      } catch {
        throw new Error("页面 JSON 无效，请先修正并保存");
      }
      if (JSON.stringify(persisted.content) !== JSON.stringify(visibleContent)) {
        throw new Error("当前页有未保存修改，请先保存本页修改；保存后无需重新 OCR，可直接重新生成音频");
      }
      await action("audio-replace-page", "", detail.draft.version, pageNo);
      await Promise.all([loadDetail(id, pageNo), loadAudioIssues(id, pageNo)]);
      notice(`第 ${pageNo} 页 OCR 数据有效，旧音频已清除并重新生成本页全部音频`);
    });
  }
  async function terminateAndRestartPageAudio() {
    if (!detail || !currentPageActiveAudioJob || audioRestartSubmitting) return;
    if (
      !confirm(
        `确定立即终止第 ${pageNo} 页当前音频任务吗？系统会清除该页已有的正式音频、失败候选和 QA 记录，然后重新生成；OCR、翻译和其他页面不会被清除。`,
      )
    ) {
      return;
    }
    setAudioRestartSubmitting(true);
    await run(async () => {
      await action(
        "audio-restart-page",
        "",
        detail.draft.version,
        pageNo,
      );
      await Promise.all([loadDetail(id, pageNo), loadAudioIssues(id, pageNo)]);
      notice(`第 ${pageNo} 页旧任务已终止，旧音频已清除并重新进入生成队列`);
    });
    setAudioRestartSubmitting(false);
  }
  async function queueTranslation() {
    if (translationSubmitting || translationProcessing) return;
    setTranslationSubmitting(true);
    await run(async () => {
      await action("translate", "", detail.draft.version);
      const latest = await loadDetail(id, pageNo);
      const task = latest?.jobs?.find(
        (job: Row) => job.kind === "translate" && Number(job.page) === pageNo,
      );
      notice(
        task?.status === "completed"
          ? `第 ${pageNo} 页缺失翻译和音标已完成`
          : `第 ${pageNo} 页翻译和音标已进入重新生成队列`,
      );
    });
    setTranslationSubmitting(false);
  }
  async function queueNextPage() {
    const next = lastPage + 1;
    await run(async () => {
      const latest = await savePageReview("", "audio");
      await action("next-page", "", latest.draft.version);
      setWaitingPage(next);
      await loadDetail(id, next);
      notice(`第 ${next} 页已进入单页生成队列`);
    });
  }
  async function queueReOCR(targetPage: number) {
    if (processing || !editableDraft || !detail) return;
    if (
      !confirm(
        `重新 OCR 第 ${targetPage} 页会清除该页已有片段、翻译、音标、审核状态和音频文件，保留页面图片。确定继续吗？`,
      )
    ) {
      return;
    }
    await run(async () => {
      await action("reocr", "", detail.draft.version, targetPage);
      setPageNo(targetPage);
      setWaitingPage(targetPage);
      await loadDetail(id, targetPage);
      notice(`第 ${targetPage} 页已进入重新 OCR 队列`);
    });
  }
  async function approveWithoutAudio() {
    const next = lastPage + 1;
    const reason = currentHasOCRContent
      ? "本页片段均设置为不生成音频"
      : "本页没有可朗读 OCR 内容";
    await run(async () => {
      // A previous attempt may already have saved the page but failed
      // before queuing the next OCR job.  In that case, reuse the current
      // draft version and only resume the missing next-page action.
      const latest = currentReviewed
        ? detail
        : await savePageReview("", "audio");
      if (audioFreePageNeedsNext) {
        await action("next-page", "", latest.draft.version);
        setWaitingPage(next);
        await loadDetail(id, next);
        notice(`第 ${pageNo} 页已跳过音频并生成第 ${next} 页 OCR`);
        return;
      }
      notice(`${reason}，审核已确认并跳过音频`);
    });
  }
  return (
    <>
      <section className="admin-panel">
        <h2>PDF 教材逐页工作流</h2>
        <p>
          上传后仅生成第 1 页 OCR（含单词坐标与置信度）；OCR 确认后按已启用的口音生成音频，
          完成试听确认后才能生成下一页。
        </p>
        {can("content.write") && (
          <form
            className="form-grid"
            onSubmit={(e) => {
              e.preventDefault();
              const form = e.currentTarget;
              void run(async () => {
                const out = await api<{ id: string }>("/admin/drafts", {
                  method: "POST",
                  body: new FormData(form),
                });
                form.reset();
                await loadList();
                setID(out.id);
                notice("PDF 已进入第 1 页 OCR 生成队列");
              });
            }}
          >
            <label>
              教材标识
              <input
                name="book_id"
                required
                pattern="[a-z0-9][a-z0-9_-]{1,79}"
              />
            </label>
            <label>
              名称
              <input name="title" required />
            </label>
            <label>
              年级
              <input
                name="grade"
                type="number"
                min="1"
                max="6"
                defaultValue="4"
              />
            </label>
            <label>
              学期
              <select name="term">
                <option>上册</option>
                <option>下册</option>
              </select>
            </label>
            <label>
              版本
              <input name="edition" defaultValue="项目教材" />
            </label>
            <label>
              PDF（最多128MB/200页）
              <input
                name="file"
                type="file"
                accept="application/pdf,.pdf"
                required
              />
            </label>
            <fieldset className="audio-settings">
              <legend>发音设置</legend>
              <input type="hidden" name="american_enabled" value={String(americanEnabled)} />
              <input type="hidden" name="british_enabled" value={String(britishEnabled)} />
              <div className="audio-settings-options">
                <label className={`audio-setting-card ${americanEnabled ? "" : "is-disabled"}`}>
                  <span className="audio-setting-toggle">
                    <input
                      type="checkbox"
                      checked={americanEnabled}
                      disabled={selectedDraftLoading || (editingSelectedDraft && (!can("content.write") || !editableDraft || processing || audioSettingsSaving))}
                      onChange={e => editingSelectedDraft
                        ? void updateAudioSetting("american_enabled", e.target.checked)
                        : setCreateAmerican(e.target.checked)}
                    />
                    <span><strong>美式英语</strong><small>{editingSelectedDraft ? `${detail.draft.tts_model} · ${detail.draft.tts_model === "qwen3-tts-flash" ? detail.draft.tts_voice : "aiden"}` : "模型与音色读取网站设置"}</small></span>
                  </span>
                  <span className="audio-role-chip">{accentStatusLabel("en-US")}</span>
                </label>
                <label className={`audio-setting-card ${britishEnabled ? "" : "is-disabled"}`}>
                  <span className="audio-setting-toggle">
                    <input
                      type="checkbox"
                      checked={britishEnabled}
                      disabled={selectedDraftLoading || (editingSelectedDraft && (!can("content.write") || !editableDraft || processing || audioSettingsSaving))}
                      onChange={e => editingSelectedDraft
                        ? void updateAudioSetting("british_enabled", e.target.checked)
                        : setCreateBritish(e.target.checked)}
                    />
                    <span><strong>英式英语</strong><small>{editingSelectedDraft ? `${detail.draft.tts_model} · ${detail.draft.tts_model === "qwen3-tts-flash" ? detail.draft.tts_voice : "ryan"}` : "模型与音色读取网站设置"}</small></span>
                  </span>
                  <span className="audio-role-chip">{accentStatusLabel("en-GB")}</span>
                </label>
              </div>
              <p className="audio-settings-note">
                {selectedDraftLoading
                  ? "正在加载所选草稿的发音设置…"
                  : audioSettingsSaving
                    ? "正在保存到数据库…"
                    : editingSelectedDraft
                      ? "点击开关立即保存到数据库；后续单页音频生成将统一按这里启用的口音执行。角色固定匹配。"
                      : "上传时写入数据库；后续单页音频生成将统一按这里选择的口音执行。角色固定匹配。"}
              </p>
            </fieldset>
            <button className="admin-primary" disabled={selectedDraftLoading}>上传并生成第 1 页 OCR</button>
          </form>
        )}
        <label>
          选择草稿
          <select
            value={id}
            onChange={(e) => {
              setWaitingPage(0);
              setID(e.target.value);
            }}
          >
            <option value="">请选择</option>
            {list.map((d) => (
              <option key={d.id} value={d.id}>
                {d.title} · {d.status} · {d.id.slice(0, 8)}
              </option>
            ))}
          </select>
        </label>
      </section>
      {detail && (
        <section className="admin-panel">
          <h2>
            {detail.draft.title} <small>{detail.draft.status}</small>
          </h2>
          <p>
            版本 {detail.draft.version} · 已生成 {pages.length}/
            {sourcePageCount || "?"} 页
            {tracking && (
              <span className="job-live">自动跟踪中（每 2 秒刷新）</span>
            )}
          </p>
          <p className="admin-note">
            OCR：{detail.draft.ocr_model || "local-paddleocr"} · TTS：{detail.draft.tts_model || "local-qwen3-tts"}
            {detail.draft.tts_voice ? ` · 音色 ${detail.draft.tts_voice}` : ""}
          </p>
          {draftModelSettings && <form className="draft-model-switcher" onSubmit={(event) => { event.preventDefault(); void switchDraftModels(); }}>
            <label>后续 OCR 默认模型<select value={draftModelSettings.ocr_model} disabled={!can("content.write") || !editableDraft || processing} onChange={(event) => setDraftModelSettings({ ...draftModelSettings, ocr_model: event.target.value })}>{draftModels.filter((model) => model.type === "ocr" && model.enabled).map((model) => <option key={model.id} value={model.id} disabled={!model.available}>{model.name}{model.available ? "" : `（${model.unavailable_reason || "不可用"}）`}</option>)}</select></label>
            <label>后续 TTS 默认模型<select value={draftModelSettings.tts_model} disabled={!can("content.write") || !editableDraft || processing} onChange={(event) => { const model = draftModels.find((item) => item.id === event.target.value); setDraftModelSettings({ ...draftModelSettings, tts_model: event.target.value, tts_voice: model?.default_voice || "" }); }}>{draftModels.filter((model) => model.type === "tts" && model.enabled).map((model) => <option key={model.id} value={model.id} disabled={!model.available}>{model.name}{model.available ? "" : `（${model.unavailable_reason || "不可用"}）`}</option>)}</select></label>
            <label>线上 TTS 音色<select value={draftModelSettings.tts_voice} disabled={!can("content.write") || !editableDraft || processing || draftVoices.length === 0} onChange={(event) => setDraftModelSettings({ ...draftModelSettings, tts_voice: event.target.value })}>{draftVoices.map((voice) => <option key={voice.id} value={voice.id}>{voice.display_name}</option>)}</select></label>
            <button className="admin-primary" disabled={!can("content.write") || !editableDraft || processing}>应用到未锁定页面</button>
            <small>已完成 OCR 与音频确认的页面会锁定原模型、文本和音频；切换 TTS 只清理未锁定页面的临时音频。</small>
          </form>}
          <div className="page-workflow">
            <strong>
              {processing
                ? "正在生成当前页…"
                : sourcePageCount && lastPage >= sourcePageCount
                  ? "全部页面已生成"
                : audioRequired
                  ? "当前页面需按 OCR、音频顺序审核"
                  : "当前页面只需审核 OCR（发音已关闭）"}
            </strong>
            <span>
              {lastPage
                ? `第 ${lastPage} 页 ${pages.find((item) => item.position === lastPage)?.audio_checked ? "已确认" : pages.find((item) => item.position === lastPage)?.checked ? "等待音频确认" : "等待 OCR 确认"}`
                : "正在准备第 1 页"}
            </span>
          </div>
          <div className="job-list">
            {visibleJobs.map((job: Row) => {
              const total = Number(job.total) || 0,
                reportedProgress = Number(job.progress) || 0,
                // Older completed translate jobs may have stored 1/2 because
                // completion updated progress but not total. Render any
                // completed job at its terminal state while the backend fix
                // corrects new records to 2/2.
                progress = job.status === "completed" && total > 0 ? total : reportedProgress,
                percent = total
                  ? Math.min(100, Math.round((progress / total) * 100))
                  : 0,
                isAudioJob = String(job.kind).startsWith("audio"),
                isAudioItem = job.kind === "audio-item",
                accentLabel = job.accent === "en-US" ? "美式" : job.accent === "en-GB" ? "英式" : "",
                itemActive = isAudioItem && (job.status === "queued" || job.status === "running");
              return (
                <article className={`job-card ${job.status}`} key={job.id}>
                  <header>
                    <strong>
                      {jobKindLabels[job.kind] || job.kind}
                      {job.page ? ` · 第 ${job.page} 页` : ""}
                    </strong>
                    <span>{jobStatusLabels[job.status] || job.status}</span>
                  </header>
                  {(job.model_id || job.provider || job.request_id) && (
                    <small className="job-model-meta">
                      模型 {job.model_id || "—"} · 供应商 {job.provider || "—"}
                      {job.request_id ? ` · request_id ${job.request_id}` : ""}
                    </small>
                  )}
                  {isAudioJob &&
                    Number(job.page) === pageNo &&
                    (job.status === "queued" || job.status === "running") && (
                      <div className="job-recovery-action">
                        <button
                          type="button"
                          className="job-kill-restart"
                          disabled={!can("content.write") || audioRestartSubmitting}
                          onClick={() => void terminateAndRestartPageAudio()}
                        >
                          {audioRestartSubmitting
                            ? "正在终止并重新排队…"
                            : "终止当前任务并重新生成本页音频"}
                        </button>
                        <small>仅清除当前页音频，不影响 OCR、翻译和其他页面</small>
                      </div>
                    )}
                  {total > 0 ? (
                    <>
                      {itemActive ? (
                        <div className="job-indeterminate" role="progressbar" aria-label="单项音频正在处理" />
                      ) : (
                        <progress max={total} value={progress} />
                      )}
                      {isAudioJob && !isAudioItem &&
                        job.status === "running" && progress < total && (
                          <div className="job-progress-activity" aria-label="正在生成并检查当前音频项" />
                        )}
                      <small>
                        {job.kind === "translate"
                          ? job.status === "completed" || progress >= total
                            ? "整页翻译与审核完成"
                            : progress === 0
                            ? "整页翻译准备中"
                            : progress === 1
                            ? "整页翻译完成，正在整页审核"
                            : "整页翻译处理中"
                          : isAudioJob
                          ? job.status === "queued"
                            ? isAudioItem
                              ? `等待重新生成 ${job.item_id}${accentLabel ? `（${accentLabel}）` : ""}`
                              : `等待后台处理 · ${progress}/${total} 个音频项目`
                            : job.status === "running" && progress < total
                              ? isAudioItem
                                ? `正在重新生成并进行 QA：${job.item_id}${accentLabel ? `（${accentLabel}）` : ""}`
                                : `${progress}/${total} 个音频项目已完成，正在生成并检查当前项`
                              : isAudioItem
                                ? job.status === "completed"
                                  ? `单项重新生成并通过 QA：${job.item_id}${accentLabel ? `（${accentLabel}）` : ""}`
                                  : `单项重新生成结束：${job.item_id}${accentLabel ? `（${accentLabel}）` : ""}`
                                : `${progress}/${total} 个音频项目`
                          : job.page
                          ? `PDF 第 ${job.page}/${total} 页`
                          : `${progress}/${total} 页`}
                        {!itemActive ? ` · ${percent}%` : ""}
                      </small>
                    </>
                  ) : job.status === "running" ? (
                    <>
                      <div className="job-indeterminate" />
                      <small>
                        {job.kind === "translate"
                          ? "正在等待整页翻译或审核返回…"
                          : isAudioJob
                            ? "正在准备本页音频项目，随后开始生成与 QA…"
                          : "正在检查运行环境并读取 PDF 页数…"}
                      </small>
                    </>
                  ) : job.status === "queued" ? (
                    <small>任务已进入队列，等待后台处理…</small>
                  ) : null}
                  {job.error && (
                    <div className="job-error">
                      <strong>{readableJobError(job.error)}</strong>
                      {isAudioJob && audioIssues.length > 0 && (
                        <p><button onClick={() => openAudioReview(id, pageNo)}>前往人工审核页面（{audioIssues.length} 项）</button></p>
                      )}
                      {job.error.includes("Poppler") ||
                      job.error.includes("pdfinfo") ||
                      job.error.includes("pdftoppm") ? (
                        <p>
                          macOS 可执行：<code>brew install poppler</code>
                          ，安装后重启服务，再点击“重新排队”。
                        </p>
                      ) : null}
                      <details>
                        <summary>查看技术错误</summary>
                        <code>{job.error}</code>
                      </details>
                    </div>
                  )}
                </article>
              );
            })}
          </div>
          <div className="action-row">
            {detail.draft.status === "draft" && can("content.write") && (
              <>
                {lastPage > 0 && sourcePageCount > lastPage && currentAudioReviewed && (
                  <button
                    className="admin-primary"
                    disabled={processing || !currentIsLast || currentHasIssues}
                    title={
                      !currentIsLast
                        ? "请切换到最新一页后再继续"
                        : currentHasIssues
                          ? "请先补全并保存页面中的待完成内容"
                          : ""
                    }
                    onClick={() => void queueNextPage()}
                  >
                    确认音频并生成第 {lastPage + 1} 页 OCR
                  </button>
                )}
                <button
                  disabled={
                    processing ||
                    sourcePageCount === 0 ||
                    lastPage !== sourcePageCount
                  }
                  onClick={() => void run(() => action("submit"))}
                >
                  提交整本审核
                </button>
              </>
            )}
            {detail.draft.status === "in_review" && can("content.review") && (
              <>
                <button onClick={() => void run(() => action("approve"))}>
                  审核通过
                </button>
                <button
                  onClick={() => {
                    const note = prompt("退回意见");
                    if (note) void run(() => action("reject", note));
                  }}
                >
                  退回修改
                </button>
              </>
            )}
            {detail.draft.status === "approved" && can("content.publish") && (
              <button
                className="admin-primary"
                onClick={() => void run(() => action("publish"))}
              >
                发布
              </button>
            )}
            {["in_review", "approved"].includes(detail.draft.status) &&
              can("content.write") && (
                <button onClick={() => void run(() => action("withdraw"))}>
                  撤回编辑
                </button>
              )}
            {detail.draft.status === "failed" && (
              audioIssues.length > 0 ? (
                <button className="audio-issue-entry" onClick={() => openAudioReview(id, pageNo)}>
                  前往人工审核页面（{audioIssues.length}）
                </button>
              ) : (
                <button onClick={() => void run(() => action("retry"))}>
                  重新排队
                </button>
              )
            )}
            {detail.draft.status !== "published" && can("content.write") && (
              <button onClick={() => void deleteDraft()}>删除草稿</button>
            )}
          </div>
          {detail.pages.length > 0 && (
            <div className="draft-grid">
              <aside>
                {detail.pages.map((item: Row) => (
                  <div key={item.position}>
                    <button
                      className={item.position === pageNo ? "active" : ""}
                      onClick={() => setPageNo(item.position)}
                    >
                      第 {item.position} 页{" "}
                      {item.checked && item.audio_checked ? "✓ 已确认" : "待审核"}
                    </button>
                    <small className="draft-page-model">{item.checked && item.audio_checked ? "已锁定" : "未锁定"} · {item.ocr_model || detail.draft.ocr_model || "local-paddleocr"} / {item.tts_model || detail.draft.tts_model || "local-qwen3-tts"}</small>
                  </div>
                ))}
              </aside>
              {page && (
                <div>
                  <label>
                    标题
                    <input
                      disabled={processing}
                      value={page.title}
                      onChange={(e) =>
                        setPage({ ...page, title: e.target.value })
                      }
                    />
                  </label>
                  <label>
                    单元
                    <input
                      disabled={processing}
                      value={page.unit}
                      onChange={(e) =>
                        setPage({ ...page, unit: e.target.value })
                      }
                    />
                  </label>
                  <DraftPageEditor
                    content={page.content}
                    image={page.image}
                    draftId={id}
                    page={pageNo}
                    availableAccents={[
                      ...(detail.draft.american_enabled ? ["en-US" as const] : []),
                      ...(detail.draft.british_enabled ? ["en-GB" as const] : []),
                    ]}
                    editable={
                      can("content.write") &&
                      editableDraft &&
                      !processing
                    }
                    onChange={(value) => {
                      setPage({ ...page, content: value });
                      setRaw(JSON.stringify(value, null, 2));
                    }}
                    onCommit={async (value) => {
                      if (!(await runResult(() => savePageReview("片段已保存", "none", value)))) {
                        throw new Error("片段保存失败，请修正错误后重试");
                      }
                    }}
                    onRegenerateAudio={regenerateAudioItem}
                    onUploadWordAudio={uploadWordAudio}
                  />
                  <label>
                    页面 JSON
                    <textarea
                      disabled={processing}
                      rows={18}
                      value={raw}
                      onChange={(e) => setRaw(e.target.value)}
                    />
                  </label>
                  {page.issues.length > 0 && (
                    <ul className="admin-error">
                      {page.issues.map((issue: string) => (
                        <li key={issue}>{issue}</li>
                      ))}
                    </ul>
                  )}
                  <p className="admin-note">
                    审核状态：
                    {!currentHasAudioContent
                      ? currentHasOCRContent
                        ? "本页片段均设置为不生成音频，可直接审核通过"
                        : "本页没有可朗读 OCR 内容，可直接审核通过并跳过音频"
                      : currentReviewed
                      ? audioRequired
                        ? `本页 OCR 与${enabledAccentLabel}均已确认`
                        : "本页 OCR 已确认（已跳过音频）"
                      : currentHasAudioIssues
                        ? `本页有 ${audioIssues.length} 个音频需要单独处理`
                      : currentOCRReviewed
                        ? audioGeneratedForCurrent
                          ? audioRequired
                            ? `OCR 已确认，待试听并确认${enabledAccentLabel}`
                            : "OCR 已确认，待点击确认跳过音频"
                          : `OCR 已确认，待生成${enabledAccentLabel}`
                        : "待确认 OCR 正文、坐标与置信度"}
                  </p>
                  <div className="action-row">
                    <button
                      disabled={!can("content.write") || !editableDraft || processing}
                      onClick={() => void queueReOCR(pageNo)}
                      title="清除当前页 OCR、翻译、音标、审核状态和音频后重新识别"
                    >
                      重新 OCR 本页
                    </button>
                    {currentHasAudioContent && audioRequired && (
                      <button
                        className="audio-page-replace"
                        disabled={
                          !can("content.write") ||
                          !editableDraft ||
                          detail.draft.status !== "draft" ||
                          processing ||
                          currentHasIssues
                        }
                        onClick={() => void queuePageAudioReplacement()}
                        title={
                          currentHasIssues
                            ? "请先修正本页 OCR/翻译/音标中的实际问题"
                            : currentOCRReviewed
                              ? "清除本页全部正式音频和失败候选后重新生成，不影响 OCR 和其他页面"
                              : "OCR 数据完整，无需重新 OCR；点击后自动确认 OCR 并重新生成本页全部音频"
                        }
                      >
                        清除旧音频并重新生成本页
                      </button>
                    )}
                    <button
                      disabled={
                        !can("content.write") ||
                        !editableDraft ||
                        processing ||
                        translationSubmitting
                      }
                      onClick={() => void run(() => savePageReview())}
                    >
                      保存本页修改
                    </button>
                    {currentHasOCRContent && (
                        <button
                          disabled={processing || translationProcessing || translationSubmitting || !currentIsLast}
                          onClick={() => void queueTranslation()}
                          title={!currentIsLast ? "请切换到最新页后补全" : "每次点击都会先清空本页已有翻译、词义和音标，再重新生成"}
                        >
                          {translationProcessing || translationSubmitting ? "正在重新生成翻译和音标…" : "一键补全翻译和音标"}
                        </button>
                    )}
                    <button
                      className={currentHasAudioIssues ? "admin-primary audio-issue-entry" : "admin-primary"}
                      disabled={
                        !can("content.write") ||
                        detail.draft.status !== "draft" ||
                        processing ||
                        currentHasIssues ||
                        (currentReviewed && !audioFreePageNeedsNext)
                      }
                      title={
                        currentHasIssues
                          ? "请先补全并保存页面中的待完成内容"
                          : currentHasAudioIssues
                            ? `进入异常处理，逐项处理剩余的 ${audioIssues.length} 个失败音频`
                          : !currentHasAudioContent
                            ? audioFreePageNeedsNext
                              ? `本页无需生成音频，将审核通过并生成第 ${lastPage + 1} 页 OCR`
                              : currentHasOCRContent
                                ? "本页片段均设置为不生成音频，将直接审核通过"
                                : "本页没有可朗读 OCR 内容，将跳过音频生成"
                            : !currentOCRReviewed
                            ? audioGeneratedForCurrent
                              ? "先确认 OCR 正文、坐标与置信度"
                              : `确认 OCR 后才会生成本页${enabledAccentLabel}`
                            : !audioGeneratedForCurrent
                              ? `OCR 已确认，现在生成本页${enabledAccentLabel}`
                              : !audioRequired
                                ? "当前草稿已关闭发音，本页将跳过音频"
                                : `试听${enabledAccentLabel}后再确认`
                      }
                      onClick={() => {
                        if (currentHasAudioIssues) {
                          openAudioReview(id, pageNo);
                        } else if (!currentHasAudioContent) {
                          void approveWithoutAudio();
                        } else if (!currentOCRReviewed) {
                          if (audioGeneratedForCurrent) {
                            void run(() => savePageReview("本页 OCR 已确认", "ocr"));
                          } else {
                            void queueAudio();
                          }
                        } else if (!audioGeneratedForCurrent) {
                          void queueAudio();
                        } else if (!currentAudioReviewed) {
                          if (currentIsLast && sourcePageCount > lastPage) {
                            void queueNextPage();
                          } else {
                            void run(() =>
                              savePageReview(
                                audioRequired ? `本页${enabledAccentLabel}已确认` : "本页 OCR 已确认（已跳过音频）",
                                "audio",
                              ),
                            );
                          }
                        }
                      }}
                    >
                      {currentHasAudioIssues
                        ? `处理 ${audioIssues.length} 个失败音频`
                        : !currentHasAudioContent
                        ? audioFreePageNeedsNext
                          ? currentReviewed
                            ? `继续生成第 ${lastPage + 1} 页 OCR`
                            : `审核通过并生成第 ${lastPage + 1} 页 OCR`
                          : currentHasOCRContent
                            ? "审核通过（本页不生成音频）"
                            : "审核通过（跳过音频）"
                        : !currentOCRReviewed
                        ? audioGeneratedForCurrent
                          ? "确认本页 OCR"
                          : audioRequired
                            ? `确认 OCR 并生成${enabledAccentLabel}`
                            : "确认本页 OCR（跳过音频）"
                        : !audioGeneratedForCurrent
                          ? `生成本页${enabledAccentLabel}`
                          : !currentAudioReviewed
                            ? currentIsLast && sourcePageCount > lastPage
                              ? audioRequired
                                ? `确认${enabledAccentLabel}并生成第 ${lastPage + 1} 页 OCR`
                                : `确认本页并生成第 ${lastPage + 1} 页 OCR`
                              : audioRequired
                                ? `确认本页${enabledAccentLabel}`
                                : "确认本页（跳过音频）"
                            : "本页审核已完成"}
                    </button>
                  </div>
                </div>
              )}
            </div>
          )}
        </section>
      )}
    </>
  );
}

function Table({
  rows,
  action,
}: {
  rows: Row[];
  action?: (row: Row) => React.ReactNode;
}) {
  const safeRows = rows || [];
  if (safeRows.length === 0) return <p>暂无数据。</p>;
  const keys = Object.keys(safeRows[0]).filter(
    (key) => key !== "password_hash",
  );
  return (
    <div className="table-scroll">
      <table>
        <thead>
          <tr>
            {keys.map((key) => (
              <th key={key}>{key}</th>
            ))}
            {action && <th>操作</th>}
          </tr>
        </thead>
        <tbody>
          {safeRows.map((row, index) => (
            <tr key={row.id || index}>
              {keys.map((key) => (
                <td key={key}>
                  {typeof row[key] === "object"
                    ? JSON.stringify(row[key])
                    : String(row[key] ?? "")}
                </td>
              ))}
              {action && <td>{action(row)}</td>}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
