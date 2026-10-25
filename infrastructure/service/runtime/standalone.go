package runtime

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
)

// Pinned CPython 3.13.17 / python-build-standalone 20261021. Digests are
// the published SHA-256 values of the install_only_stripped release assets:
// https://github.com/astral-sh/python-build-standalone/releases/tag/20261021
var standaloneDigests = map[string]string{
	"x86_64-unknown-linux-gnu":   "f829adc9d888e76c8801b4799ed444188d35239fcae3a9808a99ec5cb0d8d92c",
	"aarch64-unknown-linux-gnu":  "c6857c65b996701a5da104cbb49b35249c0c026dc0664239b04f7156aa88326a",
	"x86_64-unknown-linux-musl":  "089e64ce3a7969382e4782e2051c12d6de143e9f7d102c89a8c8d54e7c554b90",
	"aarch64-unknown-linux-musl": "5af630e7e458af4675ea9a39b220b80baed17bc7a3e22b0fc97115a54fa0e96e",
	"x86_64-apple-darwin":        "ce9c748c8f7525c0052247edb2435fdb1cac89c77e687e0e199eaf2b827668c2",
	"aarch64-apple-darwin":       "8dc60610a82f1e73be7faf964383e02520f95f6e39592a5c37c3809f5a7b798e",
	"x86_64-pc-windows-msvc":     "56cde3b1a1dcc36576753cb408ee0c3568d211baafa0eee40423adc147c480f2",
	"aarch64-pc-windows-msvc":    "9287d32654828930872c8225f53618dac9b06dd3af5d4c3e9ab1c576e0741c96",
}

func standaloneArtifact(ctx context.Context) (artifact, error) {
	arch := map[string]string{"amd64": "x86_64", "arm64": "aarch64"}[runtime.GOARCH]
	target := ""
	switch runtime.GOOS {
	case "linux":
		libc := "gnu"
		out, _ := exec.CommandContext(ctx, "ldd", "--version").CombinedOutput()
		if strings.Contains(strings.ToLower(string(out)), "musl") {
			libc = "musl"
		}
		target = arch + "-unknown-linux-" + libc
	case "darwin":
		target = arch + "-apple-darwin"
	case "windows":
		target = arch + "-pc-windows-msvc"
	}
	digest, ok := standaloneDigests[target]
	if !ok {
		return artifact{}, fmt.Errorf("automatic Python download is unavailable for %s/%s; install Python >=3.11 on PATH", runtime.GOOS, runtime.GOARCH)
	}
	return artifact{url: "https://github.com/astral-sh/python-build-standalone/releases/download/20261021/cpython-3.13.17%2B20261021-" + target + "-install_only_stripped.tar.gz", sha256: digest}, nil
}
