# PHPM：我构建，你离线签名，再上传草稿

适用：Windows/Linux amd64 离线签名设备，需要 Python 3.10+。正式根私钥和发布私钥都留在你管理的加密存储中；不要放进仓库、网站目录、GitHub Secrets 或聊天。

## 1. 我负责构建材料

GitHub Actions 的 **Prepare unsigned offline release materials** 接收候选版本、成功的阶段 A PHP 8.4/8.5 运行 ID。它生成：

- 带实际候选版本号的 Windows/Linux PHPM 客户端。
- 两个平台的离线维护工具、签名脚本、安装器和许可证。
- 已通过门禁的 PHP/Redis 包与原始验收报告。
- `release-inputs.json`：精确源码提交、来源运行/产物摘要和每个材料文件的 SHA-256。

材料包为 artifact `phpm-offline-signing-materials-RUN_ID`，保留 14 天，不是正式发行包。工作流用一次性测试密钥验证完整离线签名→安装→实际版本检查，测试密钥和测试签名包随后丢弃。正式私钥不参与 CI。

我提供运行链接和 `release-inputs.json` 的 SHA-256。你下载并解压到可移动介质，带到离线设备。请求摘要应从你审阅的运行记录核对；构建摘要用于交接完整性，不代替对源码、构建来源的信任判断。

## 2. 你在离线设备核对材料

以下 PowerShell 示例假设解压目录为 `E:\phpm-materials`，密钥目录为你自己创建、备份的加密存储 `E:\phpm-keys`。实际位置由你选择。

```powershell
cd E:\phpm-materials
Get-FileHash .\release-inputs.json -Algorithm SHA256
```

先将结果与我提供、并在 Actions 记录中核对过的请求摘要比较。匹配后，用可信的 PowerShell 核对将要执行的签名脚本，再检查全部材料：

```powershell
$request = Get-Content .\release-inputs.json -Raw | ConvertFrom-Json
if ((Get-FileHash .\offline-sign.py -Algorithm SHA256).Hash.ToLower() -ne $request.files.'offline-sign.py'.sha256) { throw '签名脚本摘要不匹配' }
python .\offline-sign.py check --input . --request-sha256 BUILD_REQUEST_SHA256
```

`BUILD_REQUEST_SHA256` 替换为已核对的请求摘要。校验通过后才执行材料包里的维护工具。材料中夹带额外文件、文件被修改或缺失时会拒绝处理。

## 3. 首次生成正式密钥与授权（仅第一次）

```powershell
$tool = '.\tools\windows-amd64\phpm-release.exe'
& $tool keygen --private E:\phpm-keys\root.seed --public E:\phpm-keys\root.pub
& $tool keygen --private E:\phpm-keys\publisher-1.seed --public E:\phpm-keys\publisher-1.pub
& $tool trust --root-key E:\phpm-keys\root.seed --version 1 --key E:\phpm-keys\publisher-1.pub --output E:\phpm-keys\trust-v1
```

分别备份两套密钥，记录输出的根公钥指纹。seed 文件自身没有加密，必须由你选择的存储/备份方式提供加密保护。根私钥只用于授权、续期和撤销；每次发行只需要发布私钥和已有授权目录。默认授权 90 天、发行清单 30 天，需要在到期前续签。

Linux 使用 `tools/linux-amd64/phpm-release`，按需 `chmod +x`，并将路径替换为 Linux 路径；后面的 Python 签名命令相同。

## 4. 一条命令离线签名

```powershell
python .\offline-sign.py sign --input . --request-sha256 BUILD_REQUEST_SHA256 --root E:\phpm-keys\root.pub --fingerprint ROOT_SHA256 --trust E:\phpm-keys\trust-v1 --key E:\phpm-keys\publisher-1.seed --sequence 1 --output E:\phpm-signed-release
```

替换两个摘要占位符。输出目录必须不存在且位于材料包外。后续发行的 `--sequence` 必须递增；修改授权时授权 `--version` 也必须递增。版本号和源码提交自动取自已经核对的构建请求，签名脚本不联网，不需要 Go/PHP。

## 5. 我负责验证并上传草稿

只把 `E:\phpm-signed-release` 最终目录、公钥及根公钥指纹交给我；`root.seed` 和 `publisher-1.seed` 始终留在你那里。公钥/指纹应通过可信渠道核对。

我在联网设备执行完整签名、期限、清单和文件校验，通过后运行：

```sh
python build/release/publish.py --repo longyongzhong/phpm --tag v0.1.0-rc.1 --bundle /transfer/phpm-signed-release --root /trusted/root.pub --fingerprint ROOT_SHA256 --verifier /reviewed/phpm-release
```

公开和私有仓库都支持。脚本只创建 draft prerelease，按资产清单上传并核对远端大小/摘要，不覆盖已有 Release，不自动公开发行。草稿链接供你审阅；正式点发布前再明确确认。使用 `--test-preview` 的演练包不可作为正式信任根。

密钥轮换、撤销、手动更换根公钥和首次安装的信任边界见源码文档 `docs/offline-release.zh-CN.md`。
