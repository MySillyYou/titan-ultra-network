package carfile

import (
	blocks "github.com/ipfs/go-block-format"
	"github.com/linguohua/titan/api"
	"github.com/linguohua/titan/node/carfile/downloader"
)

// implement DownloadOperation interface in carfile_download_mgr.go
type downloadOperation struct {
	carfileOperation *CarfileOperation
	downloader       downloader.BlockDownloader
}

func (dOperation *downloadOperation) downloadResult(carfile *carfileCache, isComplete bool) error {
	return dOperation.carfileOperation.downloadResult(carfile, isComplete)
}

func (dOperation *downloadOperation) downloadBlocks(cids []string, sources []*api.DowloadSource) ([]blocks.Block, error) {
	return dOperation.downloader.DownloadBlocks(cids, sources)
}

func (dOperation *downloadOperation) saveBlock(data []byte, blockHash, carfileHash string) error {
	return dOperation.carfileOperation.saveBlock(data, blockHash, carfileHash)
}