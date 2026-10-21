package http

// BootstrapErrorEntry maps an error_code to a stable Chinese message
// and a deterministic non-zero exit code for scripted consumption (D-10, D-13).
type BootstrapErrorEntry struct {
	Message  string
	ExitCode int
}

// BootstrapErrorEntries is the single source of truth shared by the
// handoff API and the bootstrap shell script. Adding or changing an
// entry here must be reflected in deploy/bootstrap/cloud-bootstrap.sh.
var BootstrapErrorEntries = map[string]BootstrapErrorEntry{
	"auth_invalid":           {Message: "用户名或密码错误", ExitCode: 10},
	"account_disabled":       {Message: "账号已被停用，请联系管理员", ExitCode: 11},
	"account_expired":        {Message: "账号已过期，请联系管理员续期", ExitCode: 12},
	"host_not_found":         {Message: "未找到可用主机，请联系管理员分配", ExitCode: 13},
	"start_failed":           {Message: "主机启动失败，请稍后重试", ExitCode: 14},
	"ssh_not_ready":          {Message: "SSH 端口在超时时间内未就绪，请稍后重试", ExitCode: 15},
	"egress_binding_missing": {Message: "主机未绑定出口 IP，请联系管理员", ExitCode: 16},
}

// LookupBootstrapError returns the entry for a given error_code,
// falling back to a generic message if the code is unknown.
func LookupBootstrapError(errorCode string) BootstrapErrorEntry {
	if entry, ok := BootstrapErrorEntries[errorCode]; ok {
		return entry
	}
	return BootstrapErrorEntry{
		Message:  "未知错误，请联系管理员",
		ExitCode: 1,
	}
}
