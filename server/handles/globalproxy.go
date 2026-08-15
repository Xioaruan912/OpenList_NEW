package handles

import (
	"time"

	driver115pkg "github.com/OpenListTeam/OpenList/v4/drivers/115"
	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/db"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/internal/setting"
	"github.com/OpenListTeam/OpenList/v4/server/common"
	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

// ---- 全局出站代理（/@manage/proxy 全局代理策略）----
// 控制 115 驱动客户端（API/上传）的出站代理：
//   - off：直连
//   - manual：始终使用指定节点
//   - auto：任一 115 挂载处于风控（IsStorageBlocked）时使用健康节点，否则直连（反应式）
const (
	globalProxyModeOff    = "off"
	globalProxyModeAuto   = "auto"
	globalProxyModeManual = "manual"

	globalProxyPollInterval = 30 * time.Second // 风控窗口 5 分钟，30s 轮询足够快速反应
)

func globalProxyMode() string {
	m := setting.GetStr(conf.GlobalProxyMode, globalProxyModeOff)
	if m != globalProxyModeAuto && m != globalProxyModeManual {
		return globalProxyModeOff
	}
	return m
}

func globalProxyNodeID() uint {
	return uint(setting.GetInt(conf.GlobalProxyNode, 0))
}

// applyProxyToDrivers 将出站代理应用到已加载的 115 驱动客户端（API/上传）
func applyProxyToDrivers(addr string) {
	for _, drv := range op.GetAllStorages() {
		if s, ok := drv.(interface{ SetUploadProxy(string) }); ok {
			s.SetUploadProxy(addr)
		}
	}
}

// refreshGlobalProxyAssign 按当前全局代理策略重新指派出站代理并应用到 115 驱动。
// auto：任一 115 挂载风控 → 选健康节点；全部正常 → 直连。
func refreshGlobalProxyAssign() {
	mode := globalProxyMode()
	var addr string
	switch mode {
	case globalProxyModeManual:
		id := globalProxyNodeID()
		if node, err := db.GetProxyNodeById(id); err == nil && node.Status != model.ProxyNodeStatusDisable {
			addr = node.Address()
		}
	case globalProxyModeAuto:
		if globalAnyStorageBlocked() {
			nodes := usableProxyNodes()
			if len(nodes) > 0 {
				ordered := pickLeastUsedNodes(nodes)
				addr = ordered[0].Address()
				log.Infof("[proxy] 115 风控中，全局出站代理切换到节点 %s(%d)", ordered[0].Name, ordered[0].ID)
			}
		}
	default: // off
	}
	op.SetGlobalProxy(addr)
	applyProxyToDrivers(addr)
}

// globalAnyStorageBlocked 是否有任一 115 挂载处于风控状态
func globalAnyStorageBlocked() bool {
	for _, m := range currentMountPaths() {
		if driver115pkg.IsStorageBlocked(m) {
			return true
		}
	}
	return false
}

// StartGlobalProxyAssign 启动时立即指派一次并定时刷新（30s 轮询，响应 5 分钟风控窗口）
func StartGlobalProxyAssign() {
	refreshGlobalProxyAssign()
	go func() {
		t := time.NewTicker(globalProxyPollInterval)
		defer t.Stop()
		for range t.C {
			refreshGlobalProxyAssign()
		}
	}()
}

// ---- API ----

// GlobalProxyConfigResp GET /admin/proxy/policy
type GlobalProxyConfigResp struct {
	Mode    string                `json:"mode"`
	NodeID  uint                  `json:"node_id"`
	Nodes   []ProxyNodeTrafficEx  `json:"nodes"`
	Global  string                `json:"global_proxy_address"`
	Current string                `json:"current"` // 当前实际生效地址（空=直连）
}

func globalProxyConfigData() GlobalProxyConfigResp {
	resp := GlobalProxyConfigResp{
		Mode:   globalProxyMode(),
		NodeID: globalProxyNodeID(),
		Nodes:  proxyNodesWithTraffic(),
		Global: conf.Conf.ProxyAddress,
	}
	if resp.Mode != globalProxyModeOff {
		resp.Current = op.GetGlobalProxy()
	}
	return resp
}

// GlobalProxyConfig GET /admin/proxy/policy
func GlobalProxyConfig(c *gin.Context) {
	common.SuccessResp(c, globalProxyConfigData())
}

// GlobalProxySetReq POST /admin/proxy/policy
type GlobalProxySetReq struct {
	Mode   string `json:"mode" binding:"required,oneof=off auto manual"`
	NodeID uint   `json:"node_id"`
}

// GlobalProxySet POST /admin/proxy/policy
func GlobalProxySet(c *gin.Context) {
	var req GlobalProxySetReq
	if err := c.ShouldBind(&req); err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	if req.Mode == globalProxyModeManual && req.NodeID != 0 {
		if _, err := db.GetProxyNodeById(req.NodeID); err != nil {
			common.ErrorStrResp(c, "proxy node not found", 400)
			return
		}
	}
	items := []model.SettingItem{
		{Key: conf.GlobalProxyMode, Value: req.Mode},
	}
	if req.Mode == globalProxyModeManual {
		items = append(items, model.SettingItem{Key: conf.GlobalProxyNode, Value: itoaUint(req.NodeID)})
	}
	if err := op.SaveSettingItems(items); err != nil {
		common.ErrorResp(c, err, 500, true)
		return
	}
	refreshGlobalProxyAssign()
	common.SuccessResp(c, globalProxyConfigData())
}
