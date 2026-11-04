# Hermes 受管模型供应商分组

CM 下发的受管目录需要分别显示 `ClawManager → auto` 和 `icompify → 已保存模型`。供应商 ID 保留 `clawmanager-*` 内部命名空间，避免与 Hermes 原生供应商和自动路由冲突；显示名称使用用户可识别的供应商名。

固定版本 Hermes 会按 API 地址、凭据、协议和额外请求头聚合供应商。多个受管供应商共用 CM Gateway 时，仅使用不同 ID 仍会合并到 auto 分组。因此配置生成器复用原生 `extra_headers` 分组维度，增加内部供应商 ID 的 SHA-256 标识；该标识不参与授权或路由，不替代 CM 的权限校验。

受管供应商设置 `discover_models=false`，仅使用 CM 已保存且授权的目录。合并既有 YAML 后，裁剪本次受管供应商的模型成员，保留合法成员元数据和其他用户自定义供应商，避免旧别名残留。没有直接修改上游 Python 源码。

2026-11-04 在测试节点 10.130.81.54 完成 Go race 配置回归；固定版本原生供应商 inventory 在禁网环境验证了共用地址和凭据时仍分为 ClawManager、icompify、另一个供应商三组，模型分别为 1/10/1 项。Smoke 文件哈希同步到源码锁；候选镜像通过原有制品清单生成与校验，未伪造 release acceptance。

候选 Runtime 镜像已构建但**尚未部署，实际桌面模型列表验收仍待完成**。提交源码不代表该部署步骤已完成；更新需要协调默认池模板、CM 镜像默认配置与真实休眠证明，不能在有未确认写入时直接替换 Runtime。

测试节点证据：`/root/cm-pod-elastic-20261104/hermes-groups-results/hermes-groups.log`、`hermes-groups-contract-red.log`、`hermes-provider-groups-image.json` 和候选镜像构建日志。
