package tasks

import (
	"context"
	"testing"
)

// IMAGE_PULL_POLICY=if-not-present（默认）：本地镜像已存在时 pullImage 必须直接
// 跳过 docker pull，不向 registry 发请求。
func TestPullImage_SkipsWhenPresentLocally(t *testing.T) {
	prevDockerRunner := dockerRunner
	defer func() { dockerRunner = prevDockerRunner }()

	var pulled bool
	dockerRunner = func(_ context.Context, args ...string) ([]byte, error) {
		if len(args) >= 2 && args[0] == "pull" {
			pulled = true
		}
		// image inspect 成功（本地存在），pull 不应被调用
		if args[0] == "image" && args[1] == "inspect" {
			return []byte("sha256:local-image-id"), nil
		}
		return nil, nil
	}

	w := &Worker{}
	w.pullImage(context.Background(), "t-1", "h-1", "managed-user:test")

	if pulled {
		t.Fatal("docker pull must be skipped when the image exists locally")
	}
}

// IMAGE_PULL_POLICY=always：即使本地存在也强制拉取（决策不跳过）。
func TestPullImage_AlwaysPolicyPulls(t *testing.T) {
	t.Setenv("IMAGE_PULL_POLICY", "always")

	prevDockerRunner := dockerRunner
	defer func() { dockerRunner = prevDockerRunner }()

	var inspected bool
	dockerRunner = func(_ context.Context, args ...string) ([]byte, error) {
		if args[0] == "image" && args[1] == "inspect" {
			inspected = true
			return []byte("sha256:local-image-id"), nil
		}
		return nil, nil
	}

	if shouldSkipPull(context.Background(), "managed-user:test") {
		t.Fatal("IMAGE_PULL_POLICY=always must not skip pull")
	}
	if inspected {
		t.Fatal("always policy must not inspect the local image at all")
	}
}
