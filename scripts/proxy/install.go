// Package proxydeploy 提供代理节点一键部署脚本（go:embed）。
package proxydeploy

import _ "embed"

// Script 是 proxy_deploy.sh 的完整内容，
// 通过公开接口 /api/proxy/install.sh 下发，
// 供节点 VPS 上 `curl -fsSL ... | bash -s -- ...` 直接执行。
//
//go:embed proxy_deploy.sh
var Script []byte