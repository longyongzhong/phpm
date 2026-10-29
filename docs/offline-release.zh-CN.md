# 离线签名与客户端分发

本文说明可选的完全离线签名。当前日常发布路线见 [GitHub 自动签名指南](github-release.zh-CN.md)：根私钥本机保管，发布私钥用于受保护的 Environment。

适用范围：公开或私有仓库的离线分发工具链。正式私钥由维护者在离线设备上生成并备份；GitHub Actions 仅使用临时测试密钥演练。当前没有内置正式根公钥，也没有自动公开发布。推荐先阅读[三步交接操作指南](release-quickstart.zh-CN.md)。

## 信任关系

根密钥签署 `trust.json`，授权一个或多个发布公钥。发布密钥分别签署 `catalog.json`（PHP/扩展包）和 `clients.json`（PHPM 客户端及完整资产清单）。三个角色采用不同的签名消息前缀，不能互换签名。

客户端固定根公钥，并要求操作者通过独立渠道核对其 SHA-256 指纹。下载目录旁边的 `root-key.pub` 只用于搬运，不能自行证明可信。全部签名均为 Ed25519；SHA-256 校验文件完整性。签名证明签发者认可这些文件，不自动证明构建服务器未被入侵、源码无漏洞或编译可复现。

`trust.json` 最长有效 365 天，目录和客户端清单最长 90 天且不得超过授权有效期。工具默认目录/客户端 30 天、授权 90 天。机器时钟必须准确；超时后先更新签名元数据再下载。

## 维护者首次正式签发

以下是离线设备上的示例命令。`/offline/keys` 应位于用户自己管理的加密存储中，不能放在仓库、网站目录、CI、聊天附件或待上传的发布目录中。工具生成的 seed 是未加密私钥文件，文件权限不能代替加密备份；丢失根私钥会失去后续授权能力。

从审阅过的源码构建维护工具，再离线执行：

```sh
go build -trimpath -o phpm-release ./cmd/phpm-release
./phpm-release keygen --private /offline/keys/root.seed --public /offline/keys/root.pub
./phpm-release keygen --private /offline/keys/publisher-1.seed --public /offline/keys/publisher-1.pub
./phpm-release trust --root-key /offline/keys/root.seed --version 1 \
  --key /offline/keys/publisher-1.pub --output /offline/trust-v1
```

记录两个命令输出的公钥指纹，并分别备份两套私钥。根私钥只用于授权、撤销和续期；日常签发使用发布私钥。

联网构建机运行阶段 A，下载通过门禁的两版 PHP 包及报告，并构建 Linux/Windows 客户端。将构建包、源码提交号、许可证和审阅过的安装器带到离线签名设备；核对 GitHub 运行记录、构建日志、源码来源、上游签名及包校验值。报告 JSON 自身不能替代对构建来源的审阅。

```sh
./phpm-release catalog --key /offline/keys/publisher-1.seed \
  --root /offline/keys/root.pub --trust /offline/trust-v1 \
  --sequence 1 --output /offline/php-bundle /inputs/php84 /inputs/php85

./phpm-release clients --bundle /offline/php-bundle \
  --root /offline/keys/root.pub --key /offline/keys/publisher-1.seed \
  --version 0.1.0 --commit FULL_40_CHARACTER_SOURCE_COMMIT \
  --linux /inputs/phpm --windows /inputs/phpm.exe \
  --notices /inputs/reviewed-source --installer /inputs/reviewed-source/scripts/install-phpm.py \
  --output /offline/release-0.1.0

./phpm-release verify --bundle /offline/release-0.1.0 \
  --root /offline/keys/root.pub --fingerprint ROOT_SHA256
```

每次变更目录必须递增 `--sequence`，每次变更授权必须递增 `--version`，不能在同一序号下重签不同内容。输出目录必须不存在。工具拒绝意外文件，发布目录只包含公钥、签名、元数据、包和报告。客户端包包含 PHPM、Go 和 YAML 的许可证文件。

仅将最终签名目录带回联网机，重新校验后上传 Release 草稿：

```sh
python3 build/release/publish.py --repo longyongzhong/phpm --tag v0.1.0-candidate \
  --bundle /transfer/release-0.1.0 --root /trusted/root.pub --fingerprint ROOT_SHA256 \
  --verifier /reviewed/phpm-release
```

上传使用已有 `gh` 登录，支持公开和私有仓库。脚本只创建 draft prerelease，逐个上传清单中的资产并核对 GitHub 返回的大小和摘要；不会覆盖旧资产。中途失败保留草稿供检查，不伪装完成，不自动删除。转为正式发行需要另行明确决策，脚本不修改仓库可见性。以上命令尚未代表用户已生成正式密钥或已正式发行。

## 新机器安装客户端

