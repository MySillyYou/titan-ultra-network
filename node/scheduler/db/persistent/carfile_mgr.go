package persistent

import (
	"context"
	"fmt"
	"math/rand"
	"time"

	"github.com/linguohua/titan/api/types"

	"github.com/jmoiron/sqlx"
	"github.com/linguohua/titan/node/modules/dtypes"
	"golang.org/x/xerrors"
)

type CarfileDB struct {
	DB *sqlx.DB
}

func NewCarfileDB(db *sqlx.DB) *CarfileDB {
	return &CarfileDB{db}
}

// UpdateReplicaInfo update replica info
func (c *CarfileDB) UpdateReplicaInfo(cInfo *types.ReplicaInfo) error {
	query := fmt.Sprintf(`UPDATE %s SET end_time=NOW(), status=?, done_size=? WHERE id=? AND (status=? or status=?)`, replicaInfoTable)
	result, err := c.DB.Exec(query, cInfo.Status, cInfo.DoneSize, cInfo.ID, types.CacheStatusCaching, types.CacheStatusWaiting)
	if err != nil {
		return err
	}

	r, err := result.RowsAffected()
	if err != nil {
		return err
	}

	if r < 1 {
		return xerrors.New("nothing to update")
	}

	return nil
}

// SetCarfileReplicasTimeout set timeout status of replicas
func (c *CarfileDB) SetCarfileReplicasTimeout(hash string) error {
	query := fmt.Sprintf(`UPDATE %s SET end_time=NOW(), status=? WHERE carfile_hash=? AND (status=? or status=?)`, replicaInfoTable)
	_, err := c.DB.Exec(query, types.CacheStatusFailed, hash, types.CacheStatusCaching, types.CacheStatusWaiting)

	return err
}

// InsertOrUpdateReplicaInfo Insert or update replica info
func (c *CarfileDB) InsertOrUpdateReplicaInfo(infos []*types.ReplicaInfo) error {
	query := fmt.Sprintf(
		`INSERT INTO %s (id, carfile_hash, node_id, status, is_candidate) 
				VALUES (:id, :carfile_hash, :node_id, :status, :is_candidate) 
				ON DUPLICATE KEY UPDATE status=VALUES(status)`, replicaInfoTable)

	_, err := c.DB.NamedExec(query, infos)

	return err
}

// UpdateOrCreateCarfileRecord update storage record info
func (c *CarfileDB) UpdateOrCreateCarfileRecord(info *types.CarfileRecordInfo) error {
	cmd := fmt.Sprintf(
		`INSERT INTO %s (carfile_hash, carfile_cid, state, edge_replicas, candidate_replicas, expiration, total_size, total_blocks, server_id, end_time) 
				VALUES (:carfile_hash, :carfile_cid, :state, :edge_replicas, :candidate_replicas, :expiration, :total_size, :total_blocks, :server_id, NOW()) 
				ON DUPLICATE KEY UPDATE total_size=VALUES(total_size), total_blocks=VALUES(total_blocks), state=VALUES(state), end_time=NOW()`, carfileInfoTable)

	_, err := c.DB.NamedExec(cmd, info)
	return err
}

// CarfileInfo get storage info with hash
func (c *CarfileDB) CarfileInfo(hash string) (*types.CarfileRecordInfo, error) {
	var info types.CarfileRecordInfo
	cmd := fmt.Sprintf("SELECT * FROM %s WHERE carfile_hash=?", carfileInfoTable)
	err := c.DB.Get(&info, cmd, hash)
	if err != nil {
		return nil, err
	}

	return &info, nil
}

// QueryCarfilesRows ...
func (c *CarfileDB) QueryCarfilesRows(ctx context.Context, limit, offset int, serverID dtypes.ServerID) (rows *sqlx.Rows, err error) {
	if limit == 0 {
		limit = loadCarfileInfoMaxCount
	}
	if limit > loadCarfileInfoMaxCount {
		limit = loadCarfileInfoMaxCount
	}

	cmd := fmt.Sprintf("SELECT * FROM %s WHERE state<>'Finalize' AND server_id=? order by carfile_hash asc LIMIT ? OFFSET ? ", carfileInfoTable)
	return c.DB.QueryxContext(ctx, cmd, serverID, limit, offset)
}

// CarfileRecordInfos get storage record infos
func (c *CarfileDB) CarfileRecordInfos(page int, states []string) (info *types.ListCarfileRecordRsp, err error) {
	num := loadCarfileInfoMaxCount
	info = &types.ListCarfileRecordRsp{}

	countCmd := fmt.Sprintf(`SELECT count(carfile_hash) FROM %s WHERE state in (?) `, carfileInfoTable)
	countQuery, args, err := sqlx.In(countCmd, states)
	if err != nil {
		return
	}

	countQuery = c.DB.Rebind(countQuery)
	err = c.DB.Get(&info.Cids, countQuery, args...)
	if err != nil {
		return
	}

	info.TotalPage = info.Cids / num
	if info.Cids%num > 0 {
		info.TotalPage++
	}

	if info.TotalPage == 0 {
		return
	}

	if page > info.TotalPage {
		page = info.TotalPage
	}
	info.Page = page

	selectCmd := fmt.Sprintf(`SELECT * FROM %s WHERE state in (?) order by carfile_hash asc LIMIT ?,?`, carfileInfoTable)
	selectQuery, args, err := sqlx.In(selectCmd, states, num*(page-1), num)
	if err != nil {
		return
	}

	selectQuery = c.DB.Rebind(selectQuery)
	err = c.DB.Select(&info.CarfileRecords, selectQuery, args...)
	if err != nil {
		return
	}

	return
}

