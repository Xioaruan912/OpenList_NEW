package handles

import (
	"sort"
	"sync"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/db"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/internal/setting"
	"github.com/OpenListTeam/OpenList/v4/server/common"
	"github.com/gin-gonic/gin"
)

// 缩略图代理选择模式
const (
	thumbProxyModeOff    = "off"
	thumbProxyModeAuto   = "auto"
	thumbProxyModeManual = "manual"
)

// 风控自动切换参数
const (
	proxyRiskThreshold = 3                // 连续失败达到该次数标记为风控
	proxyRiskCooldown  = 1 * time.Hour    // 风控持续时长：等待 30 分钟~1 小时后自动恢复可用；之后复用成功即自动解除风控状态
)

// ---- 配置读取 ----

func thumbProxyMode() string {
	m := setting.GetStr(conf.ThumbProxyMode, thumbProxyModeOff)
	if m != thumbProxyModeAuto && m != thumbProxyModeManual {
		return thumbProxyModeOff
	}
	return m
}

func thumbProxyNodeID() uint {
	return uint(setting.GetInt(conf.ThumbProxyNode, 0))
}

// resolveThumbProxy 解析缩略图请求应使用的代理节点。
// 返回 (代理地址, 节点ID)；nodeID=0 表示未使用代理节点（缩略图走系统默认网络）。
// 手动模式指定节点不可用时自动回退到任一健康节点。
func resolveThumbProxy() (string, uint) {
	mode := thumbProxyMode()
	if mode == thumbProxyModeOff {
		return "", 0
	}
	nodes := usableProxyNodes()
	if len(nodes) == 0 {
		return "", 0
	}
	if mode == thumbProxyModeManual {
		id := thumbProxyNodeID()
		for i := range nodes {
			if nodes[i].ID == id {
				return nodes[i].Address(), nodes[i].ID
			}
		}
	}
	// auto：优先使用当前指派的"下载节点"；节点失效则回退最近最少使用
	thumbAssignMu.Lock()
	assignedAddr, assignedID := thumbDownloadAddr, thumbDownloadID
	thumbAssignMu.Unlock()
	if assignedID != 0 && assignedAddr != "" {
		for i := range nodes {
			if nodes[i].ID == assignedID {
				return nodes[i].Address(), nodes[i].ID
			}
		}
	}
	// 手动节点不可用：选择最近最少使用的健康节点
	return pickLeastUsedNode(nodes)
}

func pickLeastUsedNode(nodes []model.ProxyNode) (string, uint) {
	var best *model.ProxyNode
	var bestAt time.Time
	for i := range nodes {
		at := time.Time{}
		if nodes[i].LastUsedAt != nil {
			at = *nodes[i].LastUsedAt
		}
		if best == nil || at.Before(bestAt) {
			best = &nodes[i]
			bestAt = at
		}
	}
	if best == nil {
		return "", 0
	}
	return best.Address(), best.ID
}

// ---- 下载/上传代理指派 ----

// 自动模式下，缩略图"下载侧"与"上传侧"使用不同节点，避免同一节点被瞬时风控
var (
	thumbAssignMu     sync.Mutex
	thumbDownloadAddr string
	thumbDownloadID   uint
)

// pickLeastUsedNodes 按最近使用时间升序返回健康节点（越靠前越少使用）
func pickLeastUsedNodes(nodes []model.ProxyNode) []model.ProxyNode {
	out := make([]model.ProxyNode, len(nodes))
	copy(out, nodes)
	sort.SliceStable(out, func(i, j int) bool {
		ai := time.Time{}
		aj := time.Time{}
		if out[i].LastUsedAt != nil {
			ai = *out[i].LastUsedAt
		}
		if out[j].LastUsedAt != nil {
			aj = *out[j].LastUsedAt
		}
		return ai.Before(aj)
	})
	return out
}

