package model

import (
	"time"
)

// 代理节点状态
const (
	ProxyNodeStatusNormal  = "normal"  // 正常
	ProxyNodeStatusRisk    = "risk"    // 风控/失败过多，暂停使用
	ProxyNodeStatusDisable = "disabled" // 手动停用
)

// ProxyNode 代理节点：用户其他 VPS 上部署的 http / ss 代理，
// 用于 115 缩略图下载与 API 请求分散出口 IP，降低网盘风控触发概率。
type ProxyNode struct {
	ID          uint       `json:"id" gorm:"primaryKey"`
	Name        string     `json:"name" gorm:"uniqueIndex;size:64"` // 节点名称（识别用，如 vps-hk）
	Type        string     `json:"type" gorm:"size:16"`             // http / ss
	Host        string     `json:"host" gorm:"size:255"`            // 代理服务器地址（IP 或域名）
	Port        int        `json:"port"`                            // 代理端口
	Password    string     `json:"password" gorm:"size:255"`        // 代理密码
	TrafficPort int        `json:"traffic_port"`                    // trafficd 统计服务监听端口（可选，已不再需要）
	Token       string     `json:"token" gorm:"size:128"`           // trafficd 鉴权 token（可选）
	Remark      string     `json:"remark" gorm:"size:255"`          // 备注
	Status      string     `json:"status" gorm:"size:16;default:normal"` // 节点状态
	FailCount   int        `json:"fail_count"`                      // 连续失败次数（达到阈值标记风控）
	RiskUntil   *time.Time `json:"risk_until,omitempty"`            // 风控解除时间（nil=未风控）
	TotalRx     int64      `json:"total_rx"`                        // 通过该代理累计接收字节（OpenList 侧计数）
	TotalTx     int64      `json:"total_tx"`                        // 通过该代理累计发送字节（OpenList 侧计数）
	LastUsedAt  *time.Time `json:"last_used_at,omitempty"`          // 最近一次使用时间
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

// IsRisk 是否处于风控状态（风控且未到解除时间）
func (n *ProxyNode) IsRisk() bool {
	return n.Status == ProxyNodeStatusRisk && n.RiskUntil != nil && time.Now().Before(*n.RiskUntil)
}

// IsUsable 是否可用于选择（未停用，且不在风控期）
func (n *ProxyNode) IsUsable() bool {
	if n.Status == ProxyNodeStatusDisable {
		return false
	}
	return !n.IsRisk()
}

// Address 构建代理连接地址（http://proxy:pass@host:port 或 socks5://...）
func (n *ProxyNode) Address() string {
	scheme := "http"
	host := n.Host
	if n.Type == "socks5" || n.Type == "ss" {
		scheme = "socks5"
	}
	port := n.Port
	if port <= 0 {
		return ""
	}
	if n.Password == "" {
		return scheme + "://" + host + ":" + itoa(port)
	}
	return scheme + "://proxy:" + n.Password + "@" + host + ":" + itoa(port)
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var b [20]byte
	p := len(b)
	for i > 0 {
		p--
		b[p] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		p--
		b[p] = '-'
	}
	return string(b[p:])
}