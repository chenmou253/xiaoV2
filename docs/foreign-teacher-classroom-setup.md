# 外教 1v1 课堂接入说明

当前代码已接入教师账号、排班/请假、管理员排课、学生预约、课程结算、RTC 与互动白板页面。阅读和教材制作功能继续沿用现有入口。

## 服务端配置

在项目根目录私有 `.env` 或部署环境中设置以下变量。密钥仅进入 Go 服务端，不写入前端构建环境。

```dotenv
AGORA_APP_ID=
AGORA_APP_CERTIFICATE=
AGORA_WHITEBOARD_APP_IDENTIFIER=
AGORA_WHITEBOARD_ACCESS_KEY=
AGORA_WHITEBOARD_SECRET_KEY=
AGORA_WHITEBOARD_REGION=sg
```

`AGORA_WHITEBOARD_REGION` 与声网白板项目区域一致。教师激活链接依赖现有邮件配置：生产环境配置 SMTP 与实际 HTTPS `APP_ORIGIN`；本地开发可查看 `DEV_MAIL_DIR` 中的邮件。未配置 Agora 时，账号与排课页面仍可使用，进入课堂会明确提示服务未配置。

启动方式与现有项目相同：构建前端并启动 Go 服务。首次启动通过现有 `AutoMigrate` 新建教师、课程、出勤等表，并为超级管理员新增教师及课程权限。非超级管理员须由角色管理页分配 `teachers.read/write`、`lessons.read/write/cancel`；手工排课还需要 `users.read` 来选择学生。

## 入口

- 学生：`/account`，含预约、课程与资料。
- 外教：`/teacher/login`，由管理员创建账号并通过邮件设置密码。
- 管理员：`/admin` 中的“外教管理”和“课程管理”。
- 课前设备检测：在学生或教师课程列表进入；正式课堂从检测页进入。

## 上线前工作

需要在实际 Agora RTC 与白板项目中核对房间区域、令牌、设备权限、屏幕共享、断线重连和网络域名；当前环境没有供应商密钥，尚未完成真实双人通话联调。正式环境要求 HTTPS，并应按实际网络请求收紧 CSP 域名列表。

取消课程会停止服务端发放新令牌、通知正常客户端退出并禁用白板。已经连接且不执行前端逻辑的 RTC 客户端能否被供应商立即强制踢出，仍需在供应商环境验证；上线前应确认该能力或明确运营处理方式。当前出勤由浏览器连接回执和心跳计算，适合学习记录，不能直接用于工资争议裁定。

Fastboard 上游仍带有若干中等级别的传递依赖告警；项目已将 `jspdf` 固定到修复较高风险告警的版本。升级后的课堂与 PDF 导出相关兼容性需要在真实白板项目中检查。
