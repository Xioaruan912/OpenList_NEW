package handles

import (
	"github.com/OpenListTeam/OpenList/v4/internal/db"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/server/common"
	"github.com/gin-gonic/gin"
)

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
	refreshThumbProxyAssign()
	go checkNodeHealth(*node)
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
	refreshThumbProxyAssign()
	go checkNodeHealth(*old)
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
	proxyHealthMu.Lock()
	delete(proxyHealthItems, req.ID)
	proxyHealthMu.Unlock()
	refreshThumbProxyAssign()
	common.SuccessResp(c, nil)
}