// SucceedReplicasByCarfile get succeed replica nodeID by hash
func (c *CarfileDB) SucceedReplicasByCarfile(hash string, nType types.NodeType) ([]string, error) {
	isC := false

	switch nType {
	case types.NodeCandidate:
		isC = true
	case types.NodeEdge:
		isC = false
	default:
		return nil, xerrors.Errorf("node type is err:%d", nType)
	}

	var out []string
	query := fmt.Sprintf(`SELECT node_id FROM %s WHERE carfile_hash=? AND status=? AND is_candidate=?`,
		replicaInfoTable)

	if err := c.DB.Select(&out, query, hash, types.CacheStatusSucceeded, isC); err != nil {
		return nil, err
	}

	return out, nil
}

// ReplicaInfosByCarfile get storage replica infos by hash
func (c *CarfileDB) ReplicaInfosByCarfile(hash string, needSucceed bool) ([]*types.ReplicaInfo, error) {
	var out []*types.ReplicaInfo
	if needSucceed {
		query := fmt.Sprintf(`SELECT * FROM %s WHERE carfile_hash=? AND status=?`, replicaInfoTable)

		if err := c.DB.Select(&out, query, hash, types.CacheStatusSucceeded); err != nil {
			return nil, err
		}
	} else {
		query := fmt.Sprintf(`SELECT * FROM %s WHERE carfile_hash=? `, replicaInfoTable)

		if err := c.DB.Select(&out, query, hash); err != nil {
			return nil, err
		}
	}

	return out, nil
}

// RandomCarfileFromNode Get a random carfile from the node
func (c *CarfileDB) RandomCarfileFromNode(nodeID string) (string, error) {
	query := fmt.Sprintf(`SELECT count(carfile_hash) FROM %s WHERE node_id=? AND status=?`, replicaInfoTable)

	var count int
	if err := c.DB.Get(&count, query, nodeID, types.CacheStatusSucceeded); err != nil {
		return "", err
	}

	if count < 1 {
		return "", xerrors.Errorf("node %s no cache", nodeID)
	}

	rand := rand.New(rand.NewSource(time.Now().UnixNano()))
	// rand count
	index := rand.Intn(count)

	var hashes []string
	cmd := fmt.Sprintf("SELECT carfile_hash FROM %s WHERE node_id=? AND status=? LIMIT %d,%d", replicaInfoTable, index, 1)
	if err := c.DB.Select(&hashes, cmd, nodeID, types.CacheStatusSucceeded); err != nil {
		return "", err
	}

	if len(hashes) > 0 {
		return hashes[0], nil
	}

	return "", nil
}

// ResetCarfileExpiration reset expiration time with storage record
func (c *CarfileDB) ResetCarfileExpiration(carfileHash string, eTime time.Time) error {
	cmd := fmt.Sprintf(`UPDATE %s SET expiration=? WHERE carfile_hash=?`, carfileInfoTable)
	_, err := c.DB.Exec(cmd, eTime, carfileHash)

	return err
}

// MinExpiration Get the minimum expiration time
func (c *CarfileDB) MinExpiration() (time.Time, error) {
	query := fmt.Sprintf(`SELECT MIN(expiration) FROM %s`, carfileInfoTable)

	var out time.Time
	if err := c.DB.Get(&out, query); err != nil {
		return out, err
	}

	return out, nil
}

// ExpiredCarfiles load all expired carfiles
func (c *CarfileDB) ExpiredCarfiles() ([]*types.CarfileRecordInfo, error) {
	query := fmt.Sprintf(`SELECT * FROM %s WHERE expiration <= NOW()`, carfileInfoTable)

	var out []*types.CarfileRecordInfo
	if err := c.DB.Select(&out, query); err != nil {
		return nil, err
	}

	return out, nil
}

// UnDoneNodes load undone nodes for carfile
func (c *CarfileDB) UnDoneNodes(hash string) ([]string, error) {
	var nodes []string
	query := fmt.Sprintf(`SELECT node_id FROM %s WHERE carfile_hash=? AND (status=? or status=?)`, replicaInfoTable)
	err := c.DB.Select(&nodes, query, hash, types.CacheStatusCaching, types.CacheStatusWaiting)
	return nodes, err
}

