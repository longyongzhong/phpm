# 默认使用 PHPM 自建签名源

当前源码将 Linux 的默认 PHP 下载改为 PHPM 自建签名源，并已生成 rc.3 签名草稿，可在 [Releases 页面](https://github.com/longyongzhong/phpm/releases)登录审阅，尚未公开。这是客户端行为变更，已发布 rc.2 仍需显式注册源；它不会因服务器配置变更自动获得新功能。

rc.3 是客户端更新，复用 rc.2 已验证的 PHP/扩展构建，因此内置 PHP 目录仍指向 rc.2；客户端版本与 PHP 包目录版本相互独立。

## 使用方式

在 Debian 12 x86_64 / glibc 2.36 或 Debian 13 x86_64 / glibc 2.41 上，准备好 [Debian 运行库](linux-test-rc2.zh-CN.md#准备系统运行库)，然后直接执行：

```sh
phpm php available
phpm php install 8.4
phpm init --php 8.4
phpm install
phpm ext install xlswriter
phpm ext install redis
phpm verify
```

无需下载根公钥文件、手动 `source add` 或传 `--source`。首次查询/安装会用客户端内置的根公钥验证授权与目录，成功后保存来源和防回退状态。签名、期限、系统或下载检查失败时直接报错，不会切换第三方来源。PHPM 不会自动安装系统运行库、修改 PATH 或重启服务。

当前内置源名为 `phpm`，固定目录为公开 rc.2 的 `catalog.json`，最低目录序号 `40999599815`，提供 PHP 8.4.27/8.5.12 和匹配的 Redis 6.3.0、xlswriter 3.0.0。根公钥 SHA-256：

```text
b9aaa6269e68f95ea18147d09c043ca9c8ae332903e0bc04b23c34ad98e35d32
```

公钥随客户端固定；不会把下载服务器旁边的公钥自动视为可信。内置来源固定到已审阅的发行目录，不自动选择 GitHub latest 或未审阅的最新预发布。目录过期需要新的有效目录和客户端更新/显式自定义源，不能绕过有效期。发布者更换默认目录时保留既有授权、撤销、序号和摘要记录，不降低防回退计数。

维护者更新默认目录时同时审阅 `default_source.go` 的地址与最低序号。更换发行根公钥需要同步客户端固定值，并通过与 `release/trust/root-key.pub` 的一致性检查；不能只替换服务器旁边的公钥文件。已配置来源的根冲突会明确报错，不自动覆盖。

`phpm` 是保留的内置源名称，不能用 `source add` 或 `source replace-root` 覆盖；原有同名来源若与内置信任根冲突则报错，不覆盖文件。自定义源使用其他名称，仍可通过原有显式公钥/根指纹注册和 `--source NAME` 选择，并按原有流程轮换根。

## 保留第三方与自定义来源

StaticPHP 改为显式选择，Linux x86_64 可执行：

```sh
phpm php available --provider staticphp
phpm php install 8.4 --provider staticphp
```

iStoreOS/OpenWrt 不支持自建 Debian 包；可显式尝试 StaticPHP 或导入匹配的本地构建。StaticPHP 的全静态模块限制不变。Windows 仍默认 PHP 官方 Windows 源，macOS/其他架构仍使用本地导入。

`--provider staticphp` 和 `--source NAME` 不能同时指定。新安装的自建 PHP 环境执行 `phpm php upgrade 8.5` 时自动沿用签名源；已有 StaticPHP 项目升级仍沿用 StaticPHP，已有自定义签名源项目仍沿用该来源。主动换源需显式指定 `--source NAME` / `--provider staticphp`，并测试候选环境后激活。

## 验证范围

单元测试覆盖内置根与受控公开根一致、未验证数据不能首次注册、客户端最低序号、防回退历史保留、同名错误根不覆盖、来源参数冲突和已有项目来源继承。

CI 保留显式 StaticPHP 的原有 bootstrap，新增 Debian 12/13 全新容器默认来源验收：无系统 PHP/编译器、非 root、无手工注册，实际下载验证生产签名元数据和包，执行 Excel 读写、Redis 加载、冻结安装、8.4 → 8.5 候选、激活与精确文件回退。[CI 41075574189](https://github.com/longyongzhong/phpm/actions/runs/41075574189) 六项全部通过；详细证据见 [验收记录](validation.md#linux-default-signed-source-2026-10-30)。

公开 rc.3 后可从其 Assets 下载 `phpm-0.1.0-rc.3-linux-amd64.tar.gz`。签名清单中的 SHA-256 为 `07daa0913b4e15f05a0ec6ad54c898a19d01771e25dc587f94a4d4fa4c7f5170`；草稿状态下不能匿名下载。切换客户端后可在独立 PHPM_HOME 和测试项目中执行上方命令。
