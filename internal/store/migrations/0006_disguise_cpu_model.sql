-- 0006_disguise_cpu_model.sql
-- hosts 增加伪装 CPU 型号：disguise_cpu_model。空串表示未配置，容器内
-- spoof 模块回落镜像默认（AMD EPYC 7763）；非空时由控制面在 create_host
-- 时注入 DISGUISE_CPU_MODEL 环境变量（entrypoint prepare_container_disguise
-- 据此透传 SPOOF_CPU_MODEL）。
ALTER TABLE hosts ADD COLUMN disguise_cpu_model TEXT NOT NULL DEFAULT '';
