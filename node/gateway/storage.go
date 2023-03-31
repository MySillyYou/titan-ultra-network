package gateway

import (
	"context"
	"io"

	"github.com/ipfs/go-cid"
	"github.com/ipfs/go-libipfs/blocks"
)

type Storage interface {
	GetCar(root cid.Cid) (io.ReadSeekCloser, error)
	HasCar(root cid.Cid) (bool, error)

	HasBlock(ctx context.Context, c cid.Cid) (bool, error)
	GetBlock(ctx context.Context, c cid.Cid) (blocks.Block, error)
}