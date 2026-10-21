package tasks

import (
	"strings"
	"testing"
)

// TestBuildCreateArgs_DisguiseCPUModel 锁定 migration 0006 透传语义：
// hosts.disguise_cpu_model 非空时注入 -e DISGUISE_CPU_MODEL=...（容器
// entrypoint prepare_container_disguise 据此伪装 CPU 型号）；空串不注入，
// 容器内 spoof 模块回落镜像默认（AMD EPYC 7763）。
func TestBuildCreateArgs_DisguiseCPUModel(t *testing.T) {
	w := &Worker{}

	t.Run("non_empty_injects_env", func(t *testing.T) {
		req := minimalCreateHostRequest("h-cpu-model")
		req.DisguiseCPUModel = "Intel(R) Xeon(R) Platinum 8375C CPU @ 2.90GHz"

		args, err := w.buildCreateArgs(req, "c-cpu", "c-cpu", nil)
		if err != nil {
			t.Fatalf("buildCreateArgs: %v", err)
		}
		joined := strings.Join(args, " ")
		want := "-e DISGUISE_CPU_MODEL=Intel(R) Xeon(R) Platinum 8375C CPU @ 2.90GHz"
		if !strings.Contains(joined, want) {
			t.Fatalf("args 缺少 %q：%s", want, joined)
		}
	})

	t.Run("empty_omits_env", func(t *testing.T) {
		req := minimalCreateHostRequest("h-cpu-default")
		req.DisguiseCPUModel = ""

		args, err := w.buildCreateArgs(req, "c-cpu-def", "c-cpu-def", nil)
		if err != nil {
			t.Fatalf("buildCreateArgs: %v", err)
		}
		for _, a := range args {
			if strings.HasPrefix(a, "DISGUISE_CPU_MODEL=") {
				t.Fatalf("空串时不得注入 DISGUISE_CPU_MODEL，实际出现 %q", a)
			}
		}
	})

	t.Run("tz_injection_unaffected", func(t *testing.T) {
		req := minimalCreateHostRequest("h-cpu-tz")
		req.Timezone = "Asia/Shanghai"
		req.DisguiseCPUModel = "AMD EPYC 9654 96-Core Processor"

		args, err := w.buildCreateArgs(req, "c-tz", "c-tz", nil)
		if err != nil {
			t.Fatalf("buildCreateArgs: %v", err)
		}
		joined := strings.Join(args, " ")
		if !strings.Contains(joined, "-e TZ=Asia/Shanghai") {
			t.Fatalf("TZ 注入应不受影响，实际 args：%s", joined)
		}
	})
}
