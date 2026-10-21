import { Check, X, AlertCircle, Minus, Loader2 } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogFooter,
} from "@/components/ui/dialog";
import type { TestResult, ProbeStage } from "@/hooks/use-egress-ips";

interface TestResultDialogProps {
  result: TestResult | null;
  stage: ProbeStage | null;
  message: string;
  open: boolean;
  onOpenChange: (open: boolean) => void;
}

const stageOrder: { key: Exclude<ProbeStage, "done" | "error">; label: string }[] = [
  { key: "pulling", label: "拉取镜像" },
  { key: "starting", label: "初始化容器" },
  { key: "connecting", label: "建立连接" },
  { key: "testing", label: "执行检测" },
];

export function TestResultDialog({
  result,
  stage,
  message,
  open,
  onOpenChange,
}: TestResultDialogProps) {
  const isRunning = stage !== null && stage !== "done" && stage !== "error";
  const isError = stage === "error";
  const showResult = result && (stage === "done" || !isRunning);

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-md">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-3">
            出口 IP 测试
            {showResult && <StatusBadge status={result.status} />}
          </DialogTitle>
        </DialogHeader>

        {isRunning && (
          <div className="space-y-4">
            <StageProgress currentStage={stage} />
            <p className="text-sm text-muted-foreground flex items-center gap-2">
              <Loader2 className="h-4 w-4 animate-spin" />
              {message}
            </p>
          </div>
        )}

        {isError && (
          <div className="rounded-lg border border-red-200 bg-red-50 p-4">
            <p className="text-sm text-red-700 flex items-center gap-2">
              <AlertCircle className="h-4 w-4" />
              {message || "检测出错"}
            </p>
          </div>
        )}

        {showResult && (
          <div className="space-y-4">
            {result.message && (
              <p className="text-sm text-muted-foreground">{result.message}</p>
            )}
            {result.results && (
              <div className="space-y-4">
                <ConnectivitySection result={result.results.connectivity} />
                <EgressIPSection result={result.results.egress_ip} />
                <DNSLeakSection result={result.results.dns_leak} />
              </div>
            )}
            <p className="text-xs text-muted-foreground">
              测试时间：{formatDateTime(result.tested_at)}
            </p>
          </div>
        )}

        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            关闭
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function StageProgress({ currentStage }: { currentStage: ProbeStage }) {
  const currentIndex = stageOrder.findIndex((s) => s.key === currentStage);

  return (
    <div className="space-y-3">
      {stageOrder.map((s, idx) => {
        const isActive = idx === currentIndex;
        const isCompleted = idx < currentIndex;
        return (
          <div key={s.key} className="flex items-center gap-3">
            <span
              className={`flex h-6 w-6 shrink-0 items-center justify-center rounded-full text-xs font-medium ${
                isCompleted
                  ? "bg-green-100 text-green-700"
                  : isActive
                    ? "bg-blue-100 text-blue-700"
                    : "bg-gray-100 text-gray-400"
              }`}
            >
              {isCompleted ? (
                <Check className="h-3.5 w-3.5" />
              ) : (
                idx + 1
              )}
            </span>
            <span
              className={`text-sm ${
                isActive
                  ? "font-medium text-foreground"
                  : isCompleted
                    ? "text-muted-foreground"
                    : "text-gray-400"
              }`}
            >
              {s.label}
            </span>
            {isActive && (
              <Loader2 className="ml-auto h-4 w-4 animate-spin text-blue-600" />
            )}
          </div>
        );
      })}
    </div>
  );
}

function StatusBadge({ status }: { status: string }) {
  switch (status) {
    case "passed":
      return (
        <Badge className="bg-green-100 text-green-700 hover:bg-green-100">
          全部通过
        </Badge>
      );
    case "partial":
      return (
        <Badge className="bg-yellow-100 text-yellow-700 hover:bg-yellow-100">
          部分通过
        </Badge>
      );
    case "failed":
      return (
        <Badge className="bg-red-100 text-red-700 hover:bg-red-100">
          全部失败
        </Badge>
      );
    default:
      return <Badge variant="secondary">测试错误</Badge>;
  }
}

function StatusIcon({ status }: { status: string }) {
  switch (status) {
    case "pass":
      return (
        <span className="flex h-5 w-5 items-center justify-center rounded-full bg-green-100">
          <Check className="h-3 w-3 text-green-600" />
        </span>
      );
    case "fail":
      return (
        <span className="flex h-5 w-5 items-center justify-center rounded-full bg-red-100">
          <X className="h-3 w-3 text-red-600" />
        </span>
      );
    default:
      return (
        <span className="flex h-5 w-5 items-center justify-center rounded-full bg-gray-100">
          <AlertCircle className="h-3 w-3 text-gray-500" />
        </span>
      );
  }
}

function ConnectivitySection({
  result,
}: {
  result: TestResult["results"]["connectivity"];
}) {
  return (
    <div className="flex items-start gap-3 rounded-lg border p-3">
      <StatusIcon status={result.status} />
      <div className="flex-1">
        <p className="text-sm font-medium">连通性检测</p>
        {result.status === "pass" && result.latency_ms !== undefined && (
          <p className="text-xs text-muted-foreground">
            延迟: {result.latency_ms}ms
          </p>
        )}
        {result.error && <p className="text-xs text-red-600">{result.error}</p>}
      </div>
    </div>
  );
}

function EgressIPSection({
  result,
}: {
  result: TestResult["results"]["egress_ip"];
}) {
  return (
    <div className="flex items-start gap-3 rounded-lg border p-3">
      <StatusIcon status={result.status} />
      <div className="flex-1">
        <p className="text-sm font-medium">出口 IP 检测</p>
        {result.ip && (
          <p className="mt-1 text-sm">
            出口 IP: <span className="font-mono font-semibold">{result.ip}</span>
          </p>
        )}
        {result.sources && Object.keys(result.sources).length > 0 && (
          <div className="mt-2 space-y-0.5 text-xs text-muted-foreground">
            <p className="font-medium">检测来源:</p>
            {Object.entries(result.sources).map(([source, ip]) => (
              <p key={source} className="pl-2">
                {source}: <span className="font-mono">{ip}</span>
              </p>
            ))}
          </div>
        )}
        {result.error && <p className="text-xs text-red-600">{result.error}</p>}
      </div>
    </div>
  );
}

function DNSLeakSection({
  result,
}: {
  result: TestResult["results"]["dns_leak"];
}) {
  if (result.status === "skip") {
    return (
      <div className="flex items-start gap-3 rounded-lg border border-dashed p-3">
        <span className="flex h-5 w-5 items-center justify-center rounded-full bg-gray-100">
          <Minus className="h-3 w-3 text-gray-400" />
        </span>
        <div className="flex-1">
          <p className="text-sm font-medium text-muted-foreground">DNS 泄漏检测</p>
          <p className="text-xs text-muted-foreground">
            仅在容器运行时检测，探针测试不适用
          </p>
        </div>
      </div>
    );
  }

  return (
    <div className="flex items-start gap-3 rounded-lg border p-3">
      <StatusIcon status={result.status} />
      <div className="flex-1">
        <p className="text-sm font-medium">DNS 泄漏检测</p>
        {result.dns_servers_detected &&
          result.dns_servers_detected.length > 0 && (
            <div className="mt-1 text-xs text-muted-foreground">
              <p>检测到的 DNS 服务器:</p>
              {result.dns_servers_detected.map((dns) => (
                <p key={dns} className="pl-2 font-mono">
                  {dns}
                </p>
              ))}
            </div>
          )}
        {result.local_dns_leaked && (
          <p className="mt-1 text-xs text-red-600">检测到本地 DNS 泄漏</p>
        )}
        {result.error && <p className="text-xs text-red-600">{result.error}</p>}
      </div>
    </div>
  );
}

function formatDateTime(dateStr: string) {
  return new Date(dateStr).toLocaleString("zh-CN", {
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
  });
}
