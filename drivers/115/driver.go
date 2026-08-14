package _115

import (
	"context"
	"strings"
	"sync"

	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	streamPkg "github.com/OpenListTeam/OpenList/v4/internal/stream"
	"github.com/OpenListTeam/OpenList/v4/pkg/http_range"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	driver115 "github.com/SheltonZhu/115driver/pkg/driver"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
	"golang.org/x/time/rate"
)

type Pan115 struct {
	model.Storage
	Addition
	client     *driver115.Pan115Client
	limiter    *rate.Limiter
	appVerOnce sync.Once
}

// multiRootID 多根模式下的虚拟根目录 ID
const multiRootID = "__multi_root__"

// GetRootId 单根返回真实根 ID；多根返回虚拟根 ID（挂载点显示各所选文件夹）
func (d *Pan115) GetRootId() string {
	ids := d.rootIDs()
	if len(ids) > 1 {
		return multiRootID
	}
	return ids[0]
}

// GetRoot 多根模式：返回虚拟根 obj，其下列出所有挂载的文件夹
func (d *Pan115) GetRoot(ctx context.Context) (model.Obj, error) {
	ids := d.rootIDs()
	if len(ids) == 1 {
		return &model.Object{
			ID:       ids[0],
			Name:     op.RootName,
			Modified: d.GetStorage().Modified,
			IsFolder: true,
			Mask:     model.Locked,
		}, nil
	}
	return &model.Object{
		ID:       multiRootID,
		Name:     op.RootName,
		Modified: d.GetStorage().Modified,
		IsFolder: true,
		Mask:     model.Locked,
	}, nil
}

func (d *Pan115) Config() driver.Config {
	return config
}

func (d *Pan115) GetAddition() driver.Additional {
	return &d.Addition
}

func (d *Pan115) Init(ctx context.Context) error {
	d.appVerOnce.Do(d.initAppVer)
	if d.LimitRate > 0 {
		d.limiter = rate.NewLimiter(rate.Limit(d.LimitRate), 1)
	}
	return d.login()
}

func (d *Pan115) WaitLimit(ctx context.Context) error {
	if d.limiter != nil {
		return d.limiter.Wait(ctx)
	}
	return nil
}

func (d *Pan115) Drop(ctx context.Context) error {
	return nil
}

func (d *Pan115) List(ctx context.Context, dir model.Obj, args model.ListArgs) ([]model.Obj, error) {
	if err := d.WaitLimit(ctx); err != nil {
		return nil, err
	}
	// 多根模式：虚拟根下列出所有挂载的文件夹
	if dir.GetID() == multiRootID {
		return d.listMultiRoots(ctx)
	}
	files, err := d.getFiles(dir.GetID())
	if err != nil && !errors.Is(err, driver115.ErrNotExist) {
		return nil, err
	}
	return utils.SliceConvert(files, func(src FileObj) (model.Obj, error) {
		return &src, nil
	})
}

// listMultiRoots 列出所有挂载根文件夹（虚拟文件夹，名称取自 115 真实文件夹名）
func (d *Pan115) listMultiRoots(ctx context.Context) ([]model.Obj, error) {
	var objs []model.Obj
	for _, id := range d.rootIDs() {
		f, err := d.getNewFile(id)
		if err != nil {
			log.Warnf("115 get multi root [%s] failed: %v", id, err)
			continue
		}
		objs = append(objs, &model.Object{
			ID:       f.GetID(),
			Name:     f.GetName(),
			Modified: f.ModTime(),
			IsFolder: true,
			Mask:     model.Locked,
		})
	}
	return objs, nil
}

func (d *Pan115) Link(ctx context.Context, file model.Obj, args model.LinkArgs) (*model.Link, error) {
	if err := d.WaitLimit(ctx); err != nil {
		return nil, err
	}
	f, ok := file.(*FileObj)
	if !ok {
		// 多根虚拟文件夹等非文件对象不支持生成直链
		return nil, errors.New("not a file object")
	}
	userAgent := args.Header.Get("User-Agent")
	downloadInfo, err := d.client.DownloadWithUA(f.PickCode, userAgent)
	if err != nil {
		return nil, err
	}
	link := &model.Link{
		URL:    downloadInfo.Url.Url,
		Header: downloadInfo.Header,
	}
	return link, nil
}

func (d *Pan115) MakeDir(ctx context.Context, parentDir model.Obj, dirName string) (model.Obj, error) {
	if err := d.WaitLimit(ctx); err != nil {
		return nil, err
	}

	result := driver115.MkdirResp{}
	form := map[string]string{
		"pid":   parentDir.GetID(),
		"cname": dirName,
	}
	req := d.client.NewRequest().
		SetFormData(form).
		SetResult(&result).
		ForceContentType("application/json;charset=UTF-8")

	resp, err := req.Post(driver115.ApiDirAdd)

	err = driver115.CheckErr(err, &result, resp)
	if err != nil {
		return nil, err
	}
	f, err := d.getNewFile(result.FileID)
	if err != nil {
		return nil, nil
	}
	return f, nil
}

