package model

import "time"

// ThumbnailRecord stores the durable metadata for generated thumbnails.
// PathKey is a stable SHA-256 of kind+path and avoids indexing arbitrarily long paths.
// Fingerprint identifies the current object content/version and is used as the cache key.
type ThumbnailRecord struct {
	ID          uint      `json:"id" gorm:"primaryKey"`
	PathKey     string    `json:"path_key" gorm:"size:64;uniqueIndex"`
	Kind        string    `json:"kind" gorm:"size:16;index"`
	Path        string    `json:"path" gorm:"type:text"`
	Fingerprint string    `json:"fingerprint" gorm:"size:64;index"`
	CacheKey    string    `json:"cache_key" gorm:"size:64;index"`
	RemoteName  string    `json:"remote_name" gorm:"type:text"`
	ObjectID    string    `json:"object_id" gorm:"type:text"`
	Size        int64     `json:"size"`
	Modified    int64     `json:"modified"`
	Indexed     bool      `json:"indexed" gorm:"index"`
	Cloud       bool      `json:"cloud" gorm:"index"`
	Excluded    bool      `json:"excluded" gorm:"index"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}
