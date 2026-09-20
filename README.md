# xiaoV2 · 小小点读家

本项目将 `/Users/jiechen/english` 的学生账号、管理员账号、RBAC、审计、教材制作/审核/发布及点读阅读能力迁移到 Go 1.25.6 + Gin + GORM + MySQL，并保留 `xiaoV2` 的统一 `book_id` 资源协议。源项目仅作为参考，不会被修改。

## 启动

项目会自动读取根目录 `.env`；已有进程环境变量优先于 `.env`。密码不会被打印。先安装 Poppler 和项目隔离的 PaddleOCR 环境，再构建前端、启动 Gin：

```bash
cd /Users/jiechen/xiaoV2
brew install poppler python@3.12
python3.12 -m venv .venv
.venv/bin/pip install -r requirements-ocr.txt
.venv/bin/pip install -r requirements-audio.txt
.venv/bin/pip install -r requirements-translate.txt
npm --prefix web install
npm --prefix web run build
go run ./cmd/server
```

启动命令会立即打印两个地址：

- 前台：<http://127.0.0.1:8080/>
- 学生账号：<http://127.0.0.1:8080/account>
- 管理后台：<http://127.0.0.1:8080/admin>
- 管理员登录：<http://127.0.0.1:8080/admin/login>

首次使用后台时，在私有 `.env` 临时设置 `ADMIN_EMAIL` 和至少 12 字节的 `ADMIN_PASSWORD`，然后执行：

```bash
go run ./cmd/server bootstrap
```

初始化已有超级管理员时命令不会重复创建。完成后可以从环境中移除 `ADMIN_PASSWORD`。管理员与学生是两个独立账号域，会话 Cookie、密码和角色不共享。

## 功能

- 学生：注册、验证邮箱、登录/退出、忘记/重置密码。
- 阅读：动态书架、页面目录和英美音点读；所有已发布教材均可直接阅读。
- 管理员：独立登录/找回密码、创建/启停管理员、角色和权限分配。
- 运维：学生启停、网站设置、操作审计。
- 模型设置：后台从统一注册表读取 OCR/TTS 模型与音色；默认继续使用本地 PaddleOCR 与本地 Qwen3-TTS，可切换到百炼 `qwen3.5-ocr` / `qwen3-tts-flash`。
- 教材：PDF 上传后只生成第 1 页 OCR；OCR 正文、坐标与置信度确认后才生成该页英美音频，试听确认后才可生成下一页 OCR；草稿复制、元信息/正文/坐标 JSON 编辑、整本审核、发布与上下架。
- 工作任务：Gin 启动时内置教材 worker；也可单独执行 `go run ./cmd/server worker`。

本地开发默认将验证/重置邮件写到 `.local/mail`，不会发送真实邮件。生产环境可清空 `DEV_MAIL_DIR` 并配置 SMTP。

## 教材资源协议

MySQL 是目录和权限的唯一可信来源。文件统一放在：

```text
storage/books/{book_id}/
├── source/
├── pages/
├── audio/
├── tts/
├── ocr/
├── text/
├── metadata/pages/
└── cache/
```

`book_id` 必须以字母或数字开头，长度 1–80，只允许字母、数字、`-`、`_`。Go 与 Python 都拒绝绝对路径、目录穿越和越界资源。草稿先保存在 `storage/editor/{draft_id}`；审核发布后才归档到稳定的 `storage/books/{book_id}`。

也可在命令行生产和导入已人工校对的资源：

```bash
.venv/bin/python scripts/prepare_book.py --input /path/book.pdf --book-id my-book --title '教材名称' --grade 4 --semester 上册
.venv/bin/python scripts/generate_audio.py --book-id my-book --page 1 --resource-root storage/books
go run ./cmd/bookctl --publish import my-book
```

PDF 转换需要本机 Poppler（`pdfinfo`、`pdftoppm`）。每一页先由 `pdftoppm` 以 300 DPI 渲染为最终 PNG；OCR 坐标归一化到该页面矩形（`[x,y,w,h]`），因而在任意屏幕尺寸都与 PNG 保持同一方向和位置。转换器优先检查 PDF 自带文本层；文本缺失或质量不合格时，使用常驻的 `ocr_daemon.py` 加载 PP-OCRv5 检测模型和 `PP-OCRv6_medium_rec` 识别模型，后续页面复用同一个模型进程，保留行和单词置信度、单词框并标记低置信度内容。OCR 会安全清洗引号、空白和省略号，过滤装饰性低置信度噪声，并把同段落中未以句末标点结束的视觉换行合并；标题、项目、表格和填空布局保持独立。管理后台的“一键补全翻译和音标”通过 `.env` 配置的阿里云百炼 OpenAI-compatible 接口（当前模型为 `qwen3.7-flash`）一次提交整页内容，再顺序执行整页翻译和整页审核；每页最多只有这两次模型调用，不按句子并发请求，限流会自动退避重试。音标仍由 `eng-to-ipa` 离线库补全，只填空白内容，不覆盖人工内容，按钮可重复点击。首次 OCR 会联网下载模型到 `.local/paddlex`，后续复用缓存；本项目不再依赖 Tesseract。

