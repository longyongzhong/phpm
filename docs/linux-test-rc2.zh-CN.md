# Debian 12/13 测试：0.1.0-rc.2

当前 rc.2 是未公开草稿。下面的公开下载命令须等维护者公开该版本后执行；草稿资产不能匿名下载。

适用 Debian 12 x86_64 / glibc 2.36 或 Debian 13 x86_64 / glibc 2.41，不适用 iStoreOS/OpenWrt。两套包分别构建，客户端自动选择匹配目标。客户端仍需升级到 rc.2；rc.1 不认识 Debian 12 目标。

## 准备系统运行库

以下会安装 PHP 需要的系统运行库，不安装系统 PHP、编译器或开发头文件。以具备管理员权限的账号执行，Debian 12 用：

```sh
apt-get update && apt-get install -y --no-install-recommends \
  curl ca-certificates libc6 libcurl4 libjpeg62-turbo libonig5 \
  libpng16-16 libsqlite3-0 libssl3 libxml2 libzip4 tzdata zlib1g
```

Debian 13 则使用：

```sh
apt-get update && apt-get install -y --no-install-recommends \
  curl ca-certificates libc6 libcurl4t64 libjpeg62-turbo libonig5 \
  libpng16-16t64 libsqlite3-0 libssl3t64 libxml2 libzip5 tzdata zlib1g
```

## 下载与核对客户端

摘要来自已核验根授权签名的 rc.2 客户端清单。使用独立目录，不修改 PATH、系统 PHP 或服务。

```sh
mkdir -p "$HOME/phpm-test-0.1.0-rc.2" &&
cd "$HOME/phpm-test-0.1.0-rc.2" &&
curl -fL -C - --retry 3 -o phpm.tar.gz https://github.com/longyongzhong/phpm/releases/download/v0.1.0-rc.2/phpm-0.1.0-rc.2-linux-amd64.tar.gz &&
printf '%s\n' 'c3eacddf917a5c6f21403a1dbe45144c48896f005c56cb6b855916399365e57b  phpm.tar.gz' | sha256sum -c - &&
tar -xzf phpm.tar.gz &&
./phpm version
```

应输出 `phpm.tar.gz: OK` 和 `0.1.0-rc.2`。

## 注册签名源并安装 PHP

以下命令在同一个终端继续执行。单独的 PHPM_HOME 隔离本轮测试缓存与环境；它只影响当前终端及子进程。

```sh
PHPM="$HOME/phpm-test-0.1.0-rc.2/phpm"
export PHPM_HOME="$HOME/.phpm-rc2-test"
cd "$HOME/phpm-test-0.1.0-rc.2" &&
curl -fL --retry 3 -o root-key.pub https://github.com/longyongzhong/phpm/releases/download/v0.1.0-rc.2/root-key.pub &&
"$PHPM" source add phpm-rc2 \
  --catalog https://github.com/longyongzhong/phpm/releases/download/v0.1.0-rc.2/catalog.json \
  --root ./root-key.pub \
  --fingerprint b9aaa6269e68f95ea18147d09c043ca9c8ae332903e0bc04b23c34ad98e35d32 &&
"$PHPM" source verify phpm-rc2 &&
"$PHPM" php available --source phpm-rc2 &&
"$PHPM" php install 8.4 --source phpm-rc2
```

源名称 `phpm-rc2` 只需注册一次。指纹是原始 Ed25519 公钥的 SHA-256，由 PHPM 解析并核对；不要拿公钥文本文件的 `sha256sum` 代替它。签名/下载失败会直接停止，不回退 StaticPHP。

## 项目与扩展测试

在独立项目目录运行，不要覆盖业务项目现有配置：

```sh
mkdir -p "$HOME/phpm-example-rc2" &&
cd "$HOME/phpm-example-rc2" &&
"$PHPM" init --php 8.4 &&
"$PHPM" install &&
"$PHPM" ext install xlswriter &&
"$PHPM" ext install redis &&
"$PHPM" verify &&
"$PHPM" exec -- php --ri xlswriter &&
"$PHPM" exec -- php --ri redis
```

应加载 xlswriter 3.0.0、Redis 6.3.0。额外扩展来自该 PHP 精确构建对应的签名包；无需本机编译。基础扩展例如 curl、pdo_mysql 可使用 `ext install curl`、`ext install pdo_mysql` 从 PHP 随附模块启用。

## 升级候选与回退

```sh
"$PHPM" php upgrade 8.5 --source phpm-rc2
"$PHPM" diff
"$PHPM" verify --candidate
"$PHPM" exec --candidate -- php -v
"$PHPM" exec --candidate -- php --ri xlswriter
```

测试通过后，用命令实际打印的候选 ID 执行 `"$PHPM" activate 候选ID`；需要回退时执行 `"$PHPM" rollback`，然后 `"$PHPM" verify`。配置激活不会自动重启已经运行的 PHP/FPM/业务进程。

本轮新增四套组合已通过全新容器的真实 PHP、Excel 读写、FPM、SDK 重编译、签名源安装与同版本载荷回退验收。尚未在操作者 Debian 12 服务器或实际业务项目完成验收，也未对新增组合重做独立 VM 的跨版本测试。