需要 Python 3.10+、支持 Ed25519 的 OpenSSL 3；不需要预装 PHP 或 Go。安装器必须来自已经审阅、可信的源码，不能先执行来源不明的安装脚本再依赖其自检。

私有草稿不能使用未认证的公共下载地址。先用自己的 GitHub 登录下载完整资产，例如在已创建的空目录执行 `gh release download TAG --repo longyongzhong/phpm --dir .`，再离线安装：

```sh
python3 scripts/install-phpm.py --source /downloads/release \
  --root /trusted/root.pub --fingerprint ROOT_SHA256 \
  --version 0.1.0 --dest "$HOME/tools/phpm-0.1.0"
"$HOME/tools/phpm-0.1.0/phpm" version
```

Windows 使用 `python` 和 Windows 路径，随后运行目标目录的 `phpm.exe`；OpenSSL 未在 PATH 时使用 `--openssl PATH_TO_OPENSSL`。支持 Linux/Windows amd64 客户端，暂不提供 macOS/ARM 包。

安装器验证授权、角色签名、期限、包绑定和包 SHA-256，拒绝链接、路径穿越、重复条目、缺少许可证或已有目标目录。先在临时目录完成校验，再提交整个目录；不修改 PATH、系统 PHP、服务配置，也不自动启动下载的程序。可以手动将目录加到 PATH。

`--source` 也可指定 HTTPS 资产目录，要求支持严格的分段 Range 下载。公开且发布完整资产后可使用 GitHub Release 下载目录；尚未发布的草稿仍应采用上述认证下载方式。安装失败需重新运行，安装器当前不保留跨次断点。

初次安装只有外部根指纹和可选的预期版本，没有历史状态，无法独立判断从未见过的、尚未过期的旧授权是否为最新版本。这不是完整 TUF 实现；应通过可信渠道获取当前授权/发行版本。安装器只装入新目录，不提供原地自更新；升级客户端时安装到另一目录并验证后手动切换。

## 注册 PHP 源与撤销

```sh
phpm source add official-test --catalog /downloads/release/catalog.json \
  --root /trusted/root.pub --fingerprint ROOT_SHA256
phpm source verify official-test
phpm php install 8.4 --source official-test
```

PHP 签名源当前仅覆盖 Debian 13 x86_64 NTS，不能据此声称支持 OpenWrt、其他发行版或全部扩展。原来的 `source add --key` 直签源仍兼容；根授权模式不会降级为直签。

发布密钥轮换：离线生成 publisher-2，根密钥签发更高版本授权，先同时授权新旧公钥，使用新密钥签发递增目录。确认迁移后，再用更高版本授权加入 `--revoke OLD_PUBLISHER_SHA256`。每份后续授权必须完整保留全部历史撤销 ID；即使当前没有可用发布密钥，也可发布仅含撤销记录的授权来冻结新下载。

PHPM 在取目录时保存最高授权版本、该版本的原始内容摘要、全部已见撤销及最高目录序号；拒绝回退、同序号换内容和撤销记录消失。即使目录仍由已撤销密钥签署而验证失败，也先保存新的有效撤销记录。撤销不杀死现有进程，已有锁定环境仍可离线运行；应对受影响业务另行评估、切换和重启。

根密钥更换必须通过独立渠道核验新根，不能由服务器自动换掉本地根。先用新根签署严格更高版本且保留完整撤销历史的授权及相应目录，再执行：

```sh
phpm source replace-root official-test --root /trusted/new-root.pub \
  --old-fingerprint OLD_ROOT_SHA256 --new-fingerprint NEW_ROOT_SHA256
```

该操作验证全部新元数据后才写入，并保留目录防回退状态。根私钥泄露后，客户端尚未获知的撤销无法靠网络自动解决，必须人工分发可信的新根和当前版本信息。

## 临时密钥演练与离线材料构建

`Offline-root client release rehearsal (stage D)` 接受已成功的阶段 A 两个运行 ID，构建两平台客户端、使用临时根与发布密钥、产生短期签名包并交叉验证。可选上传逐个资产到 Release 草稿。临时私钥在临时目录中生成，签完删除，既不作为 artifact 也不进入 Release，不使用正式密钥或 Secrets。

`Prepare unsigned offline release materials` 专门构建正式离线签名的交接材料，包含客户端、离线工具、签名脚本、已验收源包和完整校验清单；不生成正式签名。实际签发仍由用户在离线设备执行 `offline-sign.py sign`，再把仅含公开资产的最终目录交回联网发布端。

阶段 C 的独立 Debian VM 同时验证：无系统 PHP/Go 的非 root 用户通过 HTTPS 安装签名客户端，注册根授权 PHP 源，再执行真实 PHP 8.4→8.5→8.4、MySQL/Redis、CLI/FPM 测试。临时根指纹通过受控 SSH 测试通道注入，不能视为正式信任根。