// RemoveCarfileRecord remove storage
func (c *CarfileDB) RemoveCarfileRecord(carfileHash string) error {
	tx, err := c.DB.Beginx()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// cache info
	cCmd := fmt.Sprintf(`DELETE FROM %s WHERE carfile_hash=? `, replicaInfoTable)
	_, err = tx.Exec(cCmd, carfileHash)
	if err != nil {
		return err
	}

	// data info
	dCmd := fmt.Sprintf(`DELETE FROM %s WHERE carfile_hash=?`, carfileInfoTable)
	_, err = tx.Exec(dCmd, carfileHash)
	if err != nil {
		return err
	}

	return tx.Commit()
}

// LoadCarfileRecordsWithNodes load carfile record hashes with nodes
func (c *CarfileDB) LoadCarfileRecordsWithNodes(nodeIDs []string) (hashes []string, err error) {
	// get carfiles
	getCarfilesCmd := fmt.Sprintf(`select carfile_hash from %s WHERE node_id in (?) GROUP BY carfile_hash`, replicaInfoTable)
	carfilesQuery, args, err := sqlx.In(getCarfilesCmd, nodeIDs)
	if err != nil {
		return
	}

	carfilesQuery = c.DB.Rebind(carfilesQuery)
	err = c.DB.Select(&hashes, carfilesQuery, args...)

	return
}

// RemoveReplicaInfoWithNodes remove replica info with nodes
func (c *CarfileDB) RemoveReplicaInfoWithNodes(nodeIDs []string) error {
	// remove cache
	cmd := fmt.Sprintf(`DELETE FROM %s WHERE node_id in (?)`, replicaInfoTable)
	query, args, err := sqlx.In(cmd, nodeIDs)
	if err != nil {
		return err
	}

	query = c.DB.Rebind(query)
	_, err = c.DB.Exec(query, args...)

	return err
}

func (c *CarfileDB) GetBlockDownloadInfoByID(id string) (*types.DownloadRecordInfo, error) {
	query := fmt.Sprintf(`SELECT * FROM %s WHERE id = ?`, blockDownloadInfo)

	var out []*types.DownloadRecordInfo
	if err := c.DB.Select(&out, query, id); err != nil {
		return nil, err
	}

	if len(out) > 0 {
		return out[0], nil
	}
	return nil, nil
}

func (c *CarfileDB) GetNodesByUserDownloadBlockIn(minute int) ([]string, error) {
	starTime := time.Now().Add(time.Duration(minute) * time.Minute * -1)

	query := fmt.Sprintf(`SELECT node_id FROM %s WHERE complete_time > ? group by node_id`, blockDownloadInfo)

	var out []string
	if err := c.DB.Select(&out, query, starTime); err != nil {
		return nil, err
	}

	return out, nil
}

func (c *CarfileDB) GetReplicaInfosWithNode(nodeID string, index, count int) (info *types.NodeReplicaRsp, err error) {
	info = &types.NodeReplicaRsp{}

	cmd := fmt.Sprintf("SELECT count(id) FROM %s WHERE node_id=?", replicaInfoTable)
	err = c.DB.Get(&info.TotalCount, cmd, nodeID)
	if err != nil {
		return
	}

	cmd = fmt.Sprintf("SELECT carfile_hash,status FROM %s WHERE node_id=? order by id asc LIMIT %d,%d", replicaInfoTable, index, count)
	if err = c.DB.Select(&info.Replica, cmd, nodeID); err != nil {
		return
	}

	return
}

func (c *CarfileDB) GetBlockDownloadInfos(nodeID string, startTime time.Time, endTime time.Time, cursor, count int) ([]types.DownloadRecordInfo, int64, error) {
	query := fmt.Sprintf(`SELECT * FROM %s WHERE node_id = ? and created_time between ? and ? limit ?,?`, blockDownloadInfo)

	var total int64
	countSQL := fmt.Sprintf(`SELECT count(*) FROM %s WHERE node_id = ? and created_time between ? and ?`, blockDownloadInfo)
	if err := c.DB.Get(&total, countSQL, nodeID, startTime, endTime); err != nil {
		return nil, 0, err
	}

	if count > loadBlockDownloadMaxCount {
		count = loadBlockDownloadMaxCount
	}

	var out []types.DownloadRecordInfo
	if err := c.DB.Select(&out, query, nodeID, startTime, endTime, cursor, count); err != nil {
		return nil, 0, err
	}

	return out, total, nil
}

func (c *CarfileDB) CarfileReplicaList(startTime time.Time, endTime time.Time, cursor, count int) (*types.ListCarfileReplicaRsp, error) {
	var total int64
	countSQL := fmt.Sprintf(`SELECT count(*) FROM %s WHERE end_time between ? and ?`, replicaInfoTable)
	if err := c.DB.Get(&total, countSQL, startTime, endTime); err != nil {
		return nil, err
	}

	if count > loadReplicaInfoMaxCount {
		count = loadReplicaInfoMaxCount
	}

	query := fmt.Sprintf(`SELECT * FROM %s WHERE end_time between ? and ? limit ?,?`, replicaInfoTable)

	var out []*types.ReplicaInfo
	if err := c.DB.Select(&out, query, startTime, endTime, cursor, count); err != nil {
		return nil, err
	}

	return &types.ListCarfileReplicaRsp{Datas: out, Total: total}, nil
}