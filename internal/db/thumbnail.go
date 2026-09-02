package db

import (
	"errors"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func GetThumbnailRecord(pathKey string) (*model.ThumbnailRecord, error) {
	if db == nil {
		return nil, gorm.ErrInvalidDB
	}
	var record model.ThumbnailRecord
	err := db.Where("path_key = ?", pathKey).First(&record).Error
	if err != nil {
		return nil, err
	}
	return &record, nil
}

// UpsertThumbnailIdentity updates only object identity fields and preserves Indexed/Cloud state.
// changed reports that a previously known non-empty fingerprint changed.
func UpsertThumbnailIdentity(record *model.ThumbnailRecord) (changed bool, err error) {
	if db == nil {
		return false, gorm.ErrInvalidDB
	}
	var current model.ThumbnailRecord
	err = db.Where("path_key = ?", record.PathKey).First(&current).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		err = db.Create(record).Error
		return false, err
	}
	if err != nil {
		return false, err
	}
	changed = current.Fingerprint != "" && record.Fingerprint != "" && current.Fingerprint != record.Fingerprint
	err = db.Model(&current).Updates(map[string]interface{}{
		"kind":         record.Kind,
		"path":         record.Path,
		"fingerprint":  record.Fingerprint,
		"strategy":     record.Strategy,
		"cache_key":    record.CacheKey,
		"remote_name":  record.RemoteName,
		"object_id":    record.ObjectID,
		"size":         record.Size,
		"modified":     record.Modified,
		"last_seen_at": record.LastSeenAt,
	}).Error
	return changed, err
}

func ListThumbnailRecords(kind string) ([]model.ThumbnailRecord, error) {
	if db == nil {
		return nil, gorm.ErrInvalidDB
	}
	var records []model.ThumbnailRecord
	err := db.Where("kind = ?", kind).Order("id ASC").Find(&records).Error
	return records, err
}

func SetThumbnailFailure(pathKey, class, message string, failedAt, retryAfter time.Time) error {
	if db == nil {
		return gorm.ErrInvalidDB
	}
	return db.Model(&model.ThumbnailRecord{}).
		Where("path_key = ?", pathKey).
		Updates(map[string]interface{}{
			"failure_class":   class,
			"failure_message": message,
			"failure_count":   gorm.Expr("failure_count + ?", 1),
			"failed_at":       failedAt,
			"retry_after":     retryAfter,
		}).Error
}

func ClearThumbnailFailure(pathKey string) error {
	if db == nil {
		return gorm.ErrInvalidDB
	}
	return db.Model(&model.ThumbnailRecord{}).
		Where("path_key = ?", pathKey).
		Updates(map[string]interface{}{
			"failure_class":   "",
			"failure_message": "",
			"failed_at":       time.Time{},
			"retry_after":     time.Time{},
		}).Error
}

func TouchThumbnailAccess(pathKey string, at time.Time) error {
	if db == nil {
		return gorm.ErrInvalidDB
	}
	return db.Model(&model.ThumbnailRecord{}).Where("path_key = ?", pathKey).
		Update("last_accessed_at", at).Error
}

func MarkThumbnailGenerated(pathKey string, at time.Time, durationMS int64) error {
	if db == nil {
		return gorm.ErrInvalidDB
	}
	return db.Model(&model.ThumbnailRecord{}).Where("path_key = ?", pathKey).
		Updates(map[string]interface{}{
			"last_generated_at": at,
			"last_generate_ms":  durationMS,
			"generate_count":    gorm.Expr("generate_count + ?", 1),
			"failure_class":     "",
			"failure_message":   "",
			"failed_at":         time.Time{},
			"retry_after":       time.Time{},
		}).Error
}

func DeleteThumbnailRecord(pathKey string) error {
	if db == nil {
		return gorm.ErrInvalidDB
	}
	return db.Where("path_key = ?", pathKey).Delete(&model.ThumbnailRecord{}).Error
}

func SetThumbnailIndexed(record model.ThumbnailRecord, indexed bool) error {
	if db == nil {
		return gorm.ErrInvalidDB
	}
	record.Indexed = indexed
	return db.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "path_key"}},
		DoUpdates: clause.Assignments(map[string]interface{}{
			"kind":    record.Kind,
			"path":    record.Path,
			"indexed": indexed,
		}),
	}).Create(&record).Error
}

func SetThumbnailCloud(record model.ThumbnailRecord, cloud bool) error {
	if db == nil {
		return gorm.ErrInvalidDB
	}
	record.Cloud = cloud
	return db.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "path_key"}},
		DoUpdates: clause.Assignments(map[string]interface{}{
			"kind":        record.Kind,
			"path":        record.Path,
			"remote_name": record.RemoteName,
			"cloud":       cloud,
		}),
	}).Create(&record).Error
}

