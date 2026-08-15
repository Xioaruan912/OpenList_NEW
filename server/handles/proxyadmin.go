package handles

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/db"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/server/common"
	"github.com/gin-gonic/gin"
)

// ProxyAdminPage GET /admin/proxy
// 代理管理与流量查看页（独立静态页，不依赖前端构建产物）
func ProxyAdminPage(c *gin.Context) {
	c.Header("Content-Type", "text/html; charset=utf-8")
	_, _ = c.Writer.Write(proxyAdminHTML)
}

// ProxyTrafficResp trafficd 返回的实时流量统计
type ProxyTrafficResp struct {
	OK       bool   `json:"ok"`
	Hostname string `json:"hostname"`
	Uptime   int64  `json:"uptime"`
	Time     string `json:"time"`
	RXBytes  int64  `json:"rx_bytes"` // 累计接收字节（代理从外部拉取）
	TXBytes  int64  `json:"tx_bytes"` // 累计发送字节（代理下发给客户端）
	RXRate   int64  `json:"rx_rate"`  // 近 60s 平均接收速率 B/s
	TXRate   int64  `json:"tx_rate"`  // 近 60s 平均发送速率 B/s
	Conns    int    `json:"conns"`    // 当前 TCP 连接数
}

// ProxyNodeTraffic 节点 + 实时流量（含错误信息）
type ProxyNodeTraffic struct {
	model.ProxyNode
	Traffic *ProxyTrafficResp `json:"traffic,omitempty"`
	Error   string            `json:"error,omitempty"`
}

// queryTraffic 请求节点 trafficd 统计接口
func queryTraffic(node *model.ProxyNode) (*ProxyTrafficResp, error) {
	if node.Host == "" || node.TrafficPort <= 0 {
		return nil, fmt.Errorf("host or traffic_port not configured")
	}
	u := fmt.Sprintf("http://%s:%d/stats?token=%s", node.Host, node.TrafficPort, url.QueryEscape(node.Token))
	client := &http.Client{Timeout: 4 * time.Second}
	resp, err := client.Get(u)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("trafficd http %d", resp.StatusCode)
	}
	var t ProxyTrafficResp
	if err := json.NewDecoder(resp.Body).Decode(&t); err != nil {
		return nil, err
	}
	return &t, nil
}

// ListProxyNodes GET /api/admin/proxy/list
func ListProxyNodes(c *gin.Context) {
	nodes, err := db.GetProxyNodes()
	if err != nil {
		common.ErrorResp(c, err, 500, true)
		return
	}
	common.SuccessResp(c, nodes)
}

// ProxyNodeReq 创建/更新请求
type ProxyNodeReq struct {
	ID          uint   `json:"id"`
	Name        string `json:"name" binding:"required"`
	Type        string `json:"type" binding:"required,oneof=http ss"`
	Host        string `json:"host" binding:"required"`
	Port        int    `json:"port" binding:"required,min=1,max=65535"`
	Password    string `json:"password"`
	TrafficPort int    `json:"traffic_port"`
	Token       string `json:"token"`
	Remark      string `json:"remark"`
}

func bindProxyNodeReq(c *gin.Context) (*ProxyNodeReq, error) {
	var req ProxyNodeReq
	if err := c.ShouldBind(&req); err != nil {
		return nil, err
	}
	return &req, nil
}

// CreateProxyNode POST /api/admin/proxy/create
func CreateProxyNode(c *gin.Context) {
	req, err := bindProxyNodeReq(c)
	if err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	node := &model.ProxyNode{
		Name:        req.Name,
		Type:        req.Type,
		Host:        req.Host,
		Port:        req.Port,
		Password:    req.Password,
		TrafficPort: req.TrafficPort,
		Token:       req.Token,
		Remark:      req.Remark,
	}
	if err := db.CreateProxyNode(node); err != nil {
		common.ErrorResp(c, err, 500, true)
		return
	}
	common.SuccessResp(c, node)
}

// UpdateProxyNode POST /api/admin/proxy/update
func UpdateProxyNode(c *gin.Context) {
	req, err := bindProxyNodeReq(c)
	if err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	if req.ID == 0 {
		common.ErrorStrResp(c, "id is required", 400)
		return
	}
	old, err := db.GetProxyNodeById(req.ID)
	if err != nil {
		common.ErrorResp(c, err, 500, true)
		return
	}
	old.Name = req.Name
	old.Type = req.Type
	old.Host = req.Host
	old.Port = req.Port
	if req.Password != "" {
		old.Password = req.Password
	}
	old.TrafficPort = req.TrafficPort
	if req.Token != "" {
		old.Token = req.Token
	}
	old.Remark = req.Remark
	if err := db.UpdateProxyNode(old); err != nil {
		common.ErrorResp(c, err, 500, true)
		return
	}
	common.SuccessResp(c, old)
}

// DeleteProxyNode POST /api/admin/proxy/delete
func DeleteProxyNode(c *gin.Context) {
	var req struct {
		ID uint `json:"id" binding:"required"`
	}
	if err := c.ShouldBind(&req); err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	if err := db.DeleteProxyNodeById(req.ID); err != nil {
		common.ErrorResp(c, err, 500, true)
		return
	}
	common.SuccessResp(c, nil)
}

// ProxyTraffic GET /api/admin/proxy/traffic
// 批量查询所有代理节点的实时流量（每个节点 4s 超时）
func ProxyTraffic(c *gin.Context) {
	nodes, err := db.GetProxyNodes()
	if err != nil {
		common.ErrorResp(c, err, 500, true)
		return
	}
	result := make([]ProxyNodeTraffic, 0, len(nodes))
	for _, node := range nodes {
		item := ProxyNodeTraffic{ProxyNode: node}
		if t, err := queryTraffic(&node); err != nil {
			item.Error = err.Error()
		} else {
			item.Traffic = t
		}
		result = append(result, item)
	}
	common.SuccessResp(c, result)
}