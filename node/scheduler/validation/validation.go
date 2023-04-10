package validation

import (
	"context"
	"math/rand"
	"time"

	"github.com/google/uuid"
	"github.com/linguohua/titan/api"
	"github.com/linguohua/titan/api/types"
	"github.com/linguohua/titan/node/cidutil"
	"golang.org/x/xerrors"
)

const (
	duration           = 10              // Validation duration per node (Unit:Second)
	validationInterval = 5 * time.Minute // validation start-up time interval (Unit:minute)
)

// starts the validation process.
func (m *Manager) startValidation(ctx context.Context) {
	ticker := time.NewTicker(validationInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if enable := m.isEnabled(); !enable {
				continue
			}

			if err := m.startNewRound(); err != nil {
				log.Errorf("start new round: %v", err)
			}
		case <-m.close:
			return
		}
	}
}

func (m *Manager) stopValidation(ctx context.Context) error {
	close(m.close)
	return nil
}

// returns whether or not validation is currently enabled.
func (m *Manager) isEnabled() bool {
	cfg, err := m.config()
	if err != nil {
		log.Errorf("enable err:%s", err.Error())
		return false
	}

	return cfg.EnableValidation
}

// startNewRound is a method of the Manager that starts a new validation round.
func (m *Manager) startNewRound() error {
	if m.curRoundID != "" {
		// Set the timeout status of the previous verification
		err := m.nodeMgr.SetValidationResultsTimeout(m.curRoundID)
		if err != nil {
			log.Errorf("round:%s SetValidationResultsTimeout err:%s", m.curRoundID, err.Error())
		}
	}

	roundID := uuid.NewString()
	m.curRoundID = roundID
	m.seed = time.Now().UnixNano() // TODO from filecoin

	vrs := m.PairValidatorsAndValidatableNodes()

	vReqs, dbInfos := m.getValidationDetails(vrs)
	if vReqs == nil {
		return xerrors.New("assignValidator map is null")
	}

	err := m.nodeMgr.SetValidationResultInfos(dbInfos)
	if err != nil {
		log.Errorf("SetValidationResultInfos err:%s", err.Error())
		return nil
	}

	for nodeID, reqs := range vReqs {
		go m.sendValidationReqToNodes(nodeID, reqs)
	}

	return nil
}

// sends a validation request to a node.
func (m *Manager) sendValidationReqToNodes(nID string, req *api.ValidationReq) {
	cNode := m.nodeMgr.GetNode(nID)
	if cNode != nil {
		err := cNode.Validatable(context.Background(), req)
		if err != nil {
			log.Errorf("%s BeValidate err:%s", nID, err.Error())
		}
		return
	}

	log.Errorf("%s BeValidate Node not found", nID)
}

// get validation details.
func (m *Manager) getValidationDetails(vrs []*VWindow) (map[string]*api.ValidationReq, []*types.ValidationResultInfo) {
	bReqs := make(map[string]*api.ValidationReq)
	vrInfos := make([]*types.ValidationResultInfo, 0)

	for _, vr := range vrs {
		vID := vr.NodeID
		vNode := m.nodeMgr.GetCandidateNode(vID)
		if vNode == nil {
			log.Errorf("%s validator not exist", vNode)
			continue
		}

		for nodeID := range vr.ValidatableNodes {
			cid, err := m.getNodeValidationCID(nodeID)
			if err != nil {
				log.Errorf("%s getNodeValidationCID err:%s", nodeID, err.Error())
				continue
			}

			dbInfo := &types.ValidationResultInfo{
				RoundID:     m.curRoundID,
				NodeID:      nodeID,
				ValidatorID: vID,
				Status:      types.ValidationStatusCreate,
				Cid:         cid,
			}
			vrInfos = append(vrInfos, dbInfo)

			req := &api.ValidationReq{
				CID:        cid,
				RandomSeed: m.seed,
				Duration:   duration,
				TCPSrvAddr: vNode.TCPAddr(),
			}

			bReqs[nodeID] = req
		}
	}

	return bReqs, vrInfos
}

// retrieves a random validation CID from the node with the given ID.
func (m *Manager) getNodeValidationCID(nodeID string) (string, error) {
	count, err := m.nodeMgr.LoadNodeReplicaCount(nodeID)
	if err != nil {
		return "", err
	}

	if count < 1 {
		return "", xerrors.New("Node has no replica")
	}

	rand := rand.New(rand.NewSource(time.Now().UnixNano()))
	// rand count
	offset := rand.Intn(count)

	cids, err := m.nodeMgr.LoadAssetCIDsByNodeID(nodeID, 1, offset)
	if err != nil {
		return "", err
	}

	if len(cids) < 1 {
		return "", xerrors.New("Node has no replica")
	}

	return cids[0], nil
}

