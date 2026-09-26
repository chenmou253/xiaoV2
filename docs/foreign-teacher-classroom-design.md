# xiaoV2 外教 1v1 课程系统设计方案（评审修订版）

状态：第一版功能已按本稿进入实现；供应商联调与上线验收仍待完成。修订日期：2026-09-26。基于用户提供的原方案及当前仓库代码整理。

## 1. 评审结论

原方案以 `Lesson` 为业务核心、让教材点读与在线课堂平级，方向可行。建议保留 React + Go + MySQL，音视频使用 Agora RTC，白板使用 Fastboard，第一版不引入 RTM。实施前须明确以下事项：

| 优先级 | 原方案缺口 | 修订决定 |
| --- | --- | --- |
| P0 | 现有认证代码只识别 `admin`，其他值默认落入 `student` | `student`、`teacher`、`admin` 三种账号类型采用显式分支；未知类型立即拒绝。教师拥有独立表、会话 Cookie、密码重置令牌和路由守卫。 |
| P0 | “MySQL Transaction 创建 Lesson”不足以防止并发重叠 | 所有排课入口在同一事务内按固定顺序锁定教师行、学生行，再查重叠并写入；冲突返回 409。 |
| P0 | 登录后只凭前端倒计时结束课堂 | 后端统一控制加入和续签，定时任务结算状态；前端按服务器时间离开；RTC 权限有效期与课程截止一致，白板房间结束时禁用。 |
| P0 | `join` 接口既发令牌又被当作出勤证据 | “获取令牌”和“实际在场”分开；出勤须由实际连接回执/心跳确认，断网与刷新可恢复。 |
| P1 | 白板房间创建与数据库事务跨服务，失败路径未定义 | Lesson 先提交，再由可重试的房间准备流程创建并保存 UUID；失败显示“课堂准备中”，保留可重试状态。 |
| P1 | 时间窗口、缺席、实际授课分钟数存在歧义 | 下文给出状态判定表和连接时段口径；两人都未到记为 `expired`。 |
| P1 | 原分期将 Teacher Time Off 和取消排在预约之后 | 可约时间计算从第一版就纳入请假；至少支持管理员取消，学生自助取消可后置。 |
| P1 | 未说明浏览器、HTTPS 和现有 CSP 限制 | 摄像头/麦克风/共享需要安全上下文；上线前确认浏览器矩阵和声网域名的最小 CSP 允许列表。 |

## 2. 本次交付边界

第一版提供教师账号、管理员管理、开放时间与请假、管理员排课、学生预约、学生/教师课程列表、课前设备检测、1v1 RTC、Fastboard、教师屏幕共享、倒计时、自动结课、课程记录和月度统计。教材点读继续独立运行。

第一版不含支付、录制、字幕、教材同步、聊天、工资结算、家长账号。管理员取消纳入第一版，因为排错课、请假和供应商故障都需要可用的运营处置入口；学生自助取消和改课留待第二版。

上线对象先限定桌面 Chrome、Edge、Safari 的近期版本。手机端是否支持屏幕共享、扬声器选择及白板完整工具，要按浏览器实际能力再确定；不支持时隐藏对应入口并给出说明。

## 3. 与当前 xiaoV2 的接入方式

- 当前认证主体在 `internal/model/platform.go`，会话解析在 `internal/repository/platform.go`，按路径判断账号类型的逻辑在 `internal/handler/platform.go`，路由在 `internal/router/router.go`。新增教师不能复用 Admin Role，也不能让未知 `kind` 继续进入 Student 分支。
- 当前 `web/src/App.tsx` 仅按 `/account`、`/admin` 分流，其余路径落到书架；新增 `/teacher/*`、`/account/*` 和 `/classroom/*` 时须先分流。学生 `/account` 保留已有登录/注册/重置能力，并升级为学习中心。
- 管理后台导航及权限在 `web/src/Admin.tsx`、`internal/database/database.go` 和 `internal/service/platform.go`。新增 `teachers.read`、`teachers.write`、`lessons.read`、`lessons.write`、`lessons.cancel` 权限，并赋给超级管理员；按岗位分配给其他管理员。教师不进入现有 RBAC 管理页。
- 当前 API 返回形状为 `{code,message,data}`；新增 API 保持一致。会话 Cookie 沿用 HttpOnly、Secure、SameSite 和 `/api/v1` 路径约定；写接口沿用现有请求来源校验。
- 当前安全头的 CSP 只有 `'self'`，无法直接放行 RTC 与白板所需连接。接入供应商时，按实际 SDK 网络请求为 `connect-src`、`worker-src` 等添加最小域名列表，避免全域通配。
- 当前数据库通过 GORM `AutoMigrate` 启动迁移。新增表可在开发阶段沿用，但上线前应审核索引、约束和现有数据迁移；不要依赖 AutoMigrate 自动修正破坏性的结构变化。

