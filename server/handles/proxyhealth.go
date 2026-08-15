package handles

import (
	"crypto/tls"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/db"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/go-resty/resty/v2"
	log "github.com/sirupsen/logrus"
)

// ---- 节点真实连通性健康检查 ----
// 通过节点代理实际发起 访问目标网站/下载/上传 请求，判断节点能否真正中继流量。
// trafficd 探针在线只代表监控进程可连，不代表代理可用（如 gost 认证失败 407）。

const (
	proxyHealthBlockTTL = 3 * time.Minute // 检查失败后从选择中排除的时长
	proxyHealthFreshTTL = 2 * time.Minute // 健康结果有效期（超时视为未知）
	proxyHealthInterval = 60 * time.Second
)

// ProxyHealth 对外暴露的健康检查结果
type ProxyHealth struct {
	OK          bool   `json:"ok"`
	AccessOK    bool   `json:"access_ok"`
	DownloadOK  bool   `json:"download_ok"`
	UploadOK    bool   `json:"upload_ok"`
	LatencyMS   int64  `json:"latency_ms"`
	Error       string `json:"error,omitempty"`
	CheckedUnix int64  `json:"checked_at"`
}

var (
	proxyHealthMu    sync.Mutex
	proxyHealthItems = map[uint]*ProxyHealth{}
	proxyHealthBusy  = map[uint]bool{}
)

// nodeHealthBlocked 节点是否有近期失败的健康记录（用于从代理选择中排除）
func nodeHealthBlocked(id uint) bool {
	proxyHealthMu.Lock()
	defer proxyHealthMu.Unlock()
	e, ok := proxyHealthItems[id]
	return ok && !e.OK && time.Since(time.Unix(e.CheckedUnix, 0)) < proxyHealthBlockTTL
}

func nodeHealthSnapshot(id uint) *ProxyHealth {
	proxyHealthMu.Lock()
	defer proxyHealthMu.Unlock()
	if e, ok := proxyHealthItems[id]; ok && time.Since(time.Unix(e.CheckedUnix, 0)) < proxyHealthFreshTTL {
		return e
	}
	return nil
}

// usableProxyNodes 可用且健康检查通过的节点
func usableProxyNodes() []model.ProxyNode {
	nodes, err := db.GetUsableProxyNodes()
	if err != nil {
		return nil
	}
	out := nodes[:0]
	for _, n := range nodes {
		if nodeHealthBlocked(n.ID) {
			continue
		}
		out = append(out, n)
	}
	return out
}

// checkNodeHealth 对单个节点执行真实流量检查（访问目标站/下载/上传）。
// 并发去重：同一节点不会同时检查两次。
func checkNodeHealth(n model.ProxyNode) {
	proxyHealthMu.Lock()
	if proxyHealthBusy[n.ID] {
		proxyHealthMu.Unlock()
		return
	}
	proxyHealthBusy[n.ID] = true
	proxyHealthMu.Unlock()
	defer func() {
		proxyHealthMu.Lock()
		delete(proxyHealthBusy, n.ID)
		proxyHealthMu.Unlock()
	}()

	e := &ProxyHealth{CheckedUnix: time.Now().Unix()}
	cli := resty.New()
	cli.SetTimeout(12 * time.Second)
	cli.SetProxy(n.Address())
	cli.SetTLSClientConfig(&tls.Config{InsecureSkipVerify: true})
	cli.SetHeader("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/120.0.0.0 Safari/537.36")

	start := time.Now()
	// 1) 访问目标网站 + 下载：115 SSO 返回任意 HTTP 状态即中继成功；gstatic 204 兜底
	targets := []string{
		"https://passportapi.115.com/app/1.0/web/1.0/check/sso",
		"https://www.gstatic.com/generate_204",
	}
	var lastErr error
	for _, u := range targets {
		resp, err := cli.R().Get(u)
		if err != nil {
			lastErr = err
			continue
		}
		if resp.StatusCode() == http.StatusProxyAuthRequired {
			lastErr = fmt.Errorf("proxy auth required (407)")
			continue
		}
		lastErr = nil
		e.AccessOK = true
		if resp.StatusCode() == http.StatusOK || resp.StatusCode() == http.StatusNoContent {
			e.DownloadOK = true
		}
		break
	}
	if lastErr != nil {
		e.Error = lastErr.Error()
	} else {
		e.LatencyMS = time.Since(start).Milliseconds()
	}

	// 2) 上传：POST 小数据到回显端点（尽力而为，端点不可达不影响判定）
	if lastErr == nil {
		upResp, upErr := cli.R().
			SetHeader("Content-Type", "application/json").
			SetBody(`{"probe":1}`).
			Post("https://postman-echo.com/post")
		if upErr == nil && (upResp.StatusCode() == http.StatusOK || upResp.StatusCode() == http.StatusCreated) {
			e.UploadOK = true
		}
	}

	e.OK = e.AccessOK && e.DownloadOK
	proxyHealthMu.Lock()
	proxyHealthItems[n.ID] = e
	proxyHealthMu.Unlock()

	if !e.OK {
		log.Warnf("[proxy] 节点 %s(%d) 连通性检查失败: %s", n.Name, n.ID, e.Error)
		// 手动指定/自动指派的节点失败：立即重新指派健康节点
		refreshThumbProxyAssign()
	} else {
		log.Debugf("[proxy] 节点 %s(%d) 连通正常 latency=%dms upload=%v", n.Name, n.ID, e.LatencyMS, e.UploadOK)
		// 连通恢复正常即视为节点已安全，自动解除风控状态（否则会一直等冷却期结束）
		recordProxySuccess(n.ID)
	}
}

// runProxyHealthChecks 检查所有非停用节点
func runProxyHealthChecks() {
	nodes, err := db.GetProxyNodes()
	if err != nil {
		return
	}
	for i := range nodes {
		n := nodes[i]
		if n.Status == model.ProxyNodeStatusDisable {
			continue
		}
		go checkNodeHealth(n)
	}
}

// StartProxyHealthCheck 启动后台健康检查（立即一轮 + 每 60s 一轮）
func StartProxyHealthCheck() {
	go func() {
		runProxyHealthChecks()
		t := time.NewTicker(proxyHealthInterval)
		defer t.Stop()
		for range t.C {
			runProxyHealthChecks()
		}
	}()
}
