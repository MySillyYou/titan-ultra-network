package cache

import (
	"context"
	"io"
	"os"

	lru "github.com/hashicorp/golang-lru"
	"github.com/ipfs/go-cid"
	"github.com/ipfs/go-libipfs/blocks"
	carv2 "github.com/ipld/go-car/v2"
	"github.com/ipld/go-car/v2/blockstore"
	"github.com/ipld/go-car/v2/index"
	titanindex "github.com/linguohua/titan/node/asset/index"
	"github.com/linguohua/titan/node/asset/storage"
	"github.com/multiformats/go-multihash"
	"golang.org/x/xerrors"
)

const sizeOfBuckets = 128

type Key string

// lruCache asset index for cache
type lruCache struct {
	storage storage.Storage
	cache   *lru.Cache
}

type cacheValue struct {
	bs          *blockstore.ReadOnly
	readerClose io.ReadCloser
	idx         index.Index
}

func newLRUCache(storage storage.Storage, maxSize int) (*lruCache, error) {
	b := &lruCache{storage: storage}
	cache, err := lru.NewWithEvict(maxSize, b.onEvict)
	if err != nil {
		return nil, err
	}
	b.cache = cache

	return b, nil
}

func (lru *lruCache) getBlock(ctx context.Context, root, block cid.Cid) (blocks.Block, error) {
	key := Key(root.Hash().String())
	v, ok := lru.cache.Get(key)
	if !ok {
		if err := lru.add(root); err != nil {
			return nil, xerrors.Errorf("add cache %s %w", root.String(), err)
		}

		log.Debugf("add asset %s to cache", block.String())

		if v, ok = lru.cache.Get(key); !ok {
			return nil, xerrors.Errorf("asset %s not exist", root.String())
		}
	}

	if c, ok := v.(*cacheValue); ok {
		return c.bs.Get(ctx, block)
	}

	return nil, xerrors.Errorf("can not convert interface to *cacheValue")
}

func (lru *lruCache) hasBlock(ctx context.Context, root, block cid.Cid) (bool, error) {
	key := Key(root.Hash().String())
	v, ok := lru.cache.Get(key)
	if !ok {
		if err := lru.add(root); err != nil {
			return false, err
		}

		log.Debugf("check asset %s index from cache", block.String())

		if v, ok = lru.cache.Get(key); !ok {
			return false, xerrors.Errorf("asset %s not exist", root.String())
		}
	}

	if c, ok := v.(*cacheValue); ok {
		return c.bs.Has(ctx, block)
	}

	return false, xerrors.Errorf("can not convert interface to *cacheValue")
}

func (lru *lruCache) assetIndex(root cid.Cid) (index.Index, error) {
	key := Key(root.Hash().String())
	v, ok := lru.cache.Get(key)
	if !ok {
		if err := lru.add(root); err != nil {
			return nil, err
		}

		if v, ok = lru.cache.Get(key); !ok {
			return nil, xerrors.Errorf("asset %s not exist", root.String())
		}
	}

	if c, ok := v.(*cacheValue); ok {
		return c.idx, nil
	}

	return nil, xerrors.Errorf("can not convert interface to *cacheValue")
}

func (lru *lruCache) add(root cid.Cid) error {
	reader, err := lru.storage.GetAsset(root)
	if err != nil {
		return err
	}

	f, ok := reader.(*os.File)
	if !ok {
		return xerrors.Errorf("can not convert asset %s reader to file", root.String())
	}

	idx, err := lru.getAssetIndex(f)
	if err != nil {
		return err
	}

	bs, err := blockstore.NewReadOnly(f, idx, carv2.ZeroLengthSectionAsEOF(true))
	if err != nil {
		return err
	}

	cache := &cacheValue{bs: bs, readerClose: f, idx: idx}
	lru.cache.Add(Key(root.Hash().String()), cache)

	return nil
}

func (lru *lruCache) remove(root cid.Cid) {
	lru.cache.Remove(Key(root.Hash().String()))
}

func (lru *lruCache) onEvict(key interface{}, value interface{}) {
	if c, ok := value.(*cacheValue); ok {
		c.bs.Close()
		c.readerClose.Close()
	}
}

func (lru *lruCache) getAssetIndex(r io.ReaderAt) (index.Index, error) {
	// Open the CARv2 file
	cr, err := carv2.NewReader(r)
	if err != nil {
		panic(err)
	}
	defer cr.Close()

	// Read and unmarshall index within CARv2 file.
	ir, err := cr.IndexReader()
	if err != nil {
		return nil, err
	}
	idx, err := index.ReadFrom(ir)
	if err != nil {
		return nil, err
	}

	iterableIdx, ok := idx.(index.IterableIndex)
	if !ok {
		return nil, xerrors.Errorf("idx is not IterableIndex")
	}

	records := make([]index.Record, 0)
	iterableIdx.ForEach(func(m multihash.Multihash, u uint64) error {
		record := index.Record{Cid: cid.NewCidV0(m), Offset: u}
		records = append(records, record)
		return nil
	})

	idx = titanindex.NewMultiIndexSorted(sizeOfBuckets)
	if err := idx.Load(records); err != nil {
		return nil, err
	}
	// convert to titan index
	return idx, nil
}