## 4. 领域模型

### 账号与资料

- `teachers`：`id`、唯一小写邮箱、密码哈希、`verified`、`active`、展示名、头像、国家、IANA 时区、简介、创建/更新时间。由管理员创建；首次登录建议通过一次性激活/设置密码链接，避免后台展示或传递明文初始密码。
- `teacher_sessions`、`teacher_email_tokens`：按现有学生/管理员会话模式独立建表；停用教师或重置密码时删除其会话。
- `student_profiles`：以 `student_id` 为主键/外键，存展示名、头像、年级、IANA 时区及家长联系人。邮箱和登录状态继续属于 `students`。缺少 profile 时以学生邮箱作展示回退。

### 排课

- `teacher_availabilities`：教师、当地星期、开始/结束分钟数、IANA 时区、启用状态；第一版每条区间限定同一当地日期内，跨午夜拆两条。保存时拒绝教师自身的重叠区间。
- `teacher_time_offs`：教师、UTC 起止、原因；即使预约尚未开放也应先设计，避免预约后再改变算法。
- `lessons`：唯一业务课程表，包含 `student_id`、`teacher_id`、UTC 计划起止、计划分钟数、来源 `student_booking|admin`、状态、可选备注、RTC 频道、白板 UUID/准备状态、grace 秒数、教师/学生首次实际连接时间、实际开始/结束时间、实际共同在线秒数、创建/更新时间。课程起止一律满足 `end > start`；`duration_minutes` 与起止一致。RTC 频道随机且全局唯一，不能只用可猜的课程序号。
- `lesson_participants`：每节课分配教师、学生各自的 RTC UID；若教师摄像头和屏幕同时对远端发布，还需独立的屏幕共享 UID。UID 在同一频道内唯一，不直接使用不同账号表的主键。
- `lesson_presence_segments`：角色、连接开始、最后心跳、离开时间；用于记录断线重连。`join` 发令牌不写入出勤，SDK 成功连接后再报告在线。客户端心跳只是第一版的出勤依据，不能作为工资结算级别的不可抵赖证据；若以后需要结算，应接入供应商服务端事件并对账。

所有时间戳存 UTC。MySQL 连接设为 UTC；API 输出带 `Z`/时区偏移的 RFC 3339 时间。时区字段存 IANA 名称，例如 `Europe/London`，不存固定 `+08:00` 作为用户时区。

## 5. 开放时间与预约

默认业务规则由服务端配置：30 分钟一节、至少提前 30 分钟、最多提前 14 天、每名学生每天最多 3 节。管理员可在后台调整；本次修订建议先把规则集中在 Go 配置/设置层，随后再做后台配置 UI。学生端只读取规则，不持有独立判断值。

`GET /api/v1/teachers/:id/availability?date=YYYY-MM-DD` 中的 `date` 定义为**学生当地日期**，必须由已登录学生 profile 的时区解释。服务端按教师所在时区生成当地开放窗口，再转 UTC，与教师请假、教师课程、学生已有课程求交，并应用提前量、最远日期和学生当地日上限。返回的 slot 带 UTC 起止及 `available`；前端只负责按学生时区显示。跨时区/夏令时切换日要逐个候选当地时间解析：不存在的当地时刻不产生 slot；重复的当地时刻以不同 UTC 起点区分，并在 UI 显示时区缩写/偏移。

