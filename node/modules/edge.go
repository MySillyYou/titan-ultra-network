package modules

import (
	"github.com/linguohua/titan/node/carfile/cache"
	"github.com/linguohua/titan/node/carfile/fetcher"
	"github.com/linguohua/titan/node/carfile/storage"
	"github.com/linguohua/titan/node/config"
	"github.com/linguohua/titan/node/device"
	"github.com/linguohua/titan/node/modules/dtypes"
	datasync "github.com/linguohua/titan/node/sync"
	"github.com/linguohua/titan/node/validate"
	"golang.org/x/time/rate"
)

func NewDevice(bandwidthUP, bandwidthDown int64) func(nodeID dtypes.NodeID, internalIP dtypes.InternalIP, storageMgr *storage.Manager) *device.Device {
	return func(nodeID dtypes.NodeID, internalIP dtypes.InternalIP, storageMgr *storage.Manager) *device.Device {
		return device.NewDevice(string(nodeID), string(internalIP), bandwidthUP, bandwidthDown, storageMgr)
	}
}

func NewRateLimiter(device *device.Device) *rate.Limiter {
	return rate.NewLimiter(rate.Limit(device.GetBandwidthUp()), int(device.GetBandwidthUp()))
}

func NewNodeStorageManager(path dtypes.CarfileStorePath) (*storage.Manager, error) {
	return storage.NewManager(string(path), nil)
}

func NewCacheManager(fetchBatch int) func(storageMgr *storage.Manager, bFetcher fetcher.BlockFetcher) (*cache.Manager, error) {
	return func(storageMgr *storage.Manager, bFetcher fetcher.BlockFetcher) (*cache.Manager, error) {
		opts := &cache.ManagerOptions{Storage: storageMgr, BFetcher: bFetcher, DownloadBatch: fetchBatch}
		return cache.NewManager(opts)
	}
}

func NewBlockFetcherFromCandidate(cfg *config.EdgeCfg) fetcher.BlockFetcher {
	return fetcher.NewCandidate(cfg.FetchBlockTimeout, cfg.FetchBlockRetry)
}

func NewDataSync(cacheMgr *cache.Manager) *datasync.DataSync {
	return datasync.NewDataSync(cacheMgr)
}

func NewNodeValidate(cacheMgr *cache.Manager, device *device.Device) *validate.Validate {
	return validate.NewValidate(cacheMgr, device)
}
