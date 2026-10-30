# Linux 测试：0.1.0-rc.1

本文保留 rc.1 的测试范围。新增 Debian 12 与 xlswriter 的 rc.2 已公开，
新测试请使用 [rc.2 测试指南](linux-test-rc2.zh-CN.md)。

[公开预发布版本](https://github.com/longyongzhong/phpm/releases/tag/v0.1.0-rc.1)。

先区分两件事：Linux amd64 的 PHPM 客户端使用 `CGO_ENABLED=0` 的静态 Go 构建，可在 Debian 12 x86_64 上先试运行；本次自建的 PHP/Redis/SDK 包只支持 **Debian 13 x86_64、glibc 2.41、NTS**，不能直接用于 Debian 12。

## 在当前 Debian 12 服务器测试客户端

需要 `curl`、`sha256sum`、`tar`。以下整段在 Linux 执行，安装到独立目录，不修改 PATH、系统 PHP或服务：

```sh
mkdir -p "$HOME/phpm-test-0.1.0-rc.1" &&
cd "$HOME/phpm-test-0.1.0-rc.1" &&
curl -fL -C - --retry 3 -o phpm.tar.gz https://github.com/longyongzhong/phpm/releases/download/v0.1.0-rc.1/phpm-0.1.0-rc.1-linux-amd64.tar.gz &&
printf '%s\n' '0f3234b8096f4c73edc4603e6c7eda3d0a54fd01ea04ced6509a28dc9cae9ef8  phpm.tar.gz' | sha256sum -c - &&
tar -xzf phpm.tar.gz &&
./phpm version
```

预期输出：

```text
phpm.tar.gz: OK
0.1.0-rc.1
```

之后可执行 `./phpm help` 查看命令。上面的 SHA-256 来自已验证根授权签名的 `clients.json`。下载中断后可重新执行下载命令，从本地已有字节续传；完整校验成功后才解压。

## PHP 运行时测试范围

Debian 12 的自建签名 PHP 目标尚未实现。默认 Linux `phpm php install` 使用第三方 StaticPHP，不能据此视为测试了此次自建签名源。

如果要完整测试这次发布的签名 PHP/Redis 包，请使用 Debian 13 x86_64 的测试环境，并按 [签名源使用指南](distribution.zh-CN.md) 安装对应运行库、注册源。根公钥指纹：

```text
b9aaa6269e68f95ea18147d09c043ca9c8ae332903e0bc04b23c34ad98e35d32
```

源地址为 `https://github.com/longyongzhong/phpm/releases/download/v0.1.0-rc.1/catalog.json`；此源使用根授权模式，注册时使用 `--root` 和 `--fingerprint`，不是旧版直签源的 `--key` 参数。完整根授权安装说明见 [离线发行指南的客户端与源章节](offline-release.zh-CN.md)。