排课与学生预约复用一个 `CreateLesson` 服务。校验教师、学生均存在且可用，时间对齐、开放时间、请假、预约提前量/最远日期、学生每日上限，以及双方课程重叠。管理员手工排课可显式绕过开放时间和提前量，但不能绕过双方冲突；绕过操作要写审计日志。所有占用时间的状态只有 `scheduled`、`in_progress`。重叠采用半开区间 `[start,end)`：`existing.start < new.end AND existing.end > new.start`。

**并发协议**：开启 MySQL 事务，先按固定顺序 `SELECT ... FOR UPDATE` 锁教师行，再锁学生行，然后查询双方占用区间并创建 Lesson。锁前不要执行会建立旧一致性快照的普通查询；使用 `READ COMMITTED` 或对冲突查询使用当前读，确保等待锁后看到前一笔已提交预约。学生预约和管理员排课必须共用这条路径；请假写入也要先锁对应教师行，再查课程冲突。这样同一教师的并发预约会串行，学生跨教师同时预约也会在学生锁上串行。出现死锁或锁超时按有限次数重试整个事务，最终返回 409；数据库索引覆盖 `(teacher_id, scheduled_start_at)` 和 `(student_id, scheduled_start_at)`。仅靠普通事务和“先查再写”仍会双订。

重复点击使用客户端生成的幂等键；`POST /bookings` 与管理员排课保存 `(actor_type, actor_id, idempotency_key)` 的唯一约束及结果。同键重复提交返回同一 Lesson，不生成第二节。

## 6. 课程状态与出勤

状态：`scheduled`、`in_progress`、`completed`、`teacher_no_show`、`student_no_show`、`cancelled`、`expired`。任何终态都不能重新进入。状态变更仅通过服务层，使用条件更新或行锁保证幂等。

| 时机 | 条件 | 结果 |
| --- | --- | --- |
| 排课提交 | 校验通过 | `scheduled` |
| 计划开始后 | 两端均有真实连接记录且有共同在线时间 | `in_progress`，第一次共同在线时记 `actual_start_at` |
| 管理员取消 | 尚未进入终态 | `cancelled`，写取消人、原因、时间；停止后续发令牌 |
| 计划结束时 | 计划上课区间内两端曾共同在线 | `completed` |
| 计划结束时 | 计划上课区间内仅学生实际连接过 | `teacher_no_show` |
| 计划结束时 | 计划上课区间内仅教师实际连接过 | `student_no_show` |
| 计划结束时 | 计划上课区间内双方均无实际连接记录 | `expired` |

缺席不在开始后 10 分钟立即终结，以免晚到者无法进入；可在 10 分钟时给管理员“可能缺席”提示，最终状态仍在计划结束时结算。网络中断不立刻判缺席；两端连接历史与心跳租约共同决定在线区间。

`actual_end_at` 取最后一次共同在线结束时刻，且不晚于计划结束；`teaching_seconds` 为双方在线区间交集与计划上课区间的交集总秒数，课前及断线期间不计时。`actual_start_at`/`actual_end_at` 仅表示首尾时间，不能用二者相减代替授课秒数。心跳间隔建议 20 秒、服务端宽限 60 秒，服务端按最近心跳结算；这属于可调运营口径，应在上线前确认。教师月度授课时间按 `teaching_seconds` 聚合，月份按教师当地时区解释后换算 UTC 边界；学生按学生当地月份统计。同一节课可能在两地归属不同月份，属于预期结果。

需要独立的课程结算任务定期扫描已到结束时间的非终态 Lesson，宕机重启后补跑。`leave` 只结束本次在线区间，不直接把课程标为 completed；老师或学生在允许窗口内可重新进入。

## 7. 课堂资源、权限与时间

课前 15 分钟可打开设备检测；`join` 允许区间是 `[start-10min,end)`。计划结束时前端提示结束并停止授课；已在房间者最多延续 60 秒供断线收尾，不再接受新加入。服务器拒绝终态、非课程本人、停用账号及窗口外请求，返回明确错误码。

