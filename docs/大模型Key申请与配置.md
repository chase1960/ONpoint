# 大模型 Key 申请与本机配置

适用项目：当前目录的 有据。更新：2026-10-24。

## 1. 申请百炼 Key（默认方案）

1. 打开 [阿里云百炼控制台](https://bailian.console.aliyun.com/)，注册或登录阿里云账号。
2. 首次使用按页面提示开通百炼服务；如提示实名认证，由你在官方页面完成。充值、付款由你自行决定。
3. 地域选择 **华北2（北京）**。进入 **API Key** 页面，点击 **创建 API Key**。个人本机演示可使用自己的账号与默认业务空间。
4. 如需限制模型，选择自定义权限、开启访问模型范围，只勾选 `qwen-flash`。如页面允许备注，可填 `youju-demo`。
5. 创建后复制 Key，留在本机配置入口使用，**不要粘贴到聊天、截图、前端代码或说明文档中**。
6. 如需使用新版业务空间域名，另在业务空间管理页面复制业务空间 ID；它不是 API Key。

官方指引：[获取与配置 API Key](https://help.aliyun.com/zh/model-studio/get-api-key)、[首次调用千问 API](https://help.aliyun.com/zh/model-studio/first-api-call-to-qwen)。Key、域名和模型权限必须属于同一地域。

本机默认模型为 `qwen-flash`，使用仍受官方支持的北京接入地址 `https://dashscope.aliyuncs.com/compatible-mode/v1`。提供业务空间 ID 后改用官方建议的 `https://业务空间ID.cn-beijing.maas.aliyuncs.com/compatible-mode/v1`。参考：[OpenAI 兼容接口](https://help.aliyun.com/zh/model-studio/compatibility-of-openai-with-dashscope)。

截图中的价格只作历史参考，实际额度和费用以控制台及[官方价格页](https://help.aliyun.com/zh/model-studio/model-pricing)为准；有免费额度时注意有效期与适用模型。预算告警通常只是通知，不能当作消费硬上限。

## 2. 把 Key 配置到项目

在此文件夹空白处右键，选择“在终端中打开”；使用 PowerShell，运行：

```powershell
.\Configure-LLM.ps1
```

显示 `API Key:` 后粘贴 Key，按回车。输入会隐藏，粘贴后屏幕不显示原文是正常现象。不要把 Key 直接写在命令后面，以免留入命令历史。

若使用业务空间 ID：

```powershell
.\Configure-LLM.ps1 -WorkspaceId '替换为你的业务空间ID'
```

保存成功会显示 `Saved with Windows encryption...`。配置仅保存在本项目 `.secrets/` 中：

- `llm.credential.xml`：Windows DPAPI 加密凭证，仅同一电脑、同一 Windows 用户可解密。
- `llm.settings.json`：平台、模型与地址，不含密钥。

`.secrets/` 已加入项目忽略列表；复制项目、打包或共享时也应排除此目录。此保护不能阻止以你同一 Windows 身份运行的程序读取凭证。更换电脑、Windows 用户或部署云服务，需要重新配置。

如系统提示“禁止运行脚本”，可以仅对这一次启动使用：

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\Configure-LLM.ps1
```

该参数仅用于本次进程，不更改系统的永久执行策略；若单位策略仍禁止，按单位要求处理。

## 3. 验证与启动

仅检查本地配置，不调用模型：

```powershell
.\Test-LLM.ps1
```

需要确认真实 Key、模型权限和网络时，手动执行一次在线测试：

```powershell
.\Test-LLM.ps1 -Online
```

在线测试仅向选定平台发送固定文本 `Reply with OK only.`，限制最多 16 个输出 token，关闭思考，不发送客户资料；可能消耗少量额度。只请求一次，不自动重试。成功显示 `Online check passed`。本地检查成功不能代替在线验证。

常见失败：401 检查 Key 与地域；402 检查余额；403 检查模型权限/服务状态；404 检查模型名/域名；429 检查限流/额度。脚本不打印 Key 或平台原始错误响应。

配置后重新运行启动脚本，新进程才会读取配置：

```powershell
.\Start-Youju.ps1
```

启动脚本解密凭证并注入本次服务进程环境：`LLM_PROVIDER`、`LLM_BASE_URL`、`LLM_MODEL`、`LLM_API_KEY`，以及对应的 `DASHSCOPE_API_KEY` 或 `DEEPSEEK_API_KEY`。不会写系统永久环境变量，也不会把密钥返回网页。

**当前完成边界：**提供申请指引、加密保存、启动加载和手动在线连通性测试。Go 业务引擎目前仍使用本地规则与模板，尚未调用生成式模型完成客服回复；配置 Key 不会自动改变业务行为。TypeSafe/JEV 使用另一套 Key，此次不会自动启用它。合成业务数据的模型接入与效果验证另需实现。

## 4. 已有 DeepSeek Key 时

可以复用，无需再注册百炼。没有 Key 时，打开 [DeepSeek 开放平台](https://platform.deepseek.com/)，登录后在 API Keys 页面创建 Key，再运行：

```powershell
.\Configure-LLM.ps1 -Provider DeepSeek
.\Test-LLM.ps1
```

默认模型 `deepseek-flash`，地址 `https://api.deepseek.com`，依据 [DeepSeek 官方首次调用文档](https://api-docs.deepseek.com/)。后续模型更名可通过配置脚本的 `-Model` 参数修改。两平台只保存一份当前选择的配置，再次配置会替换本项目之前的选择。
