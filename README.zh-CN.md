# PHPM

Linux 官方源码构建已在 GitHub Actions 完成首次验证，面向 Debian 13 x64，
通过 SDK 迁移重编译、运行时、扩展和 FPM 检查。
操作和范围见 [构建说明](build/linux/README.zh-CN.md)。阶段 B 另提供显式可信源安装、
签名目录和锁文件精确恢复，见 [分发与安装](docs/distribution.zh-CN.md)。
默认下载源保持不变；正式签名密钥与生产部署尚未启用。

阶段 C 已在独立 Debian VM 中完成真实 8.4.27 → 8.5.12 → 回退，验证 MySQL、Redis、
FPM 与旧进程保持原版本；范围和运行方式见 [VM 验收说明](build/linux/vm/README.zh-CN.md)。
这轮使用内置用例，实际业务项目尚未验证。

[English](README.md)

PHPM 为每个项目生成独立的 PHP CLI 环境，跨项目共享 PHP 构建与兼容扩展。
当前版本为 **0.1.0-dev**，已实现首个可运行闭环，不是生产部署工具。

## 已实现

- Go CLI，不依赖预装 PHP 即可启动。
- Windows x64 官方 PHP 下载、TS/NTS 选择、官方 SHA-256 校验及下载缓存。
- Linux x64 从 StaticPHP 社区获取静态 NTS PHP，校验发布摘要和 ELF，无需预装 PHP/Go 或系统级包安装。
- Debian 13 x64 可显式注册 Ed25519 公钥和签名目录，通过 `php install --source 名称`
  安装构建包；支持匹配的 Redis、候选升级及 `install --frozen --source 名称` 精确恢复。
- 将本地 PHP 分发目录复制到独立存储，记录版本、PHP API、构建信息和文件摘要。
- 导入本地 DLL/SO，区分普通扩展和 Zend 扩展，启动 PHP 验证名称与精确版本。
- `ext install` 从官方 Windows 扩展目录在线安装，自动匹配 PHP/TS/NTS/工具链/x64，并更新项目配置和锁文件。
- 严格解析 `phpm.yaml`，拒绝未知字段、重复字段、多文档和加载配置注入。
- 平台锁文件、不可变环境、并发写入锁、失败保留旧锁文件。
- 项目配置查找、独立 INI 加载、环境验证和命令执行。
- 子进程继承项目 PHP 的 PATH、PHPRC 和配置扫描目录。
- `composer` 使用项目 PHP 快速运行 Composer；`ext check` 检查 Composer 声明及锁文件中的平台要求。
- `ext install --missing` 按 Composer 约束批量安装缺失扩展，验证成功后统一提交项目配置。

Linux x64 已接入预编译静态 PHP 获取；macOS 和其他 Linux 架构仍使用本地导入，自动源码构建尚未实现。
本地已交叉编译不等于对应平台通过真实运行验证，详见 [验证记录](docs/validation.md)。

## 构建

需要 Go 1.23 或更新版本：

```powershell
go build -o bin/phpm.exe ./cmd/phpm
go test ./...
go vet ./...
```

Linux/macOS 将输出文件名改为 `bin/phpm`。
以下示例假定自行将构建产物加入 PATH；也可使用可执行文件的完整路径。

## Windows 快速开始

在业务项目目录执行：

```powershell
phpm init --php 8.4
phpm php install 8.4
phpm install
phpm exec -- php -v
phpm exec -- php --ini
phpm verify
```

默认安装 NTS；TS 使用 `phpm php install 8.4 --ts`。
只使用官方当前目录中存在的版本，不会把不存在的精确版本悄悄替换成其他补丁。
官方包的 `-1` 等打包修订号与 PHP 自身版本分别处理。

已有 PHP 可导入完整目录：

```powershell
phpm php import D:\packages\php-8.4 --binary php.exe
phpm php list
```

这会复制文件，不修改源安装。Linux/macOS 示例：

```sh
phpm php import /opt/portable-php --binary bin/php
```

导入目录必须是不含符号链接、特殊文件的可迁移分发目录。不要导入 `/usr`，
也不要导入包含 PHPM_HOME 的目录。工具不会自动打包系统动态库或修复绝对链接路径。

## Linux x64 从零安装

Linux x64（包括待实测的 iStoreOS/OpenWrt）使用同样的命令：

```sh
phpm php available
phpm init --php 8.4
phpm php install 8.4
phpm install
phpm exec -- php -v
```

无需系统预装 PHP、Go、curl 或 tar。下载与解包由 PHPM 完成，安装只写入
`PHPM_HOME`，不执行 `opkg`/`apt` 或远程安装脚本。目标仓库需要有写入和执行权限，
网络需要能验证 HTTPS 证书并访问 StaticPHP 的资源站。

