# Debian 官方源码构建：阶段 A

当前源码新增 Debian 12/13 × PHP 8.4/8.5 和独立 xlswriter 包，详见
[预编译扩展说明](../../docs/precompiled-extensions.zh-CN.md)。下文原始验收记录来自
Debian 13；新增组合需要新的真实构建验收，不包含在已发布 rc.1 中。

此目录是 PHP/Redis 构建验证入口，不是已发布的生产下载源。客户端当前的
Linux 默认来源在当前源码中已改为自建签名源，StaticPHP 需显式选择；
已发布 rc.2 的行为不变，详见 [默认来源说明](../../docs/default-source.zh-CN.md)。

## 当前目标

- Debian 13 x86_64、PHP 8.4.27 / 8.5.12 NTS、Redis 6.3.0。
- 独立 CLI/FPM 运行时、可选共享扩展、配套开发工具包。
- GitHub Actions 手动构建，先完成实际编译和安装验证，再设计正式发布。
- 生产机器不需要编译器，但需要 `runtime-packages.txt` 中的 Debian 运行库。
- Debian 包不能直接用于 OpenWrt/iStoreOS；当前不承诺其他发行版或架构兼容。

2026-10-28 的[首次成功构建](https://github.com/longyongzhong/phpm/actions/runs/40666487813)
已生成 PHP 8.4.27 CLI/FPM、SDK 和 Redis 6.3.0 包，通过 SDK 迁移重编译及
全新 Debian 容器内的 17 项运行时检查。完整结果与限制见
[验证记录](../../docs/validation.md)。这不代表其他发行版或生产服务已验证。

## 在 GitHub 上运行

1. 将完整项目上传到 GitHub 仓库，包含 `go.mod`、`go.sum`、Go 源码、此目录和
   `.github/workflows/debian-source-build.yml`。不要上传 `.cache`、本地 PHPM Home 或任何私钥；
   `keys/php-8.4.asc` 是必须包含的公开验证密钥。
2. 工作流文件进入默认分支后，在 **Actions** 中启用工作流。
3. 选择 **Debian PHP source build (stage A)**，点击 **Run workflow**。
   选择固定配方 `8.4` 或 `8.5`。8.5 配方使用该系列独立发布公钥；OPcache 按上游规则
   内置进 PHP，不再要求独立 opcache.so。
4. 等待 `Build packages and run SDK/fresh-container checks` 完成。失败时查看该步骤
   日志；不会把失败的构建作为成功产物上传。
5. 成功后，在本次运行的 **Artifacts** 中下载
   `phpm-debian13-source-build-<运行编号>-<尝试次数>`。

工作流只申请仓库读取权限，不创建仓库、不发布 Release、不上传签名私钥、不注册服务。
产物保留 14 天，是待验证的未签名包，不是长期分发入口。
首次构建耗时取决于下载和编译资源；工作流超时上限为 75 分钟。

## 本地 Linux/Docker 构建

构建主机需要 Python 3.10+、Go 1.23+ 和已启动的 Linux Docker/BuildKit。
宿主机不需要预装 PHP，不会在宿主机执行 apt 或源码安装。

```sh
python3 build/linux/build.py check
python3 -m unittest discover -s build/linux/tests -v
python3 build/linux/build.py build --output .cache/debian13-stage-a

# 独立构建第二版；输出目录必须不同。
python3 build/linux/build.py check --php 8.5
python3 build/linux/build.py build --php 8.5 --output .cache/debian13-php85
```

输出目录必须不存在或为空，已有文件不会被清除。Docker 只接收临时组装的最小上下文：
构建脚本、公开验证密钥、构建出的 PHPM CLI，以及显式提供的源码缓存。
不会将整个工作区或 `.cache` 发送给 Docker。

可选的离线源码缓存包含以下四个文件：

```text
php-8.4.27.tar.xz
php-8.4.27.tar.xz.asc
redis-6.3.0.tgz
xlswriter-3.0.0.tgz
composer.phar
```

```sh
python3 build/linux/build.py verify-inputs /path/to/inputs
python3 build/linux/build.py build --inputs /path/to/inputs --output .cache/debian13-stage-a
```

缓存只避免下载上述文件，不能使 Docker 镜像、Debian 包和 Go 依赖变成离线安装。
`verify-inputs` 需要 GnuPG；构建时会在容器中重复验证。

## 构建输入与信任

`recipe.json` 固定 PHP/Redis/Composer 的精确版本、官方 HTTPS URL、源码摘要、
PHP 发布签名完整指纹、Debian amd64 镜像摘要和 Debian 快照时间。

- PHP 摘要和发布者指纹来自 [PHP 官网](https://www.php.net/downloads.php?source=Y)。
  验证密钥来源见 `keys/README.md`；未知签名者会失败，需要维护者显式更新配方。
- Redis 来自 `https://pecl.php.net/get/redis-6.3.0.tgz`。当前摘要是维护时从官方
  HTTPS 下载后固定的值，不宣称存在 Redis 发布者的独立签名。
- Composer 2.10.3 来自 `https://getcomposer.org/download/2.10.3/composer.phar`，
  只用于隔离环境中的 Composer 检查，不作为 PHP 运行时组成部分。
- Debian 镜像摘要来自 Docker Hub 的 `library/debian` 官方镜像元数据。
  apt 使用固定时间的 Debian、updates、security 快照，并校验 Debian 仓库签名。
  快照使用 HTTP；包真实性依靠 apt 的签名和摘要链。为读取历史快照关闭有效期检查，
  不关闭仓库签名校验。
- 编译采用通用 x86-64 参数，不使用 `-march=native`。源码解压保留生成文件时间顺序，
  下载和编译分开；编译、SDK 重编译及运行验证使用 `--network=none`。

固定输入和归档顺序不等于已经证明逐字节可复现。构建结果记录编译器、软件包版本、
配置选项、PHP API、glibc 和配方文件摘要，供后续比较和审计。
快照、依赖和构建编号也需要随安全更新重新评审，不能永久冻结旧库。

## 产物

| 文件 | 内容 |
|---|---|
| `*-runtime.tar.gz` | `bin/php`、`sbin/php-fpm`、`ext/*.so`、构建记录和许可证 |
| `*-redis.tar.gz` | `redis.so`、匹配的 PHP 构建标识、源码信息和许可证 |
| `*-sdk.tar.gz` | 同构建 PHP、头文件、构建文件、可迁移的 phpize/php-config |
| `build-result.json` | 构建身份、兼容目标、输入、依赖、产物大小及摘要 |
| `SHA256SUMS` | 三种包及 build-result.json 的摘要 |
| `sdk-verification.json` | 解包并迁移 SDK 后实际重编译/加载 Redis 的检查记录 |
| `verification/runtime-verification.json` | 干净运行容器的已完成检查与限制 |

PHP 自带的共享模块包括 bcmath、curl、exif、fileinfo、gd（PNG/JPEG）、mbstring、
OPcache、pdo_mysql、sockets、zip；基础 JSON、PDO、SQLite、OpenSSL 等随运行时内置。
FPM 不创建服务、不开放宿主机端口、不修改 Nginx。

SDK 修正了上游生成脚本中的安装前缀。构建使用暂存安装，不在原编译前缀创建运行时，
以免掩盖迁移缺陷。SDK 重编译测试使用另一目录；不承诺上游 phpize 支持带空格的源码/
SDK 构建路径。运行时另行测试带空格的解包目录和 PHPM 导入。

## 构建必须通过的检查

1. 固定源码摘要、PHP 官方发布签名。
2. PHP `tests/basic`、JSON、PDO SQLite 的上游测试子集；失败返回非零，不隐藏 FAIL。
3. CLI/FPM 和共享扩展文件生成、Redis 精确版本实际加载。
4. SDK 解包到新目录，验证前缀/头文件，重新编译 Redis 并实际加载。
5. 新的 Debian 运行容器：无系统 PHP、无编译器，使用非 root 用户运行。
6. 归档摘要、运行时迁移、共享库解析、PHPM 导入和所有共享扩展的启用。
7. 独立 Redis 包导入、项目冻结安装、两个项目的扩展和 INI 隔离。
8. SQLite 实际查询、localhost 解析、Redis 服务读写、本地 HTTPS 服务的证书验证。
9. Composer 平台要求检查和已满足条件下的 `ext install --missing`。
10. FPM 实际 FastCGI 请求，验证项目 Redis 和 memory_limit 配置。
11. 两个同 PHP 版本的不同运行时负载之间准备候选、激活和回退，检查原文件精确恢复。

第 11 项只验证负载切换机制；它不代表已验证真实 PHP 补丁/次版本升级。
MySQL 目前只验证 PDO MySQL 模块加载和 Composer 要求，没有启动真实 MySQL 服务。
容器共享宿主内核；独立 Debian VM、长期业务运行和生产流量切换仍需后续验证。

## 阶段边界

本阶段不实现签名制品仓库、客户端远端目录/按锁下载、独立扩展在线下载、自动更新、
FPM 托管和生产部署。构建成功后仍需审阅报告、验证包及真实业务；不能直接标为生产稳定。
后续阶段在这些验证结果的基础上接入 GitHub Releases 和客户端，而不是直接改默认下载源。

阶段 B 的签名目录、显式客户端来源、锁文件恢复和私有 Release 草稿入口已另行实现，
使用方法及密钥边界见 [分发说明](../../docs/distribution.zh-CN.md)。阶段 A 本身仍只负责
生成和验收原构建包，不持有正式发布私钥。