加入接口返回 `server_time`、计划起止、`grace_seconds`、RTC App ID/频道/UID/短时令牌、白板 app identifier/region/UUID/房间令牌。前端用响应时的单调时钟计算剩余时间，页面可见性恢复时重新向服务端同步；不把本机墙上时间当唯一依据。下课前 5 分钟、1 分钟提示；计划结束时停止教学交互，最多 60 秒内释放音视频和白板并跳转完成页。

RTC 令牌在 Go 服务端以 App Certificate 生成，频道、UID、有效期绑定单节课。令牌有效期与加入/发布权限有效期都设到 `scheduled_end_at + grace` 附近，并处理即将到期的续签事件；服务端在结束或取消后拒绝续签。**仅缩短 token 字符串有效期不能保证已连接用户自动离开**，须同时设置 RTC 权限到期、前端退出及后端禁发；强制断开能力在正式上线前用供应商环境验证。前端不接触证书。

白板由服务端调用供应商 REST API 创建房间，保存 UUID，加入时签发房间令牌；证书/SDK Token 不到浏览器。Lesson 提交成功后由可重试的准备任务创建房间，采用 `pending/ready/failed` 状态；一次只允许一个准备者，若创建成功但写库失败，记录可清理的孤儿房间。房间 region 与客户端 SDK region 必须一致。课程取消时立即、正常结束后 grace 到期时禁用白板房间以移除已连接用户；结算任务与资源关闭任务分别幂等重试。学生只显示画笔、橡皮、文字；教师显示完整工具与清空。隐藏按钮属于产品权限，不应视为严格的白板服务端授权；若“学生绝不能清空”是强约束，开发前须验证供应商是否支持对应操作级权限。

教师共享屏幕时，优先保证教师摄像头、学生视频和共享画面可同时显示：教师另建一个仅发布屏幕视频的 RTC client，使用独立 UID 和令牌；屏幕音频默认关闭以避免回声。浏览器主动结束共享、网络重连、页面退出都要清理屏幕 client 并恢复白板布局。若实际 SDK/浏览器组合无法满足多路发布，可退化为“屏幕替代教师摄像头”，但须在产品验收前明确。

设备检测包括摄像头预览、麦克风音量、输出测试和基础网络探测。输出设备切换受浏览器能力与权限限制；不支持时用系统默认扬声器，页面不展示不可用的下拉框。摄像头、麦克风、屏幕捕获要求 HTTPS 或 localhost。

## 8. API 契约

沿用现有 `/api/v1` 与 `{code,message,data}`。所有列表分页；所有写操作校验当前身份，并记录必要审计。下表列出第一版最小接口，字段以本设计的模型与时间定义为准。

| 身份 | 接口 | 说明 |
| --- | --- | --- |
| 教师 | `POST /teacher/auth/login`、`POST /teacher/auth/logout`、`POST /teacher/auth/forgot`、`POST /teacher/auth/reset`、`GET /teacher/me` | 独立会话域 |
| 教师 | `GET /teacher/lessons`、`GET /teacher/statistics?month=YYYY-MM`、`GET/PATCH /teacher/profile` | 仅本人的资料与课程 |
| 学生 | `GET /student/dashboard`、`GET /lessons`、`GET /lessons/:id`、`GET/PATCH /student/profile` | 仅本人课程 |
| 学生 | `GET /teachers`、`GET /teachers/:id/availability?date=YYYY-MM-DD`、`POST /bookings` | 仅列 active 教师；预约带幂等键 |
| 双方 | `POST /classrooms/:id/join`、`POST /classrooms/:id/presence`、`POST /classrooms/:id/leave` | 教师端使用 `/teacher/classrooms/:id/*`，同一业务服务 |
| 管理员 | `GET/POST /admin/teachers`、`PATCH /admin/teachers/:id`、`GET/POST /admin/teachers/:id/availability`、`GET/POST /admin/teachers/:id/time-off` | 以独立权限控制；修改开放时间不自动改变已排课程 |
| 管理员 | `GET/POST /admin/lessons`、`POST /admin/lessons/:id/cancel`、`GET /admin/teachers/:id/statistics` | 排课、取消和统计 |

