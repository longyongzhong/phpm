# 独立 Debian VM 的真实版本升级验收：阶段 C

本轮只运行项目内置验收，不接入用户业务项目。目标是官方源码构建的
PHP 8.4.27 → 8.5.12 → 8.4.27，配套 Redis 6.3.0，并连接真实 MySQL 8.4.13。
源码包必须先通过阶段 A 的源码签名、编译、上游测试子集、SDK 与运行容器验收。

## 运行

在私有仓库 Actions 手动选择 **Debian VM real PHP upgrade (stage C)**：

- `base_run`：成功的 PHP 8.4 阶段 A 运行编号。
- `target_run`：成功的 PHP 8.5 阶段 A 运行编号。
- `soak_seconds`：120、600 或 1800 秒。默认两分钟，仅为有限时长重复验收。

工作流只读仓库，不发布 Release、不配置生产密钥。它在 GitHub Ubuntu runner 上
安装 QEMU 测试工具，启动真正的 Debian 客体 OS，使用 TCG，无需嵌套 KVM 权限。
客体有自己的内核和 boot ID；验收拒绝与宿主共享 boot ID 或运行在容器中的结果。

也可以在具备 Go、Python、Linux Docker、QEMU、cloud-localds、OpenSSH、OpenSSL
和 curl 的 Linux x86_64 测试主机运行：

```sh
python3 build/linux/vm/run.py --base /path/to/accepted-php84 --target /path/to/accepted-php85 --output /path/to/new-report --soak-seconds 120
```

控制器只创建自己的临时 VM、磁盘和唯一命名的 MySQL 容器，不连接现有 VM 或数据库。
不得把这些内置脚本改指向生产数据库后直接运行。宿主需要足够空间下载约 325 MiB
的 Debian 镜像和 MySQL 镜像；12 GiB 客体磁盘是稀疏 overlay，不覆盖原镜像。

## 固定输入和隔离

- Debian 镜像取自官方 `20261026-2643` 目录，SHA-512 固定在 `recipe.json`，与该目录
  官方 `SHA512SUMS` 核对。这里的依据为官方 HTTPS 和固定发布摘要，不宣称该云镜像
  具有独立 OpenPGP 签名。PHP 源码本身仍要求其发布者签名。
- MySQL 使用 Docker Official Image 的精确 8.4.13 amd64 manifest digest，发布元数据
  见 [官方 repo-info](https://github.com/docker-library/repo-info/blob/master/repos/mysql/remote/8.4.13.md)。
- 客体通过 Debian 签名快照安装运行库、Redis 服务和 FastCGI 测试工具，无系统 PHP、
  Go 或编译器。安装后删除外网默认路由，验收仅访问宿主回环映射的制品服务和 MySQL。
- MySQL 仅绑定宿主 `127.0.0.1` 的动态端口。数据库、账号和密码都是本次临时实例
  专用，密码不进入命令参数或报告。Redis 使用客体用户的私有 Unix socket，无 TCP。
- SSH 客户端和客体主机密钥每次独立生成；预先固定主机公钥，不关闭主机密钥验证。
  HTTPS 使用测试 CA 并保持证书验证，制品目录另验证临时 Ed25519 公钥。
- 临时签名 seed 生成目录后立即删除；VM 磁盘、cloud-init seed、SSH/TLS 私钥、数据库
  配置均不上传。退出时停止自己创建的进程和容器，并清理临时目录。

## 验收范围

1. 签名源安装真实 PHP 8.4，通过 Composer `--missing` 安装匹配扩展并冻结环境。
2. MySQL 预处理语句、utf8mb4 数据、事务回滚；Redis 实际读写与跨版本数据保留。
3. 不存在的升级目标必须被拒绝，当前项目文件不变。
4. 准备真实 PHP 8.5 候选，确认 PHP API 改变，候选 CLI、Composer、数据库检查通过。
5. 旧版本 Redis SO 导入新 PHP 必须被 ABI 检查拒绝，合法候选仍可验证。
6. 检查 PHP 8.4 的共享 OPcache 正确转成 PHP 8.5 内置 OPcache。
7. 两个独立 FPM master/Unix socket 都执行真实数据库请求，使用各自 PHP 和项目 INI。
8. 激活后新 PHPM 启动使用 8.5，已运行的旧 CLI/FPM 仍保持 8.4。
9. 对激活后的 CLI/FPM 重复进行数据库读写和事务检查，记录实际次数和持续时间。
10. 回退到原 8.4 环境，YAML/lock 字节完全恢复，数据保留，原 FPM 继续工作。

## 报告与边界

成功才生成 `acceptance.json` 和客体 `guest-report.json`，记录镜像摘要、内核/boot ID、
PHP API、版本序列、运行时 ID、MySQL 版本与重复测试样本。工作流也保留控制器、
串口和客体诊断日志；失败上传诊断文件不表示验收成功，以 job 和 `status` 为准。

这验证独立 VM 中的内置集成流程，不代表已验证实际业务、长时间高并发、生产 FPM
流量切换、服务管理器、数据库迁移回退或正式信任根。真实业务项目按用户选择稍后接入。

参考：[Debian 官方镜像目录](https://cloud.debian.org/images/cloud/trixie/20261026-2643/)、
[QEMU 系统仿真](https://www.qemu.org/docs/master/system/introduction.html)、
[cloud-init NoCloud](https://docs.cloud-init.io/en/latest/reference/datasources/nocloud.html)。
