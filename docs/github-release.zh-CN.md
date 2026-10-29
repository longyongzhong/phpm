# GitHub 自动签名：首次配一次，以后自动生成草稿

当前推荐的日常发布路线。一次本机初始化、一个 GitHub Environment Secret，随后我负责触发构建、签名和草稿上传。原有完全离线签名仍可选用。

## 你只需首次配置

在仓库目录的 PowerShell 执行：

```powershell
cd D:\phpstudy_pro\WWW\xzdawn\phpm
.\bin\phpm-release.exe setup-github
```

它自动生成或复用 `C:\Users\11260\phpm-signing` 下的根密钥、发布密钥，并生成 `release\trust` 中的公开授权文件。私钥目录位于仓库/网站目录之外；既有密钥不覆盖，已存在的公开授权目录也不覆盖。

根私钥只用于首次授权和后续续期/撤销；保留本机备份，不上传。发布密钥供 GitHub 日常签名使用。本机 `.seed` 文件没有加密，备份由你保管。默认根授权有效 365 天，发行元数据有效 30 天。

执行完成告诉我，我会核对并提交 `release/trust` 中的四个公开文件。接着你只需：

1. 在 PowerShell **自行执行**下面命令，把发布私钥复制到剪贴板；它不在终端打印私钥。
2. 打开仓库 **Settings → Environments → phpm-release**。
3. 在 **Environment secrets → Add environment secret** 中，名称填 `PHPM_RELEASE_SEED`，值粘贴后保存。

```powershell
Get-Content "$env:USERPROFILE\phpm-signing\publisher.seed" -Raw | Set-Clipboard
```

不要复制 `root.seed`，也不需要把私钥或剪贴板内容发给我。配置完成后可自行清空剪贴板。

## 以后每次发布

告诉我候选版本，例如 `0.1.0-rc.1`。我检查代码和已有阶段 A 构建，运行 **GitHub signed release draft**，填写版本及两版阶段 A 构建运行 ID。工作流：

1. 校验 `main` 分支及已提交的公开根授权。
2. 构建带候选版本号的 Windows/Linux 客户端，验证 PHP/扩展包，完成临时密钥验收。
3. 在 `phpm-release` Environment 中使用 `PHPM_RELEASE_SEED` 签名、核验；临时私钥文件在步骤结束时清理。
4. 另一任务重新核对签名、版本、源码提交和上传摘要，创建 **draft prerelease**。

签名目录序号使用本次 Actions run ID。不会覆盖已有 Release/资产；重试已有版本若已生成草稿，会拒绝覆盖，先检查旧草稿再决定如何处理。源码、授权和材料必须来自同一个受控提交。

我给你草稿链接审阅，正式公开发行单独确认；触发此工作流不会直接公开发行。

## 权限与维护

`phpm-release` Environment 配置为仅允许 `main` 分支。日常 CI、PR、材料构建和草稿上传任务都不读取发布密钥，只有签名步骤获得该 Secret。上传任务单独持有 `contents: write`，签名任务只有读权限。签名步骤不将密钥放进参数、日志、仓库、缓存或上传资产。

有权修改 `main` 上签名工作流的维护者可以改变密钥使用方式，应审阅工作流变更。GitHub Secret 保护存储和受控注入，不替代仓库权限管理。

根授权到期/发布密钥轮换时，再由本机根密钥签发更高版本授权，并保留完整撤销历史；更新公开文件和 Environment Secret。`setup-github` 是首次初始化助手，涉及既有撤销历史的维护使用 `phpm-release trust`，详见离线指南。根密钥始终不进入 Actions。