func (d *Pan115) Move(ctx context.Context, srcObj, dstDir model.Obj) (model.Obj, error) {
	if err := d.WaitLimit(ctx); err != nil {
		return nil, err
	}
	if err := d.client.Move(dstDir.GetID(), srcObj.GetID()); err != nil {
		return nil, err
	}
	f, err := d.getNewFile(srcObj.GetID())
	if err != nil {
		return nil, nil
	}
	return f, nil
}

func (d *Pan115) Rename(ctx context.Context, srcObj model.Obj, newName string) (model.Obj, error) {
	if err := d.WaitLimit(ctx); err != nil {
		return nil, err
	}
	if err := d.client.Rename(srcObj.GetID(), newName); err != nil {
		return nil, err
	}
	f, err := d.getNewFile((srcObj.GetID()))
	if err != nil {
		return nil, nil
	}
	return f, nil
}

func (d *Pan115) Copy(ctx context.Context, srcObj, dstDir model.Obj) error {
	if err := d.WaitLimit(ctx); err != nil {
		return err
	}
	return d.client.Copy(dstDir.GetID(), srcObj.GetID())
}

func (d *Pan115) Remove(ctx context.Context, obj model.Obj) error {
	if err := d.WaitLimit(ctx); err != nil {
		return err
	}
	return d.client.Delete(obj.GetID())
}

func (d *Pan115) Put(ctx context.Context, dstDir model.Obj, stream model.FileStreamer, up driver.UpdateProgress) (model.Obj, error) {
	if err := d.WaitLimit(ctx); err != nil {
		return nil, err
	}

	var (
		fastInfo *driver115.UploadInitResp
		dirID    = dstDir.GetID()
	)

	if ok, err := d.client.UploadAvailable(); err != nil || !ok {
		return nil, err
	}
	if stream.GetSize() > d.client.UploadMetaInfo.SizeLimit {
		return nil, driver115.ErrUploadTooLarge
	}
	//if digest, err = d.client.GetDigestResult(stream); err != nil {
	//	return err
	//}

	const PreHashSize int64 = 128 * utils.KB
	hashSize := PreHashSize
	if stream.GetSize() < PreHashSize {
		hashSize = stream.GetSize()
	}
	reader, err := stream.RangeRead(http_range.Range{Start: 0, Length: hashSize})
	if err != nil {
		return nil, err
	}
	preHash, err := utils.HashReader(utils.SHA1, reader)
	if err != nil {
		return nil, err
	}
	preHash = strings.ToUpper(preHash)
	fullHash := stream.GetHash().GetHash(utils.SHA1)
	if len(fullHash) != utils.SHA1.Width {
		_, fullHash, err = streamPkg.CacheFullAndHash(stream, &up, utils.SHA1)
		if err != nil {
			return nil, err
		}
	}
	fullHash = strings.ToUpper(fullHash)

	// rapid-upload
	// note that 115 add timeout for rapid-upload,
	// and "sig invalid" err is thrown even when the hash is correct after timeout.
	if fastInfo, err = d.rapidUpload(stream.GetSize(), stream.GetName(), dirID, preHash, fullHash, stream); err != nil {
		return nil, err
	}
	if matched, err := fastInfo.Ok(); err != nil {
		return nil, err
	} else if matched {
		f, err := d.getNewFileByPickCode(fastInfo.PickCode)
		if err != nil {
			return nil, nil
		}
		return f, nil
	}

	var uploadResult *UploadResult
	// 闪传失败，上传
	if stream.GetSize() <= 10*utils.MB { // 文件大小小于10MB，改用普通模式上传
		if uploadResult, err = d.UploadByOSS(ctx, &fastInfo.UploadOSSParams, stream, dirID, up); err != nil {
			return nil, err
		}
	} else {
		// 分片上传
		if uploadResult, err = d.UploadByMultipart(ctx, &fastInfo.UploadOSSParams, stream.GetSize(), stream, dirID, up); err != nil {
			return nil, err
		}
	}

	file, err := d.getNewFile(uploadResult.Data.FileID)
	if err != nil {
		return nil, nil
	}
	return file, nil
}

func (d *Pan115) OfflineList(ctx context.Context) ([]*driver115.OfflineTask, error) {
	resp, err := d.client.ListOfflineTask(0)
	if err != nil {
		return nil, err
	}
	return resp.Tasks, nil
}

func (d *Pan115) OfflineDownload(ctx context.Context, uris []string, dstDir model.Obj) ([]string, error) {
	return d.client.AddOfflineTaskURIs(uris, dstDir.GetID(), driver115.WithAppVer(appVer))
}

func (d *Pan115) DeleteOfflineTasks(ctx context.Context, hashes []string, deleteFiles bool) error {
	return d.client.DeleteOfflineTasks(hashes, deleteFiles)
}

func (d *Pan115) GetDetails(ctx context.Context) (*model.StorageDetails, error) {
	info, err := d.client.GetInfo()
	if err != nil {
		return nil, err
	}
	return &model.StorageDetails{
		DiskUsage: model.DiskUsage{
			TotalSpace: info.SpaceInfo.AllTotal.Size,
			UsedSpace:  info.SpaceInfo.AllUse.Size,
		},
	}, nil
}

var _ driver.Driver = (*Pan115)(nil)