// refreshThumbProxyAssign 按当前模式重新指派缩略图"下载节点"。
// auto：下载=最不常用节点；manual：下载=指定节点；off：清空指派（走全局代理/直连）。
// 注：115 驱动（API/上传）的出站代理由全局代理策略（/admin/proxy/policy）管理。
func refreshThumbProxyAssign() {
	mode := thumbProxyMode()
	switch mode {
	case thumbProxyModeManual:
		id := thumbProxyNodeID()
		nodes, err := db.GetUsableProxyNodes()
		if err == nil {
			for i := range nodes {
				if nodes[i].ID == id {
					thumbAssignMu.Lock()
					thumbDownloadAddr = nodes[i].Address()
					thumbDownloadID = nodes[i].ID
					thumbAssignMu.Unlock()
					break
				}
			}
		}
	case thumbProxyModeAuto:
		nodes := usableProxyNodes()
		if len(nodes) == 0 {
			thumbAssignMu.Lock()
			thumbDownloadAddr, thumbDownloadID = "", 0
			thumbAssignMu.Unlock()
		} else {
			ordered := pickLeastUsedNodes(nodes)
			thumbAssignMu.Lock()
			thumbDownloadAddr = ordered[0].Address()
			thumbDownloadID = ordered[0].ID
			thumbAssignMu.Unlock()
		}
	default: // off
		thumbAssignMu.Lock()
		thumbDownloadAddr, thumbDownloadID = "", 0
		thumbAssignMu.Unlock()
	}
}

// StartThumbProxyAssign 应用启动时调用：立即指派一次并定时刷新（每次刷新会重新均衡下载/上传节点）
func StartThumbProxyAssign() {
	refreshThumbProxyAssign()
	go func() {
		t := time.NewTicker(5 * time.Minute)
		defer t.Stop()
		for range t.C {
			refreshThumbProxyAssign()
		}
	}()
}

// ---- 流量统计（内存窗口 + DB 累计）----

type proxyRateStat struct {
	rx    int64
	tx    int64
	start time.Time
	conns int64
}

var (
	proxyRateMu  sync.Mutex
	proxyRates   = map[uint]*proxyRateStat{}
	proxyConnCnt = map[uint]*int64{}
)

// proxyConnAdd 通过节点发起一次请求前调用（连接计数）
func proxyConnAdd(nodeID uint) {
	if nodeID == 0 {
		return
	}
	proxyRateMu.Lock()
	defer proxyRateMu.Unlock()
	if c := proxyConnCnt[nodeID]; c != nil {
		*c++
	} else {
		cnt := int64(1)
		proxyConnCnt[nodeID] = &cnt
	}
}

// proxyConnDel 请求结束（无论成败）后调用
func proxyConnDel(nodeID uint) {
	if nodeID == 0 {
		return
	}
	proxyRateMu.Lock()
	defer proxyRateMu.Unlock()
	if c := proxyConnCnt[nodeID]; c != nil && *c > 0 {
		*c--
	}
}

// recordProxyUse 记录一次通过节点的成功请求流量（rx=接收字节，tx=发送字节）
func recordProxyUse(nodeID uint, rx, tx int64) {
	if nodeID == 0 {
		return
	}
	now := time.Now()
	proxyRateMu.Lock()
	st := proxyRates[nodeID]
	if st == nil || now.Sub(st.start) > 60*time.Second {
		st = &proxyRateStat{start: now}
		proxyRates[nodeID] = st
	}
	st.rx += rx
	st.tx += tx
	proxyRateMu.Unlock()
	db.AddProxyNodeTraffic(nodeID, rx, tx)
}

// recordProxySuccess 记录成功请求：重置连续失败计数；风控冷却期过后被复用且成功即视为已安全，自动解除风控状态
func recordProxySuccess(nodeID uint) {
	if nodeID == 0 {
		return
	}
	node, err := db.GetProxyNodeById(nodeID)
	if err != nil {
		return
	}
	changed := false
	if node.FailCount != 0 {
		node.FailCount = 0
		changed = true
	}
	if node.Status == model.ProxyNodeStatusRisk {
		node.Status = model.ProxyNodeStatusNormal
		node.RiskUntil = nil
		changed = true
	}
	if changed {
		_ = db.UpdateProxyNode(node)
	}
}

