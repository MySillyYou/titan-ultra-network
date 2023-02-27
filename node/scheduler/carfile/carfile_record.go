package carfile

import (
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/linguohua/titan/api"
	"github.com/linguohua/titan/node/scheduler/db/cache"
	"github.com/linguohua/titan/node/scheduler/db/persistent"
	"github.com/linguohua/titan/node/scheduler/node"
	"golang.org/x/xerrors"
)

const (
	rootCacheStep = iota
	candidateCacheStep
	edgeCacheStep
	endStep
)

// CarfileRecord CarfileRecord
type CarfileRecord struct {
	nodeManager    *node.Manager
	carfileManager *Manager

	carfileCid  string
	carfileHash string
	replica     int
	totalSize   int64
	totalBlocks int
	expiredTime time.Time

	downloadSources  []*api.DownloadSource
	candidateReploca int
	CacheTaskMap     sync.Map

	lock sync.RWMutex

	edgeReplica          int               // An edge node represents a reliability
	step                 int               // dispatchCount int
	nodeCacheErrs        map[string]string // [deviceID]msg
	edgeNodeCacheSummary string
}

func newCarfileRecord(manager *Manager, cid, hash string) *CarfileRecord {
	return &CarfileRecord{
		nodeManager:     manager.nodeManager,
		carfileManager:  manager,
		carfileCid:      cid,
		carfileHash:     hash,
		downloadSources: make([]*api.DownloadSource, 0),
		nodeCacheErrs:   make(map[string]string),
	}
}

func loadCarfileRecord(hash string, manager *Manager) (*CarfileRecord, error) {
	dInfo, err := persistent.GetCarfileInfo(hash)
	if err != nil {
		return nil, err
	}

	carfileRecord := &CarfileRecord{}
	carfileRecord.carfileCid = dInfo.CarfileCid
	carfileRecord.nodeManager = manager.nodeManager
	carfileRecord.carfileManager = manager
	carfileRecord.totalSize = dInfo.TotalSize
	carfileRecord.replica = dInfo.Replica
	carfileRecord.totalBlocks = dInfo.TotalBlocks
	carfileRecord.expiredTime = dInfo.ExpiredTime
	carfileRecord.carfileHash = dInfo.CarfileHash
	carfileRecord.downloadSources = make([]*api.DownloadSource, 0)
	carfileRecord.nodeCacheErrs = make(map[string]string)

	caches, err := persistent.GetCarfileReplicaInfosWithHash(hash, false)
	if err != nil {
		log.Errorf("loadData hash:%s, GetCarfileReplicaInfosWithHash err:%s", hash, err.Error())
		return carfileRecord, err
	}

	for _, cacheInfo := range caches {
		if cacheInfo == nil {
			continue
		}

		c := &CacheTask{
			id:            cacheInfo.ID,
			deviceID:      cacheInfo.DeviceID,
			carfileRecord: carfileRecord,
			doneSize:      cacheInfo.DoneSize,
			doneBlocks:    cacheInfo.DoneBlocks,
			status:        cacheInfo.Status,
			isCandidate:   cacheInfo.IsCandidate,
			carfileHash:   cacheInfo.CarfileHash,
			nodeManager:   carfileRecord.nodeManager,
			createTime:    cacheInfo.CreateTime,
			endTime:       cacheInfo.EndTime,
		}

		if c.status == api.CacheStatusSucceeded {
			if c.isCandidate {
				carfileRecord.candidateReploca++

				cNode := carfileRecord.nodeManager.GetCandidateNode(c.deviceID)
				if cNode != nil {
					carfileRecord.downloadSources = append(carfileRecord.downloadSources, &api.DownloadSource{
						CandidateURL:   cNode.GetRPCURL(),
						CandidateToken: string(carfileRecord.carfileManager.writeToken),
					})
				}
			} else {
				carfileRecord.edgeReplica++
			}
		}

		carfileRecord.CacheTaskMap.Store(cacheInfo.DeviceID, c)
	}

	return carfileRecord, nil
}

