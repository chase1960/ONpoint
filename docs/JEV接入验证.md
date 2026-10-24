# JEV 本机接入验证

日期：2026-10-24（北京时间）。沿用本机已有的系统代理与用户级密钥完成接入，不把密钥写入配置文件或代码。

## 已实现

- `Start-ONpoint.ps1` 加载独立的 `JEV-Config.ps1` 和非敏感 `jev.settings.json`，读取当前进程／Windows 用户环境中的密钥。
- 读取 Windows 已启用的静态 HTTP/HTTPS 代理，仅传入 JEV 客户端；不修改系统代理或全局 HTTP_PROXY。支持显式 `TYPESAFE_PROXY_URL`，本机回环目标继续直连。
- Go 客户端通过显式代理执行 HTTPS CONNECT，保持 TLS 证书校验和禁止重定向；代理地址及密钥不进入前端状态或日志。
- 默认固定 `jev-1.14.0`，shadow 模式，15 秒超时。仅界面触发的合成样例可外发；没有后台预热、自动批量调用或自动重试。
- 新增 `-CheckJEV`，通过实际 Go 客户端只读取模型列表，不打开业务数据库、不调用推理。

## 实际验证结果

`Start-ONpoint.ps1 -CheckJEV` 返回：

```json
{"Status":"ok","Models":["jev-latest","jev-preview"],"ProxyEnabled":true,"InferenceRun":false}
```

这验证了本机密钥鉴权及代理链路，不证明模型判断准确率、成本或延迟。之前直连得到的 HTTP 451 不能单独用于判断账号能否使用；本机通过现有系统代理已成功访问官方接口。

全量 Go 测试通过，包含实际本机 HTTPS 代理隧道、模型列表与推理协议模拟、无上游响应或密钥泄漏、超时配置、无代理默认行为和原有业务回归。模拟推理只访问本机测试服务器。前端现有 10 项展示测试、PowerShell 配置测试和脚本语法检查均通过。

已核对旧实例位于本工作区，使用 `data/ota-routing.db`，随后备份数据库并重启。运行中的服务状态：`Mode=shadow`、`Model=jev-1.14.0`、`Configured=true`、`Ready=true`。备份位于 `cache/ota-routing-before-jev-20261024-100205.db`。

访问 http://127.0.0.1:8088 。刷新原页面即可。服务启动不执行推理；在界面触发合成样例判定或批量对照后会产生账户用量，本次未执行在线推理实验。

## 再次启动或检查

```powershell
.\Start-ONpoint.ps1 -CheckJEV
.\Start-ONpoint.ps1
```

可在 `jev.settings.json` 中长期设为 `mode: off`，或启动前通过 `$env:JEV_MODE='off'` 临时停用。系统代理关闭时，脚本会明确报错，不会静默改走另一条网络路径。直接运行 Go 的环境配置说明见 README。
