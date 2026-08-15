package db

import (
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/pkg/errors"
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