func (d *CarfileRecord) candidateCacheExisted() bool {
	exist := false

	d.CacheTaskMap.Range(func(key, value interface{}) bool {
		if value == nil {
			return true
		}

		c := value.(*CacheTask)
		if c == nil {
			return true
		}

		exist = c.isCandidate && c.status == api.CacheStatusSucceeded
		if exist {
			return false
		}

		return true
	})

	return exist
}

func (d *CarfileRecord) startCacheTasks(nodes []string, isCandidate bool) (isRunning bool) {
	isRunning = false

	// set caches status
	err := persistent.UpdateCarfileReplicaStatus(d.carfileHash, nodes, api.CacheStatusRunning)
	if err != nil {
		log.Errorf("startCacheTasks %s , UpdateCarfileReplicaStatus err:%s", d.carfileHash, err.Error())
		return
	}

	err = cache.CacheTasksStart(d.carfileHash, nodes, cacheTimeoutTime)
	if err != nil {
		log.Errorf("startCacheTasks %s , CacheTasksStart err:%s", d.carfileHash, err.Error())
		return
	}

	errorList := make([]string, 0)

	for _, deviceID := range nodes {
		// find or create cache task
		var cacheTask *CacheTask
		cI, exist := d.CacheTaskMap.Load(deviceID)
		if !exist || cI == nil {
			cacheTask, err = newCacheTask(d, deviceID, isCandidate)
			if err != nil {
				log.Errorf("newCacheTask %s , node:%s,err:%s", d.carfileCid, deviceID, err.Error())
				errorList = append(errorList, deviceID)
				continue
			}
			d.CacheTaskMap.Store(deviceID, cacheTask)
		} else {
			cacheTask = cI.(*CacheTask)
		}

		// do cache
		err = cacheTask.startTask()
		if err != nil {
			log.Errorf("startCacheTasks %s , node:%s,err:%s", d.carfileCid, cacheTask.deviceID, err.Error())
			errorList = append(errorList, deviceID)
			continue
		}

		isRunning = true
	}

	if len(errorList) > 0 {
		// set caches status
		err := persistent.UpdateCarfileReplicaStatus(d.carfileHash, errorList, api.CacheStatusFailed)
		if err != nil {
			log.Errorf("startCacheTasks %s , UpdateCarfileReplicaStatus err:%s", d.carfileHash, err.Error())
		}

		_, err = cache.CacheTasksEnd(d.carfileHash, errorList)
		if err != nil {
			log.Errorf("startCacheTasks %s , CacheTasksEnd err:%s", d.carfileHash, err.Error())
		}
	}

	return
}

func (d *CarfileRecord) cacheToCandidates(needCount int) error {
	result := d.findAppropriateCandidates(d.CacheTaskMap, needCount)
	if len(result.list) <= 0 {
		return xerrors.Errorf("allCandidateCount:%d,filterCount:%d,insufficientDiskCount:%d,need:%d", result.allNodeCount, result.filterCount, result.insufficientDiskCount, needCount)
	}

	if !d.startCacheTasks(result.list, true) {
		return xerrors.New("running err")
	}

	return nil
}

func (d *CarfileRecord) cacheToEdges(needCount int) error {
	if len(d.downloadSources) <= 0 {
		return xerrors.New("not found cache sources")
	}

	result := d.findAppropriateEdges(d.CacheTaskMap, needCount)
	d.edgeNodeCacheSummary = fmt.Sprintf("allEdgeCount:%d,filterCount:%d,insufficientDiskCount:%d,need:%d", result.allNodeCount, result.filterCount, result.insufficientDiskCount, needCount)

	if len(result.list) <= 0 {
		return xerrors.New("not found edge")
	}

	if !d.startCacheTasks(result.list, false) {
		return xerrors.New("running err")
	}

	return nil
}

