package db

import (
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/pkg/errors"
	"gorm.io/gorm"
)

// CreateProxyNode 创建代理节点
func CreateProxyNode(node *model.ProxyNode) error {
	return errors.WithStack(db.Create(node).Error)
}

// UpdateProxyNode 更新代理节点
func UpdateProxyNode(node *model.ProxyNode) error {
	return errors.WithStack(db.Save(node).Error)
}

// DeleteProxyNodeById 按 ID 删除代理节点
func DeleteProxyNodeById(id uint) error {
	return errors.WithStack(db.Delete(&model.ProxyNode{}, id).Error)
}

// GetProxyNodeById 按 ID 获取代理节点
func GetProxyNodeById(id uint) (*model.ProxyNode, error) {
	var node model.ProxyNode
	node.ID = id
	if err := db.First(&node).Error; err != nil {
		return nil, errors.WithStack(err)
	}
	return &node, nil
}

// GetProxyNodes 获取全部代理节点（按 ID 升序）
func GetProxyNodes() ([]model.ProxyNode, error) {
	var nodes []model.ProxyNode
	if err := db.Order("id asc").Find(&nodes).Error; err != nil {
		return nil, errors.WithStack(err)
	}
	return nodes, nil
}

// GetUsableProxyNodes 获取可选节点：未停用且不在风控期
func GetUsableProxyNodes() ([]model.ProxyNode, error) {
	var nodes []model.ProxyNode
	now := time.Now()
	if err := db.Where("status != ? AND (risk_until IS NULL OR risk_until <= ? OR status != ?)",
		model.ProxyNodeStatusDisable, now, model.ProxyNodeStatusRisk).
		Order("id asc").Find(&nodes).Error; err != nil {
		return nil, errors.WithStack(err)
	}
	return nodes, nil
}

// AddProxyNodeTraffic 原子累加节点累计流量并更新最近使用时间
func AddProxyNodeTraffic(nodeID uint, rx, tx int64) {
	if nodeID == 0 || (rx == 0 && tx == 0) {
		return
	}
	db.Model(&model.ProxyNode{}).Where("id = ?", nodeID).Updates(map[string]interface{}{
		"total_rx":     gorm.Expr("total_rx + ?", rx),
		"total_tx":     gorm.Expr("total_tx + ?", tx),
		"last_used_at": time.Now(),
		"updated_at":   time.Now(),
	})
}

// SetProxyNodeStatus 设置节点状态（normal/risk/disabled）
func SetProxyNodeStatus(nodeID uint, status string) error {
	return errors.WithStack(db.Model(&model.ProxyNode{}).Where("id = ?", nodeID).Update("status", status).Error)
}