此后端固定使用 [StaticPHP v3 common](https://dl.static-php.dev/v3/php-bin/common/)
社区构建，不是 PHP 官方 Linux 二进制。只列出有对应 `.sha256` 的 Linux x64 CLI
包；选择最新匹配数字版本或请求的精确版本，核对发布的 SHA-256，拒绝不匹配、
动态链接或错误架构的 ELF，然后实际执行 PHP 并再次验证最终安装路径。
摘要来源于发布者的 HTTPS 站点，不等于独立签名认证。

该构建为静态 NTS，并预编译了固定的一组常用模块，例如 PDO MySQL、OpenSSL、
mbstring 和 Redis。内置模块始终随运行时存在，其版本由该构建决定，不能作为独立
SO 切换版本；PHP 升级时也跟随运行时变化。`--ts` 不适用于此下载后端。
缺少的其他扩展仍需兼容的本地导入，Linux 在线扩展编译尚未实现。

`php upgrade` 同样接入该后端，候选验证、激活和回滚规则保持一致。存在于软件源
不代表任意项目都兼容，仍需检查 Composer 要求和应用测试。当前可用范围和实测
进度见验证记录；没有在具体 Linux 主机完成运行测试前，不据此承诺兼容。

## 在线安装扩展

先进入已有 `phpm.yaml` 且已安装 PHP 的项目目录，执行：

```powershell
phpm ext install redis
phpm ext install redis@6.2.0
phpm ext install xdebug
phpm ext install mongodb
phpm exec -- php --ri redis
```

不指定版本时，按数字正式版本从新到旧寻找匹配的 Windows 包；指定精确版本时
不会悄悄换版本。下载后验证 DLL 和完整环境，成功后自动更新 `phpm.yaml` 和
`phpm.lock`，不需要手动编辑配置或再执行 `install`。已有锁文件中的 PHP 构建与
其他仍匹配的扩展会被保留，不会顺带升级。

Xdebug 自动使用 Zend 加载器。PHP 自带模块可直接启用：

```powershell
phpm ext install curl
phpm ext install opcache
```

自带模块版本由 PHP 构建决定；JSON 等静态模块无需下载，只记录项目要求。
在线第三方扩展支持 Windows x64 官方目录 ZIP，以及显式签名 Debian 源中绑定精确
PHP 构建的 Redis 包。其他 Linux/macOS 可启用自带模块或使用本地导入，客户端不做现场源码编译。

官方扩展目录没有统一发布校验值。首次下载信任官方 HTTPS 来源，并记录实际
SHA-256，后续使用缓存时重新核对；可加 `--sha256 HASH` 强制匹配独立获得的
预期 ZIP 摘要（自带模块则核对 DLL/SO 摘要）。实际计算的摘要不等于官方签名。

项目文件写入失败时恢复原配置及锁文件。异常退出可能留下
`.phpm-transaction.json`，此时执行会被阻止，下一次 `install` 或 `ext install`
会先恢复旧文件。如果检测到用户之后又编辑了文件，则停止恢复并保留备份，
不会覆盖用户编辑。若同时遗留进程锁，先确认原操作已结束再清理锁。

## 本地扩展导入与项目声明

官方目录没有对应包时，可使用可信本地二进制；PIE 后端尚未实现。
导入会实际执行 PHP 加载原生模块，只应使用可信来源的文件。

```powershell
phpm ext import redis@6.2.0 --file D:\packages\redis\php_redis.dll
phpm ext import xdebug@3.4.0 --file D:\packages\xdebug\php_xdebug.dll --zend
```

示例版本用于说明语法，不代表所有 PHP/平台组合均有对应产物。
可加 `--sha256 HASH` 核对模块摘要；`--package-dir DIRECTORY` 一并复制依赖文件。
Windows 子进程 PATH 包含这些目录，但这不等于完整 DLL/系统库隔离。
需要其他可选 PHP 扩展才能加载的模块，当前独立导入探测暂不支持。
扩展按模块名字母顺序加载，尚无依赖拓扑排序。

导入仅写入产物存储。编辑项目配置后再安装环境：

```yaml
schema: 1
php: "8.4"
extensions:
  redis: "6.2.0"
ini:
  memory_limit: "256M"
  display_errors: "0"
```

```powershell
phpm install
phpm exec -- php --ri redis
phpm info
```

默认按项目选择目标 PHP；已有匹配锁文件时使用锁定的构建。
也可使用 `--runtime ID` 指定 `phpm php list` 中的构建，无需当前项目。
同一 PHP 补丁存在多个构建时，需要在 YAML 中设置 `runtime: ID`。
内置静态扩展属于运行时，不能独立替换版本。

## 锁文件与执行规则

| 命令 | 当前含义 |
|---|---|
| `install` | 配置匹配时复用锁；配置或平台条目变化时从已导入产物重新解析 |
| `install --frozen` | 必须有当前平台、当前配置对应的锁，禁止重新解析 |
| `install --update` | 显式重新选择本地已安装构建 |
| `info` | 验证后输出环境、运行时和扩展元数据 |
| `verify` / `doctor` | 当前执行相同的完整性及实际加载检查，诊断能力尚为基础版 |
| `exec -- php ...` | 使用项目 PHP 和配置运行，保留当前工作目录及应用退出码 |

锁文件不会自动获取缺失的运行时或扩展。本地导入的相同构建需要在另一台机器
单独提供。`phpm.yaml` 和 `phpm.lock` 应一起提交版本控制。

普通嵌套 `php` 继承环境；显式绝对路径、`-n`、`-c`、`-d` 或程序重写环境变量
可以改变实际行为。PHPM 不是安全沙箱，也不会自动通过 shell 执行 Windows 批处理。
Unix 使用进程替换，以保留服务进程的信号与退出语义。

## Composer 集成

在已安装 PHPM 环境的业务项目目录执行：

```powershell
phpm composer --version
phpm ext check
phpm composer install
phpm composer require vendor/package
```

`composer` 参数、输入输出和退出码直接转交给 Composer，始终由项目选定的 PHP
启动。`phpm exec -- composer ...` 也可使用。正常 Composer 命令仍按其自身规则
运行插件、脚本和网络操作；PHPM 不替代 Composer 管理 PHP 代码依赖。

自动寻找 PATH 绝对目录中的 `composer.phar` 或 PHP 格式的 `composer`；Windows
常见的 `composer.bat` 安装会使用同目录中的 PHAR，避免批处理选错系统 PHP。
也可显式指定可信的 PHAR：

```powershell
$env:PHPM_COMPOSER = 'D:\tools\composer.phar'
```

不会自动下载 Composer 或自动运行项目目录中的 PHAR。扩展检查要求 PHP 启用 phar、Composer 2，
且其代码能在项目 PHP 下执行。旧 Composer 的弃用提示会汇总为提醒，详细信息保留在
`ext check --json` 的 `diagnostics` 中，不会单独导致检查失败；其他警告或执行错误
仍使检查失败。可以更新 Composer，或通过 `PHPM_COMPOSER` 指定兼容的 PHAR。

```powershell
phpm ext check --no-dev
phpm ext check --lock --json
phpm ext check --candidate
phpm composer --candidate check-platform-reqs --lock
```

`ext check` 使用 Composer 自身的版本约束和平台探测能力，检查 PHP、`ext-*`
及其他已声明的平台要求，显示缺失项、版本不匹配和依赖来源。忽略
`config.platform` 和忽略平台要求的环境变量，核对真实环境。存在 Composer 锁文件
时，同时检查锁定的间接依赖；默认包括开发依赖，`--no-dev` 排除根项目开发要求
和锁定的开发包。支持包的 `provide`/`replace`，会注明满足要求的替代包。

没有锁文件时仅检查根项目并明确提示无法获知间接依赖；`--lock` 要求锁文件必须
存在。锁文件过期、缺失项或版本不符时返回非零状态。检查尊重 `COMPOSER` 指定的
清单文件名，不运行项目脚本、自动加载文件或插件，也不修改 Composer/PHPM 配置
和锁文件。它检查已声明的平台要求，不代替 Composer 完整解析依赖。

可以一条命令安装检查发现的缺失扩展：

```powershell
phpm ext install --missing
phpm ext install --missing --no-dev
phpm ext install --missing --no-dev --lock
phpm composer install
```

`--missing` 要求当前项目已有验证通过的 PHPM 环境。默认包含开发依赖；`--no-dev`
排除开发要求，`--lock` 要求 Composer 锁文件必须存在。按 Composer 自身的版本约束
同时筛选根项目与锁定依赖的要求，再选择与当前 PHP 构建匹配的最新数字正式版本。
满足要求的 `provide`/`replace` 包不会触发原生扩展安装。OPcache 自动映射为 Zend 扩展。

全部扩展下载、加载和 Composer 复查通过后，统一提交 `phpm.yaml` 与 `phpm.lock`；
任一步失败保留原项目环境，已下载产物可留在缓存。Composer 文件保持不变，安装不
执行项目脚本或插件。重复执行且所有要求已满足时不修改文件。

已有扩展版本不符、PHP/系统库要求不符、Composer 锁文件过期或无法验证时会拒绝
自动安装并说明原因；它不会顺带升级 PHP 或现有扩展。需要变更已有扩展时，明确
执行例如 `phpm ext install redis@6.2.0`。没有 Composer 锁文件时仅处理根项目要求，
会提示间接依赖未知。在线下载安装仍限现有 Windows x64 扩展后端；Linux/macOS
仅支持现有自带模块能力，尚无自动源码编译。`--missing` 当前用于正式项目环境，
不支持候选环境安装，且不能与扩展名、`--sha256` 或 `--zend` 混用。

PHP 升级准备完成后，可先用 `--candidate` 检查新 PHP 的依赖兼容性，再决定激活。

## PHP 安全升级、切换与回滚

在已有配置、锁文件且 `verify` 通过的项目中执行：

```powershell
phpm php upgrade
phpm diff
phpm verify --candidate
phpm exec --candidate -- php -v
```

默认查找当前 PHP 次版本分支的最新可用构建（Windows 官方构建或 Linux x64
StaticPHP 构建），保留 TS/NTS、调试模式与平台。也可以 `phpm php upgrade 8.4`
显式指定目标分支/精确版本。
准备阶段不修改项目的 `phpm.yaml` 和 `phpm.lock`，普通 `exec` 仍用旧环境；
第三方扩展保留声明的版本，为新 PHP 重新准备，自带模块则跟随新运行时。
任意扩展缺失或不兼容时停止准备，旧环境和此前的候选环境保持可用。
原来的 PHP 次版本约束继续保持次版本约束，精确版本固定继续保持精确。

先通过 `exec --candidate` 运行项目测试，再使用准备完成时输出的候选 ID 切换：

```powershell
phpm activate 候选ID
phpm history
```

`候选ID` 请替换为实际的 `upgrade-...`。激活前会重新校验新旧环境和项目文件；
准备后若用户编辑了配置或锁文件，旧候选会被拒绝，需要重新准备。
候选测试仍使用当前项目目录和应用数据，不提供数据库或文件系统沙箱。

切换只影响后续通过 PHPM 启动的进程。已有 FPM、Webman、队列进程继续用旧 PHP；
由你或现有进程管理器选择时机重启/重载，再做应用健康检查。PHPM 不自动重启服务，
也不会把环境校验成功说成业务部署完成。

需要恢复时执行：

```powershell
phpm rollback
# 或指定 history 中的恢复点
phpm rollback 恢复点ID
```

回滚先验证保留的旧环境，再恢复原配置和锁文件内容；若激活后项目文件又被编辑，
则拒绝覆盖。它不回滚代码、数据库或应用数据，已运行进程仍需受控重启。
`history` 列出的是恢复点与准备时间，不是部署完成审计。

其他 Linux 架构、macOS 或离线场景，可预先导入目标 PHP 和匹配扩展，然后执行
`phpm php upgrade --runtime 构建ID`。`upgrade` 拒绝降级 PHP；恢复旧环境使用
`rollback`。每个项目和 PHPM_HOME 保留一个候选，成功重新准备会替换它并生成新 ID。
不要删除仍有运行中进程或恢复点依赖的运行时、扩展。

## 存储与限制

默认目录为 `~/.phpm`，也可设置 `PHPM_HOME`，或使用
`phpm --home DIRECTORY COMMAND`。仅对子进程设置环境，不改全局 PATH。
INI 使用绝对产物路径，因此生成环境后不要直接搬迁 PHPM_HOME；应在新位置重建。

```text
PHPM_HOME/
  store/runtimes/
  store/extensions/
  environments/
  projects/
  cache/
  tmp/
  locks/
```

不可变是工具的写入约定，不是操作系统权限隔离。每次执行前会校验完整产物，
并检查 PHP 实际加载的扩展与配置，当前优先保证正确性，尚未优化大目录启动成本。
不允许通过 `ini` 配置 `extension`、`zend_extension`、`extension_dir`；
值或加载路径包含换行、NUL、双引号或 `$` 时明确报错。

崩溃可能遗留锁、临时目录或未引用的产物；确认锁中 PID 对应的操作已经结束后，
才能手动清理。当前不自动删除陈旧锁、不提供垃圾回收、FPM、
服务重启或完整生产部署，也不保证跨系统底层依赖完全一致。

官方 Windows PHP 下载核对目录提供的 SHA-256；Windows 扩展摘要规则见上文。
显式 Debian 签名源另验证固定公钥的 Ed25519 目录签名，使用方式和信任边界见分发说明。
代码采用 MIT；下载的 PHP、扩展和系统库仍遵循各自许可证。

开发、验证与后续路线见 [架构说明](docs/architecture.md)、
[验证记录](docs/validation.md) 和 [贡献指南](CONTRIBUTING.md)。