func (d *CarfileRecord) initStep() {
	d.step = endStep

	if d.candidateReploca <= 0 {
		d.step = rootCacheStep
		return
	}

	if d.candidateReploca < rootCacheCount+candidateReplicaCacheCount {
		d.step = candidateCacheStep
		return
	}

	if d.edgeReplica < d.replica {
		d.step = edgeCacheStep
	}
}

func (d *CarfileRecord) nextStep() {
	d.step++

	if d.step == candidateCacheStep {
		needCacdidateCount := (rootCacheCount + candidateReplicaCacheCount) - d.candidateReploca
		if needCacdidateCount <= 0 {
			// no need to cache to candidate , skip this step
			d.step++
		}
	}
}

// cache a carfile to the node
func (d *CarfileRecord) dispatchCache(deviceID string) error {
	cNode := d.nodeManager.GetCandidateNode(deviceID)
	if cNode != nil {
		if !d.startCacheTasks([]string{deviceID}, true) {
			return xerrors.New("running err")
		}

		return nil
	}

	eNode := d.nodeManager.GetEdgeNode(deviceID)
	if eNode != nil {
		if len(d.downloadSources) <= 0 {
			return xerrors.New("not found cache sources")
		}

		if !d.startCacheTasks([]string{deviceID}, false) {
			return xerrors.New("running err")
		}

		return nil
	}

	return xerrors.Errorf("node %s not found", deviceID)
}

func (d *CarfileRecord) dispatchCaches() error {
	switch d.step {
	case rootCacheStep:
		return d.cacheToCandidates(rootCacheCount)
	case candidateCacheStep:
		if d.candidateReploca == 0 {
			return xerrors.New("rootcache is 0")
		}
		needCacdidateCount := (rootCacheCount + candidateReplicaCacheCount) - d.candidateReploca
		if needCacdidateCount <= 0 {
			return xerrors.New("no caching required to candidate node")
		}
		return d.cacheToCandidates(needCacdidateCount)
	case edgeCacheStep:
		needEdgeCount := d.replica - d.edgeReplica
		if needEdgeCount <= 0 {
			return xerrors.New("no caching required to edge node")
		}
		return d.cacheToEdges(needEdgeCount)
	}

	return xerrors.New("steps completed")
}

func (d *CarfileRecord) updateCarfileRecordInfo(endCache *CacheTask, errMsg string) error {
	d.lock.Lock()
	defer d.lock.Unlock()

	if endCache.status == api.CacheStatusSucceeded {
		if endCache.isCandidate {
			d.candidateReploca++

			cNode := d.nodeManager.GetCandidateNode(endCache.deviceID)
			if cNode != nil {
				d.downloadSources = append(d.downloadSources, &api.DownloadSource{
					CandidateURL:   cNode.GetRPCURL(),
					CandidateToken: string(d.carfileManager.writeToken),
				})
			}
		} else {
			d.edgeReplica++
		}
	}

	if endCache.status == api.CacheStatusFailed {
		// err msg
		d.nodeCacheErrs[endCache.deviceID] = errMsg
	}

	// Carfile caches end
	dInfo := &api.CarfileRecordInfo{
		CarfileHash: d.carfileHash,
		TotalSize:   d.totalSize,
		TotalBlocks: d.totalBlocks,
		Replica:     d.replica,
		ExpiredTime: d.expiredTime,
	}
	return persistent.UpdateCarfileRecordCachesInfo(dInfo)
}

