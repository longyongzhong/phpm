# 预编译 PHP 与扩展：第一批支持范围

目标是统一 PHP、扩展安装命令，由 PHPM 选择匹配的系统、架构、PHP ABI 和扩展包。生产服务器下载成品，不需要编译器；客户端本机编译和 PIE 集成仍未实现。

## 源码与已发布版本

当前源码增加 Debian 12/13 x86_64 NTS 两套构建配置，覆盖 PHP 8.4.27、8.5.12，以及独立的 Redis 6.3.0、xlswriter 3.0.0 动态扩展包。Debian 12 使用 glibc 2.36、bookworm 运行库；Debian 13 使用 glibc 2.41、trixie 运行库。缺少本机目标时直接报错，不混用安装包。

公开的 `v0.1.0-rc.1` 保持原有资产，只支持 Debian 13、Redis；新增组合必须通过真实构建验收，并以新版本发布后才能使用。源码能力不等于旧版安装包已经具备的能力。

默认 Linux 下载仍使用第三方 StaticPHP；此次没有自动注册或替换用户的来源。iStoreOS/OpenWrt、Ubuntu、ARM、macOS 尚未纳入自建预编译包范围。

## 构建与验收

GitHub Actions 的 **Debian PHP source build (stage A)** 新增 `debian` 选项，分别运行 Debian 12/13 × PHP 8.4/8.5 四个组合。

每次构建固定 Debian 官方基础镜像摘要、APT 快照、PHP 官方源码摘要和发布签名、扩展官方 PECL 源码摘要。xlswriter 摘要来自维护时官方 HTTPS 下载，不宣称存在扩展发布者的独立签名。扩展与随附库的许可证一起收集。

编译、SDK 迁移重编译和运行验收禁止外部网络。全新运行容器不含系统 PHP、编译器和开发头文件，以非 root 用户验证：

- Redis、xlswriter 使用匹配 SDK 重编译，精确版本实际加载。
- xlswriter 生成并读回 XLSX 数据，CLI/FPM 实际加载。
- 项目隔离、Composer 缺失扩展安装、冻结安装与升级回退。
- 临时测试密钥签名的 HTTPS 源下载、扩展安装、篡改拒绝与空缓存锁定恢复。

产物包含运行时、SDK、两个独立扩展包，以及 SDK、运行环境、签名源流程的验收报告。容器共享宿主内核，不能替代独立 VM 和业务项目验收。

## 签名与发布

**Prepare unsigned offline release materials** 与 **GitHub signed release draft** 保留 `base_run` / `target_run`，对应 Debian 13 PHP 8.4 / 8.5。可同时填写 `debian12_base_run` / `debian12_target_run`，加入 Debian 12 两版构建。Debian 12 输入必须成对提供；工作流检查运行与目标、PHP 分支的绑定。

维护工具将四套产物纳入同一个签名目录。客户端按本机 Debian/glibc 筛选目标，再按 PHP 版本、修订选择包；扩展绑定精确运行时构建。签名工作流只创建待审阅草稿，公开发布单独执行，不覆盖旧版资产。

后续增加 Xdebug、更多平台和显式本机编译选项。未经过构建、运行验证并发布的组合不列为可用支持。