// recordProxyFailure 记录一次通过节点的请求失败；连续失败达到阈值则标记风控（自动切换健康节点）
func recordProxyFailure(nodeID uint) {
	if nodeID == 0 {
		return
	}
	node, err := db.GetProxyNodeById(nodeID)
	if err != nil {
		return
	}
	if node.IsRisk() {
		return
	}
	node.FailCount++
	becameRisk := false
	if node.FailCount >= proxyRiskThreshold {
		riskUntil := time.Now().Add(proxyRiskCooldown)
		node.Status = model.ProxyNodeStatusRisk
		node.RiskUntil = &riskUntil
		node.FailCount = 0
		becameRisk = true
	}
	_ = db.UpdateProxyNode(node)
	// 节点变为风控后立即重新指派下载/上传节点，避免继续使用风险节点
	if becameRisk {
		refreshThumbProxyAssign()
	}
}

// nodeRateSnapshot 单节点当前实时速率/连接数
func nodeRateSnapshot(nodeID uint) (rx, tx int64, rxRate, txRate, conns int64) {
	proxyRateMu.Lock()
	defer proxyRateMu.Unlock()
	if st := proxyRates[nodeID]; st != nil {
		rx = st.rx
		tx = st.tx
		elapsed := time.Since(st.start).Seconds()
		if elapsed > 0 {
			rxRate = int64(float64(st.rx) / elapsed)
			txRate = int64(float64(st.tx) / elapsed)
		}
	}
	if c := proxyConnCnt[nodeID]; c != nil {
		conns = *c
	}
	return
}

// ---- 管理 API ----

// ThumbProxyConfigResp GET /admin/thumb/proxy
type ThumbProxyConfigResp struct {
	Mode        string               `json:"mode"`
	NodeID      uint                 `json:"node_id"`
	Effective   *ProxyNodeEffective  `json:"effective,omitempty"`
	Nodes       []ProxyNodeTrafficEx `json:"nodes"`
	GlobalProxy string               `json:"global_proxy_address"`
}

// ProxyNodeEffective 当前生效的代理节点
type ProxyNodeEffective struct {
	ID      uint   `json:"id"`
	Name    string `json:"name"`
	Address string `json:"address"`
	Status  string `json:"status"`
}

// ProxyNodeTrafficEx 节点 + 实时流量/风控状态
type ProxyNodeTrafficEx struct {
	model.ProxyNode
	IsRisk   bool            `json:"is_risk"`
	RXRate   int64           `json:"rx_rate"` // B/s
	TXRate   int64           `json:"tx_rate"`
	Conns    int64           `json:"conns"`
	WindowRX int64           `json:"window_rx"`
	WindowTX int64           `json:"window_tx"`
	Agent    *ProxyNodeAgent `json:"agent,omitempty"` // 节点探针（trafficd）上报
	Health   *ProxyHealth    `json:"health,omitempty"` // 真实连通性检查结果
}

func proxyNodesWithTraffic() []ProxyNodeTrafficEx {
	nodes, err := db.GetProxyNodes()
	if err != nil {
		return nil
	}
	out := make([]ProxyNodeTrafficEx, 0, len(nodes))
	for i := range nodes {
		n := nodes[i]
		item := ProxyNodeTrafficEx{ProxyNode: n}
		wrx, wtx, rxRate, txRate, conns := nodeRateSnapshot(n.ID)
		item.WindowRX = wrx
		item.WindowTX = wtx
		item.RXRate = rxRate
		item.TXRate = txRate
		item.Conns = conns
		item.IsRisk = n.IsRisk()
		item.Health = nodeHealthSnapshot(n.ID, n.Address())
		out = append(out, item)
	}
	// 并行拉取各节点探针（trafficd）；每个 goroutine 只写自己的下标，无竞争
	var wg sync.WaitGroup
	for i := range out {
		n := out[i]
		if n.TrafficPort <= 0 || n.Token == "" {
			continue
		}
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			out[idx].Agent = fetchNodeAgent(n.ID, n.Host, n.TrafficPort, n.Token)
		}(i)
	}
	wg.Wait()
	return out
}