func SetThumbnailExcluded(record model.ThumbnailRecord, excluded bool) error {
	if db == nil {
		return gorm.ErrInvalidDB
	}
	record.Excluded = excluded
	return db.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "path_key"}},
		DoUpdates: clause.Assignments(map[string]interface{}{
			"kind":     record.Kind,
			"path":     record.Path,
			"excluded": excluded,
		}),
	}).Create(&record).Error
}

func ListIndexedThumbnailPaths(kind string) ([]string, error) {
	if db == nil {
		return nil, gorm.ErrInvalidDB
	}
	var paths []string
	err := db.Model(&model.ThumbnailRecord{}).
		Where("kind = ? AND indexed = ?", kind, true).
		Order("id ASC").
		Pluck("path", &paths).Error
	return paths, err
}

func ListCloudThumbnailPaths(kind string) ([]string, error) {
	if db == nil {
		return nil, gorm.ErrInvalidDB
	}
	var paths []string
	err := db.Model(&model.ThumbnailRecord{}).
		Where("kind = ? AND cloud = ?", kind, true).
		Order("id ASC").
		Pluck("path", &paths).Error
	return paths, err
}

func ListExcludedThumbnailPaths(kind string) ([]string, error) {
	if db == nil {
		return nil, gorm.ErrInvalidDB
	}
	var paths []string
	err := db.Model(&model.ThumbnailRecord{}).
		Where("kind = ? AND excluded = ?", kind, true).
		Order("id ASC").
		Pluck("path", &paths).Error
	return paths, err
}

func ReplaceIndexedThumbnailPaths(kind string, records []model.ThumbnailRecord) error {
	if db == nil {
		return gorm.ErrInvalidDB
	}
	return db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.ThumbnailRecord{}).Where("kind = ?", kind).Update("indexed", false).Error; err != nil {
			return err
		}
		if len(records) == 0 {
			return nil
		}
		for i := range records {
			records[i].Indexed = true
		}
		return tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "path_key"}},
			DoUpdates: clause.AssignmentColumns([]string{"kind", "path", "indexed"}),
		}).Create(&records).Error
	})
}

func ReplaceExcludedThumbnailPaths(kind string, records []model.ThumbnailRecord) error {
	if db == nil {
		return gorm.ErrInvalidDB
	}
	return db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.ThumbnailRecord{}).Where("kind = ?", kind).Update("excluded", false).Error; err != nil {
			return err
		}
		if len(records) == 0 {
			return nil
		}
		for i := range records {
			records[i].Excluded = true
		}
		return tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "path_key"}},
			DoUpdates: clause.AssignmentColumns([]string{"kind", "path", "excluded"}),
		}).Create(&records).Error
	})
}

func ClearThumbnailIndexState(kind string) error {
	if db == nil {
		return gorm.ErrInvalidDB
	}
	return db.Model(&model.ThumbnailRecord{}).
		Where("kind = ?", kind).
		Updates(map[string]interface{}{"indexed": false, "cloud": false}).Error
}

// MoveThumbnailRecord changes only the path identity while preserving the content fingerprint,
// cache key and remote filename. This lets directory/mount renames reuse the same thumbnail.
func MoveThumbnailRecord(kind, oldPathKey, newPathKey, newPath string) (bool, error) {
	if db == nil {
		return false, gorm.ErrInvalidDB
	}
	if oldPathKey == newPathKey {
		return false, nil
	}
	moved := false
	err := db.Transaction(func(tx *gorm.DB) error {
		var source model.ThumbnailRecord
		if err := tx.Where("path_key = ?", oldPathKey).First(&source).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return err
		}

		var target model.ThumbnailRecord
		targetErr := tx.Where("path_key = ?", newPathKey).First(&target).Error
		if targetErr == nil {
			updates := map[string]interface{}{
				"kind":     kind,
				"path":     newPath,
				"indexed":  source.Indexed || target.Indexed,
				"cloud":    source.Cloud || target.Cloud,
				"excluded": source.Excluded || target.Excluded,
			}
			if target.Fingerprint == "" && source.Fingerprint != "" {
				updates["fingerprint"] = source.Fingerprint
				updates["object_id"] = source.ObjectID
				updates["size"] = source.Size
				updates["modified"] = source.Modified
			}
			if target.CacheKey == "" && source.CacheKey != "" {
				updates["cache_key"] = source.CacheKey
			}
			if target.RemoteName == "" && source.RemoteName != "" {
				updates["remote_name"] = source.RemoteName
			}
			if err := tx.Model(&target).Updates(updates).Error; err != nil {
				return err
			}
		} else if errors.Is(targetErr, gorm.ErrRecordNotFound) {
			source.ID = 0
			source.PathKey = newPathKey
			source.Kind = kind
			source.Path = newPath
			if err := tx.Create(&source).Error; err != nil {
				return err
			}
		} else {
			return targetErr
		}

		if err := tx.Where("path_key = ?", oldPathKey).Delete(&model.ThumbnailRecord{}).Error; err != nil {
			return err
		}
		moved = true
		return nil
	})
	return moved, err
}