func (d *CarfileRecord) carfileCacheResult(deviceID string, info *api.CacheResultInfo) error {
	cacheI, exist := d.CacheTaskMap.Load(deviceID)
	if !exist {
		return xerrors.Errorf("cacheCarfileResult not found deviceID:%s,cid:%s", deviceID, d.carfileCid)
	}
	c := cacheI.(*CacheTask)

	c.status = info.Status
	c.doneBlocks = info.DoneBlockCount
	c.doneSize = info.DoneSize

	if c.status == api.CacheStatusRunning {
		// update cache task timeout
		return cache.UpdateNodeCacheingExpireTime(c.carfileHash, c.deviceID, cacheTimeoutTime)
	}

	// update node info
	node := d.nodeManager.GetNode(c.deviceID)
	if node != nil {
		node.IncrCurCacheCount(-1)
	}

	err := c.updateCacheTaskInfo()
	if err != nil {
		return xerrors.Errorf("endCache %s , updateCacheTaskInfo err:%s", c.carfileHash, err.Error())
	}

	err = d.updateCarfileRecordInfo(c, info.Msg)
	if err != nil {
		return xerrors.Errorf("endCache %s , updateCarfileRecordInfo err:%s", c.carfileHash, err.Error())
	}

	cachesDone, err := cache.CacheTasksEnd(c.carfileHash, []string{c.deviceID})
	if err != nil {
		return xerrors.Errorf("endCache %s , CacheTasksEnd err:%s", c.carfileHash, err.Error())
	}

	if c.status == api.CacheStatusSucceeded {
		err = cache.IncrBySystemBaseInfo(cache.CarFileCountField, 1)
		if err != nil {
			log.Errorf("endCache IncrBySystemBaseInfo err:%s", err.Error())
		}
	}

	if !cachesDone {
		// caches undone
		return nil
	}

	// next step
	d.nextStep()

	err = d.dispatchCaches()
	if err != nil {
		d.carfileManager.carfileCacheEnd(d, err)
	}

	return nil
}

type findNodeResult struct {
	list                  []string
	allNodeCount          int
	filterCount           int
	insufficientDiskCount int
}

// find the edges
func (d *CarfileRecord) findAppropriateEdges(filterMap sync.Map, count int) *findNodeResult {
	resultInfo := &findNodeResult{}

	nodes := make([]*node.Node, 0)
	if count <= 0 {
		return resultInfo
	}

	d.nodeManager.EdgeNodeMap.Range(func(key, value interface{}) bool {
		deviceID := key.(string)
		resultInfo.allNodeCount++

		if cI, exist := filterMap.Load(deviceID); exist {
			cache := cI.(*CacheTask)
			if cache.status == api.CacheStatusSucceeded {
				resultInfo.filterCount++
				return true
			}
		}

		node := value.(*node.EdgeNode)
		if node.DiskUsage > diskUsageMax {
			resultInfo.insufficientDiskCount++
			return true
		}

		nodes = append(nodes, node.Node)
		return true
	})

	sort.Slice(nodes, func(i, j int) bool {
		return nodes[i].GetCurCacheCount() < nodes[j].GetCurCacheCount()
	})

	if count > len(nodes) {
		count = len(nodes)
	}

	for _, node := range nodes[0:count] {
		resultInfo.list = append(resultInfo.list, node.DeviceID)
	}
	return resultInfo
}

// find the candidates
func (d *CarfileRecord) findAppropriateCandidates(filterMap sync.Map, count int) *findNodeResult {
	resultInfo := &findNodeResult{}

	nodes := make([]*node.Node, 0)
	if count <= 0 {
		return resultInfo
	}

	d.nodeManager.CandidateNodeMap.Range(func(key, value interface{}) bool {
		deviceID := key.(string)
		resultInfo.allNodeCount++

		if cI, exist := filterMap.Load(deviceID); exist {
			cache := cI.(*CacheTask)
			if cache.status == api.CacheStatusSucceeded {
				resultInfo.filterCount++
				return true
			}
		}

		node := value.(*node.CandidateNode)
		if node.DiskUsage > diskUsageMax {
			resultInfo.insufficientDiskCount++
			return true
		}

		nodes = append(nodes, node.Node)
		return true
	})

	sort.Slice(nodes, func(i, j int) bool {
		return nodes[i].GetCurCacheCount() < nodes[j].GetCurCacheCount()
	})

	if count > len(nodes) {
		count = len(nodes)
	}

	for _, node := range nodes[0:count] {
		resultInfo.list = append(resultInfo.list, node.DeviceID)
	}
	return resultInfo
}
