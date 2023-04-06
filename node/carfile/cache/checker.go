package cache

import (
	"context"
	"math/rand"
	"sort"

	"github.com/ipfs/go-cid"
	"github.com/ipfs/go-libipfs/blocks"
	"github.com/linguohua/titan/node/carfile/index"
	"github.com/linguohua/titan/node/carfile/storage"
	"golang.org/x/xerrors"
)

// implement validate.RandomChecker
type randomCheck struct {
	randomSeed int64
	rand       *rand.Rand
	root       *cid.Cid
	storage.Storage
	idx *index.MultiIndexSorted
	lru *lruCache
}

func NewRandomCheck(randomSeed int64, storage storage.Storage, lru *lruCache) *randomCheck {
	return &randomCheck{randomSeed: randomSeed, Storage: storage, lru: lru}
}

func (rc *randomCheck) GetBlock(ctx context.Context) (blocks.Block, error) {
	if rc.rand == nil {
		rc.rand = rand.New(rand.NewSource(rc.randomSeed))
	}

	if rc.root == nil {
		car, err := rc.randomCar(ctx)
		if err != nil {
			return nil, xerrors.Errorf("random car %w", err)
		}
		rc.root = car
	}

	if rc.idx == nil {
		idx, err := rc.lru.carIndex(*rc.root)
		if err != nil {
			return nil, xerrors.Errorf("car index %w", err)
		}

		if multiIndex, ok := idx.(*index.MultiIndexSorted); !ok {
			return nil, xerrors.Errorf("idx is not MultiIndexSorted")
		} else {
			rc.idx = multiIndex
		}
	}

	sizeOfBucket := rc.idx.BucketSize()
	index := rc.rand.Intn(int(sizeOfBucket))
	records, err := rc.idx.GetBucket(uint32(index))
	if err != nil {
		return nil, xerrors.Errorf("get bucket %w", err)
	}

	if len(records) == 0 {
		return nil, xerrors.Errorf("no block in bucket, index %d", index)
	}

	index = rc.rand.Intn(len(records))
	record := records[index]
	return rc.lru.getBlock(ctx, *rc.root, record.Cid)
}

func (rc *randomCheck) randomCar(ctx context.Context) (*cid.Cid, error) {
	bucketHashes, err := rc.GetBucketHashes(ctx)
	if err != nil {
		return nil, err
	}

	if len(bucketHashes) == 0 {
		return nil, xerrors.Errorf("no asset exist")
	}

	bucketIDs := make([]int, 0, len(bucketHashes))
	for k := range bucketHashes {
		bucketIDs = append(bucketIDs, int(k))
	}

	sort.Ints(bucketIDs)

	r := rand.New(rand.NewSource(rc.randomSeed))
	index := r.Intn(len(bucketIDs))
	bucketID := bucketIDs[index]

	cids, err := rc.GetCarsOfBucket(ctx, uint32(bucketID))
	if err != nil {
		return nil, xerrors.Errorf("get cars of bucket %w", err)
	}

	if len(cids) == 0 {
		return nil, xerrors.Errorf("no car exist in bucket %d", bucketID)
	}

	index = r.Intn(len(cids))
	cid := cids[index]
	return &cid, nil
}