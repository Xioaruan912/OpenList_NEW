package model

import (
	"time"
)

// ProxyNode 代理节点：用户其他 VPS 上部署的 http / ss 代理，
// 用于 115 缩略图下载与 API 请求分散出口 IP，降低网盘风控触发概率。
// 配套的 trafficd 统计服务提供实时流量查询（见 scripts/proxy/）。
type ProxyNode struct {
	ID          uint      `json:"id" gorm:"primaryKey"`
	Name        string    `json:"name" gorm:"uniqueIndex;size:64"`  // 节点名称（识别用，如 vps-hk）
	Type        string    `json:"type" gorm:"size:16"`              // http / ss
	Host        string    `json:"host" gorm:"size:255"`             // 代理服务器地址（IP 或域名）
	Port        int       `json:"port"`                             // 代理端口
	Password    string    `json:"password" gorm:"size:255"`         // 代理密码
	TrafficPort int       `json:"traffic_port"`                     // trafficd 统计服务监听端口
	Token       string    `json:"token" gorm:"size:128"`            // trafficd 鉴权 token
	Remark      string    `json:"remark" gorm:"size:255"`           // 备注
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}