音频依赖见 `requirements-audio.txt`。常驻 Worker 支持 Apple MLX 的 `mlx-community/Qwen3-TTS-12Hz-0.6B-CustomVoice-8bit` 与 `mlx-community/Qwen3-TTS-12Hz-1.7B-CustomVoice-8bit`，后台可无缝选择；同一时刻只允许一个本地 Qwen TTS daemon 驻留内存，模型切换会结束旧 daemon，新模型在下一次真正生成音频时懒加载并继续常驻复用。两个本地模型共用完全相同的英美音 speaker、instruct、采样参数和 QA 流程，同时复用 faster-whisper；现有单词音频缓存继续允许跨本地模型复用。每次仍会按页面、片段、单词位置和口音生成全新的独立 WAV，不按文本复用，也不从句子音频裁剪单词。临时 WAV 只有通过 ASR、词级时间戳/能量对齐、文件大小、静音、时长、削波、拖尾和重复检测后才原子写入正式 manifest。失败项最多重试 `AUDIO_MAX_RETRY` 次，最终失败 WAV 与 JSON 留在 `tts/audio_failed/`，逐条 QA 日志写入 `tts/qa/page-NNN.jsonl`。Qwen 与 Whisper 首次运行会下载到 Hugging Face 和 `AUDIO_ASR_CACHE` 缓存，之后可以离线推理。 0.6B 可用 `TTS_MODEL_06B`（旧 `TTS_MODEL` 仍兼容）覆盖模型仓库，1.7B 可用 `TTS_MODEL_17B` 覆盖；默认分别指向上述两个 MLX 8bit 仓库。TTS 服务必须从 Apple Silicon 原生终端启动，不能运行在没有 Metal 设备的虚拟或沙箱会话中。

云模型只从服务端环境读取 `DASHSCOPE_API_KEY`，未配置时服务仍能启动，本地模型照常可用，后台会把云模型标记为不可用。可用 `DASHSCOPE_BASE_URL` 覆盖地域地址。云端 OCR 每页一次模型请求；云端 TTS 每个独立音频项一次模型请求。两者都不自动重试，也不自动回退到本地；接口错误或本地 QA 未通过时任务进入 `manual_review_required`，只有管理员明确点击重新生成才会创建新任务。本地模型继续使用原有质量门禁和重试策略。OCR JSON、TTS 目录、WAV 命名和 manifest 协议保持不变，额外记录模型、供应商与 request_id。

后台实时音频状态保存在 `textbook_audio_items`，每次生成和 QA 历史保存在 `textbook_audio_attempts`；WAV、QA JSONL 和发布兼容的 `manifest.json` 仍保存在文件系统。编辑页、审核队列和任务轮询只查询数据库，不再按音频项重复解析 manifest。历史草稿可执行 `GOCACHE=/tmp/xiaov2-go-cache go run ./cmd/bookctl migrate-audio-state` 幂等回填；服务首次访问尚未迁移的草稿时也会自动回填。


人工校对后的单页 OCR/翻译 JSON 可以直接导入已有草稿页。导入会校验 segment/word ID、句子坐标和单词框，拒绝与正在排队/运行的 OCR/TTS 任务并发，并自动重置该页旧音频状态：

```bash
go run ./cmd/bookctl import-page \
  --draft-id <draft_id> \
  --page 32 \
  --ocr /path/page-032-ocr.json \
  --content /path/page-032.json
```

成功后会写入 `storage/editor/{draft_id}/work/{book_id}/ocr/page-NNN.json` 和 `metadata/pages/page-NNN.json`，同时更新 MySQL `textbook_draft_pages.content`，后台刷新即可看到导入内容。

## API

JSON 响应统一为 `{ "code": 0, "message": "ok", "data": ... }`。主要接口：

- `/api/v1/auth/*`、`/api/v1/me`：学生认证。
- `/api/v1/admin/auth/*`、`/api/v1/admin/me`：管理员认证。
- `/api/v1/books/*`：书架、页面、图片、音频。
- `/api/v1/admin/rbac`、`roles`、`accounts`：RBAC。
- `/api/v1/admin/students`、`site-settings`、`audit`：运营管理。
- `/api/v1/admin/models`、`models/:id/voices`、`settings/models`：模型注册表、后端音色目录和模型设置。
- `/api/v1/admin/drafts/*`：上传、转换、编辑、排序、音频、审核和发布。
- `/api/v1/admin/drafts/:id/status`：轻量任务状态和每页异常计数。
- `/api/v1/admin/audio-review`：按教材和页码聚合的人工审核队列。
- `/api/v1/admin/books/*`：正式教材查看与上下架。

所有非 GET 请求需要同源浏览器请求头 `X-Requested-With: xiaov2-web`；前端 API 客户端会自动添加。

## 数据库

GORM 自动迁移书籍/页面、学生/管理员、分域会话与邮件令牌、角色权限、网站设置、审计、页面版本、教材草稿/页面/任务，以及音频当前状态/生成历史表。迁移只创建或补充结构，不写入演示教材。初始书库保持为空。

## 验证

```bash
GOCACHE=/tmp/xiaov2-go-cache go test ./internal/... ./cmd/...
GOCACHE=/tmp/xiaov2-go-cache go vet ./internal/... ./cmd/...
PYTHONPATH=scripts .venv/bin/python -m unittest discover -s scripts/tests -v
npm --prefix web run build
```
# xiaoV2
