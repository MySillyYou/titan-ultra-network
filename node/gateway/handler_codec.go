package gateway

import (
	"net/http"

	"github.com/linguohua/titan/api/types"
)

func (gw *Gateway) serveCodec(w http.ResponseWriter, r *http.Request, ticket *types.AccessTicket) {
}