关键错误：401 未登录；403 非课程参与者或账号已停用；`40310` 上课窗口已结束；404 课程不存在；409 时段被占用或幂等键冲突；503 白板/RTC 配置暂不可用。错误消息给用户可操作的信息，不回显供应商密钥或完整令牌。

## 9. 页面与统计

学生学习中心保留现有登录、注册、重置密码，加入首页、我的课程、预约课程、我的书架、我的资料。教师区只有首页、我的课程、我的课时、我的资料。管理员区增加外教、可授课时间/请假、课程排课与取消、教师课时统计；原有教材管理继续使用原权限。课堂页由学生与教师共用，默认 70% 白板/共享画面，30% 视频；窄屏按实际宽度折叠。

统计以 `lessons` 的终态和 `teaching_seconds` 为准，不为第一版新增月度汇总表。教师统计展示完成、学生缺席、取消、实际授课秒数；学生仅展示完成节数与学习时间。课程详情展示实际开始、结束、断线区间和状态原因，方便管理员核对统计。

## 10. 实施顺序与验收门槛

1. **账号**：教师表、独立认证/会话/重置、后台创建与启停、权限和审计；教师无法进入管理后台或学生账号接口。
2. **排课**：Lesson、Availability、Time Off、管理员排课/取消；UTC 与双时区预览；双方冲突检查。
3. **自主预约**：候选 slot、服务器复核、锁与幂等；两名学生同时抢同一时段最多生成一节课。
4. **媒体**：设备检测、RTC 1v1、屏幕共享；桌面浏览器与弱网重连验证。
5. **白板**：房间准备/重试、Token、教师/学生工具权限与房间结束处置。
6. **时间与出勤**：服务端时钟、提示、自动离开、任务补偿、缺席及实际共同在线秒数；刷新、断网、服务器重启不产生重复结算。
7. **统计与运营**：教师、学生、管理员月度统计；抽样核对课程记录和统计口径。

每阶段完成后可独立提交。正式上线前还要确认声网 RTC 与互动白板服务已开通、服务区域、浏览器支持、预估费用、HTTPS、供应商连接域名与 CSP，以及老师/学生跨地区服务可用性。供应商密钥仅以服务端环境变量配置。

## 11. 上线前待确认的产品决定

1. 教师和学生是否允许提前 10 分钟实际进入同一 RTC 频道？若允许，是否把课前交流计入 `teaching_seconds`？本稿默认**不计入计划开始前时间**。
2. 教师请假与已有课程冲突时，由管理员先取消/改排课程，还是允许请假先落库并生成待处理任务？本稿默认**拒绝直接覆盖已有课程并提示逐节处理**。
3. 学生白板“不能清空”是界面约束还是必须服务端强制？本稿默认**界面约束**，严格授权需供应商能力验证。
4. 统计是否用于工资或争议裁定？本稿的浏览器心跳适合学习记录，**工资结算须另设计可信出勤来源**。
5. 是否必须同时展示教师摄像头和屏幕共享？本稿按**同时展示**设计，并要求媒体阶段验证。

## 12. 参考资料

- [Agora RTC 服务端 Token 与权限有效期](https://docs.agora.io/en/realtime-media/rtc/build/authenticate-users/deploy-token-server)
- [Agora Interactive Whiteboard 房间管理与禁用](https://docs.agora.io/en/api-reference/api-ref/whiteboard/room-management)
- [Agora Interactive Whiteboard 安全与令牌](https://github.com/AgoraIO/docs-portal/blob/main/content/docs/en/realtime-media/whiteboard/reference/security.md)
- [Fastboard 官方项目与 React 用法](https://github.com/netless-io/fastboard)
- [Agora Web SDK 发布说明（屏幕共享相关）](https://docs.agora.io/en/realtime-media/video/reference/release-notes/web)
- [MDN：摄像头与麦克风安全上下文](https://developer.mozilla.org/en-US/docs/Web/API/MediaDevices/getUserMedia)
- [MDN：屏幕捕获与浏览器限制](https://developer.mozilla.org/en-US/docs/Web/API/MediaDevices/getDisplayMedia)
- [MDN：输出设备选择](https://developer.mozilla.org/en-US/docs/Web/API/HTMLMediaElement/setSinkId)
