# 签名分发与精确安装：阶段 B

阶段 A 从官方 PHP 源码编译并验证 Debian 包；阶段 B 为这些已验收的包生成
Ed25519 签名目录，并接入客户端安装、独立扩展、候选升级和锁文件恢复。
范围仍是 Debian 13 x86_64、glibc 2.41、NTS。其他 Linux、iStoreOS 和 macOS
不能使用此目标包。现有无 `--source` 的安装行为不变。

首次实际验收已通过：[阶段 B 工作流](https://github.com/longyongzhong/phpm/actions/runs/40681164443)，
包含签名安装、Composer 补扩展、空存储按锁恢复与候选回退。
对应代码的[跨平台 CI](https://github.com/longyongzhong/phpm/actions/runs/40681126574)也已通过。
加入分段传输后，[Debian 再验收](https://github.com/longyongzhong/phpm/actions/runs/40684837869)
与[全部平台 CI](https://github.com/longyongzhong/phpm/actions/runs/40684786008)再次通过。

## Debian 运行库

目标机器不需要 PHP、Go 或编译器，但需要动态运行库。由服务器管理员按现有运维流程
安装 `build/linux/runtime-packages.txt` 中的软件包；PHPM 不自动提权或修改系统包：

```sh
sudo apt-get update
sudo apt-get install --no-install-recommends ca-certificates libc6 libcurl4t64 libjpeg62-turbo libonig5 libpng16-16t64 libsqlite3-0 libssl3t64 libxml2 libzip5 tzdata zlib1g
```

## 使用可信源

首次使用前，从维护者的独立可信渠道获取公钥并核对 SHA-256 指纹。
`signing-key.pub` 可以随包提供以供比对，但不能仅因它与下载包在同一位置就信任它。
PHPM 不会自动信任下载目录中的公钥，也不会自动替换已经注册的源或密钥。

将完整 bundle 保存在一个固定目录，或把其中每个文件作为同一 HTTPS 目录的文件托管。
目录必须包含 `catalog.json`、`catalog.json.sig` 和其列出的包。
远端包托管服务必须支持 HTTP Range/206（GitHub Release 资产支持），客户端按限量分段
获取，遇到中断从本次操作已收到的字节继续，完整摘要匹配后才提交缓存。进程退出后
未完成的临时下载不会作为缓存保留。目录与签名文件仍使用普通 HTTPS 请求。

```sh
# 私有仓库阶段：登录 GitHub 下载并解压构建产物，使用本地目录。
phpm source add phpm-test --catalog /opt/phpm-bundle/catalog.json --key /path/to/verified-public-key.pub
phpm source list
phpm php available --source phpm-test
phpm php install 8.4 --source phpm-test

cd /path/to/project
phpm init --php 8.4
phpm install
phpm ext install redis
phpm ext install --missing
phpm verify
phpm exec -- php -v
```

`ext install --missing` 需要兼容的 Composer 2 PHAR，可用 `PHPM_COMPOSER` 指定。
自带扩展从已验证运行时获取；Redis 从绑定该精确 PHP 构建的签名目录获取。
其他未发布的扩展明确报错，不猜测 ABI，也不自动切到社区源或现场编译。
若存储中已有多个 PHP 构建，在 `phpm.yaml` 的 `runtime` 字段指定安装输出中的
运行时 ID；版本选择与现有本地构建选择规则保持一致。

未来有可公开访问的 HTTPS 发布目录时，可将 `--catalog` 换成其完整 HTTPS URL。
当前客户端不读取 Git 凭证，不接受 URL 内嵌密码，不实现私有 GitHub 资产 API 认证。
私有仓库或 Release 草稿应先通过已登录 GitHub/`gh` 下载，再使用本地 bundle。

## 在新机器恢复锁定环境

复制项目的 `phpm.yaml` 和 `phpm.lock`，安装相同版本的 PHPM，注册经核对公钥的源：

```sh
phpm source add phpm-test --catalog /opt/phpm-bundle/catalog.json --key /path/to/verified-public-key.pub
phpm install --frozen --source phpm-test
phpm verify
```

此命令按锁中的构建标识、包摘要、运行时身份和扩展身份恢复精确文件，不选择最新版，
不重建源码，不改项目文件。源中必须保留锁引用的旧构建及对应扩展。
仅支持来自签名目录或运行时自带的扩展；本地手工导入的其他扩展仍需显式导入。
失败可能留下已验证的缓存或不可变产物，不会把不完整环境当成安装成功。

## 升级和回退

```sh
phpm php upgrade
# 或显式选择目标版本/可信源
phpm php upgrade 8.5 --source phpm-test
phpm diff
phpm exec --candidate -- php -v
phpm verify --candidate
phpm activate 候选ID
phpm rollback
```

当前 PHP 来自签名目录时，省略 `--source` 的升级沿用该源。源中没有目标版本或
匹配扩展时停止；目录验签失败、过期或不可用时不降级为另一下载来源。
这些操作仍只切换后续 PHPM 启动的进程；生产服务重启和健康检查尚未托管。

## 维护者离线签名

维护者工具只用 Go 标准库，不需要 PHP 或第三方密码学依赖。
私钥是 32 字节 Ed25519 seed 的 base64，公钥是 32 字节公钥的 base64。
不要把私钥传入命令行参数、仓库、Actions artifact 或普通构建镜像。

```sh
go build -o phpm-release ./cmd/phpm-release

# 在隔离的签名环境生成并备份正式密钥；以下路径只是示例。
./phpm-release keygen --private /secure/offline-signing.key --public /secure/signing-key.pub

# BUILD_DIRECTORY 必须是阶段 A 已通过验收的产物目录。
./phpm-release catalog --key /secure/offline-signing.key --sequence 1 --output /release/bundle /build/accepted
```

签名前要求 SDK 六项和运行时十七项验收报告匹配构建 ID，并重新核对三个包的大小
与 SHA-256。报告本身不是独立构建证明；维护者仍须确认来源、提交、Actions 结果，
不能拿来源未知的 JSON 报告代替实际验收。
工具先暂存再提交新目录，不覆盖既有发布目录。签名对象为 `catalog.json` 的精确字节。
目录同时包含版本、构建修订、PHP API、目标平台、包名、大小和摘要。
包与详细构建报告保留原内容；目录签名不意味着 PHP 官方为 PHPM 二进制包背书。

更新时将序号递增，且保留仍被锁引用的构建，可一次传入多个已验收构建目录。
同一 PHP 版本的不同构建必须有不同 `build_revision`；同版本按修订号选择最新构建。
默认有效期 30 天，最长 90 天（可用 `--validity 720h` 指定）。过期前需要签发新目录。
客户端记录最高已接受序号和内容摘要，拒绝旧序号及同序号不同内容。
首次客户端无法识别尚未见过但仍有效的旧目录；此机制不等同于完整 TUF。
密钥轮换、撤销和多签门限尚未实现，正式信任根的保管与分发仍需发布策略。

## 私有 Actions 验证与 Release 草稿

手动运行 `Debian signed distribution (stage B)`，输入同仓库成功的阶段 A `source_run`。
它验证原运行的状态和工作流，下载其包，在没有系统 PHP/编译器的干净 Debian 容器中
以非 root 用户执行签名安装与恢复测试。测试服务仅在回环 TLS 上运行，外部网络关闭。

每次验证使用临时测试密钥，私钥仅存在测试进程内存，不写入产物。测试 bundle 的
默认有效期只有一小时，适合立即检查流程；长期测试可用自己的测试密钥重新离线签发。
临时测试公钥不是正式发布信任根，每次运行都会变化。
产物包含未改写的原构建包、签名目录、公钥和 `distribution-verification.json`。
单独的同版本修订测试包仅存在临时测试目录，不放入导出的 bundle。

`draft_release=true` 时，验收成功后另一个最小权限 job 将完整 ZIP 保存为
**draft prerelease**。不会发布 Release，不会公开仓库，也不上传私钥。
默认不创建草稿。草稿 ZIP 用于内部审阅，不是最终客户端远端目录；正式发布时应把
bundle 的各文件作为独立资产上传，使 `catalog.json` 与列出的包处在同一下载目录。

## 仍需完成的生产验证

后续的 [阶段 C](../build/linux/vm/README.zh-CN.md) 已完成内置独立 Debian VM、真实
MySQL 与跨 PHP 版本扩展迁移验收，包括两个 FPM 版本和精确回退。业务项目实测、
长时间高并发业务压测、安全更新重建与正式密钥管理仍需完成。
FPM 服务托管、生产流量切换和自动健康回退仍为后续范围。

协议与工具依据：[Go Ed25519](https://pkg.go.dev/crypto/ed25519)、
[GitHub 下载运行产物](https://cli.github.com/manual/gh_run_download)、
[GitHub 创建 Release](https://cli.github.com/manual/gh_release_create)。
