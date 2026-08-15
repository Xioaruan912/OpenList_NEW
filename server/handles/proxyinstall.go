package handles

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/db"
	"github.com/OpenListTeam/OpenList/v4/server/common"
	"github.com/gin-gonic/gin"

	proxydeploy "github.com/OpenListTeam/OpenList/v4/scripts/proxy"
)

// ProxyInstallScript GET /api/proxy/install.sh（无需登录）
// 返回代理节点一键部署脚本，供节点 VPS 上 `curl -fsSL <base>/api/proxy/install.sh | bash -s -- ...` 执行。
func ProxyInstallScript(c *gin.Context) {
	c.Data(http.StatusOK, "text/plain; charset=utf-8", proxydeploy.Script)
}

// ProxyInstallResp GET /api/admin/proxy/install 返回的安装命令信息
type ProxyInstallResp struct {
	Command      string `json:"command"`
	Type         string `json:"type"`
	Host         string `json:"host"`
	Port         int    `json:"port"`
	Password     string `json:"password"`
	HTTPPort     int    `json:"http_port"`
	TrafficPort  int    `json:"traffic_port"`
	TrafficToken string `json:"traffic_token"`
}

// ProxyInstall GET /api/admin/proxy/install?id=<nodeID>
// 根据节点配置生成一键安装命令：curl -fsSL <本服务>/api/proxy/install.sh | bash -s -- --type ... --port ...
// 同时为节点补全 trafficd 统计端口/token 并落库，供后续监控拉取。
func ProxyInstall(c *gin.Context) {
	var req struct {
		ID uint `form:"id" binding:"required"`
	}
	if err := c.ShouldBindQuery(&req); err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	node, err := db.GetProxyNodeById(req.ID)
	if err != nil {
		common.ErrorResp(c, err, 404, true)
		return
	}
	if node.TrafficPort <= 0 {
		node.TrafficPort = 9386
	}
	if node.Token == "" {
		node.Token = randHex(16)
	}
	_ = db.UpdateProxyNode(node)

	httpPort := 0
	if node.Type == "ss" {
		httpPort = node.Port + 1000
	}
	base := publicBaseURL(c)
	var b strings.Builder
	b.WriteString("curl -fsSL ")
	b.WriteString(base)
	b.WriteString("/api/proxy/install.sh | bash -s -- --type ")
	b.WriteString(node.Type)
	b.WriteString(" --port ")
	b.WriteString(strconv.Itoa(node.Port))
	if node.Password != "" {
		b.WriteString(" --password '")
		b.WriteString(node.Password)
		b.WriteString("'")
	}
	if node.Type == "ss" {
		b.WriteString(" --http-port ")
		b.WriteString(strconv.Itoa(httpPort))
	}
	b.WriteString(" --traffic-port ")
	b.WriteString(strconv.Itoa(node.TrafficPort))
	b.WriteString(" --traffic-token '")
	b.WriteString(node.Token)
	b.WriteString("'")

	common.SuccessResp(c, ProxyInstallResp{
		Command:      b.String(),
		Type:         node.Type,
		Host:         node.Host,
		Port:         node.Port,
		Password:     node.Password,
		HTTPPort:     httpPort,
		TrafficPort:  node.TrafficPort,
		TrafficToken: node.Token,
	})
}

// publicBaseURL 从请求推导服务对外可访问地址（支持反代透传 Host/Proto）
func publicBaseURL(c *gin.Context) string {
	scheme := "http"
	if c.Request.TLS != nil {
		scheme = "https"
	}
	if p := c.GetHeader("X-Forwarded-Proto"); p != "" {
		scheme = strings.Split(p, ",")[0]
	}
	host := c.Request.Host
	if h := c.GetHeader("X-Forwarded-Host"); h != "" {
		host = strings.Split(h, ",")[0]
	}
	return scheme + "://" + host
}

// randHex 生成 n 字节的随机十六进制串（token 用）
func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// ---- 节点探针（trafficd）数据拉取 ----

// ProxyNodeAgent 节点探针上报信息（trafficd）
type ProxyNodeAgent struct {
	Hostname  string `json:"hostname"`
	Uptime    int64  `json:"uptime"`
	Conns     int64  `json:"conns"`
	ProxConns int64  `json:"proxy_conns"`
	At        int64  `json:"at"`
}

type proxyAgentEntry struct {
	agent *ProxyNodeAgent
	at    time.Time
}

var (
	proxyAgentMu   sync.Mutex
	proxyAgentData = map[uint]*proxyAgentEntry{}
	proxyAgentTTL  = 8 * time.Second
)

// fetchNodeAgent 拉取单节点 trafficd 探针数据（带 8s 缓存与 2s 超时）。
// 仅当节点配置了 TrafficPort 且 Host 可访问时返回数据，失败返回 nil。
func fetchNodeAgent(nodeID uint, host string, port int, token string) *ProxyNodeAgent {
	if port <= 0 || host == "" {
		return nil
	}
	now := time.Now()
	proxyAgentMu.Lock()
	if e := proxyAgentData[nodeID]; e != nil && now.Sub(e.at) < proxyAgentTTL {
		proxyAgentMu.Unlock()
		return e.agent
	}
	proxyAgentMu.Unlock()

	var out *ProxyNodeAgent
	url := "http://" + host + ":" + strconv.Itoa(port) + "/stats?token=" + token
	client := &http.Client{Timeout: 2 * time.Second}
	if resp, err := client.Get(url); err == nil {
		if resp.StatusCode == http.StatusOK {
			var raw struct {
				OK        bool   `json:"ok"`
				Hostname  string `json:"hostname"`
				Uptime    int64  `json:"uptime"`
				Conns     int64  `json:"conns"`
				ProxConns int64  `json:"proxy_conns"`
			}
			if data, rerr := io.ReadAll(io.LimitReader(resp.Body, 1<<16)); rerr == nil {
				_ = json.Unmarshal(data, &raw)
			}
			if raw.OK {
				out = &ProxyNodeAgent{
					Hostname:  raw.Hostname,
					Uptime:    raw.Uptime,
					Conns:     raw.Conns,
					ProxConns: raw.ProxConns,
					At:        now.Unix(),
				}
			}
		}
		resp.Body.Close()
	}
	proxyAgentMu.Lock()
	proxyAgentData[nodeID] = &proxyAgentEntry{agent: out, at: now}
	proxyAgentMu.Unlock()
	return out
}