// generates a random number up to a given maximum value.
func (m *Manager) getRandNum(max int, r *rand.Rand) int {
	if max > 0 {
		return r.Intn(max)
	}

	return max
}

// updates the validation result information for a given node.
func (m *Manager) updateResultInfo(status types.ValidationStatus, vr *api.ValidationResult) error {
	resultInfo := &types.ValidationResultInfo{
		RoundID:     vr.RoundID,
		NodeID:      vr.NodeID,
		Status:      status,
		BlockNumber: int64(len(vr.Cids)),
		Bandwidth:   vr.Bandwidth,
		Duration:    vr.CostTime,
	}

	return m.nodeMgr.UpdateValidationResultInfo(resultInfo)
}

// Result handles the validation result for a given node.
func (m *Manager) Result(vr *api.ValidationResult) error {
	if vr.RoundID != m.curRoundID {
		return xerrors.Errorf("round id does not match %s:%s", m.curRoundID, vr.RoundID)
	}

	var status types.ValidationStatus
	nodeID := vr.NodeID

	defer func() {
		err := m.updateResultInfo(status, vr)
		if err != nil {
			log.Errorf("updateResultInfo [%s] fail : %s", nodeID, err.Error())
		}
	}()

	if vr.IsCancel {
		status = types.ValidationStatusCancel
		return nil
	}

	if vr.IsTimeout {
		status = types.ValidationStatusNodeTimeOut
		return nil
	}

	cid, err := m.nodeMgr.LoadNodeValidationCID(vr.RoundID, nodeID)
	if err != nil {
		status = types.ValidationStatusOther
		log.Errorf("LoadNodeValidationCID %s , %s, err:%s", vr.RoundID, nodeID, err.Error())
		return nil
	}

	hash, err := cidutil.CIDString2HashString(cid)
	if err != nil {
		status = types.ValidationStatusOther
		log.Errorf("CIDString2HashString %s, err:%s", cid, err.Error())
		return nil
	}

	rows, err := m.nodeMgr.LoadReplicasByHash(hash, []types.ReplicaStatus{types.ReplicaStatusSucceeded})
	if err != nil {
		status = types.ValidationStatusOther
		log.Errorf("Get candidates %s , err:%s", hash, err.Error())
		return nil
	}
	defer rows.Close()

	max := len(vr.Cids)
	var cCidMap map[int]string

	for rows.Next() {
		rInfo := &types.ReplicaInfo{}
		err = rows.StructScan(rInfo)
		if err != nil {
			log.Errorf("replica StructScan err: %s", err.Error())
			continue
		}

		cNodeID := rInfo.NodeID
		if cNodeID == nodeID {
			continue
		}

		node := m.nodeMgr.GetCandidateNode(cNodeID)
		if node == nil {
			continue
		}

		cCidMap, err = node.GetBlocksOfAsset(context.Background(), cid, m.seed, max)
		if err != nil {
			log.Errorf("candidate %s GetBlocksOfAsset err:%s", cNodeID, err.Error())
			continue
		}

		break
	}

	if len(cCidMap) <= 0 {
		status = types.ValidationStatusOther
		log.Errorf("handleValidationResult candidate map is nil , %s", vr.CID)
		return nil
	}

	record, err := m.nodeMgr.LoadAssetRecord(hash)
	if err != nil {
		status = types.ValidationStatusOther
		log.Errorf("handleValidationResult asset record %s , err:%s", vr.CID, err.Error())
		return nil
	}

	r := rand.New(rand.NewSource(m.seed))
	// do validate
	for i := 0; i < max; i++ {
		resultCid := vr.Cids[i]
		randNum := m.getRandNum(int(record.TotalBlocks), r)
		vCid := cCidMap[randNum]

		// TODO Penalize the candidate if vCid error

		if !m.compareCid(resultCid, vCid) {
			status = types.ValidationStatusBlockFail
			log.Errorf("round [%d] and nodeID [%s], validator fail resultCid:%s, vCid:%s,randNum:%d,index:%d", vr.RoundID, nodeID, resultCid, vCid, randNum, i)
			return nil
		}
	}

	status = types.ValidationStatusSuccess
	return nil
}

// compares two CID strings and returns true if they are equal, false otherwise
func (m *Manager) compareCid(cidStr1, cidStr2 string) bool {
	hash1, err := cidutil.CIDString2HashString(cidStr1)
	if err != nil {
		return false
	}

	hash2, err := cidutil.CIDString2HashString(cidStr2)
	if err != nil {
		return false
	}

	return hash1 == hash2
}