// ThumbProxyConfig GET /admin/thumb/proxy
// 读取缩略图代理模式、节点清单与当前生效节点
func ThumbProxyConfig(c *gin.Context) {
	common.SuccessResp(c, thumbProxyConfigData())
}

func thumbProxyConfigData() ThumbProxyConfigResp {
	mode := thumbProxyMode()
	nodeID := thumbProxyNodeID()
	nodes := proxyNodesWithTraffic()
	resp := ThumbProxyConfigResp{
		Mode:        mode,
		NodeID:      nodeID,
		Nodes:       nodes,
		GlobalProxy: conf.Conf.ProxyAddress,
	}
	addr, effID := resolveThumbProxy()
	if effID != 0 && addr != "" {
		for _, n := range nodes {
			if n.ID == effID {
				resp.Effective = &ProxyNodeEffective{ID: n.ID, Name: n.Name, Address: addr, Status: n.Status}
				break
			}
		}
	}
	return resp
}

// ThumbProxySetReq POST /admin/thumb/proxy
type ThumbProxySetReq struct {
	Mode   string `json:"mode" binding:"required,oneof=off auto manual"`
	NodeID uint   `json:"node_id"`
}

// ThumbProxySet POST /admin/thumb/proxy
func ThumbProxySet(c *gin.Context) {
	var req ThumbProxySetReq
	if err := c.ShouldBind(&req); err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	if req.Mode == thumbProxyModeManual && req.NodeID != 0 {
		if _, err := db.GetProxyNodeById(req.NodeID); err != nil {
			common.ErrorStrResp(c, "proxy node not found", 400)
			return
		}
	}
	items := []model.SettingItem{
		{Key: conf.ThumbProxyMode, Value: req.Mode},
	}
	if req.Mode == thumbProxyModeManual {
		items = append(items, model.SettingItem{Key: conf.ThumbProxyNode, Value: itoaUint(req.NodeID)})
	}
	if err := op.SaveSettingItems(items); err != nil {
		common.ErrorResp(c, err, 500, true)
		return
	}
	refreshThumbProxyAssign()
	// 立即触发一次健康检查，让前端尽快看到所选节点的真实连通状态
	go func() {
		if req.NodeID != 0 {
			if n, err := db.GetProxyNodeById(req.NodeID); err == nil {
				checkNodeHealth(*n)
			}
		} else {
			runProxyHealthChecks()
		}
	}()
	common.SuccessResp(c, thumbProxyConfigData())
}

// ProxyTraffic GET /api/admin/proxy/traffic
// 返回节点清单与 OpenList 侧统计的代理使用流量（仅统计经代理的请求，非整机流量）
func ProxyTraffic(c *gin.Context) {
	common.SuccessResp(c, proxyNodesWithTraffic())
}

// ProxyEnable POST /api/admin/proxy/enable 启用/停用节点
func ProxyEnable(c *gin.Context) {
	var req struct {
		ID     uint   `json:"id" binding:"required"`
		Status string `json:"status" binding:"required,oneof=normal disabled"`
	}
	if err := c.ShouldBind(&req); err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	if err := db.SetProxyNodeStatus(req.ID, req.Status); err != nil {
		common.ErrorResp(c, err, 500, true)
		return
	}
	common.SuccessResp(c, nil)
}

func itoaUint(i uint) string {
	if i == 0 {
		return "0"
	}
	var b [20]byte
	p := len(b)
	for i > 0 {
		p--
		b[p] = byte('0' + i%10)
		i /= 10
	}
	return string(b[p:])
}
